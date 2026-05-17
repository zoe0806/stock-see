package tools

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// SkillPrefetchResult 技能预取拆分结果：基本面全文只应出现一次，避免同时塞进 System Context（Extra）
// 与用户可见对话重复两遍。
type SkillPrefetchResult struct {
	// ContextMarkdown 注入 BuildContext「其他上下文」：技术面/行情等全文；基本面仅占位说明，不含 report 正文。
	ContextMarkdown string
	// FundamentalForUser 拼入主模型 User 消息末尾一次（基本面完整预取）；为空则表示本轮未拉基本面。
	FundamentalForUser string
}

// RunSkillHintsTools 按意图中的 skill_hints 直接执行对应 tool.BaseTool（不经 Agent 调度）。
// 基本面完整报告放在 FundamentalForUser，请勿再将全文写入 ContextMarkdown，以免与对话重复展示。
func RunSkillHintsTools(ctx context.Context, symbols []string, userMessage string, hints []string) SkillPrefetchResult {
	out := SkillPrefetchResult{}
	if len(hints) == 0 || len(symbols) == 0 {
		return out
	}
	includeReports := strings.Contains(userMessage, "财报")
	ordered := dedupeHintsPreserveOrder(hints)
	if len(ordered) == 0 {
		return out
	}

	syms := normalizePrefetchSymbols(symbols)
	perSym, global := partitionSkillHints(ordered)

	if len(syms) == 1 {
		sym := syms[0]
		ctxMD, fund := runSkillHintsOrderedSplit(ctx, sym, includeReports, ordered)
		out.ContextMarkdown = strings.TrimSpace(ctxMD)
		out.FundamentalForUser = strings.TrimSpace(fund)
		return out
	}

	// 多标的：先全局 hint，再按标的分段
	var ctxB, fundB strings.Builder
	if len(global) > 0 {
		ctxG, fundG := runSkillHintsOrderedSplit(ctx, "", includeReports, global)
		ctxG = strings.TrimSpace(ctxG)
		if ctxG != "" {
			ctxB.WriteString(ctxG)
		}
		if strings.TrimSpace(fundG) != "" {
			fundB.WriteString(strings.TrimSpace(fundG))
		}
	}
	for _, sym := range syms {
		ctxMD, fund := runSkillHintsOrderedSplit(ctx, sym, includeReports, perSym)
		ctxMD = strings.TrimSpace(ctxMD)
		fund = strings.TrimSpace(fund)
		if ctxMD != "" {
			if ctxB.Len() > 0 {
				ctxB.WriteString("\n\n\n\n")
			}
			ctxB.WriteString("## 标的 ")
			ctxB.WriteString(sym)
			ctxB.WriteString("\n\n")
			ctxB.WriteString(ctxMD)
		}
		if fund != "" {
			if fundB.Len() > 0 {
				fundB.WriteString("\n\n\n\n")
			}
			fundB.WriteString("## 标的 ")
			fundB.WriteString(sym)
			fundB.WriteString("\n\n")
			fundB.WriteString(fund)
		}
	}
	out.ContextMarkdown = strings.TrimSpace(ctxB.String())
	out.FundamentalForUser = strings.TrimSpace(fundB.String())
	return out
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

// runSkillHintsOrderedSplit 按 ordered 并行执行；基本面全文进入 fundamentalBlob，仅占位说明进入 ctxMarkdown。
func runSkillHintsOrderedSplit(ctx context.Context, symbol string, includeReports bool, ordered []string) (ctxMarkdown string, fundamentalBlob string) {
	if len(ordered) == 0 {
		return "", ""
	}
	type out struct {
		i          int
		ctxSec     string
		fundAppend string
		err        error
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
			nk := normalizeSkillHintKey(k)
			if nk == "fundamental" || nk == "news" {
				body = strings.TrimSpace(SanitizeToolTextForUser(body))
				if body == "" {
					ch <- out{i: idx, err: err}
					return
				}
				ctxSec := "### " + title + "\n\n> 完整报告已附在用户消息末尾「预取」；回复时请概括要点，勿整段重复。"
				ch <- out{i: idx, ctxSec: ctxSec, fundAppend: body}
				return
			}
			if strings.TrimSpace(body) == "" {
				ch <- out{i: idx, err: err}
				return
			}
			sec := "### " + title + "\n\n" + strings.TrimSpace(SanitizeToolTextForUser(body))
			ch <- out{i: idx, ctxSec: sec, err: err}
		}(i, key)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	ctxSections := make([]string, len(ordered))
	fundSlots := make([]string, len(ordered))
	for r := range ch {
		if r.ctxSec != "" {
			ctxSections[r.i] = r.ctxSec
		}
		if r.fundAppend != "" {
			fundSlots[r.i] = r.fundAppend
		}
	}
	var ctxB strings.Builder
	for _, s := range ctxSections {
		if s == "" {
			continue
		}
		if ctxB.Len() > 0 {
			ctxB.WriteString("\n\n\n\n")
		}
		ctxB.WriteString(s)
	}
	var fundB strings.Builder
	for _, s := range fundSlots {
		if s == "" {
			continue
		}
		if fundB.Len() > 0 {
			fundB.WriteString("\n\n\n\n")
		}
		fundB.WriteString(s)
	}
	return strings.TrimSpace(ctxB.String()), strings.TrimSpace(fundB.String())
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
