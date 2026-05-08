package combo

import "strings"

// NLQueryRewrite 基于知识库倒排槽位，对用户查询做自然语言级改写：实体标准化（证券简称+代码）、
// 时间短语与指标字段的规范中文拼接。
// explicitCode 为会话延续时的六位代码（如前端或后端缓存的「当前标的」）：当问句未命中股票槽位时，用于补全「它/这只」类指代。
func NLQueryRewrite(original string, slots RawSlots, explicitCode string) string {
	orig := strings.TrimSpace(original)
	slots = mergeSlotsWithExplicit(slots, explicitCode)
	core := composeEntityTimeMetricsNL(slots)
	if core == "" {
		return orig
	}
	//意图由skills完成，这里不再补充
	// q := dominantIntentQualifier(slots.IntentScores)
	// if q != "" {
	// 	return "关于" + core + "的" + q + "分析"
	// }
	return core
}

func mergeSlotsWithExplicit(slots RawSlots, explicitCode string) RawSlots {
	explicitCode = strings.TrimSpace(explicitCode)
	if explicitCode == "" || len(slots.MatchedStocks) > 0 || strings.TrimSpace(slots.SymbolCode) != "" {
		return slots
	}
	if len(explicitCode) != 6 || !isAllDigits(explicitCode) {
		return slots
	}
	out := slots
	out.SymbolCode = explicitCode
	out.SymbolName = PrimaryNameForCode(explicitCode)
	if strings.TrimSpace(out.SymbolName) == "" {
		out.SymbolName = explicitCode
	}
	return out
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func composeEntityTimeMetricsNL(slots RawSlots) string {
	var parts []string
	if len(slots.MatchedStocks) >= 2 {
		var seg []string
		for _, m := range slots.MatchedStocks {
			n := strings.TrimSpace(m.Name)
			c := strings.TrimSpace(m.Code)
			if n == "" {
				n = c
			}
			seg = append(seg, n+"（"+c+"）")
		}
		parts = append(parts, "对比"+strings.Join(seg, "与"))
	} else if code := strings.TrimSpace(slots.SymbolCode); code != "" {
		name := strings.TrimSpace(slots.SymbolName)
		if name == "" {
			name = code
		}
		parts = append(parts, name+"（"+code+"）")
	}
	if tp := strings.TrimSpace(slots.TimeParsed); tp != "" {
		if cn := humanizeTimeParsedCN(tp); cn != "" {
			parts = append(parts, cn)
		}
	}
	if len(slots.MetricFields) > 0 {
		labels := metricFieldLabelsCN(slots.MetricFields)
		if len(labels) == 1 {
			parts = append(parts, labels[0])
		} else if len(labels) > 1 {
			parts = append(parts, strings.Join(labels, "与"))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "")
}

func humanizeTimeParsedCN(parsed string) string {
	m := map[string]string{
		"current_year":    "今年",
		"last_year":       "去年",
		"last_3_years":    "近三年",
		"last_5_years":    "近五年",
		"last_7d":         "近一周",
		"last_30d":        "近一个月",
		"last_90d":        "近三个月",
		"last_180d":       "近半年",
		"ytd":             "今年以来",
		"last_quarter":    "上季度",
		"current_quarter": "本季度",
		"today":           "今日",
		"yesterday":       "昨日",
		"this_week":       "本周",
		"last_week":       "上周",
		"this_month":      "本月",
		"last_month":      "上月",
		"all_time":        "历史",
		"since_ipo":       "上市以来",
		"last_2_weeks":    "最近两周",
	}
	if s := m[parsed]; s != "" {
		return s
	}
	return parsed
}

func metricFieldLabelsCN(fields []string) []string {
	lab := map[string]string{
		"performance_brief": "业绩",
		"revenue": "营业收入", "net_profit": "归母净利润", "deducted_net_profit": "扣非净利润",
		"gross_margin": "毛利率", "net_margin": "净利率", "eps": "每股收益", "eps_basic": "基本每股收益",
		"eps_diluted": "稀释每股收益", "roe": "净资产收益率", "pe_ttm": "市盈率（TTM）", "pe_dynamic": "动态市盈率",
		"pb": "市净率", "debt_to_assets": "资产负债率", "current_ratio": "流动比率", "quick_ratio": "速动比率",
		"operating_cashflow": "经营活动现金流", "free_cashflow": "自由现金流", "dividend_yield": "股息率",
		"market_cap": "总市值", "float_market_cap": "流通市值", "turnover_rate": "换手率", "volume_ratio": "量比",
		"amplitude": "振幅", "change_pct": "涨跌幅", "volume": "成交量", "amount": "成交额", "avg_price": "均价",
		"high": "最高价", "low": "最低价", "open": "开盘价", "close": "收盘价",
		"macd": "MACD", "macd_golden_cross": "MACD金叉", "macd_death_cross": "MACD死叉",
		"rsi": "RSI", "kdj": "KDJ", "bollinger_bands": "布林带", "ma": "均线",
		"ma5": "5日均线", "ma10": "10日均线", "ma20": "20日均线", "ma60": "60日均线",
		"support_level": "支撑位", "resistance_level": "压力位", "golden_cross": "金叉", "death_cross": "死叉",
		"bearish_divergence": "顶背离", "bullish_divergence": "底背离", "volume_surge": "放量",
		"asset_turnover": "总资产周转率", "inventory_turnover": "存货周转率", "receivables_turnover": "应收账款周转率",
	}
	var out []string
	seen := map[string]struct{}{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		if s := lab[f]; s != "" {
			out = append(out, s)
		} else {
			out = append(out, f)
		}
	}
	return out
}

func dominantIntentQualifier(scores map[string]int) string {
	if len(scores) == 0 {
		return ""
	}
	var bestK string
	bestV := -1
	for k, v := range scores {
		if v > bestV {
			bestV = v
			bestK = k
		}
	}
	if bestK == "" || bestV <= 0 {
		return ""
	}
	m := map[string]string{
		"fundamental":     "基本面",
		"technical":       "技术面",
		"news":            "消息面",
		"sentiment":       "资金面",
		"market-trend":    "大盘环境",
		"sector":          "板块",
		"pattern":         "形态",
		"risk":            "风险",
		"scoring":         "综合评分",
		"realtime-market": "行情",
	}
	return m[bestK]
}
