// Package tools 并行分析工具：一次调用并发请求技术/基本面/消息/大盘/板块/形态/情绪，供 run_scoring 汇总。

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var dimensionOrder = []struct{ key, title string }{
	{"technical", "技术面"},
	{"fundamental", "基本面"},
	{"news", "消息面"},
	{"market_trend", "大盘趋势"},
	{"sector", "板块"},
	{"pattern", "形态"},
	{"sentiment", "情绪/资金面"},
}

func FormatSingleDimensionMarkdown(key, title string, v any) string {
	if v == nil {
		return "### " + title + "\n\n- 暂无数据\n"
	}
	fmt.Println("FormatSingleDimensionMarkdown", title)

	if s, ok := v.(string); ok {
		trimmed := strings.TrimSpace(s)
		if trimmed != "" {
			return "### " + title + "\n\n" + trimmed + "\n"
		}
		return "### " + title + "\n\n- 暂无数据\n"
	}
	if arr, ok := v.([]any); ok {
		var b strings.Builder
		b.WriteString("### " + title + "\n\n")
		for _, it := range arr {
			if s, ok := it.(string); ok {
				b.WriteString(s)
				b.WriteString("\n")
			}
		}
		return b.String()
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "### " + title + "\n\n- 数据格式异常\n"
	}
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString("### " + title + "\n\n")

	if report, _ := m["report"].(string); report != "" {
		b.WriteString(strings.TrimSpace(report))
		b.WriteString("\n")
		return b.String()
	}
	switch key {
	case "market_trend":
		if regime, _ := m["market_regime"].(string); regime != "" {
			b.WriteString("- 市场状态: " + regime + "\n")
		}
		if breadth, _ := m["breadth"].(string); breadth != "" {
			b.WriteString("- 涨跌家数: " + breadth + "\n")
		}
		if impl, _ := m["implication"].(string); impl != "" {
			b.WriteString("- 含义: " + impl + "\n")
		}

	case "sector":
		if name, _ := m["sector_name"].(string); name != "" {
			b.WriteString("- 所属板块: " + name + "\n")
		}
		if perf, _ := m["sector_performance"].(string); perf != "" {
			b.WriteString("- 板块表现: " + perf + "\n")
		}
		if impl, _ := m["implication"].(string); impl != "" {
			b.WriteString("- 含义: " + impl + "\n")
		}
	case "pattern":
		if pat, _ := m["pattern"].(string); pat != "" {
			b.WriteString("- 形态: " + pat + "\n")
		}
		if remark, _ := m["volume_remark"].(string); remark != "" {
			b.WriteString("- 量能: " + remark + "\n")
		}

	default:
		b.WriteString("- 暂无报告摘要\n")
	}
	return b.String()
}

// OnDimensionFunc 每完成一个维度时回调：key, 原始结果 v, 该维度的 Markdown。
type OnDimensionFunc func(key string, v any, markdown string)

