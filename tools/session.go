// Package tools — 会话待续意图（澄清后用户只补股票名时继承上一轮 task_kind / skill_hints）。
package tools

import (
	"strings"
	"sync"
	"time"
)

const (
	maxSessionKeyLen = 64
	ttl              = 24 * time.Hour
)

// PendingIntent 上一轮因缺标的而中断的意图（非 need_clarify 本身）。
type PendingIntent struct {
	TaskKind    string
	SkillHints  []string
	UserSnippet string // 上一轮用户原话片段，如「它的基本面」
}

type sessionEntry struct {
	symbol  string
	pending *PendingIntent
	at      time.Time
}

var (
	sessMu sync.RWMutex
	sessM  = make(map[string]sessionEntry)
)

func getEntry(sessionID string) (sessionEntry, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > maxSessionKeyLen {
		return sessionEntry{}, false
	}
	sessMu.RLock()
	e, ok := sessM[sessionID]
	sessMu.RUnlock()
	if !ok || time.Since(e.at) > ttl {
		return sessionEntry{}, false
	}
	return e, true
}

// GetPendingIntent 读取待续意图。
func GetPendingIntent(sessionID string) *PendingIntent {
	e, ok := getEntry(sessionID)
	if !ok || e.pending == nil {
		return nil
	}
	p := *e.pending
	return &p
}

// SavePendingIntent 在因缺标的而澄清前写入；覆盖该会话的旧 pending。
func SavePendingIntent(sessionID string, taskKind string, skillHints []string, userSnippet string) {
	sessionID = strings.TrimSpace(sessionID)
	taskKind = strings.TrimSpace(taskKind)
	userSnippet = strings.TrimSpace(userSnippet)
	if sessionID == "" || len(sessionID) > maxSessionKeyLen || taskKind == "" || taskKind == "need_clarify" {
		return
	}
	p := &PendingIntent{
		TaskKind:    taskKind,
		SkillHints:  append([]string(nil), skillHints...),
		UserSnippet: userSnippet,
	}
	sessMu.Lock()
	prev := sessM[sessionID]
	prev.pending = p
	prev.at = time.Now()
	sessM[sessionID] = prev
	if len(sessM) > 10000 {
		pruneSessionLocked()
	}
	sessMu.Unlock()
}

// ClearPendingIntent 本轮已成功接续并完成分析意图后清除。
func ClearPendingIntent(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	sessMu.Lock()
	if e, ok := sessM[sessionID]; ok {
		e.pending = nil
		sessM[sessionID] = e
	}
	sessMu.Unlock()
}

func pruneSessionLocked() {
	cutoff := time.Now().Add(-ttl)
	for k, e := range sessM {
		if e.at.Before(cutoff) {
			delete(sessM, k)
		}
	}
}

// Get 返回该会话最近一次成功解析的六位代码；sessionID 为空或过期则返回空。
func Get(sessionID string) string {
	e, ok := getEntry(sessionID)
	if !ok {
		return ""
	}
	return e.symbol
}

// Put 记录本轮会话标的（应为六位数字代码）。
func Put(sessionID, symbol string) {
	sessionID = strings.TrimSpace(sessionID)
	symbol = strings.TrimSpace(symbol)
	if sessionID == "" || len(sessionID) > maxSessionKeyLen || len(symbol) != 6 {
		return
	}
	for _, r := range symbol {
		if r < '0' || r > '9' {
			return
		}
	}
	sessMu.Lock()
	prev := sessM[sessionID]
	prev.symbol = symbol
	prev.at = time.Now()
	sessM[sessionID] = prev
	if len(sessM) > 10000 {
		pruneSessionLocked()
	}
	sessMu.Unlock()
}
