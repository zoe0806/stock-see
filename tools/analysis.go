// Package tools 实现 run_technical、run_fundamental、run_news、run_scoring（Phase 2 对接 Python）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// RunTechnicalTool 技术分析。
type RunTechnicalTool struct{}

func (t *RunTechnicalTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_technical",
		Desc: "对指定股票进行技术面分析：均线、MACD、KDJ、量价、支撑阻力；返回多空倾向与关键位。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码，如 600519",
				Required: true,
				Type:     schema.String,
			},
			"period": {
				Desc:     "日线/周线，默认日线",
				Required: false,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunTechnicalTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
		Period string `json:"period"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	baseURL := PythonBaseURL()
	if baseURL != "" {
		s, err := PostJSON(ctx, baseURL, "/api/analysis/technical", map[string]string{"symbol": in.Symbol})
		if err == nil {
			return FormatTechnicalResponse(s), nil
		}
	}
	return marshalError("Python 未配置或 technical 接口调用失败，请设置 STOCK_PYTHON_URL 并启动 Python 服务")
}

// FormatTechnicalResponse 将s返回的 JSON 转为可读摘要。
func FormatTechnicalResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	var b strings.Builder
	report, _ := m["report"].(string)
	if report != "" {
		b.WriteString(report)
	}
	fmt.Println("FormatTechnicalResponse", strings.TrimSpace(b.String()))
	return strings.TrimSpace(b.String())
}

// RunFundamentalTool 基本面分析。
type RunFundamentalTool struct{}

func (t *RunFundamentalTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_fundamental",
		Desc: "对指定股票进行基本面分析：PE/PB/ROE、估值、财务健康与成长性。用户提到财报时可传 include_reports 以补充资产负债表、利润表。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
			"include_reports": {
				Desc:     "为 true 时补充资产负债表、利润表等详细财务数据（用户问财报时传 true）",
				Required: false,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunFundamentalTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol         string `json:"symbol"`
		IncludeReports any    `json:"include_reports"` // true / "true" / "1" 表示需要详细财报
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
	if baseURL != "" {
		body := map[string]any{"symbol": in.Symbol}
		if includeReports {
			body["include_reports"] = true
		}
		s, err := PostJSON(ctx, baseURL, "/api/analysis/fundamental", body)
		if err == nil {
			// 若 API 返回了 report 字段，优先将其作为可读报告返回，便于前端渲染
			var out struct {
				Report string `json:"report"`
			}
			if _ = sonic.UnmarshalString(s, &out); out.Report != "" {
				return out.Report, nil
			}
			return s, nil
		}
		return marshalError("fundamental 接口调用失败: " + err.Error())
	}
	return marshalError("Python 未配置或 fundamental 接口调用失败")
}

// RunNewsTool 消息面分析（摘要与情绪）。
type RunNewsTool struct{}

func (t *RunNewsTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_news",
		Desc: "对指定股票进行消息面分析：近期公告与新闻的摘要与情绪倾向（偏多/偏空/中性）。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunNewsTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	baseURL := PythonBaseURL()
	if baseURL != "" {
		s, err := PostJSON(ctx, baseURL, "/api/analysis/news", map[string]string{"symbol": in.Symbol})
		if err == nil {
			return s, nil
		}
	}
	return marshalError("Python 未配置或 news 分析接口调用失败")
}

// RunSentimentTool 情绪/资金面分析（北向、龙虎榜、主力、热度）。
type RunSentimentTool struct{}

func (t *RunSentimentTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_sentiment",
		Desc: "对指定股票进行情绪/资金面分析：北向资金、龙虎榜、主力流向、市场热度；返回 north_flow、lhb、main_flow、sentiment_summary。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunSentimentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	baseURL := PythonBaseURL()
	if baseURL == "" {
		return marshalError("Python 未配置或 sentiment 接口调用失败")
	}
	s, err := PostJSON(ctx, baseURL, "/api/analysis/sentiment", map[string]string{"symbol": in.Symbol})
	if err != nil {
		return marshalError("sentiment 接口调用失败: " + err.Error())
	}
	// 若 API 返回了 report 字段，优先将其作为可读报告返回，便于前端渲染
	var out struct {
		Report string `json:"report"`
	}
	if _ = sonic.UnmarshalString(s, &out); out.Report != "" {
		return out.Report, nil
	}
	return s, nil
}

// RunMarketTrendTool 大盘趋势与风格（Phase 3）。
type RunMarketTrendTool struct{}

func (t *RunMarketTrendTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_market_trend",
		Desc: "获取大盘趋势与风格：主要指数、市场宽度、价值/成长风格；用于综合评分与宏观环境判断。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"index_list": {
				Desc:     "可选，关注的指数列表",
				Required: false,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunMarketTrendTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	baseURL := PythonBaseURL()
	if baseURL != "" {
		s, err := GetJSON(ctx, baseURL, "/api/analysis/market_trend")
		if err == nil {
			return FormatMarketTrendResponse(s), nil
		}
	}
	return marshalError("Python 未配置或 market_trend 接口调用失败")
}

// FormatMarketTrendResponse 将s返回的 JSON 转为可读摘要。
func FormatMarketTrendResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}

	//{"source":"tengxun","market_regime":"bull","major_indices":{"sh":{"name":"上证指数","change_pct":0.05},"sz":{"name":"深证成指","change_pct":0.85},"cyb":{"name":"创业板指","change_pct":1.74}},"breadth":"—","style":"—","implication":"顺势"}
	var b strings.Builder
	b.WriteString("\n\n")
	if market_regime, _ := m["market_regime"].(string); market_regime != "" {
		b.WriteString("\n\n市场状态: " + market_regime)
	}

	if breadth, _ := m["breadth"].(string); breadth != "" {
		b.WriteString("\n\n涨跌家数: " + breadth)
	}
	if implication, _ := m["implication"].(string); implication != "" {
		b.WriteString("\n\n含义: " + implication)
	}
	b.WriteString("\n\n")
	return b.String()
}

// RunSectorTool 板块与对标（Phase 3）。
type RunSectorTool struct{}

func (t *RunSectorTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_sector",
		Desc: "对指定股票进行板块分析：所属行业、板块涨跌、龙头与相对位置。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *RunSectorTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	baseURL := PythonBaseURL()
	if baseURL != "" {
		s, err := PostJSON(ctx, baseURL, "/api/analysis/sector", map[string]string{"symbol": in.Symbol})
		if err == nil {
			var out struct {
				Report string `json:"report"`
			}
			if _ = sonic.UnmarshalString(s, &out); out.Report != "" {
				return out.Report, nil
			}
			return s, nil
		}
	}
	return marshalError("Python 未配置或 sector 接口调用失败")
}

// RunPatternTool 形态识别（Phase 3）。
type RunPatternTool struct{}

func (t *RunPatternTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_pattern",
		Desc: "对指定股票进行K线形态识别：经典形态、颈线、突破/跌破及量能配合。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
			"period": {
				Desc:     "日线/周线，默认日线",
				Required: false,
				Type:     schema.String,
			},
			"lookback_bars": {
				Desc:     "回溯K线根数，默认 60",
				Required: false,
				Type:     schema.Integer,
			},
		}),
	}, nil
}

func (t *RunPatternTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol       string `json:"symbol"`
		Period       string `json:"period"`
		LookbackBars int    `json:"lookback_bars"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	body := map[string]any{"symbol": in.Symbol}
	if in.Period != "" {
		body["period"] = in.Period
	}
	if in.LookbackBars > 0 {
		body["lookback_bars"] = in.LookbackBars
	}
	baseURL := PythonBaseURL()
	if baseURL != "" {
		s, err := PostJSON(ctx, baseURL, "/api/analysis/pattern", body)
		if err == nil {
			return FormatPatternResponse(s), nil
		}
	}
	return marshalError("Python 未配置或 pattern 接口调用失败")
}

