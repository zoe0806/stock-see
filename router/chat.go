package router

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"stock-see/intent"
	"stock-see/intent/combo"
	"stock-see/intent/queryaug"
	"stock-see/memory"
	"stock-see/prompt"
	"stock-see/tools"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
)

// chatRequest /chat 请求体
type chatRequest struct {
	Message        string `json:"message" form:"message"`
	Symbol         string `json:"symbol" form:"symbol"`
	SessionID      string `json:"session_id" form:"session_id"`
	SessionHistory string `json:"session_history" form:"session_history"`
	Memory         string `json:"memory" form:"memory"`
	Context        string `json:"context" form:"context"`
	Workspace      string `json:"workspace" form:"workspace"`
}

func bindChatRequest(c *gin.Context) (chatRequest, error) {
	var req chatRequest
	ct := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.Contains(ct, "application/json") {
		if err := c.ShouldBindJSON(&req); err != nil {
			return req, err
		}
		return req, nil
	}
	if err := c.ShouldBind(&req); err != nil {
		return req, err
	}
	return req, nil
}

func handerChat(c *gin.Context, runner *adk.Runner, parseModel intent.ParseModel) {
	req, err := bindChatRequest(c)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid request: %v", err)
		return
	}

	userMessage := strings.TrimSpace(req.Message)
	memoryContent := req.Memory
	extraCtx := req.Context

	effectiveSym := strings.TrimSpace(req.Symbol)
	if effectiveSym == "" {
		effectiveSym = tools.Get(strings.TrimSpace(req.SessionID))
		fmt.Println("effectiveSym", effectiveSym)
		fmt.Println("req.SessionID", req.SessionID)
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
		fmt.Fprintf(c.Writer, "event: %s\n", event)
		for _, line := range strings.Split(data, "\n") {
			fmt.Fprintf(c.Writer, "data: %s\n", line)
		}
		fmt.Fprint(c.Writer, "\n")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.String(http.StatusInternalServerError, "Streaming unsupported")
		return
	}
	um := strings.TrimSpace(req.Message)
	if u := strings.TrimSpace(userMessage); u != "" {
		if um == "" {
			um = u
		} else if u != um {
			um = um + "\n" + u
		}
	}

	// 查询改写：知识库槽位&规则引擎 → 自然语言规范句（NLQueryRewrite）；FC 另见 KBContext（Few-shot 等）。
	aug := queryaug.Build(c.Request.Context(), um, req.SessionHistory, effectiveSym)
	comboRW := combo.NLQueryRewrite(um, aug.Slots, effectiveSym)
	skipFC := aug.ParsedCombo != nil && combo.ShouldSkipFC(aug.ParsedCombo, aug.Slots)
	fmt.Println("skipFC", skipFC, aug.ParsedCombo, combo.ShouldSkipFC(aug.ParsedCombo, aug.Slots))

	//pipelineTiming 管道时间统计
	pt := tools.PipelineTiming{
		IntentSlotMs:  aug.SlotMatchMs,  //词典倒排时间
		IntentRulesMs: aug.ComboRulesMs, //规则引擎时间
		RetrieveMs:    aug.RetrieveMs,   //向量检索时间
		RerankMs:      aug.RerankMs,     //重排时间
	}
	var tokenAcc *schema.TokenUsage //token用量统计

	sid := strings.TrimSpace(req.SessionID)
	pendingIntent := tools.GetPendingIntent(sid)

	var parsed *intent.ParsedIntent
	if skipFC {
		parsed = aug.ParsedCombo
	} else {
		//走FC 调用模型
		tFC := time.Now()
		var u *schema.TokenUsage
		pendingCtx := ""
		if pendingIntent != nil {
			pendingCtx = intent.FormatPendingForParse(pendingIntent)
		}
		// 意图 FC 自行产出 nl_rewritten；此处不预拼词典改写，避免覆盖多轮语义
		parsed, u = intent.ParseWithUsage(c.Request.Context(), parseModel, intent.ParseInput{
			UserMessage:     um,
			SessionHistory:  req.SessionHistory,
			ExplicitSymbol:  effectiveSym,
			KBContext:       aug.Block,
			PendingFollowUp: pendingCtx,
		})
		tokenAcc = tools.MergeTokenUsage(tokenAcc, u)
		pt.IntentFCMs = time.Since(tFC).Milliseconds()
	}

	if parsed != nil && len(parsed.Symbols) == 0 {
		if aug.Slots.SymbolCode != "" {
			parsed.Symbols = intent.NormalizeSymbols([]string{aug.Slots.SymbolCode})
		}
	}

	sym := effectiveSym
	if sym == "" && parsed != nil && len(parsed.Symbols) > 0 {
		sym = parsed.Symbols[0]
	}

	// 澄清后只补股票名：继承上一轮 fundamental 等，避免 combo 判成 quick_look
	intent.ApplySessionFollowUp(um, parsed, pendingIntent)

	nlRW := ""
	if intent.FCUsedNLRewrite(parsed) {
		nlRW = intent.EffectiveNLRewrite(um, comboRW, parsed)
	} else {
		nlRW = comboRW
	}
	if strings.TrimSpace(nlRW) == "" && parsed != nil && strings.TrimSpace(parsed.NLRewritten) != "" {
		nlRW = strings.TrimSpace(parsed.NLRewritten)
	}

	if sid != "" && sym != "" {
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
	log.Println("parsed：", parsed.SkillHints, ",Symbols:", parsed.Symbols, prefetchSyms)

	// need_clarify 或者没有标的：直接返回追问，并保存待续意图供下轮「只报名字」时接续
	if intent.ShouldStopForClarify(parsed, sym) {
		intent.SavePendingOnClarify(sid, um, parsed)
		reply := intent.ClarifyReplyText(parsed)
		log.Printf("[chat] need_clarify early return: %q", reply)
		writeSSEData("message", reply)
		pt.PrefetchMs = 0
		pt.ContextMs = 0
		pt.GenerateMs = 0
		tools.LogPipeline("[chat]", skipFC, pt, tokenAcc)
		writeSSEData("metrics", tools.MetricsJSON(skipFC, pt, tokenAcc))
		flusher.Flush()
		fmt.Fprintf(c.Writer, "event: done\ndata: \n\n")
		flusher.Flush()
		return
	}

	tPrefetch := time.Now()
	pref := tools.RunSkillHintsTools(c.Request.Context(), prefetchSyms, um, hints)
	if strings.TrimSpace(pref.ContextMarkdown) != "" {
		if extraCtx != "" {
			extraCtx += "\n\n"
		}
		extraCtx += "## 请据此归纳回答用户，避免与事实矛盾；勿编造未出现的数字。\n\n" + pref.ContextMarkdown
	}
	pt.PrefetchMs = time.Since(tPrefetch).Milliseconds()

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
	iterator := runner.Run(c.Request.Context(), messages, adk.WithSessionValues(map[string]any{
		"Context": contextBlock, //注入系统提示词{Context}
	}))

	var fullReply strings.Builder
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			fmt.Fprintf(c.Writer, "event: error\ndata: %v\n\n", event.Err)
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
					fmt.Fprintf(c.Writer, "event: error\ndata: %v\n\n", err)
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
		tools.ClearPendingIntent(sid)
	}
	fmt.Fprintf(c.Writer, "event: done\ndata: \n\n")
	flusher.Flush()
}
