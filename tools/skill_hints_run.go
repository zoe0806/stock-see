package tools

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
)

const disableSkillHintToolsEnv = "STOCK_SKILL_HINT_TOOLS_DISABLE"

// RunSkillHintsTools 按意图中的 skill_hints 直接执行对应 tool.BaseTool（不经 Agent 调度），
// 将各段 Markdown 按 hints 顺序拼接，供注入上下文「其他上下文」。
// symbol 须为六位代码；market-trend 等不依赖 symbol 的仍会执行。
// needFullReport 场景请在调用方跳过，避免与 RunAnalysisParallelStream 重复。
func RunSkillHintsTools(ctx context.Context, symbol string, userMessage string, hints []string) string {
	if strings.TrimSpace(os.Getenv(disableSkillHintToolsEnv)) == "1" {
		return ""
	}
	symbol = strings.TrimSpace(symbol)
	if len(hints) == 0 {
		return ""
	}
	includeReports := strings.Contains(userMessage, "财报")
	ordered := dedupeHintsPreserveOrder(hints)
	if len(ordered) == 0 {
		return ""
	}

	type out struct {
		i   int
		md  string
		err error
	}
	ch := make(chan out, len(ordered))
	var wg sync.WaitGroup
	for i, key := range ordered {
		wg.Add(1)
		go func(idx int, k string) {
			defer wg.Done()
			title, body, err := runSkillHintOnce(ctx, symbol, includeReports, k)
			if err != nil {
				log.Printf("[skill_hints] %s: %v", k, err)
			}
			if strings.TrimSpace(body) == "" {
				ch <- out{i: idx, err: err}
				return
			}
			sec := "### " + title + "\n\n" + strings.TrimSpace(body)
			ch <- out{i: idx, md: sec, err: err}
		}(i, key)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	sections := make([]string, len(ordered))
	for r := range ch {
		if r.md != "" {
			sections[r.i] = r.md
		}
	}
	var b strings.Builder
	for _, s := range sections {
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(s)
	}
	return strings.TrimSpace(b.String())
}

func dedupeHintsPreserveOrder(hints []string) []string {
	seen := make(map[string]struct{}, len(hints))
	var out []string
	for _, h := range hints {
		k := normalizeSkillHintKey(h)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func normalizeSkillHintKey(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	h = strings.ReplaceAll(h, "_", "-")
	return h
}

func runSkillHintOnce(ctx context.Context, symbol string, includeReports bool, key string) (title string, body string, err error) {
	switch key {
	case "realtime-market":
		t := &GetMarketDataTool{}
		if symbol == "" {
			return "行情", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "行情（get_market_data）", body, err

	case "technical":
		t := &RunTechnicalTool{}
		if symbol == "" {
			return "技术面", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "技术面（run_technical）", body, err

	case "fundamental":
		t := &RunFundamentalTool{}
		if symbol == "" {
			return "基本面", "", nil
		}
		fa := map[string]any{"symbol": symbol}
		if includeReports {
			fa["include_reports"] = true
		}
		args, _ := json.Marshal(fa)
		body, err = t.InvokableRun(ctx, string(args))
		return "基本面（run_fundamental）", body, err

	case "news":
		t := &RunNewsTool{}
		if symbol == "" {
			return "消息面", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "消息面（run_news）", body, err

	case "sentiment":
		t := &RunSentimentTool{}
		if symbol == "" {
			return "情绪/资金面", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "情绪/资金面（run_sentiment）", body, err

	case "market-trend":
		t := &RunMarketTrendTool{}
		body, err = t.InvokableRun(ctx, `{}`)
		return "大盘趋势（run_market_trend）", body, err

	case "sector":
		t := &RunSectorTool{}
		if symbol == "" {
			return "板块", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "板块（run_sector）", body, err

	case "pattern":
		t := &RunPatternTool{}
		if symbol == "" {
			return "形态", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "形态（run_pattern）", body, err

	case "scoring":
		t := &RunScoringTool{}
		if symbol == "" {
			return "综合评分", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "综合评分（run_scoring）", body, err

	case "risk":
		return key, "", nil
	default:
		log.Printf("[skill_hints] unknown hint: %q", key)
		return key, "", nil
	}
}
