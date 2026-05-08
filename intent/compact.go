package intent

import "strings"

// CompactContextLine 单行意图摘要，用于对话上下文（省 token）；与完整 queryaug 块互斥使用更佳。
func CompactContextLine(p *ParsedIntent) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("task_kind=")
	b.WriteString(string(p.TaskKind))
	if len(p.Symbols) > 0 {
		b.WriteString(";symbols=")
		b.WriteString(strings.Join(p.Symbols, ","))
	}
	if len(p.SkillHints) > 0 {
		b.WriteString(";skill_hints=")
		b.WriteString(strings.Join(p.SkillHints, ","))
	}
	if p.TimeHint != "" {
		b.WriteString(";time_hint=")
		b.WriteString(p.TimeHint)
	}
	if p.CompareAxis != "" && p.CompareAxis != "general" {
		b.WriteString(";compare_axis=")
		b.WriteString(p.CompareAxis)
	}
	return b.String()
}
