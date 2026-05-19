package intent

import (
	"regexp"
	"sort"
	"strings"

	"stock-see/tools"
)

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
	TaskKind      TaskKind `json:"task_kind"`
	Symbols       []string `json:"symbols"`                  // 6 位 A 股代码，可多标
	SymbolNames   []string `json:"symbol_names,omitempty"`   // 中文简称，辅助检索
	TimeHint      string   `json:"time_hint,omitempty"`      // 如：近三年、2023年
	CompareAxis   string   `json:"compare_axis,omitempty"`   // pe/pb/price/revenue/profit/roe/general
	SkillHints    []string `json:"skill_hints,omitempty"`    // 技能目录名：technical、news 等
	ClarifyPrompt string   `json:"clarify_prompt,omitempty"` // 建议追问用户的简短话术
	// NLRewritten 意图 FC 产出的规范用户问句（含标的+任务维度）；非空时主对话优先使用，不再用词典 NLQueryRewrite 覆盖。
	NLRewritten string  `json:"nl_rewritten,omitempty"`
	Confidence  float64 `json:"confidence,omitempty"`

	// Source 取值：llm_tool / keyword_fallback / merge_explicit
	Source string `json:"-"`
	Valid  bool   `json:"-"`
}

// ParseInput 单次解析入参。
type ParseInput struct {
	UserMessage    string
	SessionHistory string
	ExplicitSymbol string // HTTP 请求里携带的 symbol，合并进槽位
	// KBContext 知识库查询改写统一块：词典结构化 + 向量片段 + Few-shot（见 intent/queryaug）。
	KBContext string
	// PendingFollowUp 会话待续意图说明（澄清后用户补股票名）。
	PendingFollowUp string
}

var reSixDigits = regexp.MustCompile(`\b(\d{6})\b`)

var allowedTask = map[TaskKind]struct{}{
	TaskQuickLook:    {},
	TaskDeepAnalysis: {},
	TaskCompare:      {},
	TaskTrend:        {},
	TaskNewsFocus:    {},
	TaskGeneral:      {},
	TaskNeedClarify:  {},
	TaskOffTopic:     {},
	TaskFundamental:  {},
	TaskTechnical:    {},
	TaskSentiment:    {},
	TaskSector:       {},
}

var allowedCompareAxis = map[string]struct{}{
	"":        {},
	"pe":      {},
	"pb":      {},
	"price":   {},
	"revenue": {},
	"profit":  {},
	"roe":     {},
	"general": {},
}

// NormalizeSymbols 去重、排序，仅保留 6 位数字。
func NormalizeSymbols(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if len(s) == 6 && DigitsOnly(s) {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func DigitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ExtractSymbolsFromText 从用户原文补抽 A 股六位代码。
func ExtractSymbolsFromText(text string) []string {
	raw := reSixDigits.FindAllString(text, -1)
	return NormalizeSymbols(raw)
}

// ValidateAndPatch 校验枚举与代码格式；必要时用原文补抽代码；标记 Valid。
func ValidateAndPatch(p *ParsedIntent, userMessage string) {
	if p == nil {
		return
	}
	if _, ok := allowedTask[p.TaskKind]; !ok {
		p.TaskKind = TaskGeneral
	}
	if _, ok := allowedCompareAxis[strings.ToLower(strings.TrimSpace(p.CompareAxis))]; !ok {
		p.CompareAxis = "general"
	} else {
		p.CompareAxis = strings.ToLower(strings.TrimSpace(p.CompareAxis))
	}
	p.Symbols = NormalizeSymbols(p.Symbols)
	if len(p.Symbols) == 0 {
		p.Symbols = ExtractSymbolsFromText(userMessage)
	}
	if len(p.Symbols) == 0 && len(p.SymbolNames) > 0 {
		p.Symbols = NormalizeSymbols(tools.LookupStockCodesFromNames(p.SymbolNames))
	}
	// 模型常把代码写在 nl_rewritten（如「三花智控（002050）」）却漏填 symbols
	if len(p.Symbols) == 0 {
		if rw := strings.TrimSpace(p.NLRewritten); rw != "" {
			p.Symbols = ExtractSymbolsFromText(rw)
		}
	}
	// 对比类至少两个代码或依赖名称（名称留给上层模型）
	if p.TaskKind == TaskCompare && len(p.Symbols) < 2 && len(p.SymbolNames) < 2 {
		if extra := ExtractSymbolsFromText(userMessage); len(extra) >= 2 {
			p.Symbols = NormalizeSymbols(extra)
		}
	}
	p.SkillHints = dedupeTrim(p.SkillHints)
	syncSkillHintsFromTaskKind(p)
	p.SkillHints = dedupeTrim(p.SkillHints)
	p.SymbolNames = dedupeTrim(p.SymbolNames)
	p.Valid = true
}

// syncSkillHintsFromTaskKind 模型常把单维度写在 task_kind 却漏填 skill_hints；后端预取工具依赖 skill_hints，此处按 task_kind 补一条主维度。
func syncSkillHintsFromTaskKind(p *ParsedIntent) {
	if p == nil {
		return
	}
	hint := primarySkillHintForTaskKind(p.TaskKind)
	if hint == "" {
		return
	}
	for _, h := range p.SkillHints {
		if strings.EqualFold(strings.TrimSpace(h), hint) {
			return
		}
	}
	p.SkillHints = append([]string{hint}, p.SkillHints...)
}

func primarySkillHintForTaskKind(k TaskKind) string {
	switch k {
	case TaskTechnical:
		return "technical"
	case TaskFundamental:
		return "fundamental"
	case TaskSentiment:
		return "sentiment"
	case TaskSector:
		return "sector"
	case TaskNewsFocus:
		return "news"
	case TaskQuickLook:
		return "realtime-market"
	default:
		return ""
	}
}

func dedupeTrim(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

// MergeExplicitSymbol 将请求体中的 symbol 并入槽位。
func MergeExplicitSymbol(p *ParsedIntent, explicit string) {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" || p == nil {
		return
	}
	if len(explicit) == 6 && DigitsOnly(explicit) {
		p.Symbols = NormalizeSymbols(append([]string{explicit}, p.Symbols...))
		p.Source = "merge_explicit"
	}
}
