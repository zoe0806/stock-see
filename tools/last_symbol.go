// Package tools 维护多轮对话中的轻量会话态（如上一轮标的），无需前端传完整 Memory。
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

type entry struct {
	symbol string
	at     time.Time
}

var (
	mu sync.RWMutex
	m  = make(map[string]entry)
)

// Get 返回该会话最近一次成功解析的六位代码；sessionID 为空或过期则返回空。
func Get(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > maxSessionKeyLen {
		return ""
	}
	mu.RLock()
	e, ok := m[sessionID]
	mu.RUnlock()
	if !ok || time.Since(e.at) > ttl {
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
	mu.Lock()
	m[sessionID] = entry{symbol: symbol, at: time.Now()}
	if len(m) > 10000 {
		pruneLocked()
	}
	mu.Unlock()
}

func pruneLocked() {
	cutoff := time.Now().Add(-ttl)
	for k, e := range m {
		if e.at.Before(cutoff) {
			delete(m, k)
		}
	}
}
