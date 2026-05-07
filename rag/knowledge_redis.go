package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"stock-see/tools"

	"github.com/redis/go-redis/v9"
)

// knowledge.json 向量库：与资讯 rag:news:* 完全隔离的 key 前缀与索引名。
const (
	kbDocKeyPrefix    = "rag:kb:d:"
	kbIndexVectorName = "idx:rag:kb_v"
	kbIndexPrefix     = "rag:kb:d:"
	// kbMetaKey 存知识库源文件指纹，用于进程重启后跳过未变更文件的全量同步（不做 rag:kb:d:* 前缀，避免被 SCAN 清理）。
	kbMetaKey = "rag:kb:_meta"
)

// knowledgeIndexMeta Redis 中记录的知识库源文件版本（与 chunk 内容一致时跳过后续 Embed 同步）。
type knowledgeIndexMeta struct {
	FileSHA256 string `json:"file_sha256"`
}

// KnowledgeRedisIndexName RediSearch 向量索引名（运维：改维度后 FT.DROPINDEX 此名）。
func KnowledgeRedisIndexName() string { return kbIndexVectorName }

// KnowledgeChunk 写入 RedisJSON 的单条知识片段（与 kb.Document 字段对齐）。
type KnowledgeChunk struct {
	ID         string    `json:"id"`
	Category   string    `json:"category"`
	Text       string    `json:"text"`
	TextSHA256 string    `json:"text_sha256"`
	Embedding  []float32 `json:"embedding,omitempty"`
}

// KnowledgeSearchHit Redis KNN 单条命中。
type KnowledgeSearchHit struct {
	Key      string
	Chunk    KnowledgeChunk
	VecScore float64 // 余弦距离，越小越相似（与资讯侧 __vec_score 一致）
}

func kbDocKey(id string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return kbDocKeyPrefix + hex.EncodeToString(h[:12])
}

// EnsureKnowledgeVectorIndex 创建知识库向量索引（与 idx:rag:news_v 独立）。
// 若更换 embedding 模型导致维度变化，需执行 FT.DROPINDEX idx:rag:kb_v 后重新同步。
func (c *Client) EnsureKnowledgeVectorIndex(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	dim := tools.GetembeddingDim()
	err := c.rdb.Do(ctx,
		"FT.CREATE", kbIndexVectorName,
		"ON", "JSON",
		"PREFIX", "1", kbIndexPrefix,
		"SCHEMA",
		"$.category", "AS", "category", "TAG",
		"$.text", "AS", "txt", "TEXT",
		"$.embedding", "AS", "vec", "VECTOR", "FLAT", "6",
		"TYPE", "FLOAT32",
		"DIM", dim,
		"DISTANCE_METRIC", "COSINE",
	).Err()
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("FT.CREATE kb vector: %w", err)
	}
	return nil
}

