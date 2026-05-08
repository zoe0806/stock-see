// Package combo：内存倒排（意图关键词、指标同义词、时间短语、股票别名）毫秒级匹配，供默认意图模式使用。
package combo

import (
	"sort"
	"strings"
	"sync"

	"stock-see/kb"
)

// MatchedStock 问句中命中的一只标的（按出现顺序）。
type MatchedStock struct {
	Code string
	Name string
}

// RawSlots 单次匹配的原始槽位（未做冲突消解）。
type RawSlots struct {
	SymbolCode   string // 首只标的（兼容旧逻辑）
	SymbolName   string
	// MatchedStocks 问句中所有命中标的，按「首次出现位置」排序，用于「对比 A 与 B」等多标的。
	MatchedStocks []MatchedStock
	IntentScores map[string]int // intent key -> 命中关键词次数
	MetricFields []string       // 结构化字段名，如 revenue、net_profit
	TimeParsed   string         // 与 knowledge.json time_phrases.parsed 对齐，如 last_3_years
	TimeRange    string         // time_phrases.range
}

var (
	idxOnce sync.Once
	idx     *index
	idxErr  error
)

type index struct {
	// 意图：词条 -> 意图键（倒排）
	intentTerm map[string][]string
	// 股票：按别名长度降序，便于最长匹配
	stocks []stockHit
	// 指标：同义词长度降序
	metrics []synHit
	// 时间：短语长度降序
	times []timeHit
}

type stockHit struct {
	Phrase string
	Code   string
	Name   string
}

type synHit struct {
	Phrase string
	Field  string
}

type timeHit struct {
	Phrase string
	Parsed string
	Range  string
}

func defaultIndex() *index {
	idxOnce.Do(func() {
		kf, err := kb.LoadKnowledgeFile("")
		if err != nil {
			idxErr = err
			return
		}
		idx = buildIndex(kf)
	})
	if idxErr != nil {
		return nil
	}
	return idx
}

func buildIndex(k *kb.KnowledgeFile) *index {
	x := &index{
		intentTerm: make(map[string][]string),
	}
	if k == nil {
		return x
	}
	for key, words := range k.Intents {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		for _, w := range words {
			w = strings.TrimSpace(w)
			if w == "" {
				continue
			}
			x.intentTerm[w] = appendUnique(x.intentTerm[w], key)
		}
	}
	for _, s := range k.Stocks {
		code := strings.TrimSpace(s.Code)
		name := strings.TrimSpace(s.Name)
		if code == "" {
			continue
		}
		add := func(phrase string) {
			phrase = strings.TrimSpace(phrase)
			if phrase == "" {
				return
			}
			x.stocks = append(x.stocks, stockHit{Phrase: phrase, Code: code, Name: name})
		}
		add(name)
		add(code)
		for _, a := range s.Aliases {
			add(a)
		}
	}
	sort.Slice(x.stocks, func(i, j int) bool {
		return len([]rune(x.stocks[i].Phrase)) > len([]rune(x.stocks[j].Phrase))
	})

	for _, m := range k.Metrics {
		sy := strings.TrimSpace(m.Synonym)
		fd := strings.TrimSpace(m.Field)
		if sy == "" || fd == "" {
			continue
		}
		x.metrics = append(x.metrics, synHit{Phrase: sy, Field: fd})
	}
	sort.Slice(x.metrics, func(i, j int) bool {
		return len([]rune(x.metrics[i].Phrase)) > len([]rune(x.metrics[j].Phrase))
	})

	for _, tp := range k.TimePhrases {
		ph := strings.TrimSpace(tp.Phrase)
		if ph == "" {
			continue
		}
		x.times = append(x.times, timeHit{
			Phrase: ph,
			Parsed: strings.TrimSpace(tp.Parsed),
			Range:  strings.TrimSpace(tp.Range),
		})
	}
	sort.Slice(x.times, func(i, j int) bool {
		return len([]rune(x.times[i].Phrase)) > len([]rune(x.times[j].Phrase))
	})
	return x
}

func appendUnique(slice []string, s string) []string {
	for _, x := range slice {
		if x == s {
			return slice
		}
	}
	return append(slice, s)
}

// MatchSlots 对用户输入做四路最长匹配（股票别名、意图关键词、指标、时间）。
func MatchSlots(query string) RawSlots {
	q := strings.TrimSpace(query)
	var out RawSlots
	out.IntentScores = make(map[string]int)
	x := defaultIndex()
	if x == nil || q == "" {
		return out
	}
	// 同一代码只保留「在问句中最早出现」的一次（避免短别名覆盖长称呼）；多代码按出现顺序收集。
	type posHit struct {
		idx  int
		code string
		name string
	}
	byCode := make(map[string]posHit)
	for _, sh := range x.stocks {
		if !strings.Contains(q, sh.Phrase) {
			continue
		}
		idx := strings.Index(q, sh.Phrase)
		if idx < 0 {
			continue
		}
		prev, ok := byCode[sh.Code]
		if !ok || idx < prev.idx {
			byCode[sh.Code] = posHit{idx: idx, code: sh.Code, name: sh.Name}
		}
	}
	var hits []posHit
	for _, h := range byCode {
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].idx < hits[j].idx })
	for _, h := range hits {
		out.MatchedStocks = append(out.MatchedStocks, MatchedStock{Code: h.code, Name: h.name})
	}
	if len(out.MatchedStocks) > 0 {
		out.SymbolCode = out.MatchedStocks[0].Code
		out.SymbolName = out.MatchedStocks[0].Name
	}
	for term, intents := range x.intentTerm {
		if strings.Contains(q, term) {
			for _, ik := range intents {
				out.IntentScores[ik]++
			}
		}
	}
	seenM := map[string]struct{}{}
	for _, mh := range x.metrics {
		if strings.Contains(q, mh.Phrase) {
			if _, ok := seenM[mh.Field]; ok {
				continue
			}
			seenM[mh.Field] = struct{}{}
			out.MetricFields = append(out.MetricFields, mh.Field)
		}
	}
	for _, th := range x.times {
		if strings.Contains(q, th.Phrase) {
			out.TimeParsed = th.Parsed
			out.TimeRange = th.Range
			break
		}
	}
	return out
}
