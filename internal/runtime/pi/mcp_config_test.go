package pi

// C08a：app-owned MCP 配置（连接器动作走同一条通道）的写入契约。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s 不是合法 JSON: %v", path, err)
	}
	return out
}

func TestWriteHarnessMCPConfigCarriesReferenceOnly(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHarnessMCPConfig(dir, "http://127.0.0.1:39123/mcp", ""); err != nil {
		t.Fatalf("写入: %v", err)
	}
	root := readJSONFile(t, filepath.Join(dir, "mcp.json"))
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers 缺失: %v", root)
	}
	entry, ok := servers[ReservedHarnessServerName].(map[string]any)
	if !ok {
		t.Fatalf("缺少保留条目 %s: %v", ReservedHarnessServerName, servers)
	}
	headers, ok := entry["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers 缺失: %v", entry)
	}
	if headers["Authorization"] != "Bearer ${"+HarnessTokenEnv+"}" {
		t.Fatalf("Authorization 必须只写引用: %v", headers["Authorization"])
	}
	if entry["exposure"] != "direct" {
		t.Fatalf("exposure 必须是 direct: %v", entry["exposure"])
	}
	raw, _ := json.Marshal(entry)
	if strings.Contains(string(raw), "Bearer ${") == false {
		t.Fatalf("条目里应出现引用占位符: %s", raw)
	}
}

func TestWriteHarnessMCPConfigKeepsExternalServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	seed := `{"mcpServers":{"user-own":{"command":"npx","args":["-y","thing"]}}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("预置: %v", err)
	}
	if err := WriteHarnessMCPConfig(dir, "http://127.0.0.1:1/mcp", ""); err != nil {
		t.Fatalf("写入: %v", err)
	}
	servers := readJSONFile(t, path)["mcpServers"].(map[string]any)
	if _, ok := servers["user-own"]; !ok {
		t.Fatalf("外部条目被抹掉了: %v", servers)
	}
	if _, ok := servers[ReservedHarnessServerName]; !ok {
		t.Fatalf("保留条目没写进去: %v", servers)
	}
}

// TestWriteHarnessMCPConfigRejectsNonLoopback 保证工具端点不会被写到网络地址上。
func TestWriteHarnessMCPConfigRejectsNonLoopback(t *testing.T) {
	for _, bad := range []string{
		"http://10.0.0.5:1234/mcp",
		"https://example.com/mcp",
		"ftp://127.0.0.1/mcp",
		"",
	} {
		if err := WriteHarnessMCPConfig(t.TempDir(), bad, ""); err == nil {
			t.Fatalf("必须拒绝非回环端点: %q", bad)
		}
	}
}

func TestWriteHarnessMCPConfigRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	broken := `{"mcpServers":{"user-own":`
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("预置: %v", err)
	}
	if err := WriteHarnessMCPConfig(dir, "http://127.0.0.1:1/mcp", ""); err == nil {
		t.Fatal("已有文件不是合法 JSON 时必须报错")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if string(data) != broken {
		t.Fatalf("坏文件被改动了:\n%s", data)
	}
}

func TestWriteHarnessMCPConfigRequiresInputs(t *testing.T) {
	if err := WriteHarnessMCPConfig("", "http://127.0.0.1:1/mcp", ""); err == nil {
		t.Fatal("agentDir 为空必须报错")
	}
}
