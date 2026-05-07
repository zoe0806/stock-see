// Package tools 实现股票分析助手所需的工具，Phase 1 为 get_market_data、get_news（mock）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// GetMarketDataTool 返回指定标的的行情
type GetMarketDataTool struct{}

func (t *GetMarketDataTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_market_data",
		Desc: "获取指定股票代码的最新行情：最新价、涨跌幅、成交量、近期K线摘要。分析某只股票前应先调用此工具。同时获取公司简介、行业地位、产能布局、技术优势与护城河等信息",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码，如 600519（茅台）、000858（五粮液）",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *GetMarketDataTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid symbol: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	baseURL := PythonBaseURL()
	if baseURL == "" {
		log.Printf("[get_market_data] symbol=%s 未配置 STOCK_PYTHON_URL", in.Symbol)
		return marshalError("未配置 STOCK_PYTHON_URL，请先启动 Python 服务（如 start_python.bat）")
	}
	s, err := GetJSON(ctx, baseURL, "/api/market/"+in.Symbol)
	if err != nil {
		log.Printf("[get_market_data] symbol=%s 调用失败: %v", in.Symbol, err)
		return marshalError("获取行情失败: " + err.Error())
	}
	log.Printf("[get_market_data] symbol=%s 成功", in.Symbol)
	return FormatMarketResponse(s), nil
}

// GetNewsTool 返回指定标的的近期新闻/公告列表与摘要
type GetNewsTool struct{}

func (t *GetNewsTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_news",
		Desc: "获取指定股票代码的近期新闻、公告列表与摘要。用于消息面分析。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {
				Desc:     "股票代码",
				Required: true,
				Type:     schema.String,
			},
			"limit": {
				Desc:     "返回条数，默认 10",
				Required: false,
				Type:     schema.Integer,
			},
		}),
	}, nil
}

func (t *GetNewsTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := sonic.UnmarshalString(argumentsInJSON, &in); err != nil {
		return marshalError("invalid arguments: " + err.Error())
	}
	if in.Symbol == "" {
		return marshalError("symbol is required")
	}
	if in.Limit <= 0 {
		in.Limit = 10
	}
	baseURL := PythonBaseURL()
	if baseURL != "" {
		path := fmt.Sprintf("/api/news/%s?limit=%d", in.Symbol, in.Limit)
		s, err := GetJSON(ctx, baseURL, path)
		if err == nil {
			return FormatNewsResponse(s), nil
		}
	}
	items := []map[string]any{
		{"title": "公司发布业绩预告", "source": "证券时报", "date": "2025-03-06", "summary": "占位"},
		{"title": "行业政策解读", "source": "东方财富", "date": "2025-03-05", "summary": "占位"},
	}
	out := map[string]any{
		"symbol": in.Symbol,
		"source": "mock",
		"items":  items,
		"remark": "Python 未配置或调用失败。",
	}
	return toJSON(out)
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func marshalError(msg string) (string, error) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b), nil
}

// FormatMarketResponse 将行情 API 返回的 JSON 转为可读摘要（避免原始 JSON 与转义换行）。
func FormatMarketResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	var b strings.Builder
	if name, _ := m["name"].(string); name != "" {
		b.WriteString("\n\n【" + name + "】")
	}
	if sym, _ := m["symbol"].(string); sym != "" {
		b.WriteString(" " + sym + " ")
	}
	if last := numFromAny(m["last_price"]); last > 0 {
		b.WriteString(fmt.Sprintf("最新价: %.2f", last))
	}
	if pct := numFromAny(m["change_pct"]); pct != 0 {
		sign := ""
		if pct > 0 {
			sign = "+"
		}
		b.WriteString(fmt.Sprintf("  涨跌幅: %s%.2f%%", sign, pct))
	}
	if v := numFromAny(m["volume"]); v > 0 {
		b.WriteString(fmt.Sprintf("成交量: %s", formatInt(int(v))))
	}
	if open := numFromAny(m["open"]); open > 0 {
		b.WriteString(fmt.Sprintf("今开: %.2f  ", open))
	}
	if high := numFromAny(m["high"]); high > 0 {
		b.WriteString(fmt.Sprintf("最高: %.2f  ", high))
	}
	if low := numFromAny(m["low"]); low > 0 {
		b.WriteString(fmt.Sprintf("最低: %.2f", low))
	}

	b.WriteString("\n\n")
	return b.String()
}

func numFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

func formatInt(n int) string {
	if n >= 1e8 {
		return fmt.Sprintf("%.2f亿", float64(n)/1e8)
	}
	if n >= 1e4 {
		return fmt.Sprintf("%.2f万", float64(n)/1e4)
	}
	return strconv.Itoa(n)
}

// FormatNewsResponse 将新闻 API 返回的 JSON 转为可读摘要。
func FormatNewsResponse(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	var b strings.Builder

	items, _ := m["items"].([]any)
	if len(items) == 0 {
		if remark, _ := m["remark"].(string); remark != "" {
			b.WriteString(remark)
		} else {
			b.WriteString("暂无近期新闻/公告。")
		}
		return b.String()
	}
	b.WriteString("\n\n近期要闻:\n\n")
	for i, it := range items {
		item, _ := it.(map[string]any)
		if item == nil {
			continue
		}
		title, _ := item["title"].(string)
		source, _ := item["source"].(string)
		date, _ := item["date"].(string)
		if title != "" {
			b.WriteString(fmt.Sprintf("%d. %s", i+1, title))
			if source != "" || date != "" {
				b.WriteString(fmt.Sprintf("（%s %s）", source, date))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n\n")
	return b.String()
}

// StockTools 所需工具：行情、新闻、技术、基本面、消息面、情绪资金面、大盘、板块、形态、综合评分。
// StockTools 注册 Agent 可**执行**的工具。SKILL.md 仅注入说明文本，不会请求行情接口；
// 意图解析的 Function Calling 只产出槽位，同样不调 get_market_data。三者不重复。
func StockTools() []tool.BaseTool {
	return []tool.BaseTool{
		&GetMarketDataTool{},
		// &GetNewsTool{},
		&RunAnalysisParallelTool{},
		// &RunTechnicalTool{},
		// &RunFundamentalTool{},
		//&RunNewsTool{},
		// &RunSentimentTool{},
		// &RunMarketTrendTool{},
		// &RunSectorTool{},
		// &RunPatternTool{},
		&RunScoringTool{},
	}
}

// GetMarketDataMock 供 handler 直接调用以注入上下文（不经过 Agent 工具调用）。若已配置 STOCK_PYTHON_URL 可在此同步请求 Python 并返回摘要（当前仍返回占位提示）。
func GetMarketDataMock(symbol string) string {
	if symbol == "" {
		return ""
	}
	if PythonBaseURL() != "" {
		return fmt.Sprintf("【行情】%s：已配置 Python 服务，分析时可调用 get_market_data 获取实时数据。", symbol)
	}
	return fmt.Sprintf("【行情占位】%s：未配置 STOCK_PYTHON_URL 时使用占位；配置并启动 Python 服务后可获真实行情。", symbol)
}

// GetNewsMock 供 handler 直接调用以注入上下文，返回可读摘要。
func GetNewsMock(symbol string, limit int) string {
	if symbol == "" {
		return ""
	}
	if limit <= 0 {
		limit = 5
	}
	if PythonBaseURL() != "" {
		return fmt.Sprintf("【新闻】%s：已配置 Python 服务，分析时可调用 get_news 获取近期新闻/公告（共 %d 条）。", symbol, limit)
	}
	return fmt.Sprintf("【新闻占位】%s 近期新闻/公告：未配置 Python 时占位；共 %d 条。", symbol, limit)
}
