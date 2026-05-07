// Package prompt 下的技能加载：按描述匹配、加载 SKILL.md 到上下文。
package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill 表示一个技能的元数据及 SKILL.md 路径。
type Skill struct {
	Name                  string   // 技能名称（SKILL.md 所在目录名）
	Description           string   // SKILL.md 首段摘要（展示用）
	Path                  string   // SKILL.md 的路径
	MatchKeywords         []string // 意图关键词（内置 + intent.json + HTML 注释）
	AlwaysForFullReport   bool     // intent.json：全量模式下额外始终注入
	ExcludeFromFullBundle bool     // intent.json：从全量默认捆绑中排除（如实验技能）
}

// MatchOpts 技能匹配选项。
type MatchOpts struct {
	// FullReport 为 true 时（如 mode=full 且带 symbol），先注入 fullReportSkillOrder 中的核心维度技能。
	FullReport bool
}

// LoadSkillsFromDir 扫描目录，查找所有 skills/*/SKILL.md 或 skills/agent-roles/*/SKILL.md。
// 返回 Skill 列表（Name 为目录名，Description 从 SKILL.md 首段或同目录的 description 文件读取，Path 为 SKILL.md 路径）。
func LoadSkillsFromDir(root string) ([]Skill, error) {
	var skills []Skill
	root = filepath.Clean(root)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), "skill.md") || info.Name() == "SKILL.md" {
			dir := filepath.Dir(path)
			name := filepath.Base(dir)
			desc, _ := readFirstParagraph(path)
			kws, alwaysFR, excl := loadSkillIntent(dir, path, name)
			skills = append(skills, Skill{
				Name:                  name,
				Description:           desc,
				Path:                  path,
				MatchKeywords:         kws,
				AlwaysForFullReport:   alwaysFR,
				ExcludeFromFullBundle: excl,
			})
		}
		return nil
	})
	return skills, err
}

// readFirstParagraph 读取文件第一个段落（到空行或末尾）作为简短描述。
func readFirstParagraph(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(b), "\n")
	var block []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			break
		}
		block = append(block, l)
	}
	return strings.Join(block, " "), nil
}

// LoadSkillContent 读取单个技能的 SKILL.md 全文，用于注入上下文。
func LoadSkillContent(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// LoadSkillsContent 加载多个技能（按路径）的 SKILL.md 内容，用分隔符拼成一段，供 BuildContext 的 Skills 字段使用。
func LoadSkillsContent(paths []string) string {
	var parts []string
	for _, p := range paths {
		s, err := LoadSkillContent(p)
		if err != nil {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, "---\n"+s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// MatchSkills 保留兼容：等价于 MatchSkillsForRequest(skills, userMessageOrTask, MatchOpts{})。
func MatchSkills(skills []Skill, userMessageOrTask string) []string {
	return MatchSkillsForRequest(skills, userMessageOrTask, MatchOpts{})
}

// MatchSkillsForRequest 根据用户文本意图 + 可选全量报告模式，返回要注入的 SKILL.md 路径（去重保序）。
func MatchSkillsForRequest(skills []Skill, userText string, opts MatchOpts) []string {
	userText = strings.TrimSpace(userText)
	lower := strings.ToLower(userText)

	byName := make(map[string]Skill, len(skills))
	for _, s := range skills {
		byName[s.Name] = s
	}

	var out []string
	seen := map[string]struct{}{}
	add := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	if opts.FullReport {
		for _, name := range fullReportSkillOrder {
			s, ok := byName[name]
			if !ok || s.ExcludeFromFullBundle {
				continue
			}
			add(s.Path)
		}
		for _, s := range skills {
			if s.AlwaysForFullReport && !s.ExcludeFromFullBundle {
				add(s.Path)
			}
		}
	}

	for _, s := range skills {
		// 目录名英文较长时再按子串匹配，避免 news/risk 等短词误触
		if len(s.Name) >= 5 && strings.Contains(lower, strings.ToLower(s.Name)) {
			add(s.Path)
			continue
		}
		for _, kw := range s.MatchKeywords {
			if kw != "" && strings.Contains(userText, kw) {
				add(s.Path)
				break
			}
		}
	}

	if strings.Contains(userText, "http://") || strings.Contains(userText, "https://") {
		if s, ok := byName["scrapling"]; ok {
			add(s.Path)
		}
	}

	return out
}
