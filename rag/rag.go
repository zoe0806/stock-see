// Package rag 实现资讯 RAG：从数据源（当前为 Python akshare 新闻接口）拉取新闻，
// 去重后写入 Redis Stack（RedisJSON + RediSearch），支持按股票代码、时间范围检索。
// SearchByQuery 使用向量 KNN + 词法 BM25（title|summary）RRF 融合与规则重排。
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"stock-see/tools"

	"github.com/redis/go-redis/v9"
)

const (
	keyPrefix       = "rag:news:"
	indexName       = "idx:rag:news"
	indexVectorName = "idx:rag:news_v"
	indexPrefix     = "rag:news:"
)

// NewsItem 单条新闻，与 Python GET /api/news/{symbol} 返回的 item 对齐，并增加 RAG 元数据。
type NewsItem struct {
	Code      string    `json:"code"`  // 股票代码
	Title     string    `json:"title"` // 标题
	URL       string    `json:"url,omitempty"`
	Source    string    `json:"source"`    // 来源
	Date      string    `json:"date"`      // 发布日期 YYYY-MM-DD
	Summary   string    `json:"summary"`   // 摘要
	Sentiment string    `json:"sentiment"` // 情绪：positive/negative/neutral，Phase 1 可空
	Urgency   string    `json:"urgency"`   // 紧急程度：high/normal/low，Phase 1 可空
	StoredAt  string    `json:"stored_at"`
	Embedding []float32 `json:"embedding,omitempty"` // 向量，入库时写入
}

// Client RAG 客户端：拉取新闻、写入 Redis、检索。
type Client struct {
	rdb    *redis.Client
	pyBase string
}

// RedisAddr 优先从 config/stock.json 的 rag.redisAddr 读取，否则用环境变量 RAG_REDIS_ADDR。
func RedisAddr() string {
	if c := tools.GetRAGConfig(); c != nil && c.RedisAddr != "" {
		return strings.TrimSpace(c.RedisAddr)
	}
	if s := os.Getenv("RAG_REDIS_ADDR"); s != "" {
		return s
	}
	return "localhost:6379"
}

// RedisPassword 优先从 config 的 rag.redisPassword 读取，否则用环境变量 RAG_REDIS_PASSWORD。
func RedisPassword() string {
	if c := tools.GetRAGConfig(); c != nil {
		return c.RedisPassword
	}
	return os.Getenv("RAG_REDIS_PASSWORD")
}

// New 创建 RAG 客户端。rdb 为 nil 时仅拉取不落库（可用于测试）。
func New(rdb *redis.Client, pythonBaseURL string) *Client {
	if pythonBaseURL == "" {
		pythonBaseURL = tools.PythonBaseURL()
	}
	return &Client{rdb: rdb, pyBase: pythonBaseURL}
}

// NewWithRedisFromEnv 使用环境变量 RAG_REDIS_ADDR / RAG_REDIS_PASSWORD 创建 Redis 客户端并 New。
func NewWithRedisFromEnv(pythonBaseURL string) (*Client, error) {
	opt := &redis.Options{
		Addr:     RedisAddr(),
		Password: RedisPassword(),
		DB:       0,
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return New(rdb, pythonBaseURL), nil
}

// id 用于去重与 key：title+code+date 的短哈希。
func id(title, code, date string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(title) + "|" + code + "|" + date))
	return hex.EncodeToString(h[:])[:24]
}

