package harnessmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type harness struct {
	srv   *Server
	url   string
	token string
	ws    string
	t     *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ws := t.TempDir()
	srv, err := Start(context.Background(), Options{
		Tools:   []Tool{ProbeTool(ws)},
		Version: "test",
	})
	if err != nil {
		t.Fatalf("启动 MCP 服务: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })

	var token string
	for _, e := range srv.Env() {
		if v, ok := strings.CutPrefix(e, "MYAGENT_HARNESS_TOKEN="); ok {
			token = v
		}
	}
	if token == "" {
		t.Fatal("Env() 没有提供令牌")
	}
	return &harness{srv: srv, url: srv.Endpoint().URL, token: token, ws: ws, t: t}
}

// rpc 发一条 JSON-RPC 请求，返回状态码与解析后的响应。
func (h *harness) rpc(id any, method string, params any, opts ...func(*http.Request)) (int, rpcResponse) {
	h.t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, h.url, bytes.NewReader(raw))
	if err != nil {
		h.t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("发送请求: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out rpcResponse
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return resp.StatusCode, out
}

func TestInitializeEchoesProtocolVersion(t *testing.T) {
	h := newHarness(t)
	code, resp := h.rpc(1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test"},
	})
	if code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200", code)
	}
	got := resp.Result.(map[string]any)
	if got["protocolVersion"] != "2025-03-26" {
		t.Fatalf("必须回显客户端版本，实际 %v", got["protocolVersion"])
	}
	info, _ := got["serverInfo"].(map[string]any)
	if info["name"] != "myagent-harness" {
		t.Fatalf("serverInfo 不对: %v", got["serverInfo"])
	}
}

func TestNotificationGetsAcceptedWithoutBody(t *testing.T) {
	h := newHarness(t)
	// 无 id 是通知：必须 202 且没有 body，否则客户端会挂在等响应上。
	code, _ := h.rpc(nil, "notifications/initialized", nil)
	if code != http.StatusAccepted {
		t.Fatalf("通知应返回 202，实际 %d", code)
	}
}

func TestToolsListAndCallWritesMarker(t *testing.T) {
	h := newHarness(t)
	_, resp := h.rpc(2, "tools/list", nil)
	result := resp.Result.(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("期望 1 个工具，实际 %d", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "harness_probe" {
		t.Fatalf("工具名不对: %v", first["name"])
	}
	if _, ok := first["inputSchema"].(map[string]any); !ok {
		t.Fatal("inputSchema 必须是对象，否则客户端无法生成参数")
	}

	_, callResp := h.rpc(3, "tools/call", map[string]any{
		"name":      "harness_probe",
		"arguments": map[string]any{"path": "harness-ran.txt"},
	})
	cr := callResp.Result.(map[string]any)
	if cr["isError"] != false {
		t.Fatalf("调用应成功，实际 %v", cr)
	}
	marker := filepath.Join(h.ws, "harness-ran.txt")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker 未写入: %v", err)
	}
	if string(data) != "ran by harness mcp" {
		t.Fatalf("marker 内容异常: %q", data)
	}
	if calls := h.srv.Calls(); len(calls) != 1 || calls[0] != "harness_probe" {
		t.Fatalf("调用记录不对: %v", calls)
	}
}

func TestUnauthorizedNeverReachesHandler(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name  string
		apply func(*http.Request)
		want  int
	}{
		{"无令牌", func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		{"错令牌", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }, http.StatusUnauthorized},
		{"外部 Origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _ := h.rpc(9, "tools/call", map[string]any{
				"name":      "harness_probe",
				"arguments": map[string]any{"path": "should-not-exist.txt"},
			}, c.apply)
			if code != c.want {
				t.Fatalf("状态码 %d，期望 %d", code, c.want)
			}
			if calls := h.srv.Calls(); len(calls) != 0 {
				t.Fatalf("认证失败却触达了 handler: %v", calls)
			}
			if _, err := os.Stat(filepath.Join(h.ws, "should-not-exist.txt")); err == nil {
				t.Fatal("认证失败却产生了副作用")
			}
		})
	}
}

func TestPathEscapeIsRejectedWithoutSideEffect(t *testing.T) {
	h := newHarness(t)
	outside := filepath.Join(filepath.Dir(h.ws), "escaped.txt")
	cases := []string{
		"../escaped.txt",
		"a/../../escaped.txt",
		outside, // 绝对路径
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, resp := h.rpc(4, "tools/call", map[string]any{
				"name":      "harness_probe",
				"arguments": map[string]any{"path": p},
			})
			res := resp.Result.(map[string]any)
			if res["isError"] != true {
				t.Fatalf("越界路径必须失败，实际 %v", res)
			}
			if _, err := os.Stat(outside); err == nil {
				t.Fatal("越界路径产生了副作用")
			}
		})
	}
}

func TestUnknownToolAndMethod(t *testing.T) {
	h := newHarness(t)
	_, resp := h.rpc(5, "tools/call", map[string]any{"name": "nope"})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("未知工具应返回 -32602，实际 %+v", resp.Error)
	}
	_, resp = h.rpc(6, "resources/list", nil)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("未知方法应返回 -32601，实际 %+v", resp.Error)
	}
	if calls := h.srv.Calls(); len(calls) != 0 {
		t.Fatalf("未知工具不该记入调用: %v", calls)
	}
}

func TestClosedServerRefusesNewWork(t *testing.T) {
	h := newHarness(t)
	if err := h.srv.Close(context.Background()); err != nil {
		t.Fatalf("关闭: %v", err)
	}
	// 关闭后端口不再监听，或者返回 503；两者都算 fail-closed。
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, h.url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("关闭后应 503 或拒绝连接，实际 %d", resp.StatusCode)
		}
	}
	if calls := h.srv.Calls(); len(calls) != 0 {
		t.Fatalf("关闭后仍有调用到达: %v", calls)
	}
}

func TestTokenIsNotInEndpoint(t *testing.T) {
	h := newHarness(t)
	ep := h.srv.Endpoint()
	blob, _ := json.Marshal(ep)
	if strings.Contains(string(blob), h.token) {
		t.Fatal("Endpoint 里出现了令牌本体——它只能经 Env 注入子进程")
	}
	if ep.TokenRef == "" {
		t.Fatal("TokenRef 不能为空，调用方需要知道去哪取令牌")
	}
}
