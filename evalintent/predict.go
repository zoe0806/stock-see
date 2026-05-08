// Package evalintent 提供意图离线评测用的解析策略（避免 intent 包依赖 queryaug 产生循环引用）。
package evalintent

import (
	"context"

	"stock-see/intent"
	"stock-see/intent/combo"
	"stock-see/intent/queryaug"
)

// PredictFC 仅调用模型 Function Calling，与早期 -eval-intent 行为一致。
func PredictFC(cm intent.ParseModel) intent.EvalPredictor {
	return func(ctx context.Context, c intent.IntentEvalCase) *intent.ParsedIntent {
		return intent.Parse(ctx, cm, intent.ParseInput{
			UserMessage:    c.UserMessage,
			SessionHistory: c.Session,
			ExplicitSymbol: c.Symbol,
		})
	}
}

// PredictCombo 仅用词典槽位 + 组合规则（及可选 easyrules），不调用模型。
func PredictCombo() intent.EvalPredictor {
	return func(ctx context.Context, c intent.IntentEvalCase) *intent.ParsedIntent {
		aug := queryaug.Build(ctx, c.UserMessage, c.Session, c.Symbol)
		return aug.ParsedCombo
	}
}

// PredictPipeline 与 main 聊天接口一致：高置信组合槽位则跳过 FC，否则 FC（改写后的 UserMessage + KBContext）。
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
