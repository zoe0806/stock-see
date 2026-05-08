// Package fewshot：从示例库向量检索 Top-K 相似问句，将结构化 JSON 示例注入 FC / 上下文。
package fewshot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"stock-see/rag"
	"stock-see/tools"
)

// Example 单条 Few-shot。
type Example struct {
	Query      string          `json:"query"`
	Structured json.RawMessage `json:"structured"`
	embedding  []float32     `json:"-"`
}

// File 示例库文件格式。
type File struct {
	Examples []Example `json:"examples"`
}

var (
	loadMu     sync.Mutex
	cachedPath string
	cachedFile *File
)

func loadFile(path string) (*File, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if cachedFile != nil && cachedPath == path {
		return cachedFile, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	cachedPath = path
	cachedFile = &f
	return &f, nil
}

// ReloadCache 热更新示例文件后调用，便于不换进程加载新 Few-shot。
func ReloadCache() {
	loadMu.Lock()
	defer loadMu.Unlock()
	cachedFile = nil
	cachedPath = ""
}

// WarmEmbeddings 预计算示例向量。
func WarmEmbeddings(ctx context.Context, path string) error {
	f, err := loadFile(path)
	if err != nil {
		return err
	}
	dim := tools.GetembeddingDim()
	for i := range f.Examples {
		if len(f.Examples[i].embedding) == dim {
			continue
		}
		q := strings.TrimSpace(f.Examples[i].Query)
		if q == "" {
			continue
		}
		vec, err := rag.Embed(ctx, q)
		if err != nil {
			return err
		}
		if len(vec) != dim {
			return fmt.Errorf("few-shot embed dim %d != config %d", len(vec), dim)
		}
		f.Examples[i].embedding = vec
	}
	return nil
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type scored struct {
	idx int
	sim float64
}

// FormatTopK 返回注入 Prompt 的文本块；embedding 失败返回空字符串。
func FormatTopK(ctx context.Context, userQuery string, path string, topK int) string {
	if topK <= 0 {
		topK = 2
	}
	userQuery = strings.TrimSpace(userQuery)
	if userQuery == "" {
		return ""
	}
	f, err := loadFile(path)
	if err != nil || len(f.Examples) == 0 {
		return ""
	}
	if err := WarmEmbeddings(ctx, path); err != nil {
		return ""
	}
	qVec, err := rag.Embed(ctx, userQuery)
	if err != nil || len(qVec) != tools.GetembeddingDim() {
		return ""
	}

	var ss []scored
	for i := range f.Examples {
		if len(f.Examples[i].embedding) != len(qVec) {
			continue
		}
		ss = append(ss, scored{idx: i, sim: cosine(qVec, f.Examples[i].embedding)})
	}
	if len(ss) == 0 {
		return ""
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].sim > ss[j].sim })
	if len(ss) > topK {
		ss = ss[:topK]
	}

	var b strings.Builder
	b.WriteString("以下为与用户问句向量最相似的 ")
	fmt.Fprintf(&b, "%d", len(ss))
	b.WriteString(" 条结构化解析示例（仅作格式参考，须结合当前输入校验）：\n\n")
	for _, x := range ss {
		ex := f.Examples[x.idx]
		fmt.Fprintf(&b, "- 相似度 %.4f\n  示例问句：%s\n  结构化输出 JSON：%s\n\n", x.sim, ex.Query, strings.TrimSpace(string(ex.Structured)))
	}
	return strings.TrimSpace(b.String())
}