// RunAnalysisParallelStream 并行拉取七维度分析，每完成一个维度即调用 onDimension；全部完成后返回 combined 与 formattedScore。
// 用于全量报告模式下边收边推 SSE（event: section），前端可逐段渲染。
func RunAnalysisParallelStream(ctx context.Context, symbol string, includeReports bool, onDimension OnDimensionFunc) (combined map[string]any, formattedScore string, err error) {
	baseURL := PythonBaseURL()
	if baseURL == "" {
		return nil, "", nil
	}
	body := map[string]string{"symbol": symbol}
	bodyFundamental := map[string]any{"symbol": symbol}
	if includeReports {
		bodyFundamental["include_reports"] = true
	}
	combined = map[string]any{
		"symbol":    symbol,
		"technical": nil, "fundamental": nil, "news": nil,
		"market_trend": nil, "sector": nil, "pattern": nil,
		"sentiment": nil,
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	set := func(key string, v any) {
		mu.Lock()
		combined[key] = v
		mu.Unlock()
		var title string
		for _, p := range dimensionOrder {
			if p.key == key {
				title = p.title
				break
			}
		}
		if title == "" {
			title = key
		}
		md := FormatSingleDimensionMarkdown(key, title, v)
		if onDimension != nil {
			onDimension(key, v, md)
		}
	}

	wg.Add(7)
	go func() {
		defer wg.Done()
		s, _ := PostJSON(ctx, baseURL, "/api/analysis/technical", body)
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("technical", v)
	}()
	go func() {
		defer wg.Done()
		s, _ := PostJSON(ctx, baseURL, "/api/analysis/fundamental", bodyFundamental)
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("fundamental", v)
	}()
	go func() {
		defer wg.Done()
		s, _ := PostJSON(ctx, baseURL, "/api/analysis/news", body)
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("news", v)
	}()
	go func() {
		defer wg.Done()
		s, _ := GetJSON(ctx, baseURL, "/api/analysis/market_trend")
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("market_trend", v)
	}()
	go func() {
		defer wg.Done()
		s, _ := PostJSON(ctx, baseURL, "/api/analysis/sector", body)
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("sector", v)
	}()
	go func() {
		defer wg.Done()
		s, _ := PostJSON(ctx, baseURL, "/api/analysis/pattern", body)
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("pattern", v)
	}()
	go func() {
		defer wg.Done()
		s, _ := PostJSON(ctx, baseURL, "/api/analysis/sentiment", body)
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		set("sentiment", v)
	}()
	wg.Wait()

	scoreJSON, err := PostJSON(ctx, baseURL, "/api/analysis/score", map[string]any{"symbol": symbol, "inputs": combined})
	if err != nil {
		return combined, "", err
	}
	return combined, FormatScoreResultForContext(scoreJSON), nil
}

// FormatParallelResultForContext 将并行分析结果 combined 格式化为可读的 Markdown 文本，避免原始 JSON 与转义换行符。
func FormatParallelResultForContext(combined map[string]any) string {
	if combined == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n")
	for _, p := range dimensionOrder {
		v := combined[p.key]
		if v == nil {
			b.WriteString("\n### " + p.title + "\n\n- 暂无数据\n\n")
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			b.WriteString("\n### " + p.title + "\n\n- 数据格式异常\n\n")
			continue
		}
		//b.WriteString("\n### " + p.title + "\n\n")
		if report, _ := m["report"].(string); report != "" {
			b.WriteString("\n\n")
			b.WriteString(strings.TrimSpace(report))
			b.WriteString("\n\n")
			continue
		}
		// 无 report 时按维度拼简短摘要
		switch p.key {
		case "market_trend":
			if regime, _ := m["market_regime"].(string); regime != "" {
				b.WriteString("- 市场状态: " + regime + "\n")
			}
			// if breadth, _ := m["breadth"].(string); breadth != "" {
			// 	b.WriteString("- 涨跌家数: " + breadth + "\n")
			// }
			if impl, _ := m["implication"].(string); impl != "" {
				b.WriteString("- 含义: " + impl + "\n")
			}
			// if maj, ok := m["major_indices"].(map[string]any); ok && len(maj) > 0 {
			// 	for name, val := range maj {
			// 		if vv, ok := val.(map[string]any); ok {
			// 			if pct, _ := vv["change_pct"].(float64); pct != 0 {
			// 				b.WriteString(fmt.Sprintf("- %s: %.2f%%\n", name, pct))
			// 			}
			// 		}
			// 	}
			// }
		case "sector":
			if name, _ := m["sector_name"].(string); name != "" {
				b.WriteString("- 所属板块: " + name + "\n")
			}
			if perf, _ := m["sector_performance"].(string); perf != "" {
				b.WriteString("- 板块表现: " + perf + "\n")
			}
			if impl, _ := m["implication"].(string); impl != "" {
				b.WriteString("- 含义: " + impl + "\n")
			}
		case "pattern":
			if pat, _ := m["pattern"].(string); pat != "" {
				b.WriteString("- 形态: " + pat + "\n")
			}
			if remark, _ := m["volume_remark"].(string); remark != "" {
				b.WriteString("- 量能: " + remark + "\n")
			}

		default:
			b.WriteString("- 暂无报告摘要\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// FormatScoreResultForContext 将评分接口返回的 JSON 格式化为可读文本。
func FormatScoreResultForContext(scoreJSON string) string {
	if scoreJSON == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(scoreJSON), &m); err != nil {
		return scoreJSON
	}
	var b strings.Builder

	// 标的
	if sym, _ := m["symbol"].(string); sym != "" {
		b.WriteString("### 标的\n")
		b.WriteString(sym + "\n\n")
	}

	// 综合评分 + 评级（合并显示）
	total, hasTotal := m["total_score"].(float64)
	grade, hasGrade := m["grade"].(string)
	if hasTotal || hasGrade {
		b.WriteString("### 综合评分\n")
		if hasTotal && hasGrade {
			b.WriteString(fmt.Sprintf("**%.1f** (%s)\n\n", total, grade))
		} else if hasTotal {
			b.WriteString(fmt.Sprintf("**%.1f**\n\n", total))
		} else {
			b.WriteString(fmt.Sprintf("**%s**\n\n", grade))
		}
	}

	// 各维度得分
	if dims, ok := m["dimension_scores"].(map[string]any); ok && len(dims) > 0 {
		b.WriteString("### 各维度得分\n")
		for k, v := range dims {
			if f, ok := v.(float64); ok {
				b.WriteString(fmt.Sprintf("- %s: %.1f\n", k, f))
			} else if s, ok := v.(string); ok {
				b.WriteString(fmt.Sprintf("- %s: %s\n", k, s))
			}
		}
		b.WriteString("\n")
	}

	// 一句话摘要（如果有）
	if one, _ := m["one_line_summary"].(string); one != "" {
		b.WriteString(one + "\n\n")
	}

	// 可操作清单
	if list, ok := m["actionable_checklist"].([]any); ok && len(list) > 0 {
		b.WriteString("### 可操作清单\n")
		for _, item := range list {
			if s, ok := item.(string); ok {
				b.WriteString("- " + s + "\n")
			}
		}
		b.WriteString("\n")
	}

	// 免责声明
	if dis, _ := m["disclaimer"].(string); dis != "" {
		b.WriteString("### 免责声明\n")
		b.WriteString(dis + "\n")
	}

	return strings.TrimSpace(b.String())
}

// RunAnalysisParallelTool 并行执行技术、基本面、消息、大盘、板块、形态分析，返回合并结果供 run_scoring 使用。
type RunAnalysisParallelTool struct{}

func (t *RunAnalysisParallelTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_analysis_parallel",
		Desc: "对指定股票执行技术面、基本面、消息面、大盘趋势、板块、形态六维度分析，返回合并结果；随后可调用 run_scoring(symbol, inputs) 做综合评分。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码，如 600519",
				Required: true,
				Type:     schema.String,
			},
			"include_reports": {
				Desc:     "为 true 时基本面会补充资产负债表、利润表（用户问财报时传 true）",
				Required: false,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunAnalysisParallelTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol         string `json:"symbol"`
		IncludeReports any    `json:"include_reports"` // true / "true" 表示需要详细财报
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	includeReports := false
	switch v := in.IncludeReports.(type) {
	case bool:
		includeReports = v
	case string:
		includeReports = v == "true" || v == "1" || v == "yes"
	}
	baseURL := PythonBaseURL()
	if baseURL == "" {
		return marshalError("Python 未配置，请设置 STOCK_PYTHON_URL")
	}

	symbol := in.Symbol
	body := map[string]string{"symbol": symbol}
	bodyFundamental := map[string]any{"symbol": symbol}
	if includeReports {
		bodyFundamental["include_reports"] = true
	}
	combined := map[string]any{
		"symbol":    symbol,
		"technical": nil, "fundamental": nil, "news": nil,
		"market_trend": nil, "sector": nil, "pattern": nil,
		"market": nil,
	}
	var mu sync.Mutex
	var wg sync.WaitGroup

	set := func(key string, v any) {
		mu.Lock()
		combined[key] = v
		mu.Unlock()
	}

	// 并发请求
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := GetJSON(ctx, baseURL, "/api/market/"+symbol)
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("market", v)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := PostJSON(ctx, baseURL, "/api/analysis/technical", body)
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("technical", v)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := PostJSON(ctx, baseURL, "/api/analysis/fundamental", bodyFundamental)
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("fundamental", v)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := PostJSON(ctx, baseURL, "/api/analysis/news", body)
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("news", v)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := GetJSON(ctx, baseURL, "/api/analysis/market_trend")
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("market_trend", v)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := PostJSON(ctx, baseURL, "/api/analysis/sector", body)
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("sector", v)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := PostJSON(ctx, baseURL, "/api/analysis/pattern", body)
		if err == nil {
			var v any
			_ = json.Unmarshal([]byte(s), &v)
			set("pattern", v)
		}
	}()

	wg.Wait()
	//return toJSON(combined)
	// 直接返回 Markdown 报告正文作为工具结果，模型会将其纳入回复，前端才能看到；若返回 JSON，markdown 不会被用起来。
	markdown := FormatParallelResultForContext(combined)
	if markdown == "" {
		return marshalError("多维分析无结果")
	}
	return markdown + "\n\n", nil
}
