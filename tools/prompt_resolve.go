package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"stock-see/prompt"
)

// GetResolvedPrompt 返回当前配置选中的系统指令、全量报告格式片段、版本名。
// 未配置 prompt 或 activeVersion 为空时，版本名为 "builtin"，内容同代码默认模板。
func GetResolvedPrompt() (system string, fullReport string, version string, err error) {
	_ = loadStockConfig()
	cfg := stockConfig
	if cfg == nil || cfg.Prompt == nil || strings.TrimSpace(cfg.Prompt.ActiveVersion) == "" {
		return prompt.DefaultSystemInstructionTemplate, prompt.DefaultFullReportOutputFormat, "builtin", nil
	}
	vID := strings.TrimSpace(cfg.Prompt.ActiveVersion)
	def, ok := cfg.Prompt.Versions[vID]
	if !ok {
		return "", "", "", fmt.Errorf("prompt: activeVersion %q 在 prompt.versions 中不存在", vID)
	}
	sys, err := resolveSystemInstruction(def, prompt.DefaultSystemInstructionTemplate)
	if err != nil {
		return "", "", "", fmt.Errorf("prompt version %q system: %w", vID, err)
	}
	full, err := resolveFullReportFormat(def, prompt.DefaultFullReportOutputFormat)
	if err != nil {
		return "", "", "", fmt.Errorf("prompt version %q fullReport: %w", vID, err)
	}
	return sys, full, vID, nil
}

func resolveSystemInstruction(def PromptVersionFields, fallback string) (string, error) {
	if s := strings.TrimSpace(def.SystemInstructionTemplateFile); s != "" {
		return readPromptRel(s)
	}
	if body, ok, err := readFromTemplateDir(def.TemplateDir, "system.md"); err != nil {
		return "", err
	} else if ok {
		return body, nil
	}
	if strings.TrimSpace(def.SystemInstructionTemplate) != "" {
		return def.SystemInstructionTemplate, nil
	}
	return fallback, nil
}

func resolveFullReportFormat(def PromptVersionFields, fallback string) (string, error) {
	if s := strings.TrimSpace(def.FullReportOutputFormatFile); s != "" {
		return readPromptRel(s)
	}
	if body, ok, err := readFromTemplateDir(def.TemplateDir, "full_report.md"); err != nil {
		return "", err
	} else if ok {
		return body, nil
	}
	if strings.TrimSpace(def.FullReportOutputFormat) != "" {
		return def.FullReportOutputFormat, nil
	}
	return fallback, nil
}

// readFromTemplateDir 在 templateDir 下读取 name；文件不存在时 ok=false、无错误。
func readFromTemplateDir(dir, name string) (body string, ok bool, err error) {
	d := strings.TrimSpace(dir)
	if d == "" {
		return "", false, nil
	}
	rel := filepath.Join(d, name)
	b, e := readPromptRelBytes(rel)
	if e != nil {
		if os.IsNotExist(e) {
			return "", false, nil
		}
		return "", false, e
	}
	return string(b), true, nil
}

func readPromptRel(rel string) (string, error) {
	b, err := readPromptRelBytes(rel)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readPromptRelBytes(rel string) ([]byte, error) {
	p := rel
	if !filepath.IsAbs(p) {
		p = filepath.Join(stockConfigSourceDir, rel)
	}
	return os.ReadFile(p)
}

// GetEvalDefaultSuitePath 返回 config 中 eval.defaultSuitePath；未配置则为空。
func GetEvalDefaultSuitePath() string {
	_ = loadStockConfig()
	if stockConfig == nil || stockConfig.Eval == nil {
		return ""
	}
	return strings.TrimSpace(stockConfig.Eval.DefaultSuitePath)
}

// GetEvalDefaultIntentSuitePath 返回 config 中 eval.defaultIntentSuitePath；未配置则为空。
func GetEvalDefaultIntentSuitePath() string {
	_ = loadStockConfig()
	if stockConfig == nil || stockConfig.Eval == nil {
		return ""
	}
	return strings.TrimSpace(stockConfig.Eval.DefaultIntentSuitePath)
}

// GetEvalDefaultRetrievalSuitePath 返回 eval.defaultRetrievalSuitePath；未配置则为空。
func GetEvalDefaultRetrievalSuitePath() string {
	_ = loadStockConfig()
	if stockConfig == nil || stockConfig.Eval == nil {
		return ""
	}
	return strings.TrimSpace(stockConfig.Eval.DefaultRetrievalSuitePath)
}
