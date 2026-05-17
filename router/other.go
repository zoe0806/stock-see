package router

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"stock-see/cronstock"
	"stock-see/rag"
	"stock-see/tools"
	"strings"
	"time"
)

// RunRAGTicker 每小时执行一次：从 cron_stocks 取订阅列表，拉取新闻并写入 Redis（RAG）。
// 若 RAG_REDIS_ADDR 未配置或 Redis 不可用，则静默跳过。
func RunRAGTicker() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		if !tools.RAGEnabled() {
			continue
		}
		client, err := rag.NewWithRedisFromEnv("")
		if err != nil {
			continue // Redis 未配置或不可用，静默跳过
		}
		c, err := cronstock.Load()
		if err != nil || c == nil || len(c.Subscriptions) == 0 {
			continue
		}
		symbols := make([]string, 0, len(c.Subscriptions))
		for _, sub := range c.Subscriptions {
			symbols = append(symbols, sub.Symbol)
		}
		if err := client.Sync(context.Background(), symbols); err != nil {
			log.Printf("[rag] sync tick error: %v", err)
		}
	}
}

func handleBreakoutScore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Symbol   string `json:"symbol"`
		Period   string `json:"period"`
		Lookback int    `json:"lookback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyJSON(w, map[string]string{"error": "invalid body"})
		return
	}
	symbol := strings.TrimSpace(body.Symbol)
	if symbol == "" {
		replyJSON(w, map[string]string{"error": "symbol required"})
		return
	}
	baseURL := tools.PythonBaseURL()
	if baseURL == "" {
		replyJSON(w, map[string]string{"error": "Python 未配置，请设置 STOCK_PYTHON_URL"})
		return
	}
	reqBody := map[string]any{"symbol": symbol}
	if body.Period != "" {
		reqBody["period"] = body.Period
	}
	if body.Lookback > 0 {
		reqBody["lookback"] = body.Lookback
	}
	s, err := tools.PostJSON(r.Context(), baseURL, "/api/analysis/breakout_score", reqBody)
	if err != nil {
		replyJSON(w, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(s))
}

func handleRAGSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	client, err := rag.NewWithRedisFromEnv("")
	if err != nil {
		replyJSON(w, map[string]string{"error": "RAG 未配置或 Redis 不可用: " + err.Error()})
		return
	}
	var body struct {
		Symbols []string `json:"symbols"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	symbols := body.Symbols
	if len(symbols) == 0 {
		c, _ := cronstock.Load()
		if c != nil {
			for _, sub := range c.Subscriptions {
				symbols = append(symbols, sub.Symbol)
			}
		}
	}

	if len(symbols) == 0 {
		replyJSON(w, map[string]string{"error": "请提供 symbols 或在 cron_stocks 中配置订阅"})
		return
	}
	if err := client.Sync(r.Context(), symbols); err != nil {
		replyJSON(w, map[string]interface{}{"error": err.Error(), "synced_symbols": symbols})
		return
	}
	replyJSON(w, map[string]interface{}{"ok": true, "synced_symbols": symbols})
}