// FormatPatternResponse 将s返回的 JSON 转为可读摘要。
func FormatPatternResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}

	var b strings.Builder
	b.WriteString("\n\n")
	if pattern, _ := m["pattern"].(string); pattern != "" {
		b.WriteString("\n\n形态: " + pattern)
	}

	if volume_remark, _ := m["volume_remark"].(string); volume_remark != "" {
		b.WriteString(" 量能: " + volume_remark)
	}
	b.WriteString("\n\n")
	return b.String()
}

// RunBreakoutScoreTool 突破评分（9.2）：波动压缩、均线收敛、量能、接近前高、行业强度五维，0～100 分。
type RunBreakoutScoreTool struct{}

func (t *RunBreakoutScoreTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_breakout_score",
		Desc: "对指定股票进行突破潜力评分（0～100）：波动压缩、均线收敛、量能、接近前高、行业强度五维；返回分项、信号摘要与报告。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码，如 600519",
				Required: true,
				Type:     schema.String,
			},
			"period": {
				Desc:     "日线/周线，默认 daily",
				Required: false,
				Type:     schema.String,
			},
			"lookback": {
				Desc:     "回溯K线根数，默认 60",
				Required: false,
				Type:     schema.Integer,
			},
			"weights": {
				Desc:     "可选，五维权重（volatility_compression/ma_convergence/volume/near_high/sector_strength）",
				Required: false,
				Type:     schema.Object,
			},
		}),
	}, nil
}

