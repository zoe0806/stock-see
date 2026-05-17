package router

import (
	"encoding/json"
	"net/http"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
)

func InitRouter(runner *adk.Runner, chatModel *openai.ChatModel) *http.ServeMux {
	http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		handerChat(w, r, runner, chatModel)
	})
	http.HandleFunc("/api/rag/search", handleRAGSearch)

	http.HandleFunc("/break", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/break" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "./static/break.html")
	})

	http.HandleFunc("/rag", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rag" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "./static/rag.html")
	})
	http.HandleFunc("/api/breakout_score", handleBreakoutScore)
	http.HandleFunc("/api/rag/sync", handleRAGSync)
	// 提供静态文件（HTML 页面）
	http.Handle("/", http.FileServer(http.Dir("./static")))
	return http.DefaultServeMux
}

func replyJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(v)
}
