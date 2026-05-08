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
	"strconv"
	"strings"
	"time"

	"stock-see/cronstock"
	"stock-see/eval"
	"stock-see/evalintent"
	"stock-see/intent"
	"stock-see/intent/combo"
	"stock-see/intent/easyrules"
	"stock-see/intent/queryaug"
	"stock-see/kb"
	"stock-see/memory"
	"stock-see/observ"
	"stock-see/prompt"
	"stock-see/rag"
	"stock-see/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-ext/components/model/openai" //openai模型
	"github.com/cloudwego/eino/compose"
)

// loadMatchedSkillPaths 从 skills 加载 SKILL.md：内置中文意图词 + 可选 intent.json。
func loadMatchedSkillPaths(skillsRoot, matchText string) []string {
	list, err := prompt.LoadSkillsFromDir(skillsRoot)
	if err != nil {
		return nil
	}
	return prompt.MatchSkillsForRequest(list, matchText)
}

func main() {
	evalFlag := flag.Bool("eval", false, "运行离线评测集（读取 -eval-suite 或 config.eval.defaultSuitePath），打印均分后退出")
	evalSuite := flag.String("eval-suite", "", "评测集 JSON 路径（默认可由 config eval.defaultSuitePath 指定）")
	evalJSONOut := flag.String("eval-json", "", "评测汇总写入该 JSON 文件（可选）")
	evalIntentFlag := flag.Bool("eval-intent", false, "运行意图评测集，打印准确率与均分后退出")
	evalIntentSuite := flag.String("eval-intent-suite", "", "意图评测集 JSON（默认 config eval.defaultIntentSuitePath 或 data/eval/intent_suite.json）")
	evalIntentJSONOut := flag.String("eval-intent-json", "", "意图评测汇总写入该 JSON 文件（可选）")
	evalIntentMode := flag.String("eval-intent-mode", "fc", "解析路径：fc=仅模型 FC；combo=词典+规则；pipeline=与线上一致（高置信槽位跳过 FC）")
	evalIntentVerbose := flag.Bool("eval-intent-verbose", false, "逐条打印未满分用例的 Notes")
	evalRetrievalFlag := flag.Bool("eval-retrieval", false, "运行检索评测（Redis+embedding），对比 vector / hybrid / hybrid_rerank 的 Hit@K 后退出（无需对话模型）")
	evalRetrievalSuite := flag.String("eval-retrieval-suite", "", "检索评测集 JSON（默认 config eval.defaultRetrievalSuitePath 或 data/eval/retrieval_suite.json）")
	evalRetrievalJSONOut := flag.String("eval-retrieval-json", "", "检索评测汇总写入该 JSON（可选）")
	evalRetrievalVerbose := flag.Bool("eval-retrieval-verbose", false, "检索评测输出逐条 Top 标题与是否命中")
	flag.Parse()

	ctx := context.Background()

	if *evalRetrievalFlag {
		suitePath := strings.TrimSpace(*evalRetrievalSuite)
		if suitePath == "" {
			suitePath = tools.GetEvalDefaultRetrievalSuitePath()
		}
		if suitePath == "" {
			suitePath = "data/eval/retrieval_suite.json"
		}
		rc, err := rag.NewWithRedisFromEnv("")
		if err != nil {
			log.Fatalf("检索评测需要 Redis: %v", err)
		}
		sum, err := rag.RunRetrievalEval(ctx, rc, suitePath, *evalRetrievalVerbose)
		if err != nil {
			log.Fatalf("检索评测失败: %v", err)
		}
		for _, m := range sum.ByMode {
			log.Printf("检索评测 [%s] Hit@%d=%.2f%% (%d/%d)", m.Mode, m.K, m.HitRate*100, m.HitCount, m.Cases)
		}
		if p := strings.TrimSpace(*evalRetrievalJSONOut); p != "" {
			b, _ := json.MarshalIndent(sum, "", "  ")
			if err := os.WriteFile(p, b, 0644); err != nil {
				log.Fatalf("写入 %s: %v", p, err)
			}
			log.Printf("已写入 %s", p)
		}
		os.Exit(0)
	}

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

	sysTpl, promptVer, err := tools.GetResolvedPrompt()
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
			SuitePath:      suitePath,
			SystemTemplate: sysTpl,
			PromptVersion:  promptVer,
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

	if *evalIntentFlag {
		suitePath := strings.TrimSpace(*evalIntentSuite)
		if suitePath == "" {
			suitePath = tools.GetEvalDefaultIntentSuitePath()
		}
		if suitePath == "" {
			suitePath = "data/eval/intent_suite.json"
		}
		mode := strings.TrimSpace(*evalIntentMode)
		var predict intent.EvalPredictor
		switch mode {
		case "combo":
			predict = evalintent.PredictCombo()
		case "pipeline":
			predict = evalintent.PredictPipeline(chatModel)
		default:
			mode = "fc"
			predict = evalintent.PredictFC(chatModel)
		}
		sum, err := intent.RunEvalWithPredictor(ctx, suitePath, mode, predict)
		if err != nil {
			log.Fatalf("意图评测失败: %v", err)
		}
		axisPart := "compare_axis=n/a"
		if sum.CompareAxisCases > 0 {
			axisPart = fmt.Sprintf("compare_axis=%.2f%%（n=%d）", sum.CompareAxisAccuracy*100, sum.CompareAxisCases)
		}
		log.Printf("意图评测 suite=%s mode=%s evaluatedAt=%s 用例数=%d task=%.2f%% symbols=%.2f%%（n=%d） hints=%.2f%%（n=%d） %s 均分=%.2f",
			sum.SuitePath, sum.Mode, sum.EvaluatedAt, sum.Total,
			sum.TaskAccuracy*100, sum.SymbolAccuracy*100, sum.SymbolCases,
			sum.HintAccuracy*100, sum.HintCases, axisPart, sum.AverageScore)
		for _, b := range sum.ByExpectedTask {
			log.Printf("  └─ 期望 task_kind=%s: %.2f%% (%d/%d)", b.ExpectedKind, b.Accuracy*100, b.TaskHits, b.Count)
		}
		if *evalIntentVerbose {
			for _, r := range sum.Results {
				if r.Score >= 1 {
					continue
				}
				log.Printf("[eval-intent] id=%s score=%.2f task=%v syms=%v hints=%v axis=%v notes=%s",
					r.ID, r.Score, r.OKTask, r.OKSymbols, r.OKHints, r.OKCompareAxis, r.Notes)
			}
		}
		if p := strings.TrimSpace(*evalIntentJSONOut); p != "" {
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
		Description: "股票分析助手",
		Instruction: sysTpl,
		Model:       chatModel,
	}
	agentConfig.ToolsConfig = adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools.StockTools(),
		},
	}

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
		handerChat(w, r, runner, chatModel)
	})
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
	http.HandleFunc("/api/rag/search", handleRAGSearch)

	// 定时任务：每小时拉取新闻写入 RAG（Redis 未配置时自动跳过）
	go runRAGTicker()

	if tools.IntentEasyRulesEnabled() {
		if err := easyrules.Load(tools.IntentRulesFilePath()); err != nil {
			log.Printf("[easyrules] 初始加载失败（仅槽位组合生效）: %v", err)
		}
	}

	// 提供静态文件（HTML 页面）
	http.Handle("/", http.FileServer(http.Dir("./static")))
	log.Println("Server started")
	log.Fatal(http.ListenAndServe(":8080", nil))
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

