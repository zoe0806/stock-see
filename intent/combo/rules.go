package combo

import (
	"sort"
	"strings"

	"stock-see/intent"
)

// 偏基本面的财务指标字段（与技术面指标冲突消解时用）。
var fundamentalMetricFields = map[string]struct{}{
	"performance_brief": {},
	"revenue":           {}, "net_profit": {}, "deducted_net_profit": {}, "gross_margin": {}, "net_margin": {},
	"eps": {}, "eps_basic": {}, "eps_diluted": {}, "roe": {}, "pe_ttm": {}, "pe_dynamic": {}, "pb": {},
	"debt_to_assets": {}, "current_ratio": {}, "quick_ratio": {}, "operating_cashflow": {},
	"free_cashflow": {}, "dividend_yield": {}, "market_cap": {}, "float_market_cap": {},
	"asset_turnover": {}, "inventory_turnover": {}, "receivables_turnover": {},
}

var technicalMetricFields = map[string]struct{}{
	"macd": {}, "macd_golden_cross": {}, "macd_death_cross": {}, "rsi": {}, "kdj": {}, "bollinger_bands": {},
	"ma": {}, "ma5": {}, "ma10": {}, "ma20": {}, "ma60": {},
	"support_level": {}, "resistance_level": {}, "golden_cross": {}, "death_cross": {},
	"bearish_divergence": {}, "bullish_divergence": {}, "volume_surge": {},
}

func metricToCompareAxis(field string) string {
	switch field {
	case "revenue":
		return "revenue"
	case "net_profit", "deducted_net_profit":
		return "profit"
	case "pe_ttm", "pe_dynamic":
		return "pe"
	case "pb":
		return "pb"
	case "roe":
		return "roe"
	default:
		return "general"
	}
}

