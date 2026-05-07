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
)

const disableIntentEnv = "STOCK_INTENT_DISABLE"

// ParseModel 封装 *openai.ChatModel，手动调用WithTools + Generate，统一管理模型的工具调用能力，并为依赖注入提供便利。
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

	//将SubmitParsedIntentToolInfo注入到模型中
	tcm, err := cm.WithTools([]*schema.ToolInfo{SubmitParsedIntentToolInfo()})
	if err != nil {
		return nil
	}

	sys := `你是证券助手意图解析模块。只根据用户输入（及可选会话摘要、客户端已选代码）调用 submit_parsed_intent，不要输出自然语言。
沪深股票为六位数字代码。仅查价/涨跌/成交量用 quick_look；全面深度分析用 deep_analysis；投资通识无具体标的用 general。
对比两只及以上用 compare；多年财务/营收趋势用 trend；侧重新闻公告用 news_focus。
用户只说「帮我看看」「分析一下」且未给代码与标的时，可用 symbol_names 或 need_clarify。`

	msgs := []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(userText),
	}

	//调用模型生成意图，按需输出工具调用请求，实现 Function Calling
	resp, err := tcm.Generate(ctx, msgs,
		model.WithToolChoice(schema.ToolChoiceForced, submitParsedIntentToolName), //强制模型必须调用submitParsedIntentToolName工具（不能输出普通文本）
		model.WithTemperature(0.1), //控制输出文本的随机性/创造性,数值越小，越确定、保守。
		model.WithMaxTokens(512),   //最大token数
	)
	if err != nil || resp == nil {
		return nil
	}

	args := firstSubmitParsedIntentArgs(resp.ToolCalls) //从模型输出中提取 submit_parsed_intent 工具调用的参数（JSON 字符串）
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
	log.Println("Parse args", args, in.UserMessage, in.ExplicitSymbol)
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
	//校验并规范化意图
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