// SyncKnowledgeChunks 将片段同步到 Redis：文本未变则复用已有向量，避免重复 Embed。
// 同步结束后删除本次未出现的旧 key（rag:kb:d:*），避免 JSON 删改后残留。
func (c *Client) SyncKnowledgeChunks(ctx context.Context, chunks []KnowledgeChunk) error {
	if c == nil || c.rdb == nil {
		return fmt.Errorf("redis not configured")
	}
	if len(chunks) == 0 {
		return nil
	}
	if err := c.EnsureKnowledgeVectorIndex(ctx); err != nil {
		return err
	}
	dim := tools.GetembeddingDim()
	wantKeys := make(map[string]struct{}, len(chunks))
	embedded := 0
	reused := 0
	for _, ch := range chunks {
		id := strings.TrimSpace(ch.ID)
		text := strings.TrimSpace(ch.Text)
		if id == "" || text == "" {
			continue
		}
		hash := sha256Hex(text)
		key := kbDocKey(id)
		// 无论本次是否嵌入成功，都保留 key，避免清理阶段误删旧向量
		wantKeys[key] = struct{}{}

		raw, err := c.rdb.Do(ctx, "JSON.GET", key, "$").Result()
		var existing KnowledgeChunk
		if err == nil {
			existing, _ = parseRedisJSONKnowledgeChunk(raw)
		} else if err != redis.Nil {
			log.Printf("[kb-rag] JSON.GET %s: %v", key, err)
		}

		if existing.TextSHA256 == hash && len(existing.Embedding) == dim {
			reused++
			continue
		}

		vec, err := Embed(ctx, text)
		if err != nil {
			log.Printf("[kb-rag] Embed 跳过 id=%s: %v", id, err)
			continue
		}
		if len(vec) != dim {
			log.Printf("[kb-rag] Embed 维度不符 id=%s: got %d want %d", id, len(vec), dim)
			continue
		}
		doc := KnowledgeChunk{
			ID: id, Category: strings.TrimSpace(ch.Category), Text: text,
			TextSHA256: hash, Embedding: vec,
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := c.rdb.Do(ctx, "JSON.SET", key, "$", string(b)).Err(); err != nil {
			return fmt.Errorf("JSON.SET %s: %w", key, err)
		}
		embedded++
	}

	// 清理已删除的片段
	var cursor uint64
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, kbDocKeyPrefix+"*", 200).Result()
		if err != nil {
			log.Printf("[kb-rag] SCAN 清理: %v", err)
			break
		}
		for _, k := range keys {
			if _, ok := wantKeys[k]; !ok {
				if err := c.rdb.Del(ctx, k).Err(); err != nil {
					log.Printf("[kb-rag] DEL %s: %v", k, err)
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	log.Printf("[kb-rag] Redis 同步完成: 新嵌入 %d 条, 复用向量 %d 条, 当前共 %d 条", embedded, reused, len(wantKeys))
	return nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// GetKnowledgeSourceSHA256 读取上次成功同步时写入的源文件 SHA256（十六进制）；无记录则 ("", nil)。
func (c *Client) GetKnowledgeSourceSHA256(ctx context.Context) (string, error) {
	if c == nil || c.rdb == nil {
		return "", nil
	}
	raw, err := c.rdb.Do(ctx, "JSON.GET", kbMetaKey, "$").Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", nil
	}
	var wrap []knowledgeIndexMeta
	if json.Unmarshal([]byte(s), &wrap) == nil && len(wrap) == 1 {
		return strings.TrimSpace(wrap[0].FileSHA256), nil
	}
	var meta knowledgeIndexMeta
	if json.Unmarshal([]byte(s), &meta) == nil {
		return strings.TrimSpace(meta.FileSHA256), nil
	}
	return "", nil
}

// SetKnowledgeSourceSHA256 写入源文件 SHA256，供下次启动判断是否需要重新同步。
func (c *Client) SetKnowledgeSourceSHA256(ctx context.Context, fileSHA256Hex string) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	b, err := json.Marshal(knowledgeIndexMeta{FileSHA256: strings.TrimSpace(fileSHA256Hex)})
	if err != nil {
		return err
	}
	return c.rdb.Do(ctx, "JSON.SET", kbMetaKey, "$", string(b)).Err()
}

// parseRedisJSONKnowledgeChunk 解析 JSON.GET 的字符串（根对象或 [$ 包一层]）。
func parseRedisJSONKnowledgeChunk(raw interface{}) (KnowledgeChunk, bool) {
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return KnowledgeChunk{}, false
	}
	var ch KnowledgeChunk
	if json.Unmarshal([]byte(s), &ch) == nil && ch.ID != "" {
		return ch, true
	}
	var wrap []KnowledgeChunk
	if json.Unmarshal([]byte(s), &wrap) == nil && len(wrap) == 1 && wrap[0].ID != "" {
		return wrap[0], true
	}
	return KnowledgeChunk{}, false
}

// SearchKnowledgeByVector 对知识库做向量 KNN（需已 SyncKnowledgeChunks）。
func (c *Client) SearchKnowledgeByVector(ctx context.Context, query string, topK int) ([]KnowledgeSearchHit, error) {
	if c == nil || c.rdb == nil {
		return nil, fmt.Errorf("redis not configured")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 8
	}
	vec, err := Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(vec) != tools.GetembeddingDim() {
		return nil, fmt.Errorf("query embedding dim %d != %d", len(vec), tools.GetembeddingDim())
	}
	_ = c.EnsureKnowledgeVectorIndex(ctx)
	blob := float32ToBytes(vec)
	knnQuery := "*=>[KNN " + strconv.Itoa(topK) + " @vec $BLOB]"
	res, err := c.rdb.Do(ctx,
		"FT.SEARCH", kbIndexVectorName, knnQuery,
		"PARAMS", "2", "BLOB", blob,
		"DIALECT", "2",
		"RETURN", "2", "$", "__vec_score",
		"SORTBY", "__vec_score",
		"LIMIT", "0", strconv.Itoa(topK),
	).Result()
	if err != nil {
		return nil, fmt.Errorf("FT.SEARCH kb: %w", err)
	}
	return parseKnowledgeSearchResult(res, topK)
}

func parseKnowledgeSearchResult(res interface{}, limit int) ([]KnowledgeSearchHit, error) {
	var out []KnowledgeSearchHit
	toFloat := func(v interface{}) (float64, bool) {
		switch x := v.(type) {
		case float64:
			return x, true
		case float32:
			return float64(x), true
		case int64:
			return float64(x), true
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return f, true
			}
		}
		return 0, false
	}

	appendHit := func(key, jsonStr string, vecScore float64) {
		if jsonStr == "" || len(out) >= limit {
			return
		}
		var ch KnowledgeChunk
		if err := json.Unmarshal([]byte(jsonStr), &ch); err != nil {
			return
		}
		out = append(out, KnowledgeSearchHit{Key: key, Chunk: ch, VecScore: vecScore})
	}

	// 格式 1：扁平数组 [ total, key1, [ "$", jsonStr, "__vec_score", score, ... ], ... ]
	if arr, ok := res.([]interface{}); ok && len(arr) >= 1 {
		for i := 1; i < len(arr) && len(out) < limit; {
			key, _ := arr[i].(string)
			i++
			if i >= len(arr) {
				break
			}
			doc := arr[i]
			i++
			var jsonStr string
			var vecScore float64
			switch v := doc.(type) {
			case string:
				jsonStr = v
			case []interface{}:
				if len(v) >= 2 {
					if s, ok := v[1].(string); ok {
						jsonStr = s
					}
				}
				for j := 2; j+1 < len(v); j += 2 {
					if name, ok := v[j].(string); ok && name == "__vec_score" {
						if f, ok2 := toFloat(v[j+1]); ok2 {
							vecScore = f
						}
					}
				}
			}
			appendHit(key, jsonStr, vecScore)
		}
		return out, nil
	}

	// 格式 2：go-redis / RESP3 常见 map[results: [...]]（与 parseSearchResult 格式 2 一致）
	m := redisSearchResultToMap(res)
	if m == nil {
		return nil, fmt.Errorf("kb FT.SEARCH: unexpected type %T", res)
	}
	resultsVal, ok := m["results"]
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
			if sm, ok2 := raw.(map[string]interface{}); ok2 {
				itemMap = stringMapToIfaceMap(sm)
			} else {
				continue
			}
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
			if sm, ok2 := extraVal.(map[string]interface{}); ok2 {
				extraMap = stringMapToIfaceMap(sm)
			} else {
				continue
			}
		}
		jsonVal, ok := extraMap["$"]
		if !ok {
			continue
		}
		jsonStr, ok := jsonVal.(string)
		if !ok {
			continue
		}
		var vecScore float64
		if vals, ok := itemMap["values"]; ok {
			if vs, ok2 := vals.([]interface{}); ok2 {
				for i := 0; i+1 < len(vs); i += 2 {
					if name, ok3 := vs[i].(string); ok3 && name == "__vec_score" {
						if f, ok4 := toFloat(vs[i+1]); ok4 {
							vecScore = f
						}
					}
				}
			}
		}
		appendHit(key, jsonStr, vecScore)
	}
	return out, nil
}

// redisSearchResultToMap 统一 FT.SEARCH 返回的 map 形态（string key / interface key）。
func redisSearchResultToMap(res interface{}) map[interface{}]interface{} {
	switch m := res.(type) {
	case map[interface{}]interface{}:
		return m
	case map[string]interface{}:
		out := make(map[interface{}]interface{}, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	default:
		return nil
	}
}

func stringMapToIfaceMap(sm map[string]interface{}) map[interface{}]interface{} {
	out := make(map[interface{}]interface{}, len(sm))
	for k, v := range sm {
		out[k] = v
	}
	return out
}
