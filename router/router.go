package router

import (
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/gin-gonic/gin"
)

func SetupRouter(r *gin.Engine, runner *adk.Runner, chatModel *openai.ChatModel) {
	r.POST("/chat", func(c *gin.Context) {
		handerChat(c, runner, chatModel)
	})
	r.GET("/api/rag/search", handleRAGSearch)

	r.GET("/break", func(c *gin.Context) {
		c.File("./static/break.html")
	})
	r.GET("/rag", func(c *gin.Context) {
		c.File("./static/rag.html")
	})

	r.POST("/api/breakout_score", handleBreakoutScore)
	r.POST("/api/rag/sync", handleRAGSync)

	r.GET("/", func(c *gin.Context) {
		c.File("./static/index.html")
	})
}
