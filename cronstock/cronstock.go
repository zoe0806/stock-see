// Package cronstock 管理 data/cron_stocks.json 的订阅与定时推送（交易日交易时间每 5 分钟向飞书发送股票实时价格）。
package cronstock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stock-see/tools"
)

const (
	FileName = "cron_stocks.json"
)

// Subscription 单条订阅：股票代码、推送间隔（分钟）、飞书 Webhook、上次推送时间。
type Subscription struct {
	Symbol           string `json:"symbol"`
	IntervalMinutes  int    `json:"interval_minutes"`
	FeishuWebhookURL string `json:"feishu_webhook_url"`
	LastSentAt       string `json:"last_sent_at,omitempty"`
}

// CronStocks 持久化结构。
type CronStocks struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

func dataDir() string {
	dir := os.Getenv("STOCK_DATA_DIR")
	if dir != "" {
		return dir
	}
	return "data"
}

// FilePath 返回 cron_stocks.json 的完整路径。
func FilePath() string {
	return filepath.Join(dataDir(), FileName)
}

// Load 从 data/cron_stocks.json 读取，不存在或为空则返回空列表。
func Load() (*CronStocks, error) {
	path := FilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CronStocks{Subscriptions: nil}, nil
		}
		return nil, err
	}
	var out CronStocks
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Subscriptions == nil {
		out.Subscriptions = []Subscription{}
	}
	return &out, nil
}

// Save 将配置写回 data/cron_stocks.json。
func Save(c *CronStocks) error {
	path := FilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// IsTradingTime 判断当前是否为 A 股交易时间（中国时区 周一到周五 9:30-11:30, 13:00-15:00）。
func IsTradingTime() bool {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	weekday := now.Weekday()
	if weekday == time.Sunday || weekday == time.Saturday {
		return false
	}
	hour, min := now.Hour(), now.Minute()
	minutes := hour*60 + min
	// 9:30 = 570, 11:30 = 690; 13:00 = 780, 15:00 = 900
	if minutes >= 570 && minutes < 690 {
		return true
	}
	if minutes >= 780 && minutes < 900 {
		return true
	}
	return false
}

// ShouldSend 根据 interval_minutes 和 last_sent_at 判断是否该推送。
func ShouldSend(sub Subscription) bool {
	if sub.IntervalMinutes <= 0 {
		sub.IntervalMinutes = 5
	}
	if sub.LastSentAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, sub.LastSentAt)
	if err != nil {
		return true
	}
	return time.Since(t) >= time.Duration(sub.IntervalMinutes)*time.Minute
}

// FetchMarket 通过 Python 行情接口获取最新价等信息，返回 JSON 字符串。
func FetchMarket(ctx context.Context, symbol string) (string, error) {
	baseURL := tools.PythonBaseURL()
	if baseURL == "" {
		return "", fmt.Errorf("STOCK_PYTHON_URL not set")
	}
	return tools.GetJSON(ctx, baseURL, "/api/market/"+strings.TrimSpace(symbol))
}

// SendFeishu 向飞书 Webhook 发送文本消息。
func SendFeishu(ctx context.Context, webhookURL, text string) error {
	body := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("feishu webhook %s: %s", resp.Status, string(b))
	}
	return nil
}

// RunTick 在交易时间内对到期订阅拉取行情并推送到飞书，并更新 last_sent_at 写回文件。
func RunTick(ctx context.Context) error {
	if !IsTradingTime() {
		return nil
	}
	fmt.Println("RunTick")
	c, err := Load()
	if err != nil {
		return err
	}
	if len(c.Subscriptions) == 0 {
		return nil
	}
	updated := false
	for i := range c.Subscriptions {
		sub := &c.Subscriptions[i]
		if !ShouldSend(*sub) {
			continue
		}
		marketJSON, err := FetchMarket(ctx, sub.Symbol)
		if err != nil {
			fmt.Println("FetchMarket error", err)
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(marketJSON), &m) != nil {
			fmt.Println("json.Unmarshal error", err)
			continue
		}
		name, _ := m["name"].(string)
		lastPrice, _ := m["last_price"].(float64)
		changePct, _ := m["change_pct"].(float64)
		text := fmt.Sprintf("【%s】%s 现价 %.2f 元，涨跌幅 %.2f%%", sub.Symbol, name, lastPrice, changePct)
		if err := SendFeishu(ctx, sub.FeishuWebhookURL, text); err != nil {
			fmt.Println("SendFeishu error", err)
			continue
		}
		sub.LastSentAt = time.Now().Format(time.RFC3339)
		updated = true
	}
	if updated {
		return Save(c)
	}
	return nil
}
