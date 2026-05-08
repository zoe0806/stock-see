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
// symbols 为六位代码列表：单标的时行为与原先一致；多标的时为每只标的分别执行依赖 symbol 的 hint，
// 不依赖标的的 hint（如 market-trend）全局只执行一次并置于段首。
func RunSkillHintsTools(ctx context.Context, symbols []string, userMessage string, hints []string) string {
	if strings.TrimSpace(os.Getenv(disableSkillHintToolsEnv)) == "1" {
		return ""
	}
	if len(hints) == 0 {
		return ""
	}
	includeReports := strings.Contains(userMessage, "财报")
	ordered := dedupeHintsPreserveOrder(hints)
	if len(ordered) == 0 {
		return ""
	}

	syms := normalizePrefetchSymbols(symbols)
	perSym, global := partitionSkillHints(ordered)

	if len(syms) <= 1 {
		sym := ""
		if len(syms) == 1 {
			sym = syms[0]
		}
		return runSkillHintsOrdered(ctx, sym, includeReports, ordered)
	}

	// 多标的：先全局 hint，再按标的分段（避免大盘类重复跑多次）
	var b strings.Builder
	if len(global) > 0 {
		if g := runSkillHintsOrdered(ctx, "", includeReports, global); strings.TrimSpace(g) != "" {
			b.WriteString(strings.TrimSpace(g))
		}
	}
	for _, sym := range syms {
		body := runSkillHintsOrdered(ctx, sym, includeReports, perSym)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n\n\n")
		}
		b.WriteString("## 标的 ")
		b.WriteString(sym)
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(body))
	}
	return strings.TrimSpace(b.String())
}

func normalizePrefetchSymbols(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	var out []string
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if len(s) != 6 || !prefetchDigitsOnly(s) {
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

func prefetchDigitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// partitionSkillHints 将不依赖具体标的的 hint 拆出，多标的时只执行一次。
func partitionSkillHints(ordered []string) (perSymbol []string, global []string) {
	for _, k := range ordered {
		if k == "market-trend" {
			global = append(global, k)
			continue
		}
		perSymbol = append(perSymbol, k)
	}
	return
}

// runSkillHintsOrdered 按 ordered 顺序并行执行各 hint，输出顺序与 ordered 一致。
func runSkillHintsOrdered(ctx context.Context, symbol string, includeReports bool, ordered []string) string {
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
			b.WriteString("\n\n\n\n")
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
		log.Println("[skill_hints] 执行 realtime-market")
		t := &GetMarketDataTool{}
		if symbol == "" {
			return "行情", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "行情（get_market_data）", body, err

	case "technical":
		log.Println("[skill_hints] 执行 technical")
		t := &RunTechnicalTool{}
		if symbol == "" {
			return "技术面", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "技术面（run_technical）", body, err

	case "fundamental":
		log.Println("[skill_hints] 执行 fundamental")
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
		log.Println("[skill_hints] 执行 news")
		t := &RunNewsTool{}
		if symbol == "" {
			return "消息面", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "消息面（run_news）", body, err

	case "sentiment":
		log.Println("[skill_hints] 执行 sentiment")
		t := &RunSentimentTool{}
		if symbol == "" {
			return "情绪/资金面", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "情绪/资金面（run_sentiment）", body, err

	case "market-trend":
		log.Println("[skill_hints] 执行 market-trend")
		t := &RunMarketTrendTool{}
		body, err = t.InvokableRun(ctx, `{}`)
		return "大盘趋势（run_market_trend）", body, err

	case "sector":
		log.Println("[skill_hints] 执行 sector")
		t := &RunSectorTool{}
		if symbol == "" {
			return "板块", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "板块（run_sector）", body, err

	case "pattern":
		log.Println("[skill_hints] 执行 pattern")
		t := &RunPatternTool{}
		if symbol == "" {
			return "形态", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "形态（run_pattern）", body, err

	case "scoring":
		log.Println("[skill_hints] 执行 scoring")
		t := &RunScoringTool{}
		if symbol == "" {
			return "综合评分", "", nil
		}
		args, _ := json.Marshal(map[string]string{"symbol": symbol})
		body, err = t.InvokableRun(ctx, string(args))
		return "综合评分（run_scoring）", body, err

	default:
		log.Printf("[skill_hints] unknown hint: %q", key)
		return key, "", nil
	}
}
