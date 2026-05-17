package router

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"stock-see/intent"
	"stock-see/intent/combo"
	"stock-see/intent/queryaug"
	"stock-see/kb"
	"stock-see/memory"
	"stock-see/prompt"
	"stock-see/tools"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func handerChat(w http.ResponseWriter, r *http.Request, runner *adk.Runner, parseModel intent.ParseModel) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析用户消息（可扩展：symbol、mode、context、session_history、workspace、memory）
	var req struct {
		Message        string `json:"message"`
		Symbol         string `json:"symbol"` // 可选：当前分析标的（六位代码即可，无需传整段 Memory）
		SessionID      string `json:"session_id"`
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

	effectiveSym := strings.TrimSpace(req.Symbol)
	if effectiveSym == "" {
		effectiveSym = tools.Get(strings.TrimSpace(req.SessionID))
	}

	if effectiveSym != "" {
		if memoryContent == "" {
			memoryContent = memory.FormatMemoryWithLastReport(effectiveSym)
			if memoryContent == "" {
				memoryContent, _ = memory.ReadStockMemory(effectiveSym, "")
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
	aug := queryaug.Build(r.Context(), um, req.SessionHistory, effectiveSym)
	nlRW := combo.NLQueryRewrite(um, aug.Slots, effectiveSym)
	log.Println("nlRW", nlRW)
	skipFC := aug.ParsedCombo != nil && combo.ShouldSkipFC(aug.ParsedCombo, aug.Slots)

	//pipelineTiming 管道时间统计
	pt := tools.PipelineTiming{
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
			ExplicitSymbol: effectiveSym,
			KBContext:      aug.Block,
		})
		tokenAcc = tools.MergeTokenUsage(tokenAcc, u)
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

	sym := effectiveSym
	if sym == "" && parsed != nil && len(parsed.Symbols) > 0 {
		sym = parsed.Symbols[0]
	}
	if sym == "" {
		sym = kb.TopStockCodeFromHits(aug.Hits)
	}
	if sid := strings.TrimSpace(req.SessionID); sid != "" && sym != "" {
		if ns := intent.NormalizeSymbols([]string{sym}); len(ns) > 0 {
			tools.Put(sid, ns[0])
		}
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
	pref := tools.RunSkillHintsTools(r.Context(), prefetchSyms, um, hints)
	if strings.TrimSpace(pref.ContextMarkdown) != "" {
		if extraCtx != "" {
			extraCtx += "\n\n"
		}
		extraCtx += "## 请据此归纳回答用户，避免与事实矛盾；勿编造未出现的数字。\n\n" + pref.ContextMarkdown
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
	//动态构造上下文（系统提示词{Context}）
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

	// 基本面完整预取只附在用户消息末段一次，避免与 System Context（Extra）重复
	newMsg := intent.UserMessageWithNLRewrite(um, nlRW, parsed)
	if strings.TrimSpace(pref.FundamentalForUser) != "" {
		newMsg += "\n\n---\n## 预取\n\n" + strings.TrimSpace(pref.FundamentalForUser)
	}
	//log.Println("newMsg", newMsg)

	messages := []*schema.Message{schema.UserMessage(newMsg)} //该轮对话用户消息

	//生成回复
	tGen := time.Now()
	iterator := runner.Run(r.Context(), messages, adk.WithSessionValues(map[string]any{
		"Context": contextBlock, //注入系统提示词{Context}
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
					tokenAcc = tools.AppendMessageUsage(tokenAcc, msg)
				}
			}
		} else if out.Message != nil {
			tokenAcc = tools.AppendMessageUsage(tokenAcc, out.Message)
			if out.Message.Content != "" {
				fullReply.WriteString(out.Message.Content)
				writeSSEData("message", out.Message.Content)
				flusher.Flush()
			}
		}
	}
	pt.GenerateMs = time.Since(tGen).Milliseconds()

	tools.LogPipeline("[chat]", skipFC, pt, tokenAcc)
	writeSSEData("metrics", tools.MetricsJSON(skipFC, pt, tokenAcc))
	flusher.Flush()

	// 若有解析出的标的且本轮有回复，写入 memory/stock/<symbol>/<date>.md
	if sym != "" && fullReply.Len() > 0 {
		if ns := intent.NormalizeSymbols([]string{sym}); len(ns) > 0 {
			_ = memory.WriteStockMemory(ns[0], "", fullReply.String())
		}
	}
	fmt.Fprintf(w, "event: done\ndata: \n\n")
	flusher.Flush()
}
