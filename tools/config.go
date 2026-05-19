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
	Intent      *IntentConfig     `json:"intent"`
	ChatOpenAI  *ChatOpenAIConfig `json:"chatOpenAI"`
	Prompt      *PromptConfig     `json:"prompt"`
	Eval        *EvalConfig       `json:"eval"`
}

// IntentConfig 对话意图侧可选行为（与资讯 RAG 的 rag.enabled 独立）。
type IntentConfig struct {
	// KnowledgeRAGEnabled 为 false 时不使用 Redis 向量检索参与意图。
	// 为 true 时仅在「本地槽位组合不足以跳过 FC」时检索（与倒排索引互斥补充，避免每请求双重命中）；详见 IntentKnowledgeRAGEnabled 注释。
	// JSON 省略该字段时默认开启。
	KnowledgeRAGEnabled *bool `json:"knowledgeRagEnabled"`
	// FewShotExamplesPath Few-shot 结构化示例 JSON；默认 data/intent_fewshot.json。
	FewShotExamplesPath string `json:"fewShotExamplesPath"`
	// FewShotForIntentParse 为 true 时在意图 FC 的 KBContext 中附加 Few-shot（需 embedding）；默认 false。
	// 与 knowledgeRAG 二选一更佳：开向量检索时优先用 RAG，不再叠 Few-shot。
	FewShotForIntentParse *bool `json:"fewShotForIntentParse"`
	// SkipFCWhenConfident 为 true（默认）且本地槽位+规则满足高置信条件时，跳过意图 Function Calling。
	SkipFCWhenConfident *bool `json:"skipFCWhenConfident"`
	// InjectQueryAugToExtra 为 true 时将完整 queryaug 块写入对话 Extra（token 大）；默认 false，仅把改写用于 FC 的 KBContext。
	InjectQueryAugToExtra *bool `json:"injectQueryAugToExtra"`
	// InjectCompactIntentToExtra 为 true（默认）时在 Extra 追加一行意图摘要；与 injectQueryAugToExtra 可同时关以极简上下文。
	InjectCompactIntentToExtra *bool `json:"injectCompactIntentToExtra"`
	// InjectSkillPrefetchToExtra 为 true 时将 RunSkillHintsTools 的 ContextMarkdown 拼入 Extra（token 大）；默认 false。基本面全文走 User 消息末段，不重复塞 Extra。
	InjectSkillPrefetchToExtra *bool `json:"injectSkillPrefetchToExtra"`
	// UseStructuredRewriteInUserMessage 为 true（默认）时，将知识库自然语言改写句写入主模型 User 消息（见 intent.NLQueryRewrite）。
	UseStructuredRewriteInUserMessage *bool `json:"useStructuredRewriteInUserMessage"`
}

// StockPythonConfig 对应 config 中 stockPython 段。
type StockPython struct {
	BaseURL string `json:"baseURL"`
}

// PromptConfig 管理 SystemInstruction 的多版本与当前启用版本。
type PromptConfig struct {
	ActiveVersion string                         `json:"activeVersion"`
	Versions      map[string]PromptVersionFields `json:"versions"`
}

// PromptVersionFields 某一版本的模板；字段均可选，未提供则回退到代码内置默认模板。
// 解析顺序（系统指令）：systemInstructionTemplateFile → templateDir/system.md → systemInstructionTemplate → 内置。
// templateDir、*TemplateFile 路径均相对「当前 stock 配置文件」所在目录（如 config/）。
type PromptVersionFields struct {
	TemplateDir                   string `json:"templateDir"`
	SystemInstructionTemplate     string `json:"systemInstructionTemplate"`
	SystemInstructionTemplateFile string `json:"systemInstructionTemplateFile"`
	Note                          string `json:"note,omitempty"`
}

// EvalConfig 离线评测默认路径等。
type EvalConfig struct {
	DefaultSuitePath          string `json:"defaultSuitePath"`
	DefaultIntentSuitePath    string `json:"defaultIntentSuitePath"`
	DefaultRetrievalSuitePath string `json:"defaultRetrievalSuitePath"`
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

const intentKbRAGEnv = "STOCK_INTENT_KB_RAG"

// IntentKnowledgeRAGEnabled 是否允许在「需要时」使用 Redis 知识库向量检索。
// 实际调用时机由 main 控制：仅当本地倒排+槽位（及规则引擎）不足以 ShouldSkipFC 时才会 Search，避免与同一 knowledge.json 的内存倒排重复推理。
// 环境变量 STOCK_INTENT_KB_RAG：0/false/off 关闭，1/true/on 开启；未设置则读 config intent.knowledgeRagEnabled（默认 true）。
func IntentKnowledgeRAGEnabled() bool {
	if v := strings.TrimSpace(os.Getenv(intentKbRAGEnv)); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			return false
		case "1", "true", "on", "yes":
			return true
		default:
			// 未识别的值回退到配置文件
		}
	}
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.KnowledgeRAGEnabled == nil {
		return true
	}
	return *c.Intent.KnowledgeRAGEnabled
}

// IntentFewShotExamplesPath Few-shot 示例文件路径。
func IntentFewShotExamplesPath() string {
	c := loadStockConfig()
	if c != nil && c.Intent != nil && strings.TrimSpace(c.Intent.FewShotExamplesPath) != "" {
		return strings.TrimSpace(c.Intent.FewShotExamplesPath)
	}
	return filepath.Join("data", "intent_fewshot.json")
}

// IntentFewShotForIntentParse 是否在 FC 的 queryaug 块中启用 Few-shot（默认 false，与 NL 改写分工：词典改写已进 UserMessage）。
func IntentFewShotForIntentParse() bool {
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.FewShotForIntentParse == nil {
		return false
	}
	return *c.Intent.FewShotForIntentParse
}

// IntentSkipFCWhenConfident 高置信时跳过意图 FC（默认 true）。
func IntentSkipFCWhenConfident() bool {
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.SkipFCWhenConfident == nil {
		return true
	}
	return *c.Intent.SkipFCWhenConfident
}

// IntentInjectQueryAugToExtra 是否把完整 queryaug 写入对话 Extra（默认 false）。
func IntentInjectQueryAugToExtra() bool {
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.InjectQueryAugToExtra == nil {
		return false
	}
	return *c.Intent.InjectQueryAugToExtra
}

// IntentInjectCompactIntentToExtra 是否在 Extra 追加单行意图摘要（默认 false，避免与结构化改写重复占 token）。
func IntentInjectCompactIntentToExtra() bool {
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.InjectCompactIntentToExtra == nil {
		return false
	}
	return *c.Intent.InjectCompactIntentToExtra
}

// IntentInjectSkillPrefetchToExtra 是否将工具预取 Markdown 拼入 Extra（默认 false）。
func IntentInjectSkillPrefetchToExtra() bool {
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.InjectSkillPrefetchToExtra == nil {
		return false
	}
	return *c.Intent.InjectSkillPrefetchToExtra
}

// IntentUseStructuredRewriteInUserMessage 是否把结构化问法写入主模型用户消息（默认 true）。
func IntentUseStructuredRewriteInUserMessage() bool {
	c := loadStockConfig()
	if c == nil || c.Intent == nil || c.Intent.UseStructuredRewriteInUserMessage == nil {
		return true
	}
	return *c.Intent.UseStructuredRewriteInUserMessage
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
