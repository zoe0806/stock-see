// Package tools 内共享的 Python 服务 HTTP 客户端。
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// PythonBaseURL 从环境变量 STOCK_PYTHON_URL 读取，默认 http://localhost:8000；为空则使用 mock。
func PythonBaseURL() string {
	s := strings.TrimSuffix(os.Getenv("STOCK_PYTHON_URL"), "/")
	if s != "" {
		return s
	}
	return "http://localhost:8001"
}

// GetJSON 对 baseURL+path 发起 GET，将响应解析为 JSON 并返回 JSON 字符串（供工具返回）。
func GetJSON(ctx context.Context, baseURL, path string) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("Python base URL not set")
	}
	url := baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return marshalError(fmt.Sprintf("Python API %s: %s", resp.Status, string(body)))
	}
	return string(body), nil
}

// PostJSON 对 baseURL+path 发起 POST，body 为 JSON；返回响应 body 字符串。
func PostJSON(ctx context.Context, baseURL, path string, body any) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("Python base URL not set")
	}
	url := baseURL + path
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return marshalError(fmt.Sprintf("Python API %s: %s", resp.Status, string(respBody)))
	}
	return string(respBody), nil
}
