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

// UserMessageWithNLRewrite 主模型 User 消息：改写 + 原话 + 轻量作答约束（非 deep 且改写有效时）。
func UserMessageWithNLRewrite(original, nlRewritten string, p *ParsedIntent) string {
	msg := MergeRewrittenAndOriginal(original, nlRewritten)
	if p != nil && p.TaskKind != TaskDeepAnalysis {
		if strings.TrimSpace(nlRewritten) != "" && strings.TrimSpace(nlRewritten) != strings.TrimSpace(original) {
			msg += "\n请围绕规范表述直接作答，勿展开多维综合研判长文。"
		}
	}
	return msg
}
