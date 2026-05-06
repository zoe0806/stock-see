package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 混合检索：向量 KNN + RediSearch 词法/BM25，RRF 融合后再规则重排。
const (
	hybridVecFetch   = 45
	hybridLexFetch   = 45
	rrfK             = 60
	bonusTitleQuery  = 10.0 // 标题含完整查询子串
	bonusTitleTerm   = 2.0  // 标题含单个分词（可叠加，有上限）
	maxTitleTermBon  = 6.0
	bonusRecencyCap  = 5.0 // 时间越近越高，上限
	recencyHalfLife  = 45  // 约 45 天衰减一半（按天）
	maxVecDistHybrid = 0.4 // 与 parseSearchResult 向量阈值一致
)

// sourceCredibility 来源可信度加权（可随业务扩展）。
var sourceCredibility = map[string]float64{
	"证券时报": 3, "上海证券报": 3, "中国证券报": 3, "证券日报": 3,
	"上交所": 3, "深交所": 3, "北交所": 3,
	"东方财富": 2, "同花顺": 2, "雪球": 1.5, "财联社": 2,
	"第一财经": 2, "财新": 2.5, "华尔街见闻": 2,
}

// escapeRediSearchTerm 转义 RediSearch 查询语法中的特殊字符（按词使用）。
func escapeRediSearchTerm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '(', ')', '{', '}', '[', ']', '"', ':', '!', '@',
			'#', '$', '%', '^', '&', '*', '-', '=', '+', '<', '>',
			'.', ',', '?', '|', '/', '~':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// splitQueryTerms 分词：空白分隔；无空白时整段一词（便于中文整句命中）。
func splitQueryTerms(q string) []string {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	parts := strings.Fields(q)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{q}
	}
	return out
}

// buildLexicalRediSearchQuery 构造 TAG(code) + TEXT(title|summary) 查询；无可用词时返回空串。
func buildLexicalRediSearchQuery(code, userQuery string) string {
	terms := splitQueryTerms(userQuery)
	if len(terms) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(terms))
	for _, t := range terms {
		e := escapeRediSearchTerm(t)
		if e != "" {
			escaped = append(escaped, e)
		}
	}
	if len(escaped) == 0 {
		return ""
	}
	orClause := strings.Join(escaped, "|")
	// 标题或摘要任一字段命中任一词（词间 OR）
	textPart := fmt.Sprintf("(@title:(%s))|(@summary:(%s))", orClause, orClause)
	if code = strings.TrimSpace(code); code != "" {
		return fmt.Sprintf("(@code:{%s}) (%s)", code, textPart)
	}
	return "(" + textPart + ")"
}

