package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// IntentSuite 意图评测集。
type IntentSuite struct {
	Version int              `json:"version"`
	Note    string           `json:"note,omitempty"`
	Cases   []IntentEvalCase `json:"cases"`
}

// IntentEvalCase 单条用例：输入与期望槽位。
type IntentEvalCase struct {
	ID          string         `json:"id"`
	UserMessage string         `json:"userMessage"`
	Session     string         `json:"session,omitempty"`
	Symbol      string         `json:"symbol,omitempty"`
	Expected    IntentExpected `json:"expected"`
}

// IntentExpected 评测期望（task_kind 必填；symbols / skill_hints / compare_axis 非空时分别校验）。
// skill_hints：期望的维度须全部出现在解析结果中（可多不可少，大小写不敏感，下划线与连字符等价）。
type IntentExpected struct {
	TaskKind    string   `json:"task_kind"`
	Symbols     []string `json:"symbols,omitempty"`
	SkillHints  []string `json:"skill_hints,omitempty"`
	CompareAxis string   `json:"compare_axis,omitempty"`
}

// IntentEvalResult 单条结果。
type IntentEvalResult struct {
	ID               string        `json:"id"`
	ExpectedTaskKind string        `json:"expectedTaskKind,omitempty"`
	OKTask           bool          `json:"okTask"`
	OKSymbols        bool          `json:"okSymbols"`
	OKHints          bool          `json:"okHints"`
	OKCompareAxis    bool          `json:"okCompareAxis"`
	Score            float64       `json:"score"` // 0~1，按已启用维度等权平均
	Got              *ParsedIntent `json:"got,omitempty"`
	Notes            string        `json:"notes,omitempty"`
}

// TaskKindBucket 按「标注的期望 task_kind」分组的任务准确率。
type TaskKindBucket struct {
	ExpectedKind string  `json:"expectedKind"`
	Count        int     `json:"count"`
	TaskHits     int     `json:"taskHits"`
	Accuracy     float64 `json:"accuracy"`
}

// IntentEvalSummary 汇总。
type IntentEvalSummary struct {
	SuitePath           string             `json:"suitePath"`
	Mode                string             `json:"mode,omitempty"`
	EvaluatedAt         string             `json:"evaluatedAt,omitempty"`
	Total               int                `json:"total"`
	TaskAccuracy        float64            `json:"taskAccuracy"`
	SymbolAccuracy      float64            `json:"symbolAccuracy"`
	SymbolCases         int                `json:"symbolCases"`
	HintAccuracy        float64            `json:"hintAccuracy"`
	HintCases           int                `json:"hintCases"`
	CompareAxisAccuracy float64            `json:"compareAxisAccuracy"`
	CompareAxisCases    int                `json:"compareAxisCases"`
	AverageScore        float64            `json:"averageScore"`
	ByExpectedTask      []TaskKindBucket   `json:"byExpectedTask,omitempty"`
	Results             []IntentEvalResult `json:"results"`
}

// EvalPredictor 单条用例如何得到 ParsedIntent（FC / 组合槽位 / pipeline 等）。
type EvalPredictor func(ctx context.Context, c IntentEvalCase) *ParsedIntent

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

// RunEval 对评测集仅调用 Parse（Function Calling），计算准确率。
func RunEval(ctx context.Context, cm ParseModel, suitePath string) (*IntentEvalSummary, error) {
	return RunEvalWithPredictor(ctx, suitePath, "fc", PredictFCOnly(cm))
}

// PredictFCOnly 返回仅 FC 的预测器（供外部包装自定义模型时使用）。
func PredictFCOnly(cm ParseModel) EvalPredictor {
	return func(ctx context.Context, c IntentEvalCase) *ParsedIntent {
		return Parse(ctx, cm, ParseInput{
			UserMessage:    c.UserMessage,
			SessionHistory: c.Session,
			ExplicitSymbol: c.Symbol,
		})
	}
}

