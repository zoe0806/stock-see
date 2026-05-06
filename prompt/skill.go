// Package prompt 下的技能加载：按描述匹配、加载 SKILL.md 到上下文。
package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill 表示一个技能的元数据及 SKILL.md 路径。
type Skill struct {
	Name        string // 技能名称，如 ceo, cto, pm
	Description string // 用于匹配用户请求的描述
	Path        string // SKILL.md 的路径（相对或绝对）
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
			skills = append(skills, Skill{
				Name:        name,
				Description: desc,
				Path:        path,
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

// MatchSkills 根据用户消息或任务描述做简单关键词匹配，返回建议加载的技能路径列表。
// 可扩展为调用模型选择，或使用更复杂的匹配逻辑。
func MatchSkills(skills []Skill, userMessageOrTask string) []string {
	lower := strings.ToLower(userMessageOrTask)
	var out []string
	for _, s := range skills {
		if s.Description != "" && strings.Contains(lower, strings.ToLower(s.Name)) {
			out = append(out, s.Path)
			continue
		}
		// 描述中的关键词
		for _, w := range strings.Fields(s.Description) {
			if len(w) > 2 && strings.Contains(lower, strings.ToLower(w)) {
				out = append(out, s.Path)
				break
			}
		}
	}
	return out
}
