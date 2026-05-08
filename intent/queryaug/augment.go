// Package queryaug：为意图 FC 提供可选的 KBContext 补充。
//
// 词典槽位与自然语言改写已由 main 中 combo.NLQueryRewrite + MergeRewrittenAndOriginal 写入 FC 的 UserMessage，
// 此处不再重复拼接 FormatComboHints（避免与改写句同质、费 token）。
//
// FC 的 KBContext 仅保留「语义补充」二选一：knowledgeRAGEnabled 时走 Redis 向量片段；否则可在配置中开启 fewShotForIntentParse。
package queryaug

import (
	"context"
	"log"

	//"log"
	"strings"

	"stock-see/intent"
	"stock-see/intent/combo"
	"stock-see/intent/easyrules"

	//"stock-see/intent/fewshot"
	"stock-see/kb"
	"stock-see/tools"
)

// Result 单次查询增强产物。
type Result struct {
	Block       string
	Hits        []kb.Hit
	Slots       combo.RawSlots
	ParsedCombo *intent.ParsedIntent
}

// Build：槽位 + 规则 → ParsedCombo；
// 支持3种模式：槽位 + 规则、FewShot、KBContext，默认使用槽位 + 规则
// 槽位（词典倒排）先抽结构化信号，规则库再做确定性覆盖
func Build(ctx context.Context, userMessage, sessionHistory, explicitSymbol string) Result {
	um := strings.TrimSpace(userMessage)
	out := Result{}
	if um == "" {
		return out
	}

	//倒排，抽出 股票 / 意图词命中 / 指标 field / 时间短语 等原始槽位
	out.Slots = combo.MatchSlots(um)
	var p *intent.ParsedIntent
	if tools.IntentEasyRulesEnabled() {
		p = easyrules.ApplyOver(combo.ApplyComboRules(out.Slots, um), um)
	} else {
		//在槽位上做 组合与冲突消解
		p = combo.ApplyComboRules(out.Slots, um)
	}
	if p != nil {
		intent.MergeExplicitSymbol(p, explicitSymbol)
	}
	out.ParsedCombo = p
	log.Println("out.ParsedCombo", out.ParsedCombo)
	log.Println("out.Slots", out.Slots)

	// var block string
	// if tools.IntentKnowledgeRAGEnabled() {
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
	// 	fsPath := tools.IntentFewShotExamplesPath()
	// 	if fs := fewshot.FormatTopK(ctx, um, fsPath, 2); fs != "" {
	// 		block = fs
	// 	}
	// }

	// out.Block = strings.TrimSpace(block)
	return out
}
