package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s 不是合法 JSON: %v\n%s", path, err, data)
	}
	return out
}

func TestWriteMCPConfigWritesReferenceNotSecret(t *testing.T) {
	dir := t.TempDir()
	if err := writeMCPConfig(dir, "http://127.0.0.1:12345/mcp"); err != nil {
		t.Fatalf("写入: %v", err)
	}
	root := readJSON(t, filepath.Join(dir, "mcp.json"))
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers 缺失: %v", root)
	}
	entry, ok := servers["harness"].(map[string]any)
	if !ok {
		t.Fatalf("没有 harness 条目: %v", servers)
	}
	if entry["url"] != "http://127.0.0.1:12345/mcp" {
		t.Fatalf("url 不对: %v", entry["url"])
	}
	// 默认 exposure 是 codemode，模型看不到工具；必须是 direct。
	if entry["exposure"] != "direct" {
		t.Fatalf("exposure 必须是 direct，实际 %v", entry["exposure"])
	}
	raw, _ := json.Marshal(entry)
	if !strings.Contains(string(raw), "${MYAGENT_HARNESS_TOKEN}") {
		t.Fatalf("必须写环境变量引用，实际 %s", raw)
	}
	// 令牌本体不该出现在文件里：这里没有任何 hex 长串可断言，改为断言
	// "Authorization 的值里只有一个占位符"。
	headers, _ := entry["headers"].(map[string]any)
	auth, _ := headers["Authorization"].(string)
	if auth != "Bearer ${MYAGENT_HARNESS_TOKEN}" {
		t.Fatalf("Authorization 写法不对: %q", auth)
	}
}

func TestWriteMCPConfigMergesExistingServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	seed := `{"mcpServers":{"user-own":{"command":"npx","args":["-y","something"]}}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("预置文件: %v", err)
	}
	if err := writeMCPConfig(dir, "http://127.0.0.1:1/mcp"); err != nil {
		t.Fatalf("写入: %v", err)
	}
	servers := readJSON(t, path)["mcpServers"].(map[string]any)
	if _, ok := servers["user-own"]; !ok {
		t.Fatalf("已有条目被抹掉了: %v", servers)
	}
	if _, ok := servers["harness"]; !ok {
		t.Fatalf("harness 条目没写进去: %v", servers)
	}
}

func TestWriteMCPConfigRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	// 半截 JSON：真实场景是用户编辑到一半断电或被别的进程写坏。
	broken := `{"mcpServers":{"user-own":`
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("预置坏文件: %v", err)
	}
	err := writeMCPConfig(dir, "http://127.0.0.1:1/mcp")
	if err == nil {
		t.Fatal("已有文件不是合法 JSON 时必须报错，不能静默覆盖")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("读回: %v", readErr)
	}
	if string(data) != broken {
		t.Fatalf("坏文件被改动了:\n%s", data)
	}
}

func TestWriteMCPConfigRejectsBadShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":[]}`), 0o644); err != nil {
		t.Fatalf("预置: %v", err)
	}
	if err := writeMCPConfig(dir, "http://127.0.0.1:1/mcp"); err == nil {
		t.Fatal("mcpServers 是数组时必须报错，不能悄悄替换成对象")
	}
}

func TestWriteMCPConfigRequiresInputs(t *testing.T) {
	if err := writeMCPConfig("", "http://x/mcp"); err == nil {
		t.Fatal("agentDir 为空必须报错")
	}
	if err := writeMCPConfig(t.TempDir(), "  "); err == nil {
		t.Fatal("url 为空必须报错")
	}
}
