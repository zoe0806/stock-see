// Package observ 提供对话链路的耗时与 token 汇总（日志 + 可选 SSE）.
package observ

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// PipelineTiming 各阶段耗时（毫秒）。未执行的步骤为 0。
// 对应链路：槽位匹配 → 组合规则 →（可选）意图 FC → 技能预取 → 上下文组装 → 检索 → 重排 → 主对话生成。
type PipelineTiming struct {
	IntentSlotMs  int64 `json:"intent_slot_ms"`
	IntentRulesMs int64 `json:"intent_rules_ms"`
	IntentFCMs    int64 `json:"intent_fc_ms"`
	PrefetchMs    int64 `json:"prefetch_ms"`
	ContextMs     int64 `json:"context_ms"`
	RetrieveMs    int64 `json:"retrieve_ms"`
	RerankMs      int64 `json:"rerank_ms"`
	GenerateMs    int64 `json:"generate_ms"`
}

// TotalMs 求和（便于单行 total）。
func (p PipelineTiming) TotalMs() int64 {
	return p.IntentSlotMs + p.IntentRulesMs + p.IntentFCMs + p.PrefetchMs +
		p.ContextMs + p.RetrieveMs + p.RerankMs + p.GenerateMs
}

// TokenTotals 合并后的用量（各阶段 prompt/completion 已相加）。
type TokenTotals struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatMetrics 写入 SSE `metrics` 事件或日志的结构体。
type ChatMetrics struct {
	TimingMs PipelineTiming `json:"timing_ms"`
	Tokens   *TokenTotals   `json:"tokens,omitempty"`
	IntentFCSkipped bool    `json:"intent_fc_skipped,omitempty"`
}

// MergeTokenUsage 合并多次模型调用的用量（意图 FC、主对话多轮等）；nil 视为 0。
func MergeTokenUsage(dst *schema.TokenUsage, src *schema.TokenUsage) *schema.TokenUsage {
	if src == nil {
		return dst
	}
	if dst == nil {
		out := *src
		return &out
	}
	return &schema.TokenUsage{
		PromptTokens:     dst.PromptTokens + src.PromptTokens,
		CompletionTokens: dst.CompletionTokens + src.CompletionTokens,
		TotalTokens:      dst.TotalTokens + src.TotalTokens,
	}
}

// UsageToTotals 转为日志友好结构。
func UsageToTotals(u *schema.TokenUsage) *TokenTotals {
	if u == nil {
		return nil
	}
	return &TokenTotals{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// AppendMessageUsage 从单条模型消息携带的 ResponseMeta 合并用量（流式/多轮）。
func AppendMessageUsage(acc *schema.TokenUsage, msg *schema.Message) *schema.TokenUsage {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return acc
	}
	return MergeTokenUsage(acc, msg.ResponseMeta.Usage)
}

// LogPipeline 打印一行结构化链路日志；可通过环境变量 STOCK_PIPELINE_LOG=0 关闭。
func LogPipeline(prefix string, skipFC bool, pt PipelineTiming, usage *schema.TokenUsage) {
	if strings.TrimSpace(os.Getenv("STOCK_PIPELINE_LOG")) == "0" {
		return
	}
	if prefix == "" {
		prefix = "[pipeline]"
	}
	tok := UsageToTotals(usage)
	log.Printf("%s skip_fc=%v slot=%dms rules=%dms intent_fc=%dms prefetch=%dms ctx=%dms retrieve=%dms rerank=%dms gen=%dms total=%dms tokens=%+v",
		prefix, skipFC,
		pt.IntentSlotMs, pt.IntentRulesMs, pt.IntentFCMs, pt.PrefetchMs, pt.ContextMs,
		pt.RetrieveMs, pt.RerankMs, pt.GenerateMs, pt.TotalMs(), tok)
}

// MetricsJSON 供 SSE data 字段使用。
func MetricsJSON(skipFC bool, pt PipelineTiming, usage *schema.TokenUsage) string {
	m := ChatMetrics{
		TimingMs:        pt,
		Tokens:          UsageToTotals(usage),
		IntentFCSkipped: skipFC,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
