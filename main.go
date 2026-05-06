package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"stock-see/cronstock"
	"stock-see/eval"
	"stock-see/memory"
	"stock-see/prompt"
	"stock-see/rag"
	"stock-see/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-ext/components/model/openai" //openai模型
)

// loadMatchedSkillPaths 从 skills 目录加载技能列表，按用户消息匹配后返回要注入的 SKILL.md 路径列表。
// 当用户提到 资金/龙虎榜/情绪/主力 时，自动注入情绪资金面技能（sentiment）。
func loadMatchedSkillPaths(skillsRoot, userMessage string) []string {
	list, err := prompt.LoadSkillsFromDir(skillsRoot)
	if err != nil {
		return nil
	}
	paths := prompt.MatchSkills(list, userMessage)
	for _, s := range list {
		if s.Name != "sentiment" {
			continue
		}
		for _, kw := range []string{"资金", "龙虎榜", "情绪", "主力"} {
			if strings.Contains(userMessage, kw) {
				have := false
				for _, p := range paths {
					if p == s.Path {
						have = true
						break
					}
				}
				if !have {
					paths = append(paths, s.Path)
				}
				break
			}
		}
		break
	}
	return paths
}

func main() {
	evalFlag := flag.Bool("eval", false, "运行离线评测集（读取 -eval-suite 或 config.eval.defaultSuitePath），打印均分后退出")
	evalSuite := flag.String("eval-suite", "", "评测集 JSON 路径（默认可由 config eval.defaultSuitePath 指定）")
	evalJSONOut := flag.String("eval-json", "", "评测汇总写入该 JSON 文件（可选）")
	flag.Parse()

	ctx := context.Background()
	chatCfg := tools.GetChatOpenAIConfig()
	if chatCfg == nil || chatCfg.Model == "" || chatCfg.APIKey == "" {
		log.Fatalf("请在 config/stock.json 或 config/stock.example.json 中配置 chatOpenAI（model、apiKey、baseURL）")
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: chatCfg.BaseURL,
		Model:   chatCfg.Model,
		APIKey:  chatCfg.APIKey,
	})
	if err != nil {
		log.Fatalf("初始化模型失败: %v", err)
	}

	sysTpl, fullReportTpl, promptVer, err := tools.GetResolvedPrompt()
	if err != nil {
		log.Fatalf("解析 prompt 配置失败: %v", err)
	}
	log.Printf("prompt 当前版本: %s", promptVer)

	if *evalFlag {
		suitePath := strings.TrimSpace(*evalSuite)
		if suitePath == "" {
			suitePath = tools.GetEvalDefaultSuitePath()
		}
		if suitePath == "" {
			suitePath = "data/eval/suite.json"
		}
		sum, err := eval.Run(ctx, chatModel, eval.Options{
			SuitePath:        suitePath,
			SystemTemplate:   sysTpl,
			FullReportFormat: fullReportTpl,
			PromptVersion:    promptVer,
		})
		if err != nil {
			log.Fatalf("评测失败: %v", err)
		}
		log.Printf("评测完成 prompt=%s suite=%s 平均分=%.2f / 100", sum.PromptVersion, sum.SuitePath, sum.Average)
		if p := strings.TrimSpace(*evalJSONOut); p != "" {
			b, _ := json.MarshalIndent(sum, "", "  ")
			if err := os.WriteFile(p, b, 0644); err != nil {
				log.Fatalf("写入 %s: %v", p, err)
			}
			log.Printf("已写入 %s", p)
		}
		os.Exit(0)
	}

	agentConfig := &adk.ChatModelAgentConfig{
		Name:        "StockAssistant",
		Description: "股票分析助手，可获取行情与新闻并进行分析",
		Instruction: sysTpl,
		Model:       chatModel,
	}
	agentConfig.ToolsConfig = adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools.StockTools(),
		},
	}
	log.Println("股票助手: 已启用工具 get_market_data / get_news")

	agent, err := adk.NewChatModelAgent(ctx, agentConfig)
	if err != nil {
		log.Fatalf("创建 ChatModelAgent 失败: %v", err)
	}

	// Runner 用于注入会话上下文（SessionValues），使 Instruction 中的 {Context} 等占位符生效
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	// 2. 设置路由（具体路径优先于静态文件）
	http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		handerChat(w, r, runner, fullReportTpl)
	})
	http.HandleFunc("/break", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/break" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "./static/break.html")
	})
	http.HandleFunc("/cron-stock", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cron-stock" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "./static/cron-stock.html")
	})
	http.HandleFunc("/rag", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rag" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "./static/rag.html")
	})
	http.HandleFunc("/api/breakout_score", handleBreakoutScore)
	http.HandleFunc("/api/cron_stocks", handleCronStocks)
	http.HandleFunc("/api/rag/sync", handleRAGSync)
	http.HandleFunc("/api/rag/search", handleRAGSearch)

	// 定时任务：交易日交易时间每 5 分钟执行一次，向飞书推送订阅股票的实时价格
	go runCronTicker()
	// 定时任务：每小时拉取新闻写入 RAG（Redis 未配置时自动跳过）
	go runRAGTicker()

	// 提供静态文件（HTML 页面）
	http.Handle("/", http.FileServer(http.Dir("./static")))
	log.Println("Server started at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func runCronTicker() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if err := cronstock.RunTick(context.Background()); err != nil {
			log.Printf("[cron_stocks] tick error: %v", err)
		}
	}
}

