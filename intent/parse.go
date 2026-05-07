package intent

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const disableIntentEnv = "STOCK_INTENT_DISABLE"

// ParseModel 需支持 WithTools + Generate（与 *openai.ChatModel 一致）。
type ParseModel interface {
	WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error)
}

// Parse 使用强制 Function Calling 解析意图；失败或未配置时返回 nil。
func Parse(ctx context.Context, cm ParseModel, in ParseInput) *ParsedIntent {
	if strings.TrimSpace(os.Getenv(disableIntentEnv)) == "1" || cm == nil {
		return nil
	}
	userText := buildUserContent(in)
	if userText == "" {
		return nil
	}

	tcm, err := cm.WithTools([]*schema.ToolInfo{SubmitParsedIntentToolInfo()})
	if err != nil {
		return nil
	}

	sys := `你是证券助手意图解析模块。只根据用户输入（及可选会话摘要）调用 submit_parsed_intent，不要输出自然语言。
沪深股票为六位数字代码。对比两只股票用 compare；多年财务/营收趋势用 trend；侧重新闻用 news_focus。
用户只说「帮我看看」「分析一下」且未给代码时，可用 symbol_names 或 need_clarify。`

	msgs := []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(userText),
	}

	resp, err := tcm.Generate(ctx, msgs,
		model.WithToolChoice(schema.ToolChoiceForced, submitParsedIntentToolName),
		model.WithTemperature(0.1),
		model.WithMaxTokens(512),
	)
	if err != nil || resp == nil {
		return nil
	}

	args := firstSubmitParsedIntentArgs(resp.ToolCalls)
	if args == "" {
		return nil
	}

	var raw struct {
		TaskKind       string   `json:"task_kind"`
		Symbols        []string `json:"symbols"`
		SymbolNames    []string `json:"symbol_names"`
		TimeHint       string   `json:"time_hint"`
		CompareAxis    string   `json:"compare_axis"`
		SkillHints     []string `json:"skill_hints"`
		NeedFullReport bool     `json:"need_full_report"`
		ClarifyPrompt  string   `json:"clarify_prompt"`
		Confidence     float64  `json:"confidence"`
	}
	if err := sonic.UnmarshalString(args, &raw); err != nil {
		return nil
	}

	p := &ParsedIntent{
		TaskKind:       TaskKind(strings.TrimSpace(raw.TaskKind)),
		Symbols:        raw.Symbols,
		SymbolNames:    raw.SymbolNames,
		TimeHint:       strings.TrimSpace(raw.TimeHint),
		CompareAxis:    strings.TrimSpace(raw.CompareAxis),
		SkillHints:     raw.SkillHints,
		NeedFullReport: raw.NeedFullReport,
		ClarifyPrompt:  strings.TrimSpace(raw.ClarifyPrompt),
		Confidence:     raw.Confidence,
		Source:         "llm_tool",
	}
	ValidateAndPatch(p, in.UserMessage)
	MergeExplicitSymbol(p, in.ExplicitSymbol)
	return p
}

func buildUserContent(in ParseInput) string {
	var parts []string
	if u := strings.TrimSpace(in.UserMessage); u != "" {
		parts = append(parts, "【当前用户输入】\n"+u)
	}
	if h := strings.TrimSpace(in.SessionHistory); h != "" {
		if len(h) > 2000 {
			h = h[:2000] + "…"
		}
		parts = append(parts, "【会话摘要】\n"+h)
	}
	if s := strings.TrimSpace(in.ExplicitSymbol); s != "" {
		parts = append(parts, "【客户端已选标的代码】\n"+s)
	}
	return strings.Join(parts, "\n\n")
}

func firstSubmitParsedIntentArgs(calls []schema.ToolCall) string {
	for _, tc := range calls {
		if tc.Function.Name == submitParsedIntentToolName && tc.Function.Arguments != "" {
			return tc.Function.Arguments
		}
	}
	return ""
}

// DebugJSON 便于日志输出。
func DebugJSON(p *ParsedIntent) string {
	if p == nil {
		return ""
	}
	b, _ := json.Marshal(p)
	return string(b)
}
