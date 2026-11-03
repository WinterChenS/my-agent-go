package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestFixtureMCPStdioProtocol 黑盒验证 fixture MCP server 的 stdio 行为。
//
// 为什么黑盒：MCP 的关键约定就在传输层（行分隔、flush 时序、通知无响应），
// 直接调内部函数测不出来。
func TestFixtureMCPStdioProtocol(t *testing.T) {
	bin := buildFixture(t)
	s := startFixture(t, bin)

	// 1) initialize
	s.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0.0"},
		},
	})
	resp := s.read(t)
	result := resp["result"].(map[string]any)
	if got := result["protocolVersion"]; got != "2024-11-05" {
		t.Fatalf("protocolVersion 期望 2024-11-05，实际 %v", got)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities 缺失")
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools 缺失")
	}

	// 2) 通知（无 id）不得产生响应：紧接着发 tools/list，读到的必须直接是它的响应。
	s.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	resp = s.read(t)
	if id, _ := resp["id"].(float64); id != 2 {
		t.Fatalf("通知被当成请求回应了：读到 id=%v 的响应", resp["id"])
	}
	tools, ok := resp["result"].(map[string]any)["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list 未返回工具")
	}
	name := tools[0].(map[string]any)["name"]
	if name != "fixture_echo" {
		t.Fatalf("工具名期望 fixture_echo，实际 %v", name)
	}

	// 3) tools/call 正常回显
	s.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "fixture_echo",
			"arguments": map[string]any{"text": "hello"},
		},
	})
	resp = s.read(t)
	content := resp["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"]
	if text != "echo: hello" {
		t.Fatalf("回显内容不符，实际 %v", text)
	}

	// 4) 未知工具应返回 -32602
	s.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "nope", "arguments": map[string]any{}},
	})
	resp = s.read(t)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("未知工具应返回 error")
	}
	if code, _ := errObj["code"].(float64); code != -32602 {
		t.Fatalf("未知工具错误码期望 -32602，实际 %v", errObj["code"])
	}

	// 5) 未知方法应返回 -32601
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 5, "method": "resources/list"})
	resp = s.read(t)
	errObj, ok = resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("未知方法应返回 error")
	}
	if code, _ := errObj["code"].(float64); code != -32601 {
		t.Fatalf("未知方法错误码期望 -32601，实际 %v", errObj["code"])
	}
}

// TestFixtureMCPRejectsMalformedJSON 畸形输入必须回 -32700 且不断流。
func TestFixtureMCPRejectsMalformedJSON(t *testing.T) {
	bin := buildFixture(t)
	s := startFixture(t, bin)

	s.raw(t, []byte("{not json\n"))
	resp := s.read(t)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("畸形 JSON 应返回 error")
	}
	if code, _ := errObj["code"].(float64); code != -32700 {
		t.Fatalf("解析错误码期望 -32700，实际 %v", errObj["code"])
	}

	// 断流不应影响后续请求。
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/list"})
	resp = s.read(t)
	if id, _ := resp["id"].(float64); id != 9 {
		t.Fatalf("畸形输入后连接不可用，读到 id=%v", resp["id"])
	}
}

func buildFixture(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fixture-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fixture MCP 失败: %v\n%s", err, out)
	}
	return bin
}

type fixtureSession struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startFixture(t *testing.T, bin string) *fixtureSession {
	t.Helper()
	cmd := exec.Command(bin)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("取 stdin 失败: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("取 stdout 失败: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 fixture MCP 失败: %v", err)
	}
	s := &fixtureSession{cmd: cmd, in: in, out: bufio.NewReaderSize(stdout, 1<<16)}
	t.Cleanup(func() {
		_ = s.in.Close()
		_ = cmd.Wait()
	})
	return s
}

func (s *fixtureSession) raw(t *testing.T, b []byte) {
	t.Helper()
	if _, err := s.in.Write(b); err != nil {
		t.Fatalf("写 stdin 失败: %v", err)
	}
}

func (s *fixtureSession) send(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	s.raw(t, append(b, '\n'))
}

// read 读取一行响应，超时即失败：服务端忘记 flush 时会卡在这里。
func (s *fixtureSession) read(t *testing.T) map[string]any {
	t.Helper()
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.out.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("读响应失败: %v", r.err)
		}
		var m map[string]any
		if err := json.Unmarshal(r.line, &m); err != nil {
			t.Fatalf("响应不是合法 JSON: %v\n%s", err, r.line)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("读响应超时（服务端可能未 flush）")
		return nil
	}
}
