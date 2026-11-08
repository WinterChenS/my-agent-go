package resources

// MP1：本地模型发现与用户显式端点探测。
//
// 守四件事：
//  1. 两种真实形状（Ollama /api/tags、OpenAI /v1/models）都能解析；
//  2. 探不通的端点给出稳定错误码，而不是"空列表看起来像没有模型"；
//  3. 探测有界：并行、单端点超时，整体耗时不被最慢的那个拖死；
//  4. DiscoverModels **只**访问回环地址：非回环目标一个包都不发。

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

func ollamaTags(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		models := make([]map[string]any, 0, len(names))
		for _, n := range names {
			models = append(models, map[string]any{
				"name":        n,
				"model":       n,
				"size":        1234,
				"modified_at": "2026-01-01T00:00:00Z",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	}))
}

func openAIModels(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		data := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
}

// TestDiscoverModelsParsesBothShapesAndKeepsGoing 覆盖"一个端点死了不该让整轮扫描失败"。
func TestDiscoverModelsParsesBothShapesAndKeepsGoing(t *testing.T) {
	ollama := ollamaTags(t, "llama3.2:latest", "qwen2.5-coder:7b")
	defer ollama.Close()
	openai := openAIModels(t, "gpt-4o-mini", "text-embedding-3-small")
	defer openai.Close()

	svc, _, _ := newTestService(t)
	WithDiscoveryTargets([]DiscoveryTarget{
		{Kind: "ollama", ProbeURL: ollama.URL + "/api/tags", BaseURL: ollama.URL + "/v1", API: "openai-completions"},
		{Kind: "lmstudio", ProbeURL: openai.URL + "/v1/models", BaseURL: openai.URL + "/v1", API: "openai-completions"},
	})(svc)

	items, err := svc.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("扫描不应整体失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 个端点结果，实际 %d", len(items))
	}
	byKind := map[string]DiscoveredEndpoint{}
	for _, it := range items {
		byKind[it.Kind] = it
	}

	o := byKind["ollama"]
	if !o.Reachable {
		t.Fatalf("Ollama 端点应可达，实际错误 %+v", o.Error)
	}
	if o.BaseURL != ollama.URL+"/v1" {
		t.Fatalf("Ollama 的 models.json baseUrl 必须是 /v1 端点，实际 %q", o.BaseURL)
	}
	if len(o.Models) != 2 || o.Models[0].ID != "llama3.2:latest" {
		t.Fatalf("模型名单解析错误: %+v", o.Models)
	}
	if o.Error != nil {
		t.Fatalf("可达时不该有错误: %+v", o.Error)
	}

	l := byKind["lmstudio"]
	if !l.Reachable || len(l.Models) != 2 || l.Models[1].ID != "text-embedding-3-small" {
		t.Fatalf("OpenAI 形状解析错误: reachable=%v models=%+v err=%+v", l.Reachable, l.Models, l.Error)
	}
}

// TestDiscoverModelsReportsStableErrorForDeadPort 守"探不通要说清楚"。
func TestDiscoverModelsReportsStableErrorForDeadPort(t *testing.T) {
	// 先占一个端口再关掉：拿到一个几乎肯定没人监听的地址，而不是猜端口。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口: %v", err)
	}
	dead := "http://127.0.0.1:" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()

	svc, _, _ := newTestService(t)
	WithDiscoveryTargets([]DiscoveryTarget{
		{Kind: "llamacpp", ProbeURL: dead + "/v1/models", BaseURL: dead + "/v1", API: "openai-completions"},
	})(svc)

	items, err := svc.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("扫描不应整体失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 条结果，实际 %d", len(items))
	}
	got := items[0]
	if got.Reachable {
		t.Fatal("死端口不能报告为可达")
	}
	if got.Error == nil || got.Error.Code != CodeDiscoveryUnreachable {
		t.Fatalf("期望稳定错误码 %s，实际 %+v", CodeDiscoveryUnreachable, got.Error)
	}
	if len(got.Models) != 0 {
		t.Fatalf("不可达时不能编出模型列表: %+v", got.Models)
	}
}

