package intent

import "strings"

// skillCue 将 skill_hints 映射到与现有 MatchSkills 关键词兼容的触发短语。
var skillCue = map[string]string{
	"technical":       "技术面",
	"fundamental":     "基本面",
	"news":            "新闻",
	"sentiment":       "资金面",
	"market-trend":    "大盘",
	"sector":          "板块",
	"pattern":         "形态",
	"risk":            "风险",
	"scoring":         "综合评分",
	"realtime-market": "行情",
	"backtest":        "回测",
	"kronos":          "预测",
	"scrapling":       "网页",
}

// compareCue 对比维度补充词。
var compareCue = map[string]string{
	"pe":      "市盈率",
	"pb":      "市净率",
	"price":   "股价",
	"revenue": "营收",
	"profit":  "净利润",
	"roe":     "ROE",
	"general": "对比",
}

// EnrichMatchText 将解析后的槽位拼成更丰富的匹配文本，供关键词技能路由使用。
// rawUser 建议为原始用户输入（可含显式 symbol 请求里的 message）。
func EnrichMatchText(rawUser string, p *ParsedIntent) string {
	rawUser = strings.TrimSpace(rawUser)
	if p == nil || !p.Valid {
		return rawUser
	}
	var b strings.Builder
	if rawUser != "" {
		b.WriteString(rawUser)
	}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
	}
	for _, sym := range p.Symbols {
		add(sym)
	}
	for _, name := range p.SymbolNames {
		add(name)
	}
	if p.TimeHint != "" {
		add(p.TimeHint)
	}
	switch p.TaskKind {
	case TaskCompare:
		add("对比")
		if cue := compareCue[p.CompareAxis]; cue != "" {
			add(cue)
		}
	case TaskTrend:
		add("趋势")
		add("财报")
	case TaskNewsFocus:
		add("消息面")
		add("公告")
	case TaskQuickLook:
		add("行情")
		add("现价")
	case TaskDeepAnalysis:
		add("新闻")
		add("风险")
		add("综合评分")
		add("行情")
	case TaskFundamental:
		add("基本面")
	case TaskTechnical:
		add("技术面")
	case TaskSentiment:
		add("资金面")
	case TaskSector:
		add("板块")
	case TaskNeedClarify:
		if p.ClarifyPrompt != "" {
			add(p.ClarifyPrompt)
		}
	}
	for _, h := range p.SkillHints {
		if cue := skillCue[strings.ToLower(strings.TrimSpace(h))]; cue != "" {
			add(cue)
		}
	}
	if b.Len() == 0 {
		return rawUser
	}
	return b.String()
}
