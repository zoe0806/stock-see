package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

)

// IntentSuite 意图评测集。
type IntentSuite struct {
	Version int                 `json:"version"`
	Note    string              `json:"note,omitempty"`
	Cases   []IntentEvalCase    `json:"cases"`
}

// IntentEvalCase 单条用例：输入与期望槽位。
type IntentEvalCase struct {
	ID          string          `json:"id"`
	UserMessage string          `json:"userMessage"`
	Session     string          `json:"session,omitempty"`
	Symbol      string          `json:"symbol,omitempty"`
	Expected    IntentExpected  `json:"expected"`
}

// IntentExpected 评测期望（task_kind 必填；symbols / skill_hints 非空时分别校验）。
// skill_hints：期望的维度须全部出现在解析结果中（可多不可少，大小写不敏感）。
type IntentExpected struct {
	TaskKind   string   `json:"task_kind"`
	Symbols    []string `json:"symbols,omitempty"`
	SkillHints []string `json:"skill_hints,omitempty"`
}

// IntentEvalResult 单条结果。
type IntentEvalResult struct {
	ID           string        `json:"id"`
	OKTask       bool          `json:"okTask"`
	OKSymbols    bool          `json:"okSymbols"`
	OKHints      bool          `json:"okHints"`
	Score        float64       `json:"score"` // 0~1，按 task / symbols / hints 有标注的项加权平均
	Got          *ParsedIntent `json:"got,omitempty"`
	Notes        string        `json:"notes,omitempty"`
}

// IntentEvalSummary 汇总。
type IntentEvalSummary struct {
	SuitePath        string             `json:"suitePath"`
	Total            int                `json:"total"`
	TaskAccuracy     float64            `json:"taskAccuracy"`
	SymbolAccuracy   float64            `json:"symbolAccuracy"` // 仅统计 expected.symbols 非空的用例
	SymbolCases      int                `json:"symbolCases"`
	HintAccuracy     float64            `json:"hintAccuracy"` // 仅统计 expected.skill_hints 非空的用例
	HintCases        int                `json:"hintCases"`
	AverageScore     float64            `json:"averageScore"`
	Results          []IntentEvalResult `json:"results"`
}

// LoadIntentSuite 读取 JSON。
func LoadIntentSuite(path string) (*IntentSuite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s IntentSuite
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RunEval 对评测集调用 Parse，计算准确率。
func RunEval(ctx context.Context, cm ParseModel, suitePath string) (*IntentEvalSummary, error) {
	su, err := LoadIntentSuite(suitePath)
	if err != nil {
		return nil, err
	}
	if len(su.Cases) == 0 {
		return nil, fmt.Errorf("intent eval: suite has no cases")
	}
	out := &IntentEvalSummary{
		SuitePath: suitePath,
		Total:     len(su.Cases),
		Results:   make([]IntentEvalResult, 0, len(su.Cases)),
	}
	var taskHits, symHits, symTotal int
	var hintHits, hintTotal int
	var scoreSum float64
	for _, c := range su.Cases {
		in := ParseInput{
			UserMessage:    c.UserMessage,
			SessionHistory: c.Session,
			ExplicitSymbol: c.Symbol,
		}
		got := Parse(ctx, cm, in)
		res := IntentEvalResult{ID: c.ID, Got: got}
		expKind := TaskKind(strings.TrimSpace(c.Expected.TaskKind))
		if got == nil {
			res.Notes = "parse returned nil"
			res.Score = 0
			out.Results = append(out.Results, res)
			continue
		}
		res.OKTask = got.TaskKind == expKind
		if res.OKTask {
			taskHits++
		}
		expSyms := NormalizeSymbols(c.Expected.Symbols)
		if len(expSyms) > 0 {
			symTotal++
			gotSyms := NormalizeSymbols(got.Symbols)
			res.OKSymbols = stringSliceEqualSet(gotSyms, expSyms)
			if res.OKSymbols {
				symHits++
			}
		} else {
			res.OKSymbols = true // 无期望时不参与该项
		}

		expHints := normalizeEvalHints(c.Expected.SkillHints)
		if len(expHints) > 0 {
			hintTotal++
			gotHints := normalizeEvalHints(got.SkillHints)
			res.OKHints = skillHintsContainAll(gotHints, expHints)
			if res.OKHints {
				hintHits++
			}
		} else {
			res.OKHints = true
		}

		// 打分：对「有标注」的维度等权平均（至少 task）
		var parts float64
		var ok float64
		parts++
		if res.OKTask {
			ok++
		}
		if len(expSyms) > 0 {
			parts++
			if res.OKSymbols {
				ok++
			}
		}
		if len(expHints) > 0 {
			parts++
			if res.OKHints {
				ok++
			}
		}
		res.Score = ok / parts
		scoreSum += res.Score
		out.Results = append(out.Results, res)
	}
	if out.Total > 0 {
		out.TaskAccuracy = float64(taskHits) / float64(out.Total)
		out.AverageScore = scoreSum / float64(out.Total)
	}
	if symTotal > 0 {
		out.SymbolAccuracy = float64(symHits) / float64(symTotal)
	}
	out.SymbolCases = symTotal
	if hintTotal > 0 {
		out.HintAccuracy = float64(hintHits) / float64(hintTotal)
	}
	out.HintCases = hintTotal
	return out, nil
}

func normalizeEvalHints(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// skillHintsContainAll 要求 got 中（规范化后）包含 expected 中的每一项（允许多出）。
func skillHintsContainAll(gotNorm, expNorm []string) bool {
	if len(expNorm) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(gotNorm))
	for _, h := range gotNorm {
		set[h] = struct{}{}
	}
	for _, h := range expNorm {
		if _, ok := set[h]; !ok {
			return false
		}
	}
	return true
}

func stringSliceEqualSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