// TestDiscoverModelsBoundedAndParallel：4 个慢端点并行、各自 1.5s 内放弃，
// 整轮耗时应接近单个超时而不是 4 倍。
func TestDiscoverModelsBoundedAndParallel(t *testing.T) {
	var wg sync.WaitGroup
	slow := func() *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wg.Add(1)
			defer wg.Done()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		t.Cleanup(s.Close)
		return s
	}
	a, b, c, d := slow(), slow(), slow(), slow()

	svc, _, _ := newTestService(t)
	WithDiscoveryTimeout(120 * time.Millisecond)(svc)
	WithDiscoveryTargets([]DiscoveryTarget{
		{Kind: "ollama", ProbeURL: a.URL + "/api/tags", BaseURL: a.URL + "/v1", API: "openai-completions"},
		{Kind: "lmstudio", ProbeURL: b.URL + "/v1/models", BaseURL: b.URL + "/v1", API: "openai-completions"},
		{Kind: "vllm", ProbeURL: c.URL + "/v1/models", BaseURL: c.URL + "/v1", API: "openai-completions"},
		{Kind: "llamacpp", ProbeURL: d.URL + "/v1/models", BaseURL: d.URL + "/v1", API: "openai-completions"},
	})(svc)

	start := time.Now()
	items, err := svc.DiscoverModels(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("扫描不应整体失败: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("期望 4 条结果，实际 %d", len(items))
	}
	for _, it := range items {
		if it.Reachable {
			t.Fatalf("%s 超时端点不能报告可达", it.Kind)
		}
		if it.Error == nil || it.Error.Code != CodeDiscoveryTimeout {
			t.Fatalf("%s 期望 %s，实际 %+v", it.Kind, CodeDiscoveryTimeout, it.Error)
		}
	}
	// 并行 + 有界：4 × 120ms 串行也要 480ms，这里给足调度余量仍远小于串行下界。
	if elapsed > 900*time.Millisecond {
		t.Fatalf("扫描未被单端点超时限制住，耗时 %s", elapsed)
	}
}

// TestDiscoverModelsNeverDialsNonLoopback：用户没点"扫描任意端点"时，
// 发现只能碰回环。用拨号钩子记录真实目标，断言非回环一个都没有。
func TestDiscoverModelsNeverDialsNonLoopback(t *testing.T) {
	svc, _, _ := newTestService(t)
	var mu sync.Mutex
	dialed := []string{}
	WithDiscoveryTargets([]DiscoveryTarget{
		// 回环目标：允许，且必须真的被拨。
		{Kind: "vllm", ProbeURL: "http://127.0.0.1:1/v1/models", BaseURL: "http://127.0.0.1:1/v1", API: "openai-completions"},
		// 非回环目标：哪怕写进了目标清单，也必须被拒绝，且不产生拨号。
		{Kind: "evil", ProbeURL: "http://192.0.2.10:8000/v1/models", BaseURL: "http://192.0.2.10:8000/v1", API: "openai-completions"},
		{Kind: "evil6", ProbeURL: "http://[2001:db8::1]:8000/v1/models", BaseURL: "http://[2001:db8::1]:8000/v1", API: "openai-completions"},
	})(svc)
	WithDiscoveryDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})(svc)

	items, err := svc.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("扫描不应整体失败: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), dialed...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("回环目标没有被拨号：探测根本没有发生")
	}
	for _, addr := range got {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("拨号地址不可解析: %q", addr)
		}
		if !isLoopbackHost(host) {
			t.Fatalf("发现流程拨了非回环地址 %q：这会悄悄探测用户网络", host)
		}
	}

	byKind := map[string]DiscoveredEndpoint{}
	for _, it := range items {
		byKind[it.Kind] = it
	}
	for _, kind := range []string{"evil", "evil6"} {
		it, ok := byKind[kind]
		if !ok {
			t.Fatalf("目标 %s 应有一条结果（说明被明确拒绝，而不是被静默丢弃）", kind)
		}
		if it.Reachable {
			t.Fatalf("%s 不能可达", kind)
		}
		if it.Error == nil || it.Error.Code != CodeDiscoveryNotLoopback {
			t.Fatalf("%s 期望 %s，实际 %+v", kind, CodeDiscoveryNotLoopback, it.Error)
		}
	}
}

// TestProbeProviderModelsLoopbackHTTPIsSecure：回环上的明文 HTTP 是本机通信，
// 允许，且不该标成 insecure（它没过网络）。
func TestProbeProviderModelsLoopbackHTTPIsSecure(t *testing.T) {
	srv := openAIModels(t, "local-model")
	defer srv.Close()

	svc, _, _ := newTestService(t)
	probe, err := svc.ProbeProviderModels(context.Background(), srv.URL, "openai-completions", "", false)
	if err != nil {
		t.Fatalf("回环 HTTP 应被允许: %v", err)
	}
	if !probe.Reachable || len(probe.Models) != 1 || probe.Models[0].ID != "local-model" {
		t.Fatalf("探测结果错误: %+v", probe)
	}
	if probe.Insecure {
		t.Fatal("回环是本机通信，不应标 insecure")
	}
}

