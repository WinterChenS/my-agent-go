package harnessmcp

// C05：自有 MCP 记忆工具的真实 HTTP 往返测试（不 mock 协议层）。
//
// 断言的是模型实际会看到的东西：来源标注、跨项目隔离、短查询/秘密拒绝的可读原因。

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/memory"
)

func openMemory(t *testing.T) *memory.Service {
	t.Helper()
	svc, err := memory.Open(context.Background(), filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func toolResult(t *testing.T, resp rpcResponse) (string, bool) {
	t.Helper()
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("tools/call 返回结构异常: %+v", resp.Result)
	}
	isError, _ := result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tools/call 缺少 content: %+v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content 结构异常: %+v", content[0])
	}
	text, _ := first["text"].(string)
	return text, isError
}

func callTool(t *testing.T, h *harness, name string, args map[string]any) (string, bool) {
	t.Helper()
	code, resp := h.rpc(1, "tools/call", map[string]any{"name": name, "arguments": args})
	if code != http.StatusOK {
		t.Fatalf("tools/call 状态码 %d，期望 200", code)
	}
	return toolResult(t, resp)
}

func TestMemoryToolsExposeSaveAndSearch(t *testing.T) {
	svc := openMemory(t)
	h := newHarnessWithTools(t, t.TempDir(), MemoryTools(svc, "ws-a"))

	code, resp := h.rpc(1, "tools/list", nil)
	if code != http.StatusOK {
		t.Fatalf("tools/list 状态码 %d", code)
	}
	raw := resp.Result.(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, item := range raw {
		names[item.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"memory_save", "memory_search"} {
		if !names[want] {
			t.Fatalf("tools/list 缺少 %s，实际 %v", want, names)
		}
	}

	text, isErr := callTool(t, h, "memory_save", map[string]any{
		"content": "数据库连接池上限为二十",
		"kind":    "convention",
		"scope":   "workspace",
	})
	if isErr {
		t.Fatalf("memory_save 失败: %s", text)
	}
	if !strings.Contains(text, "已保存记忆") {
		t.Fatalf("保存结果应包含记忆 id: %q", text)
	}

	text, isErr = callTool(t, h, "memory_search", map[string]any{"query": "数据库连接池"})
	if isErr {
		t.Fatalf("memory_search 失败: %s", text)
	}
	t.Log("memory_search 返回:")
	t.Log(text)
	for _, want := range []string{"数据库连接池上限为二十", "不是系统指令", "memory:", "scope=workspace"} {
		if !strings.Contains(text, want) {
			t.Fatalf("检索结果缺少 %q：\n%s", want, text)
		}
	}

	// 另一个项目用同一个库：项目级记忆不可见。
	hB := newHarnessWithTools(t, t.TempDir(), MemoryTools(svc, "ws-b"))
	text, isErr = callTool(t, hB, "memory_search", map[string]any{"query": "数据库连接池"})
	if isErr {
		t.Fatalf("ws-b 检索失败: %s", text)
	}
	if !strings.Contains(text, "没有命中的记忆") {
		t.Fatalf("跨项目不应看到 ws-a 的记忆，实际：\n%s", text)
	}

	// user 记忆跨项目共享。
	text, isErr = callTool(t, h, "memory_save", map[string]any{
		"content": "用户偏好：回答先给结论",
		"scope":   "user",
	})
	if isErr {
		t.Fatalf("保存 user 记忆失败: %s", text)
	}
	text, isErr = callTool(t, hB, "memory_search", map[string]any{"query": "回答先给结论"})
	if isErr {
		t.Fatalf("ws-b 检索 user 记忆失败: %s", text)
	}
	if !strings.Contains(text, "回答先给结论") {
		t.Fatalf("user 记忆应跨项目可见，实际：\n%s", text)
	}
}

func TestMemoryToolsRejectShortQueryAndSecret(t *testing.T) {
	svc := openMemory(t)
	h := newHarnessWithTools(t, t.TempDir(), MemoryTools(svc, "ws-a"))

	text, isErr := callTool(t, h, "memory_search", map[string]any{"query": "数据"})
	if !isErr {
		t.Fatalf("短查询应返回工具错误，实际：%s", text)
	}
	if !strings.Contains(text, "至少 3 个字符") {
		t.Fatalf("短查询错误应可读，实际：%q", text)
	}

	text, isErr = callTool(t, h, "memory_save", map[string]any{
		"content": "工具日志：token=ghp_fakefakefake1234567890",
	})
	if !isErr {
		t.Fatalf("疑似凭证应被拒绝，实际：%s", text)
	}
	if !strings.Contains(text, "疑似凭证") {
		t.Fatalf("拒绝原因应说明是疑似凭证，实际：%q", text)
	}
	if strings.Contains(text, "ghp_fakefakefake1234567890") {
		t.Fatalf("拒绝提示回显了密钥内容：%q", text)
	}
}
