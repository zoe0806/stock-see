package tools

import (
	"encoding/json"
	"log"
	"strings"
)

// BackendDataUnavailable 返回给模型的纯文本提示（非 JSON），避免前端/用户看到 URL、超时等底层信息。
func BackendDataUnavailable(component string) (string, error) {
	s := "（" + component + "数据暂不可用；请勿向用户展示技术报错、超时信息或链接，可结合其他维度与常识作答。）"
	return s, nil
}

// IsJSONErrorPayload 判断是否为 {"error":"..."} 形式的工具错误载荷。
func IsJSONErrorPayload(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return false
	}
	var m struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return false
	}
	return strings.TrimSpace(m.Error) != ""
}

// SanitizeToolTextForUser 若内容为 JSON error 载荷则替换为简短说明并写日志；否则原样返回。
func SanitizeToolTextForUser(body string) string {
	if !IsJSONErrorPayload(body) {
		return body
	}
	log.Printf("[tools] suppressed JSON error payload from model context")
	return "（上游分析接口暂不可用；请勿向用户复述错误原文。）"
}
