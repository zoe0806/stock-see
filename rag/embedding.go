// Package rag 的 embedding 能力：兼容 OpenAI /embeddings 与 Ollama /api/embeddings(/api/embed)。
package rag

import (
	"stock-see/tools"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Embed 将文本转为向量。
//   - OpenAI 兼容：POST /embeddings（input）
//     "embeddingUrl": "https://ark.cn-beijing.volces.com/api/v3/embeddings/multimodal",
//     "embeddingApiKey": "d42f4921-dc33-4e2b-9748-77c596514655",
//     "embeddingModel": "doubao-embedding-vision-250615",
//   - Ollama：POST /api/embeddings（prompt）或 /api/embed（input）
//
// text 为空时返回 nil, nil。
func Embed(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	base := tools.GetembeddingBaseURL()
	key := tools.GetembeddingAPIKey()
	if base == "" {
		return nil, fmt.Errorf("embedding base URL 未配置")
	}
	model := tools.GetembeddingModel()

	lowerURL := strings.ToLower(base)
	isOllamaEmbeddings := strings.Contains(lowerURL, "/api/embeddings")
	isOllamaEmbed := strings.Contains(lowerURL, "/api/embed")
	isOllama := isOllamaEmbeddings || isOllamaEmbed || strings.Contains(lowerURL, "ollama") || strings.Contains(lowerURL, "localhost:11434") || strings.Contains(lowerURL, "127.0.0.1:11434")

	var body map[string]any
	switch {
	case isOllamaEmbeddings:
		// Ollama /api/embeddings: {model, prompt}
		body = map[string]any{"model": model, "prompt": text}
	case isOllamaEmbed:
		// Ollama /api/embed: {model, input}
		body = map[string]any{"model": model, "input": text}
	default:
		// OpenAI 兼容：火山引擎 /embeddings/multimodal 要求 input 为 [{"type":"text","text":"..."}]
		volcMultimodal := strings.Contains(lowerURL, "multimodal")
		if volcMultimodal {
			body = map[string]any{
				"model": model,
				"input": []map[string]string{{"type": "text", "text": text}},
			}
		} else {
			body = map[string]any{
				"model": model,
				"input": text,
			}
		}
	}

	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// OpenAI 兼容一般需要 key；Ollama 本地默认不需要（若配置了 key 也允许带上）
	if strings.TrimSpace(key) != "" && !isOllama {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings API %s: %s", resp.Status, string(b))
	}

	// 兼容多种返回格式：
	// - OpenAI: {"data":[{"embedding":[...]}]}
	// - 部分实现: {"embeddings":[[...]]}
	// - Ollama /api/embeddings: {"embedding":[...]}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Embedding  []float32   `json:"embedding"`
		Data       []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) > 0 {
		return out.Embeddings[0], nil
	}
	if len(out.Embedding) > 0 {
		return out.Embedding, nil
	}
	if len(out.Data) > 0 && len(out.Data[0].Embedding) > 0 {
		return out.Data[0].Embedding, nil
	}
	return nil, fmt.Errorf("embeddings API 返回空")
}