func float32ToBytes(f []float32) []byte {
	b := make([]byte, len(f)*4)
	for i, v := range f {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b
}

// FetchFromPython 调用 Python GET /api/news/{symbol}，返回解析后的 NewsItem 列表。
func (c *Client) FetchFromPython(ctx context.Context, symbol string, limit int) ([]NewsItem, error) {
	if c.pyBase == "" {
		return nil, fmt.Errorf("Python base URL not set (STOCK_PYTHON_URL)")
	}
	if limit <= 0 {
		limit = 50
	}
	path := fmt.Sprintf("/api/news/%s?limit=%d", strings.TrimSpace(symbol), limit)
	body, err := tools.GetJSON(ctx, c.pyBase, path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Symbol string `json:"symbol"`
		Items  []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Source  string `json:"source"`
			Date    string `json:"date"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("parse news response: %w", err)
	}
	code := strings.TrimSpace(out.Symbol)
	if code == "" {
		code = symbol
	}
	now := time.Now().Format(time.RFC3339)
	list := make([]NewsItem, 0, len(out.Items))
	for _, it := range out.Items {
		date := it.Date
		if len(date) > 10 {
			date = date[:10]
		}
		list = append(list, NewsItem{
			Code:     code,
			Title:    strings.TrimSpace(it.Title),
			URL:      it.URL,
			Source:   strings.TrimSpace(it.Source),
			Date:     date,
			Summary:  strings.TrimSpace(it.Summary),
			StoredAt: now,
		})
	}
	return list, nil
}

// Store 将一条新闻向量化后写入 Redis（JSON.SET）。若 embedding 未配置或失败，仍写入文档但不含向量。
func (c *Client) Store(ctx context.Context, item NewsItem) error {
	if c.rdb == nil {
		return nil
	}
	text := strings.TrimSpace(item.Title + " " + item.Summary)
	if text != "" {
		vec, err := Embed(ctx, text)
		if err != nil {
			log.Printf("[rag] Embed skip: %v", err)
		} else {
			item.Embedding = vec
		}
	}

	key := keyPrefix + id(item.Title, item.Code, item.Date)
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if err := c.rdb.Do(ctx, "JSON.SET", key, "$", string(b)).Err(); err != nil {
		return fmt.Errorf("JSON.SET: %w", err)
	}
	return nil
}

// EnsureIndex 创建 RediSearch 元数据索引；EnsureVectorIndex 创建带向量字段的索引（用于 KNN）。
func (c *Client) EnsureIndex(ctx context.Context) error {
	if c.rdb == nil {
		return nil
	}
	err := c.rdb.Do(ctx,
		"FT.CREATE", indexName,
		"ON", "JSON",
		"PREFIX", "1", indexPrefix,
		"SCHEMA",
		"$.code", "AS", "code", "TAG",
		"$.date", "AS", "date", "TAG",
		"$.title", "AS", "title", "TEXT",
		"$.summary", "AS", "summary", "TEXT",
		"$.source", "AS", "source", "TAG",
	).Err()
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("FT.CREATE: %w", err)
	}
	return c.EnsureVectorIndex(ctx)
}

// EnsureVectorIndex 创建带 VECTOR 的索引，用于语义检索。DIM 取自 config rag.embeddingDim（或 RAG_EMBEDDING_DIM）。
// 若更换了 embedding 模型导致维度变化，需在 Redis 中执行 FT.DROPINDEX idx:rag:news_v 后重启或重新 Sync，以用新维度重建索引。
func (c *Client) EnsureVectorIndex(ctx context.Context) error {
	if c.rdb == nil {
		return nil
	}
	dim := tools.GetembeddingDim()
	err := c.rdb.Do(ctx,
		"FT.CREATE", indexVectorName,
		"ON", "JSON",
		"PREFIX", "1", indexPrefix,
		"SCHEMA",
		"$.code", "AS", "code", "TAG",
		"$.date", "AS", "date", "TAG",
		"$.title", "AS", "title", "TEXT",
		"$.summary", "AS", "summary", "TEXT",
		"$.source", "AS", "source", "TAG",
		"$.embedding", "AS", "vec", "VECTOR", "FLAT", "6",
		"TYPE", "FLOAT32",
		"DIM", dim,
		"DISTANCE_METRIC", "COSINE",
	).Err()
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("FT.CREATE vector: %w", err)
	}
	return nil
}

// Sync 对给定股票列表逐个拉取新闻并写入 Redis（去重由 key=id(title,code,date) 保证）。
func (c *Client) Sync(ctx context.Context, symbols []string) error {
	if err := c.EnsureIndex(ctx); err != nil {
		log.Printf("[rag] EnsureIndex: %v", err)
		// 继续执行，可能索引已存在
	}
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		items, err := c.FetchFromPython(ctx, sym, 50)
		if err != nil {
			log.Printf("[rag] Fetch %s: %v", sym, err)
			continue
		}
		for _, item := range items {
			if err := c.Store(ctx, item); err != nil {
				log.Printf("[rag] Store %s: %v", item.Title, err)
			}
		}
		log.Printf("[rag] Sync %s: %d items", sym, len(items))
	}
	return nil
}

// SearchResult 检索命中项，带 RediSearch 返回的 key 与文档。
// Search 按日期列表的 Score 常为 0；SearchByQuery 的 Score 为融合重排后的综合分（非原始向量距离）。
type SearchResult struct {
	Key   string   `json:"key"`
	Item  NewsItem `json:"item"`
	Score float64  `json:"score,omitempty"`
}

// Search 按股票代码、日期范围做元数据检索（不走向量），按日期倒序；与 SearchByQuery 语义检索分离。
// 查的是 idx:rag:news，同一批 key 的 JSON 里含 embedding，但本接口不按相似度排序；需语义召回时请用带 q 的 SearchByQuery。
func (c *Client) Search(ctx context.Context, code, dateFrom, dateTo string, limit int) ([]SearchResult, error) {
	if c.rdb == nil {
		return nil, fmt.Errorf("redis not configured")
	}
	_ = c.EnsureIndex(ctx)
	query := "*"
	if code != "" {
		code = strings.TrimSpace(code)
		query = "@code:{" + code + "}"
	}
	limitFetch := limit
	if dateFrom != "" || dateTo != "" {
		limitFetch = limit * 3
	}
	res, err := c.rdb.Do(ctx, "FT.SEARCH", indexName, query, "RETURN", "1", "$", "SORTBY", "date", "DESC", "LIMIT", 0, limitFetch).Result()
	if err != nil {
		return nil, fmt.Errorf("FT.SEARCH: %w", err)
	}
	return parseSearchResult(res, dateFrom, dateTo, limit, 0)
}

// SearchMode 语义检索策略（用于 SearchByQueryMode 与离线检索评测消融）。
type SearchMode string

const (
	// SearchModeVector 仅向量 KNN（idx:rag:news_v），按相似度截断。
	SearchModeVector SearchMode = "vector"
	// SearchModeHybrid 向量 + BM25，RRF 融合，无规则重排。
	SearchModeHybrid SearchMode = "hybrid"
	// SearchModeHybridRerank 向量 + BM25，RRF 融合 + 标题/时效/来源等规则重排（默认，与原 SearchByQuery 一致）。
	SearchModeHybridRerank SearchMode = "hybrid_rerank"
)

// SearchByQuery 等价于 SearchByQueryMode(..., SearchModeHybridRerank, ...)。
func (c *Client) SearchByQuery(ctx context.Context, query, code, dateFrom, dateTo string, limit int) ([]SearchResult, error) {
	return c.SearchByQueryMode(ctx, SearchModeHybridRerank, query, code, dateFrom, dateTo, limit)
}

// SearchByQueryMode 按策略执行语义检索；query 为空时退化为 Search（非向量）。
func (c *Client) SearchByQueryMode(ctx context.Context, mode SearchMode, query, code, dateFrom, dateTo string, limit int) ([]SearchResult, error) {
	if c.rdb == nil {
		return nil, fmt.Errorf("redis not configured")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return c.Search(ctx, code, dateFrom, dateTo, limit)
	}
	vec, err := Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding: %w", err)
	}
	if len(vec) != tools.GetembeddingDim() {
		return nil, fmt.Errorf("embedding dim %d != index dim %d", len(vec), tools.GetembeddingDim())
	}

	fetchV := hybridVecFetch
	fetchL := hybridLexFetch
	if dateFrom != "" || dateTo != "" {
		fetchV *= 2
		fetchL *= 2
	}

	vecRes, err := c.searchVectorHybrid(ctx, code, dateFrom, dateTo, vec, fetchV)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	if mode == SearchModeVector {
		return trimSearchResults(vecRes, limit), nil
	}

	var lexRes []SearchResult
	if buildLexicalRediSearchQuery(code, query) != "" {
		var errLex error
		lexRes, errLex = c.searchLexicalBM25(ctx, code, query, dateFrom, dateTo, fetchL)
		if errLex != nil {
			log.Printf("[rag] lexical BM25: %v", errLex)
			lexRes = nil
		}
	}

	switch mode {
	case SearchModeHybrid:
		return mergeHybridRRFOnly(vecRes, lexRes, limit), nil
	case SearchModeHybridRerank, "":
		return mergeHybridResults(query, vecRes, lexRes, limit), nil
	default:
		return mergeHybridResults(query, vecRes, lexRes, limit), nil
	}
}

func trimSearchResults(res []SearchResult, limit int) []SearchResult {
	if limit <= 0 {
		limit = 10
	}
	if len(res) > limit {
		return res[:limit]
	}
	return res
}

// parseSearchResult 解析 FT.SEARCH 返回。支持两种格式：
// 1) 原始数组 [ total, key1, [ "$", jsonStr ], key2, ... ]（go-redis 常见）
// 2) map 格式 map[results:[ {id, extra_attributes:{"$":jsonStr}} ] ]（部分客户端）
// maxScore > 0 时，会根据 __vec_score（COSINE 距离）过滤掉距离过大的结果。
func parseSearchResult(res interface{}, dateFrom, dateTo string, limit int, maxScore float64) ([]SearchResult, error) {
	var out []SearchResult
	addItem := func(key, jsonStr string, score float64) bool {
		if jsonStr == "" {
			return true
		}
		var item NewsItem
		if err := json.Unmarshal([]byte(jsonStr), &item); err != nil {
			return true
		}
		if maxScore > 0 && score > maxScore {
			return true
		}
		if dateFrom != "" && item.Date < dateFrom {
			return true
		}
		if dateTo != "" && item.Date > dateTo {
			return true
		}
		out = append(out, SearchResult{Key: key, Item: item, Score: score})
		return len(out) < limit
	}
	toFloat := func(v interface{}) (float64, bool) {
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
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return f, true
			}
		}
		return 0, false
	}

	// 格式 1：扁平数组 [ total, key1, [ "$", jsonStr, "__vec_score", score, ... ], key2, ... ]
	if arr, ok := res.([]interface{}); ok && len(arr) >= 1 {
		for i := 1; i < len(arr); {
			key, _ := arr[i].(string)
			i++
			if i >= len(arr) {
				break
			}
			doc := arr[i]
			i++
			var jsonStr string
			var score float64
			switch v := doc.(type) {
			case string:
				jsonStr = v
			case []interface{}:
				if len(v) >= 2 {
					if s, ok := v[1].(string); ok {
						jsonStr = s
					}
				}
				// 查找 __vec_score
				for j := 2; j+1 < len(v); j += 2 {
					if name, ok := v[j].(string); ok && name == "__vec_score" {
						if f, ok2 := toFloat(v[j+1]); ok2 {
							score = f
						}
					}
				}
			}
			if !addItem(key, jsonStr, score) {
				break
			}
		}
		return out, nil
	}

	// 格式 2：map[results:[ {id, extra_attributes: map["$"]=jsonStr} ] ]
	resultMap, ok := res.(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("parseSearchResult: unexpected type %T", res)
	}
	resultsVal, ok := resultMap["results"]
	if !ok {
		return out, nil
	}
	resultsSlice, ok := resultsVal.([]interface{})
	if !ok {
		return out, nil
	}
	for _, raw := range resultsSlice {
		if len(out) >= limit {
			break
		}
		itemMap, ok := raw.(map[interface{}]interface{})
		if !ok {
			continue
		}
		var key string
		if idVal, ok := itemMap["id"]; ok {
			key, _ = idVal.(string)
		}
		extraVal, ok := itemMap["extra_attributes"]
		if !ok {
			continue
		}
		extraMap, ok := extraVal.(map[interface{}]interface{})
		if !ok {
			continue
		}
		jsonVal, ok := extraMap["$"]
		if !ok {
			continue
		}
		jsonStr, ok := jsonVal.(string)
		if !ok {
			continue
		}
		// 提取 __vec_score（如果有）
		var score float64
		if vals, ok := itemMap["values"]; ok {
			if vs, ok2 := vals.([]interface{}); ok2 {
				for i := 0; i+1 < len(vs); i += 2 {
					if name, ok3 := vs[i].(string); ok3 && name == "__vec_score" {
						if f, ok4 := toFloat(vs[i+1]); ok4 {
							score = f
						}
					}
				}
			}
		}
		addItem(key, jsonStr, score)
	}
	return out, nil
}

// SearchRaw 执行 FT.SEARCH 并返回原始结果，便于调试。返回 [total, key1, doc1, ...]。
func (c *Client) SearchRaw(ctx context.Context, code string, limit int) (interface{}, error) {
	if c.rdb == nil {
		return nil, fmt.Errorf("redis not configured")
	}
	query := "*"
	if code != "" {
		query = "@code:{" + strings.TrimSpace(code) + "}"
	}
	return c.rdb.Do(ctx, "FT.SEARCH", indexName, query, "SORTBY", "date", "DESC", "LIMIT", 0, limit).Result()
}
