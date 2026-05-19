// Package queryaug：为意图 FC 提供可选的 KBContext 补充。
//
// 词典槽位与自然语言改写已由 main 中 combo.NLQueryRewrite + MergeRewrittenAndOriginal 写入 FC 的 UserMessage，
// 此处不再重复拼接 FormatComboHints（避免与改写句同质、费 token）。
//
// FC 的 KBContext 仅保留「语义补充」二选一：knowledgeRAGEnabled 时走 Redis 向量片段；否则可在配置中开启 fewShotForIntentParse。
package queryaug

import (
	"context"
	"strings"
	"time"

	"stock-see/intent"
	"stock-see/intent/combo"

	//"stock-see/intent/fewshot"
	"stock-see/kb"
)

// Result 单次查询增强产物。
type Result struct {
	Block        string
	Hits         []kb.Hit
	Slots        combo.RawSlots
	ParsedCombo  *intent.ParsedIntent
	SlotMatchMs  int64 // MatchSlots 耗时（毫秒）
	ComboRulesMs int64 // MatchSlots + ApplyComboRules（含 policy_rules）耗时（毫秒）
	RetrieveMs   int64 // 向量检索（启用 IntentKnowledgeRAG 时）
	RerankMs     int64 // 重排（若检索链路拆分）
}

// Build：槽位 + 规则 → ParsedCombo；
// 支持3种模式：槽位 + 规则引擎、FewShot、KBContext，默认使用槽位 + 规则引擎
// 槽位（词典倒排）先抽结构化信号，规则库再做确定性覆盖
func Build(ctx context.Context, userMessage, sessionHistory, explicitSymbol string) Result {
	um := strings.TrimSpace(userMessage)
	out := Result{}
	if um == "" {
		return out
	}

	t0 := time.Now()
	out.Slots = combo.MatchSlots(um)
	if explicitSymbol != "" {
		out.Slots.SymbolCode = explicitSymbol
	}
	out.SlotMatchMs = time.Since(t0).Milliseconds()

	t1 := time.Now()
	p := combo.ApplyComboRules(out.Slots, um)
	out.ComboRulesMs = time.Since(t1).Milliseconds()

	if p != nil {
		intent.MergeExplicitSymbol(p, explicitSymbol)
	}
	out.ParsedCombo = p

	// var block string
	// if tools.IntentKnowledgeRAGEnabled() {
	//简单意图使用向量检索补充，只针对四要素
	// 	var qb strings.Builder
	// 	qb.WriteString(um)
	// 	if h := strings.TrimSpace(sessionHistory); h != "" {
	// 		if len(h) > 1500 {
	// 			h = h[:1500] + "…"
	// 		}
	// 		qb.WriteString("\n")
	// 		qb.WriteString(h)
	// 	}
	// 	hits, err := kb.DefaultKnowledgeIndex().Search(ctx, qb.String(), 6)
	// 	if err != nil {
	// 		log.Printf("[queryaug] kb-rag: %v", err)
	// 	} else {
	// 		out.Hits = hits
	// 		if rag := kb.FormatRAGBlock(hits); rag != "" {
	// 			block = "## 向量检索参考（knowledge.json）\n\n" + rag
	// 		}
	// 	}
	// } else if tools.IntentFewShotForIntentParse() {
	//复杂意图使用 Few-shot 补充，需要增加意图复杂度检查，目前没有实现，暂不开启
	// 	fsPath := tools.IntentFewShotExamplesPath()
	// 	if fs := fewshot.FormatTopK(ctx, um, fsPath, 2); fs != "" {
	// 		block = fs
	// 	}
	// }

	// out.Block = strings.TrimSpace(block)
	return out
}