// RunEvalWithPredictor 使用自定义解析策略跑完整评测集（如 combo / pipeline 见 evalintent 包）。
func RunEvalWithPredictor(ctx context.Context, suitePath string, mode string, predict EvalPredictor) (*IntentEvalSummary, error) {
	su, err := LoadIntentSuite(suitePath)
	if err != nil {
		return nil, err
	}
	if len(su.Cases) == 0 {
		return nil, fmt.Errorf("intent eval: suite has no cases")
	}
	if predict == nil {
		return nil, fmt.Errorf("intent eval: nil predictor")
	}
	out := &IntentEvalSummary{
		SuitePath:      suitePath,
		Mode:           strings.TrimSpace(mode),
		EvaluatedAt:    time.Now().UTC().Format(time.RFC3339),
		Total:          len(su.Cases),
		Results:        make([]IntentEvalResult, 0, len(su.Cases)),
		ByExpectedTask: make([]TaskKindBucket, 0),
	}
	var taskHits, symHits, symTotal int
	var hintHits, hintTotal int
	var axisHits, axisTotal int
	var scoreSum float64

	bucketMap := map[string]*TaskKindBucket{}
	for _, c := range su.Cases {
		got := predict(ctx, c)
		res := EvaluateCase(c, got)
		res.ExpectedTaskKind = strings.TrimSpace(c.Expected.TaskKind)
		out.Results = append(out.Results, res)

		if res.OKTask {
			taskHits++
		}
		expKind := strings.TrimSpace(c.Expected.TaskKind)
		if _, ok := bucketMap[expKind]; !ok {
			bucketMap[expKind] = &TaskKindBucket{ExpectedKind: expKind}
		}
		b := bucketMap[expKind]
		b.Count++
		if res.OKTask {
			b.TaskHits++
		}

		expSyms := NormalizeSymbols(c.Expected.Symbols)
		if len(expSyms) > 0 {
			symTotal++
			if res.OKSymbols {
				symHits++
			}
		}
		expHints := normalizeEvalHints(c.Expected.SkillHints)
		if len(expHints) > 0 {
			hintTotal++
			if res.OKHints {
				hintHits++
			}
		}
		if strings.TrimSpace(c.Expected.CompareAxis) != "" {
			axisTotal++
			if res.OKCompareAxis {
				axisHits++
			}
		}
		scoreSum += res.Score
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
	if axisTotal > 0 {
		out.CompareAxisAccuracy = float64(axisHits) / float64(axisTotal)
	}
	out.CompareAxisCases = axisTotal

	var kinds []string
	for k := range bucketMap {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		b := bucketMap[k]
		if b.Count > 0 {
			b.Accuracy = float64(b.TaskHits) / float64(b.Count)
		}
		out.ByExpectedTask = append(out.ByExpectedTask, *b)
	}

	return out, nil
}

// EvaluateCase 对单条用例打分并生成可供日志阅读的 Notes（parse nil 时 Got 为空）。
func EvaluateCase(c IntentEvalCase, got *ParsedIntent) IntentEvalResult {
	res := IntentEvalResult{ID: c.ID, Got: got}
	expKind := TaskKind(strings.TrimSpace(c.Expected.TaskKind))

	if got == nil {
		res.Notes = "解析结果为 nil（模型失败或未产出意图）"
		res.Score = 0
		return res
	}

	res.OKTask = got.TaskKind == expKind

	expSyms := NormalizeSymbols(c.Expected.Symbols)
	if len(expSyms) > 0 {
		gotSyms := NormalizeSymbols(got.Symbols)
		res.OKSymbols = stringSliceEqualSet(gotSyms, expSyms)
	} else {
		res.OKSymbols = true
	}

	expHints := normalizeEvalHints(c.Expected.SkillHints)
	if len(expHints) > 0 {
		gotHints := normalizeEvalHints(got.SkillHints)
		res.OKHints = skillHintsContainAll(gotHints, expHints)
	} else {
		res.OKHints = true
	}

	expAxis := strings.TrimSpace(c.Expected.CompareAxis)
	if expAxis != "" {
		gotAxis := strings.TrimSpace(got.CompareAxis)
		res.OKCompareAxis = strings.EqualFold(expAxis, gotAxis)
	} else {
		res.OKCompareAxis = true
	}

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
	if expAxis != "" {
		parts++
		if res.OKCompareAxis {
			ok++
		}
	}
	if parts > 0 {
		res.Score = ok / parts
	}

	res.Notes = formatEvalNotes(c, got, &res)
	return res
}

func formatEvalNotes(c IntentEvalCase, got *ParsedIntent, res *IntentEvalResult) string {
	if got == nil {
		return res.Notes
	}
	var parts []string
	if !res.OKTask {
		parts = append(parts, fmt.Sprintf("task_kind 期望=%s 实际=%s", strings.TrimSpace(c.Expected.TaskKind), got.TaskKind))
	}
	expSyms := NormalizeSymbols(c.Expected.Symbols)
	if len(expSyms) > 0 && !res.OKSymbols {
		parts = append(parts, fmt.Sprintf("symbols 期望=%v 实际=%v", expSyms, NormalizeSymbols(got.Symbols)))
	}
	expHints := normalizeEvalHints(c.Expected.SkillHints)
	if len(expHints) > 0 && !res.OKHints {
		parts = append(parts, fmt.Sprintf("skill_hints 期望包含=%v 实际=%v", expHints, normalizeEvalHints(got.SkillHints)))
	}
	if strings.TrimSpace(c.Expected.CompareAxis) != "" && !res.OKCompareAxis {
		parts = append(parts, fmt.Sprintf("compare_axis 期望=%s 实际=%s", strings.TrimSpace(c.Expected.CompareAxis), strings.TrimSpace(got.CompareAxis)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func normalizeEvalHints(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, "_", "-")
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
