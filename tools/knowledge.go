// Package kb 提供基于 data/knowledge.json 的轻量 RAG：将 JSON 展开为文档、嵌入向量后与用户问句相似度检索，供主对话与意图 FC 前置参考。
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KnowledgeFile 与 data/knowledge.json 结构一致。
type KnowledgeFile struct {
	Stocks      []StockRow          `json:"stocks"`
	Intents     map[string][]string `json:"intents"`
	Metrics     []MetricRow         `json:"metrics"`
	TimePhrases []TimePhraseRow     `json:"time_phrases"`
	// PolicyRules 槽位组合后的策略覆盖（原 intent_rules.json，见 intent/combo/policy.go）。
	PolicyRules []PolicyRuleRow `json:"policy_rules"`
}

// PolicyRuleRow 单条策略：在用户原句上匹配后对 ParsedIntent 打补丁。
type PolicyRuleRow struct {
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

type StockRow struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Industry string   `json:"industry"`
	Concepts []string `json:"concepts"`
}

type MetricRow struct {
	Synonym string `json:"synonym"`
	Field   string `json:"field"`
}

type TimePhraseRow struct {
	Phrase string `json:"phrase"`
	Range  string `json:"range"`
	Parsed string `json:"parsed"`
}

// LoadKnowledgeFile 读取 JSON；path 为空时用 DefaultKnowledgePath()。
func LoadKnowledgeFile(path string) (*KnowledgeFile, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		p = DefaultKnowledgePath()
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var k KnowledgeFile
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// DefaultKnowledgePath 优先环境变量 STOCK_KB_PATH，否则为工作目录下 data/knowledge.json。
func DefaultKnowledgePath() string {
	if e := strings.TrimSpace(os.Getenv("STOCK_KB_PATH")); e != "" {
		return e
	}
	return filepath.Join("data", "knowledge.json")
}

// KnowledgeFileSHA256Hex 计算知识库原始文件的 SHA256（十六进制），用于判断 Redis 中索引是否与磁盘一致。
func KnowledgeFileSHA256Hex(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		p = DefaultKnowledgePath()
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

var (
	stockNameIndexOnce sync.Once
	stockNameToCode    map[string]string
)

func buildStockNameIndex() {
	stockNameToCode = make(map[string]string)
	kf, err := LoadKnowledgeFile("")
	if err != nil {
		return
	}
	for _, s := range kf.Stocks {
		code := strings.TrimSpace(s.Code)
		if len(code) != 6 {
			continue
		}
		add := func(phrase string) {
			phrase = strings.TrimSpace(phrase)
			if phrase == "" {
				return
			}
			if _, ok := stockNameToCode[phrase]; !ok {
				stockNameToCode[phrase] = code
			}
		}
		add(s.Name)
		for _, a := range s.Aliases {
			add(a)
		}
	}
}

// LookupStockCodeByName 在 knowledge.json 中按名称或别名查六位代码；未命中返回空。
func LookupStockCodeByName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	stockNameIndexOnce.Do(buildStockNameIndex)
	if stockNameToCode == nil {
		return ""
	}
	return stockNameToCode[name]
}

// LookupStockCodesFromNames 将中文简称列表解析为代码（去重）。
func LookupStockCodesFromNames(names []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, n := range names {
		if c := LookupStockCodeByName(n); c != "" {
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}
