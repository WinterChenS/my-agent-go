package resources

// 本文件验证 MOD02：连接测试必须真的调用 provider（fixture），并把
// 401 / 429 / 超时 / 有效文本 / 工具调用五类结果分类准确。
//
// 需要真实 Pi：fixture 只替代 provider，Pi 仍然是真的（否则测的是我们自己
// 写的 HTTP 客户端，而不是将要运行的实现）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/runtime/pi"
)

// providerSim 是一个可控的 OpenAI 兼容端点。mode 决定它如何回应。
type providerSim struct {
	mode string

	mu     sync.Mutex
	bodies []string
}

func (s *providerSim) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	s.mu.Unlock()

	switch s.mode {
	case "401":
		http.Error(w, `{"error":{"message":"Invalid API key provided"}}`, http.StatusUnauthorized)
		return
	case "429":
		http.Error(w, `{"error":{"message":"rate limit exceeded, too many requests"}}`, http.StatusTooManyRequests)
		return
	case "slow":
		// 不回应，直到客户端放弃。用来验证"超时"被单独分类。
		<-r.Context().Done()
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	emit := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": "chatcmpl-probe", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": "probe-model",
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}

	emit(chunk(map[string]any{"role": "assistant"}, nil))

	if s.mode == "tool" && !strings.Contains(string(body), `"role":"tool"`) {
		emit(chunk(map[string]any{"tool_calls": []map[string]any{{
			"index": 0, "id": "call_probe_1", "type": "function",
			"function": map[string]any{"name": "read", "arguments": `{"path":"probe.txt"}`},
		}}}, nil))
		emit(chunk(map[string]any{}, "tool_calls"))
	} else {
		emit(chunk(map[string]any{"content": "PROBE_OK"}, nil))
		emit(chunk(map[string]any{}, "stop"))
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *providerSim) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.bodies...)
}

// testProber 起 fixture 端点并构造指向它的 Prober。
func testProber(t *testing.T, mode string) (*Prober, *providerSim, *httptest.Server) {
	t.Helper()
	if os.Getenv("MYAGENT_PI_WORKER_INTEGRATION") != "1" {
		t.Skip("设置 MYAGENT_PI_WORKER_INTEGRATION=1 才跑真实 Pi 的连接测试")
	}
	sim := &providerSim{mode: mode}
	srv := httptest.NewServer(sim)
	t.Cleanup(srv.Close)

	p := NewProber(t.TempDir(), probeInstallation(t))
	return p, sim, srv
}

func probeModel(srvURL string) ModelDefinition {
	return ModelDefinition{
		ID:            "m-probe",
		ProviderID:    "probe",
		ModelID:       "probe-model",
		API:           "openai-completions",
		BaseURL:       srvURL + "/v1",
		InputKinds:    []string{"text"},
		ContextWindow: 8192,
		MaxOutput:     1024,
	}
}

// TestProbeReachableText 覆盖"有效文本响应"：可达、有延迟、没测的能力是 unknown。
func TestProbeReachableText(t *testing.T) {
	p, sim, srv := testProber(t, "ok")

	res := p.Probe(context.Background(), probeModel(srv.URL), "")
	if res.Error != nil {
		t.Fatalf("应当可达，实际错误 %s: %s", res.Error.Code, res.Error.Message)
	}
	if !res.Reachable {
		t.Fatal("有效文本响应应判定为可达")
	}
	if res.Vision != CapabilityUnknown {
		t.Fatalf("没发图就不能声称视觉能力，实际 %s", res.Vision)
	}
	if res.LatencyMS < 0 {
		t.Fatalf("延迟不应为负: %d", res.LatencyMS)
	}

	reqs := sim.requests()
	if len(reqs) == 0 {
		t.Fatal("fixture 没收到任何请求：连接测试没有真的调用 provider")
	}
	// 第一趟必须不声明工具：文本可用性不该被工具干扰。
	var first map[string]any
	if err := json.Unmarshal([]byte(reqs[0]), &first); err != nil {
		t.Fatalf("请求不是 JSON: %v", err)
	}
	if tools, ok := first["tools"]; ok {
		list, _ := tools.([]any)
		if len(list) > 0 {
			t.Fatalf("文本探测不应声明工具，实际声明了 %d 个", len(list))
		}
	}
}

