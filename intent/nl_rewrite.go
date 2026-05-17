package intent

import "strings"

// MergeRewrittenAndOriginal 规范自然语言改写句 + 用户原话，供意图 FC 与主模型共用。
func MergeRewrittenAndOriginal(original, nlRewritten string) string {
	o := strings.TrimSpace(original)
	rw := strings.TrimSpace(nlRewritten)
	if rw == "" || rw == o {
		return o
	}
	return rw + "\n" + o
}

// UserMessageWithNLRewrite 主模型 User 消息：优先 FC 的 nl_rewritten（已含多轮意图时不重复拼原话）。
func UserMessageWithNLRewrite(original, nlRewritten string, p *ParsedIntent) string {
	var msg string
	if FCUsedNLRewrite(p) && strings.TrimSpace(nlRewritten) != "" {
		msg = strings.TrimSpace(nlRewritten)
	} else {
		msg = MergeRewrittenAndOriginal(original, nlRewritten)
	}
	if p == nil {
		return msg
	}
	switch p.TaskKind {
	case TaskQuickLook:
		msg += "\n请只回答最新行情/现价要点，勿输出多维综合长报告。"
	case TaskFundamental, TaskTechnical, TaskNewsFocus, TaskSentiment, TaskSector:
		msg += "\n请只回答用户所问的单维度，勿调用多维并行工具、勿输出六维综合长报告。"
	case TaskDeepAnalysis:
		// 深度分析不追加短约束
	default:
		if strings.TrimSpace(nlRewritten) != "" && strings.TrimSpace(nlRewritten) != strings.TrimSpace(original) {
			msg += "\n请围绕规范表述直接作答，勿展开无关多维综合研判长文。"
		}
	}
	return msg
}

// EffectiveNLRewrite 主对话用的规范问句：FC 已产出 nl_rewritten 时优先采用（含多轮合并意图）；否则回退词典改写。
func EffectiveNLRewrite(original, comboRewrite string, parsed *ParsedIntent) string {
	if parsed != nil {
		if rw := strings.TrimSpace(parsed.NLRewritten); rw != "" {
			return rw
		}
	}
	return strings.TrimSpace(comboRewrite)
}

// FCUsedNLRewrite 是否应由外层跳过词典改写（FC 已给出规范句）。
func FCUsedNLRewrite(parsed *ParsedIntent) bool {
	return parsed != nil && strings.TrimSpace(parsed.NLRewritten) != ""
}
