// Package tools 从 config/stock.json（或 STOCK_CONFIG 指定路径）读取统一配置：RAG、Embedding、Chat、Prompt 版本等。
package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// StockConfig 统一配置文件根结构。
type StockConfig struct {
	StockPython *StockPython      `json:"stockPython"`
	RAG         *RAGConfig        `json:"rag"`
	ChatOpenAI  *ChatOpenAIConfig `json:"chatOpenAI"`
	Prompt      *PromptConfig     `json:"prompt"`
	Eval        *EvalConfig       `json:"eval"`
}

// StockPythonConfig 对应 config 中 stockPython 段。
type StockPython struct {
	BaseURL string `json:"baseURL"`
}

// PromptConfig 管理 SystemInstruction / FullReportOutputFormat 的多版本与当前启用版本。
type PromptConfig struct {
	ActiveVersion string                         `json:"activeVersion"`
	Versions      map[string]PromptVersionFields `json:"versions"`
}

// PromptVersionFields 某一版本的模板；字段均可选，未提供则回退到代码内置默认模板。
// 解析顺序（系统指令）：systemInstructionTemplateFile → templateDir/system.md → systemInstructionTemplate → 内置。
// 解析顺序（全量报告格式）：fullReportOutputFormatFile → templateDir/full_report.md → fullReportOutputFormat → 内置。
// templateDir、*TemplateFile 路径均相对「当前 stock 配置文件」所在目录（如 config/）。
type PromptVersionFields struct {
	TemplateDir                   string `json:"templateDir"`
	SystemInstructionTemplate     string `json:"systemInstructionTemplate"`
	FullReportOutputFormat        string `json:"fullReportOutputFormat"`
	SystemInstructionTemplateFile string `json:"systemInstructionTemplateFile"`
	FullReportOutputFormatFile    string `json:"fullReportOutputFormatFile"`
	Note                          string `json:"note,omitempty"`
}

// EvalConfig 离线评测默认路径等。
type EvalConfig struct {
	DefaultSuitePath       string `json:"defaultSuitePath"`
	DefaultIntentSuitePath string `json:"defaultIntentSuitePath"`
}

// RAGConfig 对应 config 中 rag 段。
type RAGConfig struct {
	Enabled         bool   `json:"enabled"`
	RedisAddr       string `json:"redisAddr"`
	RedisPassword   string `json:"redisPassword"`
	Webhook         string `json:"webhook"`
	EmbeddingURL    string `json:"embeddingUrl"`
	EmbeddingAPIKey string `json:"embeddingApiKey"`
	EmbeddingModel  string `json:"embeddingModel"`
	EmbeddingDim    int    `json:"embeddingDim"`
}

// ChatOpenAIConfig 对应 config 中 chatOpenAI 段，用于 openai.NewChatModel（兼容 OpenAI/豆包/Volc 等）。
type ChatOpenAIConfig struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model"`
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseURL"`
}

var (
	stockConfig          *StockConfig
	stockConfigOnce      sync.Once
	stockConfigSourceDir string
)

func configPath() string {
	if p := os.Getenv("STOCK_CONFIG"); p != "" {
		return p
	}
	// 优先 config/stock.json，不存在则用示例配置路径
	if _, err := os.Stat("config/stock.json"); err == nil {
		return "config/stock.json"
	}
	return "config/stock.example.json"
}

// GetStockConfig 返回已加载的完整配置（仅读）；未加载或失败时为 nil。
func GetStockConfig() *StockConfig {
	stockConfig = loadStockConfig()
	return stockConfig
}

// GetChatOpenAIConfig 返回 chatOpenAI 配置，供 main 中 openai.NewChatModel 使用。
func GetChatOpenAIConfig() *ChatOpenAIConfig {
	c := loadStockConfig()
	if c == nil || c.ChatOpenAI == nil {
		return nil
	}
	return c.ChatOpenAI
}

// RAGEnabled 为 true 时定时 Sync 会执行；config 中 rag.enabled 为 false 则关闭定时拉取。
func RAGEnabled() bool {
	c := GetRAGConfig()
	if c == nil {
		return true // 无 config 时保持原行为
	}
	return c.Enabled
}

func loadStockConfig() *StockConfig {
	stockConfigOnce.Do(func() {
		path := configPath()
		if path != "" && !filepath.IsAbs(path) {
			path = filepath.Join(".", path)
		}
		ap, err := filepath.Abs(path)
		if err != nil {
			ap = path
		}
		stockConfigSourceDir = filepath.Dir(ap)

		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var root StockConfig
		if err := json.Unmarshal(data, &root); err != nil {
			return
		}
		stockConfig = &root
		if stockConfig.RAG != nil && stockConfig.RAG.EmbeddingDim <= 0 {
			stockConfig.RAG.EmbeddingDim = 1536
		}
	})
	return stockConfig
}

// getRAGConfig 读取并缓存 config 中的 rag 段；若文件不存在或解析失败则返回 nil（后续用环境变量）。
func GetRAGConfig() *RAGConfig {
	c := loadStockConfig()
	if c == nil {
		return nil
	}
	return c.RAG
}

// embedding 配置：优先 config/stock.json 的 rag 段，否则环境变量。
func GetembeddingBaseURL() string {
	if c := GetRAGConfig(); c != nil && c.EmbeddingURL != "" {
		s := strings.TrimSuffix(strings.TrimSpace(c.EmbeddingURL), "/")
		// 已是完整 path（含 embed）则直接用
		lower := strings.ToLower(s)
		if strings.Contains(lower, "embed") {
			return s
		}
		// Ollama：默认使用 /api/embeddings
		if strings.Contains(lower, "ollama") || strings.Contains(lower, "localhost:11434") || strings.Contains(lower, "127.0.0.1:11434") {
			return s + "/api/embeddings"
		}
		// 火山引擎
		if strings.Contains(lower, "volces.com") || strings.Contains(lower, "ark.cn-beijing") {
			return s + "/embeddings/multimodal"
		}
		return s + "/embeddings"
	}
	s := strings.TrimSuffix(os.Getenv("RAG_EMBEDDING_URL"), "/")
	if s != "" {
		lower := strings.ToLower(s)
		if !strings.Contains(lower, "embed") && (strings.Contains(lower, "ollama") || strings.Contains(lower, "localhost:11434") || strings.Contains(lower, "127.0.0.1:11434")) {
			return strings.TrimSuffix(s, "/") + "/api/embeddings"
		}
		return s
	}
	s = strings.TrimSuffix(os.Getenv("STOCK_OPENAI_BASE"), "/")
	if s != "" {
		return s + "/embeddings"
	}
	return "https://api.openai.com/v1/embeddings"
}

func GetembeddingAPIKey() string {
	if c := GetRAGConfig(); c != nil && c.EmbeddingAPIKey != "" {
		return c.EmbeddingAPIKey
	}
	return os.Getenv("RAG_EMBEDDING_API_KEY")
}

func GetembeddingModel() string {
	if c := GetRAGConfig(); c != nil && c.EmbeddingModel != "" {
		return c.EmbeddingModel
	}
	if s := os.Getenv("RAG_EMBEDDING_MODEL"); s != "" {
		return s
	}
	return "text-embedding-3-small"
}

func GetembeddingDim() int {
	if c := GetRAGConfig(); c != nil && c.EmbeddingDim > 0 {
		return c.EmbeddingDim
	}
	s := os.Getenv("RAG_EMBEDDING_DIM")
	if s == "" {
		return 1536
	}
	n, _ := strconv.Atoi(s)
	if n > 0 && n <= 8192 {
		return n
	}
	return 1536
}
