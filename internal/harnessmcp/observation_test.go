package harnessmcp

// CONN04/CONN05/CONN08 的外部观察：把真实 MCP 服务在 TCP 上返回的 tools/list 原样落盘。
//
// 为什么这算外部观察：Pi / 模型真正看到的就是这段 JSON 字节（工具名、描述里的风险与
// 幂等声明、inputSchema）。测试只当客户端，把服务端响应体逐字节写进证据，
// 不在测试里重新构造"工具表应该是什么"。
//
// 契约与其它域一致：目录来自 ANVIL_OBSERVATION_DIR，observation.json 合并写、
// artifact 按测试追加；未设置时什么也不写。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observeDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// toolListRecord 是一次 tools/list 的真实往返。
type toolListRecord struct {
	Test       string   `json:"test"`
	Label      string   `json:"label"`
	Endpoint   string   `json:"endpoint"`
	StatusCode int      `json:"statusCode"`
	Request    string   `json:"request"`
	Response   string   `json:"response"`
	ToolNames  []string `json:"toolNames"`
	ToolCount  int      `json:"toolCount"`
}

// observeToolList 向真实 HTTP 端点发一次 tools/list，把响应体原样写进证据。
//
// 返回值是从**响应 JSON**解析出的工具名（供调用方追加断言），不依赖进程内的工具表。
func observeToolList(t *testing.T, h *harness, label string, extra map[string]any) []string {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      900,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化 tools/list 请求失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, h.url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造 tools/list 请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送 tools/list 请求失败: %v", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 tools/list 响应失败: %v", err)
	}

	names := []string{}
	var parsed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err == nil {
		for _, tool := range parsed.Result.Tools {
			names = append(names, tool.Name)
		}
	}

	if observeDir() != "" {
		record := toolListRecord{
			Test:       t.Name(),
			Label:      label,
			Endpoint:   h.url,
			StatusCode: resp.StatusCode,
			Request:    string(raw),
			Response:   string(respBytes),
			ToolNames:  names,
			ToolCount:  len(names),
		}
		appendToolListArtifact(t, record)

		facts := map[string]any{
			"label":          label,
			"endpoint":       h.url,
			"statusCode":     resp.StatusCode,
			"toolNames":      names,
			"toolCount":      len(names),
			"responseBytes":  len(respBytes),
			"responseSha256": sha256Bytes(respBytes),
		}
		for k, v := range extra {
			facts[k] = v
		}
		facts["byTest:"+t.Name()+"/"+label] = map[string]any{
			"toolNames":  names,
			"toolCount":  len(names),
			"statusCode": resp.StatusCode,
		}
		mergeToolListObservation(t, facts)
	}
	return names
}

// appendToolListArtifact 把一次往返记录追加进 connector-tools-list.json。
func appendToolListArtifact(t *testing.T, record toolListRecord) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	path := filepath.Join(dir, "connector-tools-list.json")
	records := []json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if uerr := json.Unmarshal(raw, &records); uerr != nil {
			t.Fatalf("已有 connector-tools-list.json 不是合法 JSON 数组: %v", uerr)
		}
	}
	one, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化工具表记录失败: %v", err)
	}
	records = append(records, one)
	out, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 connector-tools-list.json 失败: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("写 connector-tools-list.json 失败: %v", err)
	}
}

// mergeToolListObservation 合并写 observation.json。
func mergeToolListObservation(t *testing.T, facts map[string]any) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "observation.json")
	merged := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if uerr := json.Unmarshal(raw, &merged); uerr != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON 对象: %v", uerr)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
	}
	merged["tests"] = tests
	merged["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// sha256Bytes 返回响应体的 sha256（十六进制），用于核对证据文件没被改过。
func sha256Bytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
