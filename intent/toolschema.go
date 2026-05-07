package intent

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

const submitParsedIntentToolName = "submit_parsed_intent"

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
			"confidence": {
				Type: schema.Number,
				Desc: "0~1 置信度",
			},
		}),
	}
}
