// Package easyrules：JSON 声明式规则层，在 combo 槽位结果之上做覆盖（便于热更新业务规则）。
package easyrules

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"stock-see/intent"
)

// Rule 单条规则：匹配成功后执行字段补丁。
type Rule struct {
	ID               string   `json:"id"`
	Priority         int      `json:"priority"`
	AllContains      []string `json:"all_contains"`
	AnyContains      []string `json:"any_contains"`
	SetTaskKind      string   `json:"set_task_kind"`
	AppendSkillHints []string `json:"append_skill_hints"`
	RemoveSkillHints []string `json:"remove_skill_hints"`
	SetCompareAxis   string   `json:"set_compare_axis"`
	SetTimeHint      string   `json:"set_time_hint"`
}

// Doc 规则文件根。
type Doc struct {
	Rules []Rule `json:"rules"`
}

var (
	mu       sync.RWMutex
	pathSeen string
	doc      *Doc
)

// Load 从路径读取规则（绝对路径或相对工作目录）。
func Load(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(".", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	sort.Slice(d.Rules, func(i, j int) bool {
		return d.Rules[i].Priority > d.Rules[j].Priority
	})
	mu.Lock()
	doc = &d
	pathSeen = path
	mu.Unlock()
	log.Printf("[easyrules] 已加载 %d 条规则 %s", len(d.Rules), path)
	return nil
}

// ApplyOver 在 combo 结果上执行规则补丁并再次校验。
func ApplyOver(base *intent.ParsedIntent, userQuery string) *intent.ParsedIntent {
	if base == nil {
		return nil
	}
	mu.RLock()
	d := doc
	mu.RUnlock()
	if d == nil || len(d.Rules) == 0 {
		intent.ValidateAndPatch(base, userQuery)
		return base
	}
	q := userQuery
	out := *base
	for _, r := range d.Rules {
		if !matches(&r, q) {
			continue
		}
		if r.SetTaskKind != "" {
			out.TaskKind = intent.TaskKind(strings.TrimSpace(r.SetTaskKind))
		}
		for _, h := range r.RemoveSkillHints {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			out.SkillHints = removeHint(out.SkillHints, h)
		}
		for _, h := range r.AppendSkillHints {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			out.SkillHints = appendUnique(out.SkillHints, h)
		}
		if r.SetCompareAxis != "" {
			out.CompareAxis = strings.TrimSpace(r.SetCompareAxis)
		}
		if r.SetTimeHint != "" {
			out.TimeHint = strings.TrimSpace(r.SetTimeHint)
		}
	}
	intent.ValidateAndPatch(&out, userQuery)
	return &out
}

func matches(r *Rule, q string) bool {
	for _, s := range r.AllContains {
		if !strings.Contains(q, strings.TrimSpace(s)) {
			return false
		}
	}
	if len(r.AnyContains) == 0 {
		return true
	}
	for _, s := range r.AnyContains {
		if strings.Contains(q, strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func removeHint(in []string, drop string) []string {
	var out []string
	d := strings.ToLower(strings.TrimSpace(drop))
	for _, x := range in {
		if strings.ToLower(strings.TrimSpace(x)) != d {
			out = append(out, x)
		}
	}
	return out
}

func appendUnique(slice []string, s string) []string {
	for _, x := range slice {
		if strings.EqualFold(x, s) {
			return slice
		}
	}
	return append(slice, s)
}
