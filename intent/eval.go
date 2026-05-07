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

// IntentExpected 评测期望（宽松：task_kind 必填；symbols 非空则集合须一致）。
type IntentExpected struct {
	TaskKind string   `json:"task_kind"`
	Symbols  []string `json:"symbols,omitempty"`
}

// IntentEvalResult 单条结果。
type IntentEvalResult struct {
	ID           string       `json:"id"`
	OKTask       bool         `json:"okTask"`
	OKSymbols    bool         `json:"okSymbols"`
	Score        float64      `json:"score"` // 0~1
	Got          *ParsedIntent `json:"got,omitempty"`
	Notes        string       `json:"notes,omitempty"`
}

// IntentEvalSummary 汇总。
type IntentEvalSummary struct {
	SuitePath        string             `json:"suitePath"`
	Total            int                `json:"total"`
	TaskAccuracy     float64            `json:"taskAccuracy"`
	SymbolAccuracy   float64            `json:"symbolAccuracy"`   // 仅统计 expected.symbols 非空的用例
	SymbolCases      int                `json:"symbolCases"`
	AverageScore     float64            `json:"averageScore"`    // 每条：task对=0.6 symbol对=0.4（无symbol期望时仅task=1）
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
			res.OKSymbols = true // 无期望时不扣分
		}
		// 打分：任务正确 0.6；若本用例要求 symbols，符号集合正确再加 0.4，否则任务正确即 1.0
		if len(expSyms) > 0 {
			if res.OKTask && res.OKSymbols {
				res.Score = 1
			} else if res.OKTask {
				res.Score = 0.6
			} else {
				res.Score = 0
			}
		} else {
			if res.OKTask {
				res.Score = 1
			} else {
				res.Score = 0
			}
		}
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
	return out, nil
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