// ApplyComboRules 槽位组合：默认值、冲突消解、查询类型推断。
func ApplyComboRules(slots RawSlots, userQuery string) *intent.ParsedIntent {
	p := &intent.ParsedIntent{
		Source:     "combo_rules",
		Confidence: comboConfidence(slots),
	}
	if slots.TimeParsed != "" {
		p.TimeHint = slots.TimeParsed
	}

	if len(slots.MatchedStocks) >= 2 {
		var codes, names []string
		for _, m := range slots.MatchedStocks {
			codes = append(codes, m.Code)
			names = append(names, m.Name)
		}
		p.Symbols = intent.NormalizeSymbols(codes)
		p.SymbolNames = names
		p.TaskKind = intent.TaskCompare
		p.SkillHints = compareDefaultSkillHints()
		if len(slots.MetricFields) > 0 {
			p.CompareAxis = metricToCompareAxis(slots.MetricFields[0])
		}
		intent.ValidateAndPatch(p, userQuery)
		return p
	}

	if slots.SymbolCode != "" {
		p.Symbols = intent.NormalizeSymbols([]string{slots.SymbolCode})
	}
	if slots.SymbolName != "" {
		p.SymbolNames = []string{slots.SymbolName}
	}

	// 意图维度：按得分排序取 Top，再做冲突消解
	type kv struct {
		k string
		v int
	}
	var pairs []kv
	for k, v := range slots.IntentScores {
		pairs = append(pairs, kv{k: k, v: v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	var ranked []string
	for _, x := range pairs {
		ranked = append(ranked, x.k)
	}

	hasFundMetric := false
	hasTechMetric := false
	for _, f := range slots.MetricFields {
		if _, ok := fundamentalMetricFields[f]; ok {
			hasFundMetric = true
		}
		if _, ok := technicalMetricFields[f]; ok {
			hasTechMetric = true
		}
	}

	primaryIntent := ""
	for _, ik := range ranked {
		if ik == "technical" && hasFundMetric && !hasTechMetric {
			continue // 冲突消解：基本面指标占优时丢弃纯技术意图命中
		}
		primaryIntent = ik
		break
	}
	if primaryIntent == "" && len(slots.MetricFields) > 0 {
		if hasFundMetric {
			primaryIntent = "fundamental"
		} else if hasTechMetric {
			primaryIntent = "technical"
		}
	}

	// 查询类型：时间范围 + 多指标 → 趋势/多年财务
	if slots.TimeParsed != "" && len(slots.MetricFields) >= 2 {
		p.TaskKind = intent.TaskTrend
	} else if primaryIntent != "" {
		switch primaryIntent {
		case "comparison":
			p.TaskKind = intent.TaskCompare
		case "fundamental":
			p.TaskKind = intent.TaskFundamental
		case "technical":
			p.TaskKind = intent.TaskTechnical
		case "news":
			p.TaskKind = intent.TaskNewsFocus
		case "sector":
			p.TaskKind = intent.TaskSector
		case "sentiment":
			p.TaskKind = intent.TaskSentiment
		case "realtime-market":
			p.TaskKind = intent.TaskQuickLook
		default:
			p.TaskKind = intent.TaskGeneral
		}
	} else {
		p.TaskKind = intent.TaskGeneral
	}

	if p.TaskKind == intent.TaskCompare {
		p.SkillHints = compareDefaultSkillHints()
	} else {
		p.SkillHints = intentKeysToSkillHints(primaryIntent, ranked)
		if len(p.SkillHints) == 0 && len(slots.MetricFields) > 0 {
			p.SkillHints = []string{"fundamental"}
		}
	}

	if len(slots.MetricFields) > 0 {
		p.CompareAxis = metricToCompareAxis(slots.MetricFields[0])
	}

	intent.ValidateAndPatch(p, userQuery)
	return p
}

// compareDefaultSkillHints 多标的对比默认拉基本面 + 技术面工具（估值/业绩可由 fundamental 覆盖）。
func compareDefaultSkillHints() []string {
	return []string{"fundamental", "technical"}
}

func intentKeysToSkillHints(primary string, ranked []string) []string {
	keyToSkill := map[string]string{
		"technical":       "technical",
		"fundamental":     "fundamental",
		"comparison":      "", // 由 compareDefaultSkillHints 或 ranked 中其他键补全
		"news":            "news",
		"sentiment":       "sentiment",
		"market-trend":    "market-trend",
		"sector":          "sector",
		"pattern":         "pattern",
		"risk":            "risk",
		"scoring":         "scoring",
		"realtime-market": "realtime-market",
	}
	var out []string
	seen := map[string]struct{}{}
	add := func(k string) {
		s := keyToSkill[k]
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if primary != "" {
		add(primary)
	}
	for _, k := range ranked {
		add(k)
	}
	return out
}

func comboConfidence(s RawSlots) float64 {
	var score float64
	if len(s.MatchedStocks) >= 2 {
		score += 0.45
	} else if s.SymbolCode != "" {
		score += 0.35
	}
	if len(s.IntentScores) > 0 {
		score += 0.25
	}
	if len(s.MetricFields) > 0 {
		score += 0.2
	}
	if s.TimeParsed != "" {
		score += 0.2
	}
	if score > 1 {
		return 1
	}
	return score
}

// ShouldSkipFC 组合槽位足够完整时跳过后续 Function Calling。
func ShouldSkipFC(p *intent.ParsedIntent, slots RawSlots) bool {
	if p == nil {
		return false
	}

	if comboConfidence(slots) < 0.55 {
		return false
	}
	if len(p.Symbols) == 0 && slots.SymbolCode == "" && len(slots.MatchedStocks) == 0 {
		return false
	}
	if p.TaskKind == intent.TaskCompare && len(p.Symbols) < 2 {
		return false
	}
	return len(p.SkillHints) > 0
}

// FormatComboHintsForPrompt 注入 FC 的说明文本。
func FormatComboHintsForPrompt(slots RawSlots, p *intent.ParsedIntent) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("【词典匹配 · 别名/指标/时间短语（内存倒排）】\n")
	b.WriteString("说明：由 knowledge.json 展开的同义词与规则组合生成，供与用户原话交叉校验。\n")
	if slots.SymbolCode != "" {
		b.WriteString("股票代码：")
		b.WriteString(slots.SymbolCode)
		b.WriteString("\n")
	}
	if slots.SymbolName != "" {
		b.WriteString("证券简称：")
		b.WriteString(slots.SymbolName)
		b.WriteString("\n")
	}
	if len(slots.MetricFields) > 0 {
		b.WriteString("指标字段：")
		b.WriteString(strings.Join(slots.MetricFields, ","))
		b.WriteString("\n")
	}
	if slots.TimeParsed != "" {
		b.WriteString("时间槽位：")
		b.WriteString(slots.TimeParsed)
		b.WriteString("\n")
	}
	b.WriteString("建议 task_kind：")
	b.WriteString(string(p.TaskKind))
	b.WriteString("\n建议 skill_hints：")
	b.WriteString(strings.Join(p.SkillHints, ","))
	b.WriteString("\n")
	return strings.TrimSpace(b.String())
}
