package intent

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

const submitParsedIntentToolName = "submit_parsed_intent"

// 意图解析默认文案；可被 config/prompt/<version>/intent_parse_system.md 与 intent_tool_desc.md 覆盖。
const DefaultIntentParseSystem = `你是证券助手意图解析模块。只根据用户输入（及可选会话摘要、客户端已选代码）调用 submit_parsed_intent，不要输出自然语言。
沪深股票为六位数字代码。
task_kind 表示「任务类型」：quick_look / compare / trend / news_focus / deep_analysis / general / need_clarify / off_topic。
skill_hints 表示「要拉取/侧重的分析维度」（与 skills 目录名一致），用于后端工具预取；**单维度任务必须在 skill_hints 里包含与 task_kind 对应的一项**，例如新闻侧重填 "news"；轻量查价填 "realtime-market"。deep_analysis 时 skill_hints 可列多个维度。
nl_rewritten 必填（need_clarify/off_topic 可留空）：一句规范中文问句，写清标的与要问什么；**结合会话摘要合并多轮**（例如上轮「它的行情」、本轮「光迅科技」→「查询光迅科技最新行情」）。
用户只说「帮我看看」「分析一下」且未给代码与标的时，可用 symbol_names 或 need_clarify。`

const DefaultSubmitParsedIntentToolDesc = `解析用户与股票分析相关的意图，必须调用本工具提交结构化结果。
规则：
- task_kind 必须选枚举之一。
- symbols 为沪深 A 股六位代码（可多）；若用户只说中文名可放在 symbol_names。
- quick_look：只要现价、涨跌、成交量、开盘收盘等轻量行情，一句问完即可；不要标成 deep_analysis。
- compare：对比两只及以上股票（估值/营收/涨跌等）；compare_axis 表示主要对比维度。
- trend：多年营收/利润/走势、近三年等时间跨度。
- news_focus：明显侧重新闻、公告、舆情。
- need_clarify：缺少标的或歧义大；clarify_prompt 写一句简短追问。
- deep_analysis：全面/深度/多维度分析；skill_hints 含 realtime-market、scoring。
- general：泛问投资概念、方法、板块筛选思路等，无具体标的或不要求查某只股票。
- off_topic：与股票分析无关。
- nl_rewritten：给主对话用的规范问句；须与 task_kind、skill_hints 一致，并体现多轮上下文。
`

// SubmitParsedIntentToolInfo 供模型 Function Calling；desc 来自 config 中 intent_tool_desc.md，空则用 DefaultSubmitParsedIntentToolDesc。
func SubmitParsedIntentToolInfo(desc string) *schema.ToolInfo {
	if strings.TrimSpace(desc) == "" {
		desc = DefaultSubmitParsedIntentToolDesc
	}
	return &schema.ToolInfo{
		Name: submitParsedIntentToolName,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"task_kind": {
				Type:     schema.String,
				Desc:     "任务类型（与 skill_hints 不同）；单维度用 fundamental/technical/sentiment/sector 时 skill_hints 须含同名维度",
				Required: true,
				Enum: []string{
					string(TaskQuickLook),
					string(TaskDeepAnalysis),
					string(TaskCompare),
					string(TaskTrend),
					string(TaskNewsFocus),
					string(TaskGeneral),
					string(TaskNeedClarify),
					string(TaskOffTopic),
					string(TaskFundamental),
					string(TaskTechnical),
					string(TaskSentiment),
					string(TaskSector),
				},
			},
			"symbols": {
				Type: schema.Array,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.String,
					Desc: "六位股票代码",
				},
				Desc: "涉及的股票代码列表，可为空",
			},
			"symbol_names": {
				Type: schema.Array,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.String,
					Desc: "中文简称如 茅台",
				},
				Desc: "股票中文名（无代码时）",
			},
			"time_hint": {
				Type: schema.String,
				Desc: "时间范围描述，如：近三年、2023年、最近一季度",
			},
			"compare_axis": {
				Type: schema.String,
				Desc: "对比维度；不明确时用 general",
				Enum: []string{"pe", "pb", "price", "revenue", "profit", "roe", "general"},
			},
			"skill_hints": {
				Type: schema.Array,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.String,
					Desc: "技能目录名",
				},
				Desc: "分析维度（目录名）；单维度任务须含与 task_kind 对应项，如 task_kind=technical 则含 technical",
			},
			"clarify_prompt": {
				Type: schema.String,
				Desc: "task_kind 为 need_clarify 时给用户的追问",
			},
			"nl_rewritten": {
				Type: schema.String,
				Desc: "规范后的完整用户问句（必填，除 need_clarify/off_topic 外）：须写清标的（名称+代码若已知）、任务与维度，如「查询光迅科技（002281）最新行情」。多轮时合并会话意图：上轮问「它的行情」、本轮仅答「光迅科技」时应写成「查询光迅科技最新行情」而非仅股票名。quick_look 须体现行情/现价；fundamental 体现基本面；勿写成全面深度分析除非 task_kind=deep_analysis。",
			},
			"confidence": {
				Type: schema.Number,
				Desc: "0~1 置信度",
			},
		}),
	}
}
