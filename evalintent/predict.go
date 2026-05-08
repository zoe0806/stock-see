// Package evalintent 提供意图离线评测用的解析策略（避免 intent 包直接依赖 queryaug 形成循环引用）。
//
// 「Predict」在此处的含义是：对评测集中的一条用户输入，预测/产出结构化意图 ParsedIntent。
// main 里通过 -eval-intent-mode 选用 FC-only、纯词典 combo、或与线上一致的 pipeline，
// 再交给 intent.RunEvalWithPredictor 统一打分。
package evalintent

import (
	"context"

	"stock-see/intent"
	"stock-see/intent/combo"
	"stock-see/intent/queryaug"
)

// PredictFC 仅调用模型 Function Calling（与最早期的 -eval-intent 行为一致）。
func PredictFC(cm intent.ParseModel) intent.EvalPredictor {
	return func(ctx context.Context, c intent.IntentEvalCase) *intent.ParsedIntent {
		return intent.Parse(ctx, cm, intent.ParseInput{
			UserMessage:    c.UserMessage,
			SessionHistory: c.Session,
			ExplicitSymbol: c.Symbol,
		})
	}
}

// PredictCombo 仅用词典槽位 + ApplyComboRules（及可选 easyrules），不调用模型。
func PredictCombo() intent.EvalPredictor {
	return func(ctx context.Context, c intent.IntentEvalCase) *intent.ParsedIntent {
		aug := queryaug.Build(ctx, c.UserMessage, c.Session, c.Symbol)
		return aug.ParsedCombo
	}
}

// PredictPipeline 与聊天接口一致：高置信组合槽位则跳过 FC，否则走 FC（改写后的 UserMessage + KBContext）。
func PredictPipeline(cm intent.ParseModel) intent.EvalPredictor {
	return func(ctx context.Context, c intent.IntentEvalCase) *intent.ParsedIntent {
		aug := queryaug.Build(ctx, c.UserMessage, c.Session, c.Symbol)
		if aug.ParsedCombo == nil {
			return nil
		}
		nlRW := combo.NLQueryRewrite(c.UserMessage, aug.Slots)
		skipFC := combo.ShouldSkipFC(aug.ParsedCombo, aug.Slots)
		if skipFC {
			return aug.ParsedCombo
		}
		umForIntent := intent.MergeRewrittenAndOriginal(c.UserMessage, nlRW)
		return intent.Parse(ctx, cm, intent.ParseInput{
			UserMessage:    umForIntent,
			SessionHistory: c.Session,
			ExplicitSymbol: c.Symbol,
			KBContext:      aug.Block,
		})
	}
}
