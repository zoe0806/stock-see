package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// builtinIntentKeywords 按技能目录名（SKILL.md 所在文件夹）预置中文/英文触发词，不依赖 SKILL.md 首段拆词。
var builtinIntentKeywords = map[string][]string{
	"technical":     {"技术面", "技术分析", "走势", "均线", "MACD", "KDJ", "RSI", "技术指标", "多空", "支撑", "压力", "金叉", "死叉"},
	"fundamental":   {"基本面", "估值", "财务", "财报", "业绩", "ROE", "PE", "PB", "盈利", "成长性", "现金流", "杜邦"},
	"news":          {"消息面", "利好", "利空", "新闻", "公告", "研报", "舆情", "媒体"},
	"sentiment":     {"资金", "北向", "龙虎榜", "主力", "情绪", "流向", "资金面", "沪深港通", "净流入", "席位"},
	"market-trend":  {"大盘", "指数", "市况", "市场环境", "风格", "上证", "深证", "创业板", "沪深300", "中证"},
	"sector":        {"板块", "行业", "龙头", "相对强弱", "同行业", "申万", "联动"},
	"pattern":       {"形态", "K线", "突破", "颈线", "头肩", "双顶", "双底", "三角形", "旗形", "楔形"},
	"risk":          {"风险", "波动", "回撤", "Beta", "波动率", "夏普", "最大回撤"},
	"scoring":       {"综合评分", "打分", "总评", "评分", "加权", "评级", "仓位"},
	"realtime-market": {"行情", "现价", "最新价", "涨跌", "实时", "多少钱", "涨了没", "跌停", "涨停", "成交量"},
	"backtest":      {"回测", "历史胜率", "样本外", "模拟盘"},
	"kronos":        {"Kronos", "kronos", "预测", "走势预测", "预后"},
	"scrapling":     {"抓取", "爬取", "网页", "正文", "反爬", "渲染", "选择器"},
}

// skillIntentFile 与 SKILL.md 同目录的 intent.json（可选）。
type skillIntentFile struct {
	Keywords []string `json:"keywords"`
}

func loadSkillIntent(skillDir, skillMDPath, skillName string) []string {
	keywords := append([]string(nil), builtinIntentKeywords[skillName]...)

	intentPath := filepath.Join(skillDir, "intent.json")
	if raw, err := os.ReadFile(intentPath); err == nil {
		var meta skillIntentFile
		if json.Unmarshal(raw, &meta) == nil {
			keywords = append(keywords, meta.Keywords...)
		}
	}

	if extra := parseSkillIntentComment(skillMDPath); len(extra) > 0 {
		keywords = append(keywords, extra...)
	}

	return dedupeStringsTrim(keywords)
}

func parseSkillIntentComment(skillMDPath string) []string {
	raw, err := os.ReadFile(skillMDPath)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	const maxScan = 80
	var out []string
	for i, line := range lines {
		if i >= maxScan {
			break
		}
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "skill-intent:")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("skill-intent:"):]
		if end := strings.Index(rest, "-->"); end >= 0 {
			rest = rest[:end]
		}
		for _, p := range strings.Split(rest, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		break
	}
	return out
}

func dedupeStringsTrim(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