// runRAGTicker 每小时执行一次：从 cron_stocks 取订阅列表，拉取新闻并写入 Redis（RAG）。
// 若 RAG_REDIS_ADDR 未配置或 Redis 不可用，则静默跳过。
func runRAGTicker() {
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

func handleCronStocks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, err := cronstock.Load()
		if err != nil {
			replyJSON(w, map[string]string{"error": err.Error()})
			return
		}
		replyJSON(w, c)
		return
	case http.MethodPost:
		var body struct {
			Symbol           string `json:"symbol"`
			IntervalMinutes  int    `json:"interval_minutes"`
			FeishuWebhookURL string `json:"feishu_webhook_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			replyJSON(w, map[string]string{"error": "invalid body"})
			return
		}
		symbol := strings.TrimSpace(body.Symbol)
		webhook := strings.TrimSpace(body.FeishuWebhookURL)
		if symbol == "" || webhook == "" {
			replyJSON(w, map[string]string{"error": "股票代码与飞书 Webhook 必填"})
			return
		}
		if body.IntervalMinutes <= 0 {
			body.IntervalMinutes = 5
		}
		if body.IntervalMinutes != 5 && body.IntervalMinutes != 10 && body.IntervalMinutes != 30 {
			replyJSON(w, map[string]string{"error": "推送间隔仅支持 5、10、30 分钟"})
			return
		}
		c, err := cronstock.Load()
		if err != nil {
			replyJSON(w, map[string]string{"error": err.Error()})
			return
		}
		c.Subscriptions = append(c.Subscriptions, cronstock.Subscription{
			Symbol:           symbol,
			IntervalMinutes:  body.IntervalMinutes,
			FeishuWebhookURL: webhook,
		})
		if err := cronstock.Save(c); err != nil {
			replyJSON(w, map[string]string{"error": err.Error()})
			return
		}
		replyJSON(w, c)
		return
	case http.MethodDelete:
		idxStr := r.URL.Query().Get("index")
		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx < 0 {
			replyJSON(w, map[string]string{"error": "invalid index"})
			return
		}
		c, err := cronstock.Load()
		if err != nil {
			replyJSON(w, map[string]string{"error": err.Error()})
			return
		}
		if idx >= len(c.Subscriptions) {
			replyJSON(w, map[string]string{"error": "index out of range"})
			return
		}
		c.Subscriptions = append(c.Subscriptions[:idx], c.Subscriptions[idx+1:]...)
		if err := cronstock.Save(c); err != nil {
			replyJSON(w, map[string]string{"error": err.Error()})
			return
		}
		replyJSON(w, c)
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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

func replyJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func handerChat(w http.ResponseWriter, r *http.Request, runner *adk.Runner, fullReportFormat string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析用户消息（可扩展：symbol、mode、context、session_history、workspace、memory）
	var req struct {
		Message        string `json:"message"`
		Symbol         string `json:"symbol"`  // 可选：当前分析标的
		Mode           string `json:"mode"`    // 可选：full = Phase 4 全量报告（并行分析+评分后由 Agent 生成总评）
		Context        string `json:"context"` // 可选：工作空间、记忆等
		SessionHistory string `json:"session_history"`
		Workspace      string `json:"workspace"`
		Memory         string `json:"memory"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	userMessage := req.Message
	skillsRoot := filepath.Join(".", "skills")
	skillPaths := loadMatchedSkillPaths(skillsRoot, userMessage)
	skillsContent := prompt.LoadSkillsContent(skillPaths)

	marketCtx := ""
	newsCtx := ""
	memoryContent := req.Memory
	extraCtx := req.Context

	needFullReport := (req.Mode == "full" || req.Mode == "full_report") && req.Symbol != ""

	if req.Symbol != "" {
		marketCtx = tools.GetMarketDataMock(req.Symbol)
		//newsCtx = tools.GetNewsMock(req.Symbol, 5)
		if memoryContent == "" {
			memoryContent = memory.FormatMemoryWithLastReport(req.Symbol)
			if memoryContent == "" {
				memoryContent, _ = memory.ReadStockMemory(req.Symbol, "")
			}
		}
	}

	// writeSSEData 按 SSE 规范发送多行 data：每行以 "data: " 前缀发送，避免 content 中的换行破坏事件边界。
	writeSSEData := func(event, data string) {
		fmt.Fprintf(w, "event: %s\n", event)
		for _, line := range strings.Split(data, "\n") {
			fmt.Fprintf(w, "data: %s\n", line)
		}
		fmt.Fprint(w, "\n")
	}

	// 设置 SSE 头（尽早设置，以便全量报告时先流式推送 section）
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Phase 4 全量报告：按维度流式推送，再汇总上下文给 Agent 生成综合报告
	if needFullReport {
		includeReports := strings.Contains(req.Message, "财报")
		combined, formattedScore, err := tools.RunAnalysisParallelStream(r.Context(), req.Symbol, includeReports, func(key string, _ any, markdown string) {
			payload, _ := json.Marshal(map[string]string{"type": key, "markdown": markdown})
			fmt.Fprintf(w, "event: section\ndata: %s\n\n", payload)
			flusher.Flush()
		})
		if err == nil && formattedScore != "" {
			extraCtx = prompt.BuildFullReportExtra(tools.FormatParallelResultForContext(combined), formattedScore, fullReportFormat)
			if userMessage == "" {
				userMessage = "根据上下文中的「本次并行分析结果」与「综合评分结果」，按「综合报告输出格式」生成该股票的综合报告（含当前行情、各维度分析、风险提示、操作建议与免责声明）。"
			}
			scorePayload, _ := json.Marshal(map[string]string{"type": "score", "markdown": formattedScore})
			fmt.Fprintf(w, "event: section\ndata: %s\n\n", scorePayload)
			flusher.Flush()
		}
	}

	contextBlock := prompt.BuildContext(prompt.ContextInput{
		SessionHistory: req.SessionHistory,
		Workspace:      req.Workspace,
		Memory:         memoryContent,
		MarketContext:  marketCtx,
		NewsContext:    newsCtx,
		Skills:         skillsContent,
		Extra:          extraCtx,
	})

	messages := []*schema.Message{schema.UserMessage(userMessage)}
	iterator := runner.Run(r.Context(), messages, adk.WithSessionValues(map[string]any{
		"Context": contextBlock,
	}))

	var fullReply strings.Builder
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			fmt.Fprintf(w, "event: error\ndata: %v\n\n", event.Err)
			flusher.Flush()
			break
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		out := event.Output.MessageOutput
		if out.IsStreaming && out.MessageStream != nil {
			out.MessageStream.SetAutomaticClose()
			for {
				msg, err := out.MessageStream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					fmt.Fprintf(w, "event: error\ndata: %v\n\n", err)
					flusher.Flush()
					break
				}
				if msg != nil && msg.Content != "" {
					//AI model 返回 stream 流式回复，包含 现在我来帮你分析等过程内容，也包含最后的综合报告
					fullReply.WriteString(msg.Content)
					writeSSEData("message", msg.Content)
					flusher.Flush()
				}
			}
		} else if out.Message != nil && out.Message.Content != "" {
			//AI model tools 工具调用返回回复
			// 【光迅科技】 002281 最新价: 88.94  涨跌幅: -10.00%成交量: 88.61万今开: 93.10  最高: 94.99  最低: 88.94
			//fullReply.WriteString(out.Message.Content)
			//writeSSEData("message", out.Message.Content)
			//fmt.Println("out.Message", out.Message.Content)
			//flusher.Flush()
		}
	}
	// Phase 1：若有 symbol 且本轮有回复，写入 memory/stock/<symbol>/<date>.md
	if req.Symbol != "" && fullReply.Len() > 0 {
		_ = memory.WriteStockMemory(req.Symbol, "", fullReply.String())
	}
	fmt.Fprintf(w, "event: done\ndata: \n\n")
	flusher.Flush()
}