func toFloat64(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case string:
		if x == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// parseFTSearchWithScores 解析 FT.SEARCH ... WITHSCORES RETURN 1 $。
// 支持 go-redis 常见扁平数组，以及 map[results: [...]]（部分版本 / RESP3）。
func parseFTSearchWithScores(res interface{}, dateFrom, dateTo string, maxDocs int) ([]SearchResult, error) {
	switch v := res.(type) {
	case []interface{}:
		return parseFTSearchWithScoresArray(v, dateFrom, dateTo, maxDocs)
	case map[interface{}]interface{}:
		return parseFTSearchWithScoresMap(v, dateFrom, dateTo, maxDocs)
	case map[string]interface{}:
		cm := make(map[interface{}]interface{}, len(v))
		for k, val := range v {
			cm[k] = val
		}
		return parseFTSearchWithScoresMap(cm, dateFrom, dateTo, maxDocs)
	default:
		return nil, fmt.Errorf("unexpected FT.SEARCH response type %T", res)
	}
}

// asIfaceMap 统一 map[string] / map[interface{}] 嵌套结构。
func asIfaceMap(v interface{}) (map[interface{}]interface{}, bool) {
	switch m := v.(type) {
	case map[interface{}]interface{}:
		return m, true
	case map[string]interface{}:
		out := make(map[interface{}]interface{}, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out, true
	default:
		return nil, false
	}
}

func parseFTSearchWithScoresArray(arr []interface{}, dateFrom, dateTo string, maxDocs int) ([]SearchResult, error) {
	var out []SearchResult
	if len(arr) < 1 {
		return out, nil
	}
	for i := 1; i < len(arr) && len(out) < maxDocs*3; {
		key, _ := arr[i].(string)
		if key == "" {
			i++
			continue
		}
		i++
		if i >= len(arr) {
			break
		}
		var bm25 float64
		var fieldArr []interface{}
		switch v := arr[i].(type) {
		case float64, float32, int, int64, string:
			if f, ok := toFloat64(v); ok {
				bm25 = f
			}
			i++
			if i >= len(arr) {
				break
			}
			fieldArr, _ = arr[i].([]interface{})
			i++
		case []interface{}:
			fieldArr = v
			bm25 = 0
			i++
		default:
			i++
			continue
		}
		jsonStr := extractJSONFromFieldArr(fieldArr)
		if jsonStr == "" {
			continue
		}
		var item NewsItem
		if err := json.Unmarshal([]byte(jsonStr), &item); err != nil {
			continue
		}
		if dateFrom != "" && item.Date < dateFrom {
			continue
		}
		if dateTo != "" && item.Date > dateTo {
			continue
		}
		out = append(out, SearchResult{Key: key, Item: item, Score: bm25})
	}
	if len(out) > maxDocs {
		out = out[:maxDocs]
	}
	return out, nil
}

// mapGet 从 map[interface{}]interface{} 按多种 key 类型取值（兼容 string / []byte key）。
func mapGet(m map[interface{}]interface{}, name string) (interface{}, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m[name]; ok {
		return v, true
	}
	if v, ok := m[interface{}(name)]; ok {
		return v, true
	}
	for k, v := range m {
		ks, ok := k.(string)
		if ok && strings.EqualFold(ks, name) {
			return v, true
		}
		if kb, ok := k.([]byte); ok && strings.EqualFold(string(kb), name) {
			return v, true
		}
	}
	return nil, false
}

// lexicalScoreFromResultMap 从 FT.WITHSCORES 的 map 条目中取文档分。
func lexicalScoreFromResultMap(itemMap map[interface{}]interface{}) float64 {
	for _, k := range []string{"score", "document_score", "doc_score"} {
		if v, ok := mapGet(itemMap, k); ok {
			if f, ok := toFloat64(v); ok {
				return f
			}
		}
	}
	if vals, ok := mapGet(itemMap, "values"); ok {
		if vs, ok := vals.([]interface{}); ok {
			for i := 0; i+1 < len(vs); i += 2 {
				name, ok := vs[i].(string)
				if !ok {
					continue
				}
				if strings.EqualFold(name, "score") || name == "$score" {
					if f, ok := toFloat64(vs[i+1]); ok {
						return f
					}
				}
			}
		}
	}
	return 0
}

func parseFTSearchWithScoresMap(resultMap map[interface{}]interface{}, dateFrom, dateTo string, maxDocs int) ([]SearchResult, error) {
	var out []SearchResult
	resultsVal, ok := mapGet(resultMap, "results")
	if !ok {
		return out, nil
	}
	resultsSlice, ok := resultsVal.([]interface{})
	if !ok {
		return out, nil
	}
	for _, raw := range resultsSlice {
		if len(out) >= maxDocs {
			break
		}
		itemMap, ok := asIfaceMap(raw)
		if !ok {
			continue
		}
		var key string
		if idVal, ok := mapGet(itemMap, "id"); ok {
			key, _ = idVal.(string)
		}
		extraVal, ok := mapGet(itemMap, "extra_attributes")
		if !ok {
			// 部分客户端用 attributes / fields
			extraVal, ok = mapGet(itemMap, "attributes")
		}
		if !ok {
			continue
		}
		extraMap, ok := asIfaceMap(extraVal)
		if !ok {
			continue
		}
		jsonVal, ok := mapGet(extraMap, "$")
		if !ok {
			continue
		}
		jsonStr, ok := jsonVal.(string)
		if !ok {
			continue
		}
		var item NewsItem
		if err := json.Unmarshal([]byte(jsonStr), &item); err != nil {
			continue
		}
		if dateFrom != "" && item.Date < dateFrom {
			continue
		}
		if dateTo != "" && item.Date > dateTo {
			continue
		}
		bm25 := lexicalScoreFromResultMap(itemMap)
		out = append(out, SearchResult{Key: key, Item: item, Score: bm25})
	}
	return out, nil
}

func extractJSONFromFieldArr(v []interface{}) string {
	if len(v) < 2 {
		return ""
	}
	// 常见格式: "$", jsonStr 或 成对 name/value
	if s, ok := v[1].(string); ok {
		if name, ok := v[0].(string); ok && name == "$" {
			return s
		}
	}
	for j := 0; j+1 < len(v); j += 2 {
		if name, ok := v[j].(string); ok && name == "$" {
			if s, ok2 := v[j+1].(string); ok2 {
				return s
			}
		}
	}
	return ""
}

// searchLexicalBM25 在 idx:rag:news 上做词法检索，Score 为 RediSearch 文档分（BM25 族，越大越好）。
func (c *Client) searchLexicalBM25(ctx context.Context, code, userQuery, dateFrom, dateTo string, fetch int) ([]SearchResult, error) {
	q := buildLexicalRediSearchQuery(code, userQuery)
	if q == "" {
		return nil, nil
	}
	if c.rdb == nil {
		return nil, fmt.Errorf("redis not configured")
	}
	if fetch <= 0 {
		fetch = hybridLexFetch
	}
	_ = c.EnsureIndex(ctx)
	res, err := c.rdb.Do(ctx,
		"FT.SEARCH", indexName, q,
		"RETURN", "1", "$",
		"WITHSCORES",
		"DIALECT", "2",
		"LIMIT", "0", strconv.Itoa(fetch),
	).Result()
	if err != nil {
		return nil, err
	}
	return parseFTSearchWithScores(res, dateFrom, dateTo, fetch)
}

// searchVectorHybrid 向量 KNN，Score 为 __vec_score（余弦距离，越小越好）。
func (c *Client) searchVectorHybrid(ctx context.Context, code, dateFrom, dateTo string, vec []float32, fetch int) ([]SearchResult, error) {
	if c.rdb == nil {
		return nil, fmt.Errorf("redis not configured")
	}
	if fetch <= 0 {
		fetch = hybridVecFetch
	}
	_ = c.EnsureVectorIndex(ctx)
	blob := float32ToBytes(vec)
	primaryFilter := "*"
	if code = strings.TrimSpace(code); code != "" {
		primaryFilter = "(@code:{" + code + "})"
	}
	knnQuery := primaryFilter + "=>[KNN " + strconv.Itoa(fetch) + " @vec $BLOB]"
	limitFetch := fetch
	if dateFrom != "" || dateTo != "" {
		limitFetch = fetch * 2
	}
	res, err := c.rdb.Do(ctx,
		"FT.SEARCH", indexVectorName, knnQuery,
		"PARAMS", "2", "BLOB", blob,
		"DIALECT", "2",
		"RETURN", "2", "$", "__vec_score",
		"SORTBY", "__vec_score",
		"LIMIT", "0", strconv.Itoa(limitFetch),
	).Result()
	if err != nil {
		return nil, err
	}
	return parseSearchResult(res, dateFrom, dateTo, fetch, maxVecDistHybrid)
}

func rankKeysOrder(results []SearchResult) []string {
	seen := make(map[string]struct{})
	var keys []string
	for _, r := range results {
		if r.Key == "" {
			continue
		}
		if _, ok := seen[r.Key]; ok {
			continue
		}
		seen[r.Key] = struct{}{}
		keys = append(keys, r.Key)
	}
	return keys
}

// rrfScores 对两路排序列表做倒数排名融合，返回每个 key 的 RRF 分数（越大越好）。
func rrfScores(vecKeys, lexKeys []string, k int) map[string]float64 {
	rankVec := make(map[string]int, len(vecKeys))
	for i, id := range vecKeys {
		rankVec[id] = i
	}
	rankLex := make(map[string]int, len(lexKeys))
	for i, id := range lexKeys {
		rankLex[id] = i
	}
	keys := make(map[string]struct{})
	for _, id := range vecKeys {
		keys[id] = struct{}{}
	}
	for _, id := range lexKeys {
		keys[id] = struct{}{}
	}
	out := make(map[string]float64, len(keys))
	kf := float64(k)
	for id := range keys {
		var s float64
		if rv, ok := rankVec[id]; ok {
			s += 1.0 / (kf + float64(rv) + 1.0)
		}
		if rl, ok := rankLex[id]; ok {
			s += 1.0 / (kf + float64(rl) + 1.0)
		}
		out[id] = s
	}
	return out
}

func vecDistanceFromResults(results []SearchResult) map[string]float64 {
	m := make(map[string]float64, len(results))
	for _, r := range results {
		m[r.Key] = r.Score
	}
	return m
}

func bm25FromResults(results []SearchResult) map[string]float64 {
	m := make(map[string]float64, len(results))
	for _, r := range results {
		m[r.Key] = r.Score
	}
	return m
}

func mergeItems(results ...[]SearchResult) map[string]NewsItem {
	out := make(map[string]NewsItem)
	for _, list := range results {
		for _, r := range list {
			if r.Key != "" {
				out[r.Key] = r.Item
			}
		}
	}
	return out
}

func parseItemDate(d string) (time.Time, bool) {
	d = strings.TrimSpace(d)
	if len(d) >= 10 {
		d = d[:10]
	}
	t, err := time.ParseInLocation("2006-01-02", d, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func recencyBonus(item NewsItem) float64 {
	t, ok := parseItemDate(item.Date)
	if !ok {
		return 0
	}
	days := time.Since(t).Hours() / 24
	if days < 0 {
		days = 0
	}
	// 指数衰减，半衰期 recencyHalfLife 天
	v := math.Exp(-math.Ln2 * days / float64(recencyHalfLife))
	return math.Min(bonusRecencyCap, v*bonusRecencyCap)
}

func sourceBonus(item NewsItem) float64 {
	s := strings.TrimSpace(item.Source)
	if s == "" {
		return 0
	}
	if v, ok := sourceCredibility[s]; ok {
		return v
	}
	for k, v := range sourceCredibility {
		if strings.Contains(s, k) {
			return v
		}
	}
	return 0
}

func titleMatchBonuses(query string, item NewsItem) (full float64, terms float64) {
	q := strings.TrimSpace(query)
	if q == "" {
		return 0, 0
	}
	title := item.Title
	if strings.Contains(title, q) {
		full += bonusTitleQuery
	}
	// 分词命中标题
	ts := splitQueryTerms(q)
	var termSum float64
	for _, t := range ts {
		if len(t) >= 2 && strings.Contains(title, t) {
			termSum += bonusTitleTerm
		}
	}
	if termSum > maxTitleTermBon {
		termSum = maxTitleTermBon
	}
	return full, termSum
}

// hybridWeakVectorDrop 向量距离很差且词法未命中且标题不含查询时丢弃。
func hybridWeakVectorDrop(key string, query string, item NewsItem, vecDist map[string]float64, lexKeys []string) bool {
	d, hasVec := vecDist[key]
	inLex := false
	for _, k := range lexKeys {
		if k == key {
			inLex = true
			break
		}
	}
	q := strings.TrimSpace(query)
	titleHit := q != "" && strings.Contains(item.Title, q)
	if hasVec && d > maxVecDistHybrid && !inLex && !titleHit {
		return true
	}
	return false
}

type scoredKey struct {
	key   string
	score float64
}

// mergeHybridResults RRF + 规则重排 + 截断。
func mergeHybridResults(query string, vecRes, lexRes []SearchResult, finalLimit int) []SearchResult {
	vecKeys := rankKeysOrder(vecRes)
	lexKeys := rankKeysOrder(lexRes)
	rrf := rrfScores(vecKeys, lexKeys, rrfK)
	items := mergeItems(vecRes, lexRes)
	vecDist := vecDistanceFromResults(vecRes)
	bm25m := bm25FromResults(lexRes)
	minB, maxB := 0.0, 1.0
	bm25Span := 1.0
	if len(bm25m) > 0 {
		minB = math.MaxFloat64
		maxB = -math.MaxFloat64
		for _, v := range bm25m {
			if v < minB {
				minB = v
			}
			if v > maxB {
				maxB = v
			}
		}
		bm25Span = maxB - minB
		if bm25Span <= 1e-9 {
			bm25Span = 1
		}
	}

	var sk []scoredKey
	for key := range rrf {
		item, ok := items[key]
		if !ok {
			continue
		}
		if hybridWeakVectorDrop(key, query, item, vecDist, lexKeys) {
			continue
		}
		rrfV := rrf[key]
		// RRF 归一化到约 0~1（两路都第一名时最大约 2/(k+1)）
		rrfNorm := rrfV / (2.0 / (float64(rrfK) + 1.0))
		if rrfNorm > 1 {
			rrfNorm = 1
		}
		var vecSim float64
		if d, ok := vecDist[key]; ok && maxVecDistHybrid > 0 {
			if d <= maxVecDistHybrid {
				vecSim = 1.0 - d/maxVecDistHybrid
			}
		}
		bmN := 0.0
		if v, ok := bm25m[key]; ok {
			bmN = (v - minB) / bm25Span
			if bmN < 0 {
				bmN = 0
			}
			if bmN > 1 {
				bmN = 1
			}
		}
		fullB, termB := titleMatchBonuses(query, item)
		final := rrfNorm*28 + vecSim*22 + bmN*22 + fullB + termB + recencyBonus(item) + sourceBonus(item)
		sk = append(sk, scoredKey{key: key, score: final})
	}
	sort.Slice(sk, func(i, j int) bool {
		if sk[i].score != sk[j].score {
			return sk[i].score > sk[j].score
		}
		return sk[i].key < sk[j].key
	})
	if finalLimit <= 0 {
		finalLimit = 10
	}
	if len(sk) > finalLimit {
		sk = sk[:finalLimit]
	}
	out := make([]SearchResult, 0, len(sk))
	for _, s := range sk {
		it := items[s.key]
		out = append(out, SearchResult{
			Key:   s.key,
			Item:  it,
			Score: s.score,
		})
	}
	return out
}