// TestProbeProviderModelsAcceptsPrivateHTTPAsInsecure：用户真实场景是局域网
// 里的明文网关（http://192.168.3.10:3425/v1）。它必须可用，但必须被标成
// insecure，界面才能如实警告"凭据会以明文经过本机网络"。
func TestProbeProviderModelsAcceptsPrivateHTTPAsInsecure(t *testing.T) {
	srv := openAIModels(t, "lan-model")
	defer srv.Close()
	// 拨号重定向到本机测试服务器：分类看的是 URL 里的私网地址，
	// 而测试不需要真的在局域网上有一台机器。
	lan := "http://192.168.3.10:3425/v1"

	svc, _, _ := newTestService(t)
	WithProbeDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	})(svc)

	probe, err := svc.ProbeProviderModels(context.Background(), lan, "openai-completions", "", false)
	if err != nil {
		t.Fatalf("私网明文 HTTP 应被允许（否则用户的局域网网关用不了）: %v", err)
	}
	if !probe.Insecure {
		t.Fatal("私网明文 HTTP 必须标注 insecure")
	}
	if !probe.Reachable || len(probe.Models) != 1 || probe.Models[0].ID != "lan-model" {
		t.Fatalf("探测结果错误: %+v", probe)
	}
}

// TestProbeProviderModelsRejectsPublicHTTPWithoutConfirmation：公网明文
// 默认拒绝；用户显式确认后才放行（仍然标 insecure）。
func TestProbeProviderModelsRejectsPublicHTTPWithoutConfirmation(t *testing.T) {
	svc, _, _ := newTestService(t)
	WithProbeTimeout(150 * time.Millisecond)(svc)
	_, err := svc.ProbeProviderModels(context.Background(), "http://203.0.113.9:8000/v1", "openai-completions", "", false)
	if err == nil {
		t.Fatal("公网明文 HTTP 未确认时必须被拒绝")
	}
	requireCode(t, err, contracts.CodeUnsupported)
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("拒绝原因要说明必须用 HTTPS，实际 %q", err.Error())
	}

	// 显式确认后放行：不再返回校验错误。
	probe, err := svc.ProbeProviderModels(context.Background(), "http://203.0.113.9:8000/v1", "openai-completions", "", true)
	if err != nil {
		t.Fatalf("显式确认后应放行: %v", err)
	}
	if !probe.Insecure {
		t.Fatal("公网明文必须标注 insecure")
	}
	if probe.Reachable {
		t.Fatal("这个地址不该真的可达（测试网段），可达结论会骗人")
	}
	if probe.Error == nil {
		t.Fatal("不可达要给出错误，而不是空结果")
	}
}

// TestProbeProviderModelsOllamaFlavor：Ollama 的模型清单在 /api/tags，
// 用户显式选了 ollama 时不能只打 /v1/models 然后说"没有模型"。
func TestProbeProviderModelsOllamaFlavor(t *testing.T) {
	srv := ollamaTags(t, "qwen2.5:3b")
	defer srv.Close()

	svc, _, _ := newTestService(t)
	probe, err := svc.ProbeProviderModels(context.Background(), srv.URL, "ollama", "", false)
	if err != nil {
		t.Fatalf("Ollama 探测失败: %v", err)
	}
	if !probe.Reachable || len(probe.Models) != 1 {
		t.Fatalf("Ollama 模型清单解析错误: %+v", probe)
	}
	if probe.API != "openai-completions" {
		t.Fatalf("Pi 没有 ollama 协议，导入时必须落到 openai-completions，实际 %q", probe.API)
	}
	if !strings.HasSuffix(probe.BaseURL, "/v1") {
		t.Fatalf("Ollama 的 OpenAI 兼容端点是 /v1，实际 %q", probe.BaseURL)
	}
}

// TestDiscoverModelsDefaultTargetsAreLoopback：默认矩阵只含回环地址。
// 这条断言防止有人"顺手"把某个云端地址加进自动扫描。
func TestDiscoverModelsDefaultTargetsAreLoopback(t *testing.T) {
	targets := DiscoveryTargets()
	if len(targets) < 4 {
		t.Fatalf("默认矩阵应覆盖 Ollama/LM Studio/vLLM/llama.cpp，实际 %d 条", len(targets))
	}
	want := map[string]string{
		"ollama":   "127.0.0.1:11434",
		"lmstudio": "127.0.0.1:1234",
		"vllm":     "127.0.0.1:8000",
		"llamacpp": "127.0.0.1:8080",
	}
	for _, tg := range targets {
		host := want[tg.Kind]
		if host == "" {
			t.Fatalf("未知默认端点 kind=%q", tg.Kind)
		}
		if !strings.Contains(tg.ProbeURL, host) {
			t.Fatalf("%s 的探测地址不是 %s: %s", tg.Kind, host, tg.ProbeURL)
		}
		if !apiExists(tg.API) {
			t.Fatalf("%s 的 api %q 不在白名单里", tg.Kind, tg.API)
		}
	}
}
