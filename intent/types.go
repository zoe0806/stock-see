// Package intent 提供多槽位用户意图解析（Function Calling）与评测。
package intent

// TaskKind 用户任务类型（与 submit_parsed_intent 工具枚举一致）。
type TaskKind string

const (
	TaskQuickLook    TaskKind = "quick_look"    // 看一眼行情/现价
	TaskDeepAnalysis TaskKind = "deep_analysis" // 多维深度分析
	TaskCompare      TaskKind = "compare"       // 多标的对比
	TaskTrend        TaskKind = "trend"         // 趋势/多年财务或营收
	TaskNewsFocus    TaskKind = "news_focus"    // 侧重新闻/消息面
	TaskGeneral      TaskKind = "general"       // 一般股票问答（默认）
	TaskNeedClarify  TaskKind = "need_clarify"  // 信息不足需澄清
	TaskOffTopic     TaskKind = "off_topic"     // 与个股分析无关
	TaskFundamental  TaskKind = "fundamental"   // 基本面分析
	TaskTechnical    TaskKind = "technical"     // 技术面分析
	TaskSentiment    TaskKind = "sentiment"     // 资金面分析
	TaskSector       TaskKind = "sector"        // 板块分析
)

// ParsedIntent 模型解析后的结构化意图（经校验、规范化）。
type ParsedIntent struct {
	TaskKind       TaskKind `json:"task_kind"`
	Symbols        []string `json:"symbols"`                // 6 位 A 股代码，可多标
	SymbolNames    []string `json:"symbol_names,omitempty"` // 中文简称，辅助检索
	TimeHint       string   `json:"time_hint,omitempty"`    // 如：近三年、2023年
	CompareAxis    string   `json:"compare_axis,omitempty"` // pe/pb/price/revenue/profit/roe/general
	SkillHints     []string `json:"skill_hints,omitempty"`  // 技能目录名：technical、news 等
	NeedFullReport bool     `json:"need_full_report"`
	ClarifyPrompt  string   `json:"clarify_prompt,omitempty"` // 建议追问用户的简短话术
	Confidence     float64  `json:"confidence,omitempty"`

	// Source 取值：llm_tool / keyword_fallback / merge_explicit
	Source string `json:"-"`
	Valid  bool   `json:"-"`
}

// ParseInput 单次解析入参。
type ParseInput struct {
	UserMessage    string
	SessionHistory string
	ExplicitSymbol string // HTTP 请求里携带的 symbol，合并进槽位
}
