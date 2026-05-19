package router

import (
	"net/http"
	"strconv"

	"stock-see/rag"

	"github.com/gin-gonic/gin"
)

func handleRAGSearch(c *gin.Context) {
	client, err := rag.NewWithRedisFromEnv("")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "RAG 未配置或 Redis 不可用: " + err.Error()})
		return
	}
	code := c.Query("code")
	from := c.Query("from")
	to := c.Query("to")
	q := c.Query("q")
	limit := 20
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	var results []rag.SearchResult
	if q != "" {
		results, err = client.SearchByQuery(c.Request.Context(), q, code, from, to, limit)
	} else {
		results, err = client.Search(c.Request.Context(), code, from, to, limit)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for i := range results {
		results[i].Item.Embedding = nil
	}
	c.JSON(http.StatusOK, gin.H{"total": len(results), "items": results})
}
