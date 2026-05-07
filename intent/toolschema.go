package intent

import "github.com/cloudwego/eino/schema"

const submitParsedIntentToolName = "submit_parsed_intent"

// SubmitParsedIntentToolInfo 供模型 Function Calling；参数即 ParsedIntent 子集。
func SubmitParsedIntentToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: submitParsedIntentToolName,
		Desc: `解析用户与股票分析相关的意图，必须调用本工具提交结构化结果。
规则：
- task_kind 必须选枚举之一。
- symbols 为沪深 A 股六位代码（可多）；若用户只说中文名可放在 symbol_names。
- quick_look：只要现价、涨跌、成交量、开盘收盘等轻量行情，一句问完即可；不要标成 deep_analysis。
- compare：对比两只及以上股票（估值/营收/涨跌等）；compare_axis 表示主要对比维度。
- trend：多年营收/利润/走势、近三年等时间跨度。
- news_focus：明显侧重新闻、公告、舆情。
- need_clarify：缺少标的或歧义大；clarify_prompt 写一句简短追问。
- fundamental / technical / sentiment / sector：用户**只要该单一维度**时用对应 task_kind，且 **skill_hints 必须包含同名项**（fundamental、technical、sentiment、sector）。
- deep_analysis：全面/深度/多维度分析；skill_hints 建议列全维度（含 realtime-market、risk、scoring 等）。
- general：泛问投资概念、方法、板块筛选思路等，无具体标的或不要求查某只股票。
- off_topic：与股票分析无关。
- skill_hints：与后端工具对应；单维度任务必填对应一项（如 technical）；可多选。可选值含 technical、fundamental、news、sentiment、market-trend、sector、pattern、risk、scoring、realtime-market 等。`,
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
			"need_full_report": {
				Type:     schema.Boolean,
				Desc:     "用户是否明确要求全面/综合/深度报告（非简单查价）",
				Required: false,
			},
			"clarify_prompt": {
				Type: schema.String,
				Desc: "task_kind 为 need_clarify 时给用户的追问",
			},
			"confidence": {
				Type: schema.Number,
				Desc: "0~1 置信度",
			},
		}),
	}
}