// TestProbeDetectsToolCalling 覆盖"模型会发工具调用"：能力标 supported，
// 而不是靠"模型说自己会"来判断。
func TestProbeDetectsToolCalling(t *testing.T) {
	p, sim, srv := testProber(t, "tool")

	res := p.Probe(context.Background(), probeModel(srv.URL), "")
	if res.Error != nil {
		t.Fatalf("探测失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if !res.Reachable {
		t.Fatal("应判定为可达")
	}
	if res.ToolCalling != CapabilitySupported {
		t.Fatalf("模型发出了工具调用，能力应为 supported，实际 %s", res.ToolCalling)
	}

	// 第二趟必须声明 read 工具，否则"能发工具调用"这件事无从谈起。
	reqs := sim.requests()
	if len(reqs) < 2 {
		t.Fatalf("应至少两趟请求（文本 + 工具），实际 %d", len(reqs))
	}
	if !strings.Contains(reqs[len(reqs)-1], `"read"`) {
		t.Fatalf("工具探测没有声明 read 工具: %s", clip(reqs[len(reqs)-1], 200))
	}
}

// TestProbeWithoutToolCalling 覆盖"模型可用但不会调工具"：标 unsupported，
// 与 unknown 区分开。
func TestProbeWithoutToolCalling(t *testing.T) {
	p, _, srv := testProber(t, "ok")

	res := p.Probe(context.Background(), probeModel(srv.URL), "")
	if res.Error != nil {
		t.Fatalf("探测失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if res.ToolCalling != CapabilityUnsupported {
		t.Fatalf("模型没有发工具调用应标 unsupported，实际 %s", res.ToolCalling)
	}
}

// TestProbeClassifiesFailures 覆盖 401 / 429 / 超时三类必须分开的失败。
func TestProbeClassifiesFailures(t *testing.T) {
	cases := []struct {
		mode    string
		code    string
		timeout time.Duration
		retrybl bool
		substr  string
	}{
		{"401", "UNAUTHORIZED", 0, false, "鉴权"},
		{"429", "RATE_LIMITED", 0, true, "限流"},
		{"slow", "RPC_TIMEOUT", 3 * time.Second, true, "超时"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			p, _, srv := testProber(t, c.mode)
			if c.timeout > 0 {
				p.SetTimeout(c.timeout)
			}
			res := p.Probe(context.Background(), probeModel(srv.URL), "")
			if res.Error == nil {
				t.Fatalf("%s 应判定为失败，实际 reachable=%v", c.mode, res.Reachable)
			}
			if res.Reachable {
				t.Fatal("失败时 reachable 必须为 false")
			}
			if res.Error.Code != c.code {
				t.Fatalf("错误码应为 %s，实际 %s（%s）", c.code, res.Error.Code, res.Error.Message)
			}
			if res.Error.Retryable != c.retrybl {
				t.Fatalf("%s 的 retryable 应为 %v，实际 %v", c.code, c.retrybl, res.Error.Retryable)
			}
			if !strings.Contains(res.Error.Message, c.substr) {
				t.Fatalf("错误文案应含 %q，实际 %q", c.substr, res.Error.Message)
			}
			// 失败时能力一律不得声称支持。
			if res.ToolCalling != CapabilityUnknown || res.Vision != CapabilityUnknown {
				t.Fatalf("失败时能力应为 unknown，实际 tool=%s vision=%s", res.ToolCalling, res.Vision)
			}
		})
	}
}

// probeInstallation 找到本机的 Pi 安装件（node + cli.js）。
func probeInstallation(t *testing.T) pi.PiInstallation {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("当前连接测试按 Windows 的 node 路径编写")
	}
	node := strings.TrimSpace(os.Getenv("MYAGENT_NODE_PATH"))
	if node == "" {
		if found, err := exec.LookPath("node"); err == nil {
			node = found
		}
	}
	if node == "" {
		t.Skip("找不到 node，跳过真实 Pi 连接测试")
	}
	root := repoRootForTest(t)
	matches, _ := filepath.Glob(filepath.Join(root, ".tools", "pi", "*",
		"node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js"))
	if len(matches) == 0 {
		t.Skip("本机没有 .tools/pi 安装件，跳过真实 Pi 连接测试")
	}
	return pi.PiInstallation{
		ID:           "probe:" + matches[0],
		Executable:   node,
		Args:         []string{matches[0]},
		Version:      "1.5.0",
		Origin:       pi.OriginExternal,
		Architecture: runtime.GOARCH,
		Runnable:     true,
	}
}
