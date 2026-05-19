package combo

import (
	"sort"
	"strings"

	"stock-see/intent"
	"stock-see/tools"
)

type policyRule struct {
	priority         int
	allContains      []string
	anyContains      []string
	setTaskKind      string
	appendSkillHints []string
	removeSkillHints []string
	setCompareAxis   string
	setTimeHint      string
}

func policyRulesFromKB(rows []tools.PolicyRuleRow) []policyRule {
	if len(rows) == 0 {
		return nil
	}
	out := make([]policyRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, policyRule{
			priority:         r.Priority,
			allContains:      r.AllContains,
			anyContains:      r.AnyContains,
			setTaskKind:      strings.TrimSpace(r.SetTaskKind),
			appendSkillHints: r.AppendSkillHints,
			removeSkillHints: r.RemoveSkillHints,
			setCompareAxis:   strings.TrimSpace(r.SetCompareAxis),
			setTimeHint:      strings.TrimSpace(r.SetTimeHint),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].priority > out[j].priority
	})
	return out
}

// applyPolicyRules 在槽位推断之后按 knowledge.json policy_rules 覆盖 task_kind / skill_hints。
// 已双股命中 compare 时不改 task_kind，避免被「现价」等规则盖掉。
func applyPolicyRules(p *intent.ParsedIntent, slots RawSlots, userQuery string) {
	if p == nil {
		return
	}
	x := defaultIndex()
	if x == nil || len(x.policyRules) == 0 {
		return
	}
	lockCompare := len(slots.MatchedStocks) >= 2 ||
		(p.TaskKind == intent.TaskCompare && len(intent.NormalizeSymbols(p.Symbols)) >= 2)

	q := userQuery
	for _, r := range x.policyRules {
		if !policyMatches(&r, q) {
			continue
		}
		if r.setTaskKind != "" && !lockCompare {
			p.TaskKind = intent.TaskKind(r.setTaskKind)
		}
		for _, h := range r.removeSkillHints {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			p.SkillHints = removeSkillHint(p.SkillHints, h)
		}
		for _, h := range r.appendSkillHints {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			p.SkillHints = appendSkillHintUnique(p.SkillHints, h)
		}
		if r.setCompareAxis != "" {
			p.CompareAxis = r.setCompareAxis
		}
		if r.setTimeHint != "" {
			p.TimeHint = r.setTimeHint
		}
	}
	if p.TaskKind == intent.TaskCompare {
		p.SkillHints = compareDefaultSkillHints()
	}
}

func policyMatches(r *policyRule, q string) bool {
	for _, s := range r.allContains {
		if !strings.Contains(q, strings.TrimSpace(s)) {
			return false
		}
	}
	if len(r.anyContains) == 0 {
		return true
	}
	for _, s := range r.anyContains {
		if strings.Contains(q, strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func removeSkillHint(in []string, drop string) []string {
	var out []string
	d := strings.ToLower(strings.TrimSpace(drop))
	for _, x := range in {
		if strings.ToLower(strings.TrimSpace(x)) != d {
			out = append(out, x)
		}
	}
	return out
}

func appendSkillHintUnique(slice []string, s string) []string {
	for _, x := range slice {
		if strings.EqualFold(x, s) {
			return slice
		}
	}
	return append(slice, s)
}
