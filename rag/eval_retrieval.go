package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"stock-see/tools"
)

// RetrievalSuite 检索离线评测集（需在 Redis 中已有与 gold 可对齐的文档；可先 Sync 再跑）。
type RetrievalSuite struct {
	Version   int             `json:"version"`
	Note      string          `json:"note,omitempty"`
	KDefault  int             `json:"k_default,omitempty"` // 默认 Top-K，缺省 5
	Cases     []RetrievalCase `json:"cases"`
	Embedding string          `json:"-"` // 运行时装填
	IndexVec  string          `json:"-"`
	IndexLex  string          `json:"-"`
}

// RetrievalCase 单条：自然语言 query + 可选 code/日期窗 + 黄金相关性判定。
type RetrievalCase struct {
	ID      string        `json:"id"`
	Query   string        `json:"query"`
	Code    string        `json:"code,omitempty"`
	From    string        `json:"from,omitempty"`
	To      string        `json:"to,omitempty"`
	K       int           `json:"k,omitempty"`
	Gold    RetrievalGold `json:"gold"`
}

// RetrievalGold 命中规则：任一子条件满足即视为该条文档「相关」；Top-K 内存在相关文档则本 case 计 1。
type RetrievalGold struct {
	KeyAny             []string `json:"key_any,omitempty"`
	TitleContainsAny   []string `json:"title_contains_any,omitempty"`
	URLContainsAny     []string `json:"url_contains_any,omitempty"`
}

// RetrievalCaseResult 单 case × 单 mode。
type RetrievalCaseResult struct {
	ID       string `json:"id"`
	Mode     string `json:"mode"`
	Hit      bool   `json:"hit"`
	TopTitles []string `json:"topTitles,omitempty"`
}

// RetrievalModeSummary 某一 SearchMode 的汇总。
type RetrievalModeSummary struct {
	Mode       string  `json:"mode"`
	K          int     `json:"k"`
	Cases      int     `json:"cases"`
	HitCount   int     `json:"hitCount"`
	HitRate    float64 `json:"hitRate"` // Hit@K 均值（二值）
}

// RetrievalEvalSummary 整场评测。
type RetrievalEvalSummary struct {
	SuitePath     string                            `json:"suitePath"`
	EvaluatedAt   string                            `json:"evaluatedAt"`
	KDefault      int                               `json:"kDefault"`
	Embedding     string                            `json:"embeddingModel,omitempty"`
	IndexVector   string                            `json:"indexVector,omitempty"`
	IndexLex      string                            `json:"indexLex,omitempty"`
	ByMode        []RetrievalModeSummary            `json:"byMode"`
	DetailsByMode map[string][]RetrievalCaseResult `json:"detailsByMode,omitempty"`
}

// LoadRetrievalSuite 读取 JSON。
func LoadRetrievalSuite(path string) (*RetrievalSuite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s RetrievalSuite
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RunRetrievalEval 对三种策略各跑一遍 suite，计算 Hit@K（二值命中比例）。
func RunRetrievalEval(ctx context.Context, c *Client, suitePath string, includeDetails bool) (*RetrievalEvalSummary, error) {
	if c == nil || c.rdb == nil {
		return nil, fmt.Errorf("retrieval eval: redis client required")
	}
	su, err := LoadRetrievalSuite(suitePath)
	if err != nil {
		return nil, err
	}
	if len(su.Cases) == 0 {
		return nil, fmt.Errorf("retrieval eval: suite has no cases")
	}
	kDef := su.KDefault
	if kDef <= 0 {
		kDef = 5
	}
	su.Embedding = tools.GetembeddingModel()
	su.IndexVec = indexVectorName
	su.IndexLex = indexName

	modes := []SearchMode{SearchModeVector, SearchModeHybrid, SearchModeHybridRerank}
	out := &RetrievalEvalSummary{
		SuitePath:     suitePath,
		EvaluatedAt:   time.Now().UTC().Format(time.RFC3339),
		KDefault:      kDef,
		Embedding:     su.Embedding,
		IndexVector:   su.IndexVec,
		IndexLex:      su.IndexLex,
		DetailsByMode: map[string][]RetrievalCaseResult{},
	}

	for _, mode := range modes {
		var hits int
		var modeDetails []RetrievalCaseResult
		for _, cs := range su.Cases {
			k := cs.K
			if k <= 0 {
				k = kDef
			}
			if !goldHasRule(cs.Gold) {
				continue
			}
			res, err := c.SearchByQueryMode(ctx, mode, cs.Query, cs.Code, cs.From, cs.To, k)
			if err != nil {
				return nil, fmt.Errorf("case %s mode %s: %w", cs.ID, mode, err)
			}
			hit := hitAtK(res, cs.Gold, k)
			if hit {
				hits++
			}
			if includeDetails {
				titles := make([]string, 0, len(res))
				for _, r := range res {
					titles = append(titles, truncateRunes(r.Item.Title, 80))
				}
				modeDetails = append(modeDetails, RetrievalCaseResult{
					ID:        cs.ID,
					Mode:      string(mode),
					Hit:       hit,
					TopTitles: titles,
				})
			}
		}
		n := countValidGoldCases(su.Cases)
		hr := 0.0
		if n > 0 {
			hr = float64(hits) / float64(n)
		}
		out.ByMode = append(out.ByMode, RetrievalModeSummary{
			Mode:     string(mode),
			K:        kDef,
			Cases:    n,
			HitCount: hits,
			HitRate:  hr,
		})
		if includeDetails && len(modeDetails) > 0 {
			out.DetailsByMode[string(mode)] = modeDetails
		}
	}
	if !includeDetails {
		out.DetailsByMode = nil
	}
	return out, nil
}

func countValidGoldCases(cases []RetrievalCase) int {
	n := 0
	for _, c := range cases {
		if goldHasRule(c.Gold) {
			n++
		}
	}
	return n
}

func goldHasRule(g RetrievalGold) bool {
	return len(g.KeyAny) > 0 || len(g.TitleContainsAny) > 0 || len(g.URLContainsAny) > 0
}

func hitAtK(results []SearchResult, g RetrievalGold, k int) bool {
	if k > len(results) {
		k = len(results)
	}
	for i := 0; i < k; i++ {
		if rowMatchesGold(results[i], g) {
			return true
		}
	}
	return false
}

func rowMatchesGold(r SearchResult, g RetrievalGold) bool {
	for _, want := range g.KeyAny {
		if strings.TrimSpace(want) != "" && r.Key == want {
			return true
		}
	}
	t := strings.ToLower(r.Item.Title)
	for _, sub := range g.TitleContainsAny {
		sub = strings.ToLower(strings.TrimSpace(sub))
		if sub != "" && strings.Contains(t, sub) {
			return true
		}
	}
	u := strings.ToLower(r.Item.URL)
	for _, sub := range g.URLContainsAny {
		sub = strings.ToLower(strings.TrimSpace(sub))
		if sub != "" && strings.Contains(u, sub) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	// 按字节截断略粗暴；评测展示足够
	for len(s) > max {
		s = s[:max]
	}
	return s + "…"
}
