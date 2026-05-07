package intent

import (
	"regexp"
	"sort"
	"strings"
)

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
		if len(s) == 6 && digitsOnly(s) {
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

func digitsOnly(s string) bool {
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
	// 对比类至少两个代码或依赖名称（名称留给上层模型）
	if p.TaskKind == TaskCompare && len(p.Symbols) < 2 && len(p.SymbolNames) < 2 {
		if extra := ExtractSymbolsFromText(userMessage); len(extra) >= 2 {
			p.Symbols = NormalizeSymbols(extra)
		}
	}
	p.SkillHints = dedupeTrim(p.SkillHints)
	p.SymbolNames = dedupeTrim(p.SymbolNames)
	p.Valid = true
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
	if len(explicit) == 6 && digitsOnly(explicit) {
		p.Symbols = NormalizeSymbols(append([]string{explicit}, p.Symbols...))
		p.Source = "merge_explicit"
	}
}
