// Package eval 提供离线 Prompt / 输出格式评测：加载 JSON 用例集，调用模型生成，再按规则打分并汇总平均分。
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"stock-see/prompt"
	"stock-see/tools"
)

// ChatGenerator 与 openai ChatModel 的 Generate 对齐，便于评测时注入同一模型实例。
type ChatGenerator interface {
	Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)
}

// Options 单次评测运行参数。
type Options struct {
	SuitePath        string
	SystemTemplate   string
	FullReportFormat string
	PromptVersion    string
}

// Suite 评测集文件结构。
type Suite struct {
	Version int    `json:"version"`
	Note    string `json:"note,omitempty"`
	Cases   []Case `json:"cases"`
}

// Case 单条用例。
type Case struct {
	ID                   string `json:"id"`
	Kind                 string `json:"kind"`
	Symbol               string `json:"symbol"`
	UserMessage          string `json:"userMessage"`
	StubParallelMarkdown string `json:"stubParallelMarkdown"`
	StubScoreMarkdown    string `json:"stubScoreMarkdown"`
	Rubric               Rubric `json:"rubric"`
}

// Rubric 可自动化检查的启发式规则（0–100 分制扣分）。
type Rubric struct {
	MinRunes                     int      `json:"minRunes"`
	RequiredSubstrings           []string `json:"requiredSubstrings"`
	ForbiddenSubstrings          []string `json:"forbiddenSubstrings"`
	PenalizeTrailingHashHeadings bool     `json:"penalizeTrailingHashHeadings"`
}

// CaseResult 单条得分与说明。
type CaseResult struct {
	ID            string   `json:"id"`
	Score         float64  `json:"score"`
	Reasons       []string `json:"reasons,omitempty"`
	OutputPreview string   `json:"outputPreview"`
}

// Summary 整场评测汇总。
type Summary struct {
	Average       float64      `json:"average"`
	PromptVersion string       `json:"promptVersion"`
	SuitePath     string       `json:"suitePath"`
	Cases         []CaseResult `json:"cases"`
}

// LoadSuite 读取 JSON 评测集。
func LoadSuite(path string) (*Suite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Suite
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Run 顺序执行每条用例，返回平均分（百分制）。
func Run(ctx context.Context, gen ChatGenerator, opt Options) (*Summary, error) {
	su, err := LoadSuite(opt.SuitePath)
	if err != nil {
		return nil, err
	}
	if len(su.Cases) == 0 {
		return nil, fmt.Errorf("eval: suite has no cases")
	}
	out := &Summary{
		PromptVersion: opt.PromptVersion,
		SuitePath:     opt.SuitePath,
		Cases:         make([]CaseResult, 0, len(su.Cases)),
	}
	var sum float64
	for _, c := range su.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return nil, fmt.Errorf("eval: case with empty id")
		}
		msgs, err := buildMessages(c, opt.SystemTemplate, opt.FullReportFormat)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		msg, err := gen.Generate(ctx, msgs)
		if err != nil {
			return nil, fmt.Errorf("case %s generate: %w", c.ID, err)
		}
		text := ""
		if msg != nil {
			text = msg.Content
		}
		sc, reasons := Score(text, c.Rubric)
		sum += sc
		preview := text
		runes := []rune(preview)
		if len(runes) > 400 {
			preview = string(runes[:400]) + "…"
		}
		out.Cases = append(out.Cases, CaseResult{
			ID:            c.ID,
			Score:         sc,
			Reasons:       reasons,
			OutputPreview: preview,
		})
		log.Printf("[eval] %s score=%.1f %v", c.ID, sc, reasons)
	}
	out.Average = sum / float64(len(su.Cases))
	return out, nil
}

func buildMessages(c Case, sysTpl, fullFmt string) ([]*schema.Message, error) {
	sym := strings.TrimSpace(c.Symbol)
	market := ""
	if sym != "" {
		market = tools.GetMarketDataMock(sym)
	}
	user := strings.TrimSpace(c.UserMessage)
	if user == "" {
		user = "请根据上下文给出分析结论。"
	}
	ctxIn := prompt.ContextInput{MarketContext: market}

	switch strings.TrimSpace(c.Kind) {
	case "", "chat":
		// 仅行情摘要 + 用户问题
	case "full_report_stub":
		par := strings.TrimSpace(c.StubParallelMarkdown)
		if par == "" {
			par = "## 技术面（模拟）\n- 趋势：**偏多**\n- 关键位：模拟数据\n\n## 基本面（模拟）\n- 估值：**中性**"
		}
		scr := strings.TrimSpace(c.StubScoreMarkdown)
		if scr == "" {
			scr = "综合评分: **68分** (持有)\n\n| 维度 | 分数 |\n|------|------|\n| 技术 | 62 |\n| 基本面 | 65 |"
		}
		ctxIn.Extra = prompt.BuildFullReportExtra(par, scr, fullFmt)
	default:
		return nil, fmt.Errorf("unknown kind %q", c.Kind)
	}

	ctxBlock := prompt.BuildContext(ctxIn)
	finalSys := strings.Replace(sysTpl, "{Context}", ctxBlock, 1)
	return []*schema.Message{
		schema.SystemMessage(finalSys),
		schema.UserMessage(user),
	}, nil
}

// Score 根据 Rubric 对模型输出打分（满分 100，按项扣分，不低于 0）。
func Score(output string, r Rubric) (float64, []string) {
	var reasons []string
	score := 100.0
	rn := len([]rune(output))
	if r.MinRunes > 0 && rn < r.MinRunes {
		score -= 20
		reasons = append(reasons, fmt.Sprintf("长度不足: %d < %d", rn, r.MinRunes))
	}
	for _, s := range r.ForbiddenSubstrings {
		if s == "" {
			continue
		}
		if strings.Contains(output, s) {
			score -= 15
			reasons = append(reasons, "包含禁用短语: "+s)
		}
	}
	for _, s := range r.RequiredSubstrings {
		if s == "" {
			continue
		}
		if !strings.Contains(output, s) {
			score -= 10
			reasons = append(reasons, "缺少必需片段: "+s)
		}
	}
	if r.PenalizeTrailingHashHeadings {
		for _, line := range strings.Split(output, "\n") {
			t := strings.TrimRight(line, " \t\r")
			if strings.HasSuffix(t, " #") || strings.HasSuffix(t, " ##") {
				score -= 12
				reasons = append(reasons, "存在行尾 # / ##: "+t)
				break
			}
		}
	}
	if score < 0 {
		score = 0
	}
	return score, reasons
}
