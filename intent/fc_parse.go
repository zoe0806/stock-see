package intent

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"log"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"stock-see/tools"
)

const disableIntentEnv = "STOCK_INTENT_DISABLE"

// ParseModel 封装 *openai.ChatModel，手动调用WithTools + Generate，统一管理模型的工具调用能力，并为依赖注入提供便利。
type ParseModel interface {
	WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error)
}

// Parse 使用强制 Function Calling 解析意图；失败或未配置时返回 nil。
func Parse(ctx context.Context, cm ParseModel, in ParseInput) *ParsedIntent {
	p, _ := ParseWithUsage(ctx, cm, in)
	return p
}

// ParseWithUsage 意图解析，返回意图结构化结果和token用量
func ParseWithUsage(ctx context.Context, cm ParseModel, in ParseInput) (*ParsedIntent, *schema.TokenUsage) {
	if strings.TrimSpace(os.Getenv(disableIntentEnv)) == "1" || cm == nil {
		return nil, nil
	}
	userText := buildUserContent(in)
	if userText == "" {
		return nil, nil
	}

	sys, toolDesc, err := tools.ResolveIntentPrompts(DefaultIntentParseSystem, DefaultSubmitParsedIntentToolDesc)
	if err != nil {
		log.Printf("[intent] ResolveIntentPrompts: %v，使用内置默认", err)
		sys, toolDesc = DefaultIntentParseSystem, DefaultSubmitParsedIntentToolDesc
	}

	tcm, err := cm.WithTools([]*schema.ToolInfo{SubmitParsedIntentToolInfo(toolDesc)})
	if err != nil {
		return nil, nil
	}

	msgs := []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(userText),
	}
	//调用模型生成回复
	resp, err := tcm.Generate(ctx, msgs,
		model.WithToolChoice(schema.ToolChoiceForced, submitParsedIntentToolName),
		model.WithTemperature(0.1),
		model.WithMaxTokens(512),
	)
	if err != nil || resp == nil {
		return nil, nil
	}

	var usage *schema.TokenUsage
	if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
		u := *resp.ResponseMeta.Usage
		usage = &u
	}

	args := firstSubmitParsedIntentArgs(resp.ToolCalls)
	if args == "" {
		return nil, usage
	}

	var raw struct {
		TaskKind      string   `json:"task_kind"`
		Symbols       []string `json:"symbols"`
		SymbolNames   []string `json:"symbol_names"`
		TimeHint      string   `json:"time_hint"`
		CompareAxis   string   `json:"compare_axis"`
		SkillHints    []string `json:"skill_hints"`
		ClarifyPrompt string   `json:"clarify_prompt"`
		NLRewritten   string   `json:"nl_rewritten"`
		Confidence    float64  `json:"confidence"`
	}
	if err := sonic.UnmarshalString(args, &raw); err != nil {
		return nil, usage
	}
	log.Println("Parse args", args, in.UserMessage, in.ExplicitSymbol)
	p := &ParsedIntent{
		TaskKind:      TaskKind(strings.TrimSpace(raw.TaskKind)),
		Symbols:       raw.Symbols,
		SymbolNames:   raw.SymbolNames,
		TimeHint:      strings.TrimSpace(raw.TimeHint),
		CompareAxis:   strings.TrimSpace(raw.CompareAxis),
		SkillHints:    raw.SkillHints,
		ClarifyPrompt: strings.TrimSpace(raw.ClarifyPrompt),
		NLRewritten:   strings.TrimSpace(raw.NLRewritten),
		Confidence:    raw.Confidence,
		Source:        "llm_tool",
	}
	ValidateAndPatch(p, in.UserMessage)
	MergeExplicitSymbol(p, in.ExplicitSymbol)
	return p, usage
}

func buildUserContent(in ParseInput) string {
	var parts []string
	if k := strings.TrimSpace(in.KBContext); k != "" {
		parts = append(parts, "【知识库查询改写与结构化参考】\n"+k)
	}
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
	if p := strings.TrimSpace(in.PendingFollowUp); p != "" {
		parts = append(parts, "【待续上一轮意图】\n"+p)
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