func handerChat(w http.ResponseWriter, r *http.Request, runner *adk.Runner, parseModel intent.ParseModel) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析用户消息（可扩展：symbol、mode、context、session_history、workspace、memory）
	var req struct {
		Message        string `json:"message"`
		Symbol         string `json:"symbol"`  // 可选：当前分析标的
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
	memoryContent := req.Memory
	extraCtx := req.Context
	if req.Symbol != "" {
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	matchText := strings.TrimSpace(req.Message)
	if u := strings.TrimSpace(userMessage); u != "" {
		if matchText == "" {
			matchText = u
		} else if u != matchText {
			matchText = matchText + "\n" + u
		}
	}
	um := strings.TrimSpace(userMessage)
	if um == "" {
		um = matchText
	}

	// 查询改写：知识库槽位 → 自然语言规范句（NLQueryRewrite）；FC 另见 KBContext（Few-shot 等）。
	aug := queryaug.Build(r.Context(), um, req.SessionHistory, req.Symbol)
	nlRW := combo.NLQueryRewrite(um, aug.Slots)
	log.Println("nlRW", nlRW)
	skipFC := aug.ParsedCombo != nil && combo.ShouldSkipFC(aug.ParsedCombo, aug.Slots)

	//pipelineTiming 管道时间统计
	pt := observ.PipelineTiming{
		IntentSlotMs:  aug.SlotMatchMs,  //词典倒排时间
		IntentRulesMs: aug.ComboRulesMs, //规则引擎时间
		RetrieveMs:    aug.RetrieveMs,   //向量检索时间
		RerankMs:      aug.RerankMs,     //重排时间
	}
	var tokenAcc *schema.TokenUsage //token用量统计

	var parsed *intent.ParsedIntent
	if skipFC {
		parsed = aug.ParsedCombo
	} else {
		tFC := time.Now()
		var u *schema.TokenUsage
		umForIntent := intent.MergeRewrittenAndOriginal(um, nlRW)
		parsed, u = intent.ParseWithUsage(r.Context(), parseModel, intent.ParseInput{
			UserMessage:    umForIntent,
			SessionHistory: req.SessionHistory,
			ExplicitSymbol: req.Symbol,
			KBContext:      aug.Block,
		})
		tokenAcc = observ.MergeTokenUsage(tokenAcc, u)
		pt.IntentFCMs = time.Since(tFC).Milliseconds()
	}

	if parsed != nil && len(parsed.Symbols) == 0 {
		if aug.Slots.SymbolCode != "" {
			parsed.Symbols = intent.NormalizeSymbols([]string{aug.Slots.SymbolCode})
		}
		if len(parsed.Symbols) == 0 {
			if c := kb.TopStockCodeFromHits(aug.Hits); c != "" {
				parsed.Symbols = intent.NormalizeSymbols([]string{c})
			}
		}
	}

	sym := strings.TrimSpace(req.Symbol)
	if sym == "" && parsed != nil && len(parsed.Symbols) > 0 {
		sym = parsed.Symbols[0]
	}
	if sym == "" {
		sym = kb.TopStockCodeFromHits(aug.Hits)
	}
	var hints []string
	if parsed != nil {
		hints = parsed.SkillHints
	}
	prefetchSyms := []string{}
	if parsed != nil {
		prefetchSyms = append(prefetchSyms, parsed.Symbols...)
	}
	if len(prefetchSyms) == 0 && sym != "" {
		prefetchSyms = []string{sym}
	}
	log.Println("parsed", parsed.SkillHints, parsed.Symbols, prefetchSyms)
	tPrefetch := time.Now()
	if block := tools.RunSkillHintsTools(r.Context(), prefetchSyms, um, hints); block != "" {
		if extraCtx != "" {
			extraCtx += "\n\n"
		}
		extraCtx += "##请据此归纳回答用户，避免与事实矛盾；勿编造未出现的数字。\n\n" + block
	}
	pt.PrefetchMs = time.Since(tPrefetch).Milliseconds()
	//根据意图加载对应skills文档，构建上下文
	// matchText = intent.EnrichMatchText(matchText, parsed)
	// if parsed != nil {
	// 	log.Printf("[intent] kind=%s symbols=%v axis=%s source=%s", parsed.TaskKind, parsed.Symbols, parsed.CompareAxis, parsed.Source)
	// }
	// skillsRoot := filepath.Join(".", "skills")
	// skillPaths := loadMatchedSkillPaths(skillsRoot, matchText)
	// skillsContent := prompt.LoadSkillsContent(skillPaths)

	tCtx := time.Now()
	contextBlock := prompt.BuildContext(prompt.ContextInput{
		SessionHistory: req.SessionHistory,
		Workspace:      req.Workspace,
		Memory:         memoryContent,
		MarketContext:  "",
		NewsContext:    "",
		Skills:         "",
		Extra:          extraCtx,
	})
	pt.ContextMs = time.Since(tCtx).Milliseconds()

	//如果规则库太小，使用原话更好
	newMsg := intent.UserMessageWithNLRewrite(um, nlRW, parsed)
	fmt.Println("newMsg", newMsg)
	messages := []*schema.Message{schema.UserMessage(newMsg)}
	tGen := time.Now()
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
					fullReply.WriteString(msg.Content)
					writeSSEData("message", msg.Content)
					flusher.Flush()
				}
				if msg != nil {
					tokenAcc = observ.AppendMessageUsage(tokenAcc, msg)
				}
			}
		} else if out.Message != nil {
			tokenAcc = observ.AppendMessageUsage(tokenAcc, out.Message)
			if out.Message.Content != "" {
				fullReply.WriteString(out.Message.Content)
				writeSSEData("message", out.Message.Content)
				flusher.Flush()
			}
		}
	}
	pt.GenerateMs = time.Since(tGen).Milliseconds()

	observ.LogPipeline("[chat]", skipFC, pt, tokenAcc)
	writeSSEData("metrics", observ.MetricsJSON(skipFC, pt, tokenAcc))
	flusher.Flush()

	// 若有 symbol 且本轮有回复，写入 memory/stock/<symbol>/<date>.md
	if req.Symbol != "" && fullReply.Len() > 0 {
		_ = memory.WriteStockMemory(req.Symbol, "", fullReply.String())
	}
	fmt.Fprintf(w, "event: done\ndata: \n\n")
	flusher.Flush()
}