func (t *RunBreakoutScoreTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol   string         `json:"symbol"`
		Period   string         `json:"period"`
		Lookback int            `json:"lookback"`
		Weights  map[string]any `json:"weights"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	body := map[string]any{"symbol": in.Symbol}
	if in.Period != "" {
		body["period"] = in.Period
	}
	if in.Lookback > 0 {
		body["lookback"] = in.Lookback
	}
	if len(in.Weights) > 0 {
		body["weights"] = in.Weights
	}
	baseURL := PythonBaseURL()
	if baseURL == "" {
		return marshalError("Python 未配置，请设置 STOCK_PYTHON_URL 并启动 Python 服务")
	}
	s, err := PostJSON(ctx, baseURL, "/api/analysis/breakout_score", body)
	if err != nil {
		return marshalError("breakout_score 接口调用失败: " + err.Error())
	}
	return FormatBreakoutScoreResponse(s), nil
}

// FormatBreakoutScoreResponse 将 breakout_score 返回的 JSON 转为可读摘要（优先 report）。
func FormatBreakoutScoreResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	if report, _ := m["report"].(string); report != "" {
		return strings.TrimSpace(report)
	}
	var b strings.Builder
	if score, ok := m["breakout_score"].(float64); ok {
		b.WriteString(fmt.Sprintf("突破评分: %.1f / 100\n", score))
	}
	if dims, _ := m["dimension_scores"].(map[string]any); dims != nil {
		b.WriteString("分项: ")
		for k, v := range dims {
			if f, ok := v.(float64); ok {
				b.WriteString(fmt.Sprintf("%s=%.1f ", k, f))
			}
		}
		b.WriteString("\n")
	}
	if signals, _ := m["signals"].([]any); len(signals) > 0 {
		b.WriteString("信号: ")
		for _, s := range signals {
			if str, ok := s.(string); ok {
				b.WriteString(str + " ")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// RunScoringTool 综合评分与可操作清单。
type RunScoringTool struct{}

func (t *RunScoringTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "run_scoring",
		Desc: "对指定股票进行综合评分：汇总技术、基本面、消息等维度，输出总分、分项、可操作清单与免责。可传入已获取的各维度结果 inputs，否则由后端拉取。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
			"inputs": {
				Desc:     "可选，各维度分析结果（technical、fundamental、news 等），便于加权汇总",
				Required: false,
				Type:     schema.Object,
			},
		}),
	}, nil
}

func (t *RunScoringTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string         `json:"symbol"`
		Inputs map[string]any `json:"inputs"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	body := map[string]any{"symbol": in.Symbol}
	if in.Inputs != nil {
		body["inputs"] = in.Inputs
	}
	baseURL := PythonBaseURL()
	if baseURL != "" {
		s, err := PostJSON(ctx, baseURL, "/api/analysis/score", body)
		if err == nil {
			return FormatScoringResponse(s), nil
		}
	}
	return marshalError("Python 未配置或 score 接口调用失败")
}

// FormatScoringResponse 将s返回的 JSON 转为可读摘要。
func FormatScoringResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}

	//"technical":40.0,"fundamental":38.0,"news":55.0,"market_trend":65.0,"sector":55.0,"pattern":55.0},"one_line_summary":"综合评分 48.0，hold；仅供参考。
	var b strings.Builder
	b.WriteString("\n\n")
	if dims, ok := m["dimension_scores"].(map[string]any); ok && dims != nil {
		// 可以定义一个顺序，使输出更整齐
		order := []string{"technical", "fundamental", "news", "market_trend", "sector", "pattern", "sentiment"}
		nameMap := map[string]string{
			"technical":    "技术面",
			"fundamental":  "基本面",
			"news":         "消息面",
			"market_trend": "大盘趋势",
			"sector":       "板块",
			"pattern":      "形态",
			"sentiment":    "情绪/资金面",
		}
		for _, key := range order {
			if val, ok := dims[key]; ok {
				// 尝试转为 float64
				var score float64
				switch v := val.(type) {
				case float64:
					score = v
				case float32:
					score = float64(v)
				case int:
					score = float64(v)
				default:
					// 无法转换则跳过或使用默认值
					continue
				}
				// 写入：例如 "【技术面】70.0"
				b.WriteString(fmt.Sprintf("【%s】%.1f ", nameMap[key], score))
			}
		}
	}

	b.WriteString("\n\n")
	return b.String()
}
