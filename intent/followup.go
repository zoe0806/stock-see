package intent

import (
	"fmt"
	"strings"

	"stock-see/tools"
)

const defaultClarifyPrompt = "请说明要分析的股票名称或六位代码（例如：贵州茅台 / 600519）。"

// HasResolvableSymbol 本轮是否已有可分析标的（请求 symbol、会话缓存、解析槽位等合并后）。
func HasResolvableSymbol(symbol string) bool {
	return strings.TrimSpace(symbol) != ""
}

// ShouldStopForClarify 意图为 need_clarify 或者没有标的，应直接追问用户，不再走工具与主对话。
func ShouldStopForClarify(p *ParsedIntent, resolvedSymbol string) bool {
	if p == nil || p.TaskKind == TaskNeedClarify || !HasResolvableSymbol(resolvedSymbol) {
		return true
	}
	return false
}

// ClarifyReplyText 返回可直接展示给用户的追问话术。
func ClarifyReplyText(p *ParsedIntent) string {
	if p != nil {
		if s := strings.TrimSpace(p.ClarifyPrompt); s != "" {
			return s
		}
	}
	return defaultClarifyPrompt
}

// FormatPendingForParse 供意图 FC 的上下文：说明上一轮缺标的、本轮补了什么。
func FormatPendingForParse(p *tools.PendingIntent) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("上一轮用户请求因缺少股票代码/名称而中断，须在本轮合并意图，不要改成 unrelated 的 quick_look。")
	if s := strings.TrimSpace(p.UserSnippet); s != "" {
		b.WriteString("\n上一轮用户原话：")
		b.WriteString(s)
	}
	b.WriteString("\n待续 task_kind：")
	b.WriteString(strings.TrimSpace(p.TaskKind))
	if len(p.SkillHints) > 0 {
		b.WriteString("\n待续 skill_hints：")
		b.WriteString(strings.Join(p.SkillHints, ","))
	}
	return b.String()
}

// ApplySessionFollowUp 用户仅补充股票名/code 时，继承上一轮 task_kind（避免 combo 把「光迅科技」判成查行情）。
func ApplySessionFollowUp(um string, parsed *ParsedIntent, pending *tools.PendingIntent) {
	if parsed == nil || pending == nil {
		return
	}
	if !HasResolvableSymbol(firstSymbol(parsed)) {
		return
	}
	if !isLikelyClarificationAnswer(um, parsed) {
		return
	}
	pt := TaskKind(strings.TrimSpace(pending.TaskKind))
	if pt == "" || pt == TaskNeedClarify || pt == TaskOffTopic {
		return
	}
	// 本轮若已被 combo/FC 判成「仅查行情」而上一轮是基本面等，以 pending 为准
	if shouldOverrideWithPending(parsed, pt) {
		parsed.TaskKind = pt
		if len(pending.SkillHints) > 0 {
			parsed.SkillHints = dedupeTrim(append([]string(nil), pending.SkillHints...))
		}
		narrowSkillHintsForSingleDimension(parsed)
		name := strings.TrimSpace(strings.Join(parsed.SymbolNames, ""))
		code := firstSymbol(parsed)
		if name == "" && code != "" {
			name = code
		}
		parsed.NLRewritten = composeFollowUpNL(name, code, parsed.TaskKind)
		parsed.Source = "session_followup"
	}
}

func firstSymbol(parsed *ParsedIntent) string {
	if parsed == nil {
		return ""
	}
	if syms := NormalizeSymbols(parsed.Symbols); len(syms) > 0 {
		return syms[0]
	}
	return ""
}

func isLikelyClarificationAnswer(um string, parsed *ParsedIntent) bool {
	um = strings.TrimSpace(um)
	if um == "" || !HasResolvableSymbol(firstSymbol(parsed)) {
		return false
	}
	// 短句或几乎只有股票名
	if len([]rune(um)) <= 16 {
		return true
	}
	if len(parsed.SymbolNames) == 1 && strings.Contains(um, parsed.SymbolNames[0]) {
		return true
	}
	return false
}

func shouldOverrideWithPending(parsed *ParsedIntent, pending TaskKind) bool {
	cur := parsed.TaskKind
	if cur == TaskGeneral || cur == TaskNeedClarify {
		return true
	}
	if pending == TaskFundamental || pending == TaskTechnical || pending == TaskNewsFocus || pending == TaskSentiment || pending == TaskSector {
		if cur == TaskQuickLook {
			return true
		}
	}
	if cur != pending {
		return isSingleDimensionTask(pending)
	}
	return false
}

func isSingleDimensionTask(k TaskKind) bool {
	switch k {
	case TaskFundamental, TaskTechnical, TaskNewsFocus, TaskSentiment, TaskSector, TaskQuickLook:
		return true
	default:
		return false
	}
}

func narrowSkillHintsForSingleDimension(p *ParsedIntent) {
	if p == nil {
		return
	}
	h := primarySkillHintForTaskKind(p.TaskKind)
	if h == "" {
		return
	}
	p.SkillHints = []string{h}
}

func composeFollowUpNL(name, code string, task TaskKind) string {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	label := name
	if name != "" && code != "" && name != code {
		label = name + "（" + code + "）"
	} else if code != "" {
		label = code
	} else if name != "" {
		label = name
	}
	switch task {
	case TaskFundamental:
		return fmt.Sprintf("分析%s的基本面", label)
	case TaskTechnical:
		return fmt.Sprintf("分析%s的技术面", label)
	case TaskNewsFocus:
		return fmt.Sprintf("分析%s的消息面", label)
	case TaskSentiment:
		return fmt.Sprintf("分析%s的资金面", label)
	case TaskSector:
		return fmt.Sprintf("分析%s的板块情况", label)
	case TaskQuickLook:
		return fmt.Sprintf("查询%s最新行情", label)
	default:
		return fmt.Sprintf("分析%s", label)
	}
}

// SavePendingOnClarify 缺标的即将澄清时，保存可续意图。
func SavePendingOnClarify(sessionID string, um string, parsed *ParsedIntent) {
	if parsed == nil {
		return
	}
	tk := string(parsed.TaskKind)
	if tk == "" || tk == "need_clarify" || tk == "off_topic" {
		return
	}
	hints := append([]string(nil), parsed.SkillHints...)
	tools.SavePendingIntent(sessionID, tk, hints, um)
}
