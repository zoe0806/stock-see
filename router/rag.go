package router

import (
	"net/http"
	"stock-see/rag"
	"strconv"
)

func handleRAGSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	client, err := rag.NewWithRedisFromEnv("")
	if err != nil {
		replyJSON(w, map[string]string{"error": "RAG 未配置或 Redis 不可用: " + err.Error()})
		return
	}
	code := r.URL.Query().Get("code")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	q := r.URL.Query().Get("q") // 语义检索：非空时按向量 KNN 召回
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, _ := strconv.Atoi(l); n > 0 && n <= 100 {
			limit = n
		}
	}
	var results []rag.SearchResult
	if q != "" {
		results, err = client.SearchByQuery(r.Context(), q, code, from, to, limit)
	} else {
		results, err = client.Search(r.Context(), code, from, to, limit)
	}
	if err != nil {
		replyJSON(w, map[string]string{"error": err.Error()})
		return
	}
	// 返回时去掉 embedding 以减小 payload
	for i := range results {
		results[i].Item.Embedding = nil
	}
	replyJSON(w, map[string]interface{}{"total": len(results), "items": results})
}
