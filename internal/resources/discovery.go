package resources

// 本地模型自动发现（MP1）。
//
// 用户原话："需要可以自动读取本地已有的模型"。这里只做一件事：
// 并行探一组**写死的回环端点**，读它们各自的模型清单。
//
// 两条不能松的边界：
//  1. 自动发现绝不访问非回环地址。目标清单是常量，拨号层再挡一道：
//     即使有人往清单里加了外网地址，连接也不会发出去，结果是
//     DISCOVERY_NOT_LOOPBACK，而不是悄悄探测用户的网络。
//  2. 有界。每个端点 1.5s 超时、并行，整轮扫描的耗时接近单个超时。
//     串行探测在"四个端点都没开"时会白等 6 秒，看起来就是界面卡死。
//
// 错误码是稳定的（前端按 code 出文案），message 只放诊断细节。

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
)

// 发现结果的稳定错误码。
const (
	CodeDiscoveryUnreachable = "DISCOVERY_UNREACHABLE"
	CodeDiscoveryTimeout     = "DISCOVERY_TIMEOUT"
	CodeDiscoveryHTTPStatus  = "DISCOVERY_HTTP_STATUS"
	CodeDiscoveryBadResponse = "DISCOVERY_BAD_RESPONSE"
	CodeDiscoveryNotLoopback = "DISCOVERY_NOT_LOOPBACK"
)

// discoveryTimeout 是单个端点的上限（与规格一致）。
const discoveryTimeout = 1500 * time.Millisecond

// providerProbeTimeout 是用户显式扫描一个端点的上限。
const providerProbeTimeout = 5 * time.Second

// DiscoveryTarget 是一个发现目标。
//
// ProbeURL 是真正 GET 的地址；BaseURL 是写进模型定义、给 Pi 用的地址。
// 两者对 Ollama 不一样：清单在 /api/tags，OpenAI 兼容端点在 /v1。
type DiscoveryTarget struct {
	Kind     string `json:"kind"`
	ProbeURL string `json:"probeUrl"`
	BaseURL  string `json:"baseUrl"`
	API      string `json:"api"`
}

// defaultDiscoveryTargets 是自动发现的完整矩阵。
//
// 只有回环地址：自动发现是一种"静默动作"，用户没有点过任何东西，
// 它不能去碰局域网或公网。
var defaultDiscoveryTargets = []DiscoveryTarget{
	{Kind: "ollama", ProbeURL: "http://127.0.0.1:11434/api/tags", BaseURL: "http://127.0.0.1:11434/v1", API: "openai-completions"},
	{Kind: "lmstudio", ProbeURL: "http://127.0.0.1:1234/v1/models", BaseURL: "http://127.0.0.1:1234/v1", API: "openai-completions"},
	{Kind: "vllm", ProbeURL: "http://127.0.0.1:8000/v1/models", BaseURL: "http://127.0.0.1:8000/v1", API: "openai-completions"},
	{Kind: "llamacpp", ProbeURL: "http://127.0.0.1:8080/v1/models", BaseURL: "http://127.0.0.1:8080/v1", API: "openai-completions"},
}

// DiscoveryTargets 返回默认矩阵的副本。
func DiscoveryTargets() []DiscoveryTarget {
	out := make([]DiscoveryTarget, len(defaultDiscoveryTargets))
	copy(out, defaultDiscoveryTargets)
	return out
}

// DiscoveredModel 是端点报告的一条模型。
type DiscoveredModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Size 只在端点提供时出现（Ollama 会给字节数）。
	Size int64 `json:"size,omitempty"`
}

// DiscoveryFailure 是稳定的失败描述。Detail 只放诊断细节，不参与文案。
type DiscoveryFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// DiscoveredEndpoint 是一个端点的扫描结论。
type DiscoveredEndpoint struct {
	Kind      string            `json:"kind"`
	BaseURL   string            `json:"baseUrl"`
	API       string            `json:"api"`
	Reachable bool              `json:"reachable"`
	LatencyMS int64             `json:"latencyMs"`
	Insecure  bool              `json:"insecure"`
	Models    []DiscoveredModel `json:"models"`
	Error     *DiscoveryFailure `json:"error,omitempty"`
}

// ProviderProbe 是用户显式扫描一个端点后的结论。
type ProviderProbe struct {
	BaseURL   string            `json:"baseUrl"`
	API       string            `json:"api"`
	Reachable bool              `json:"reachable"`
	LatencyMS int64             `json:"latencyMs"`
	Insecure  bool              `json:"insecure"`
	Models    []DiscoveredModel `json:"models"`
	Error     *DiscoveryFailure `json:"error,omitempty"`
}

// WithDiscoveryTargets 替换发现目标（测试用）。
func WithDiscoveryTargets(targets []DiscoveryTarget) Option {
	return func(s *Service) {
		if targets != nil {
			s.discoveryTargets = targets
		}
	}
}

// WithDiscoveryTimeout 覆盖单端点超时（测试用）。
func WithDiscoveryTimeout(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.discoveryTimeout = d
		}
	}
}

// WithDiscoveryDial 替换发现流程的拨号实现（测试用）。
func WithDiscoveryDial(fn func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(s *Service) {
		if fn != nil {
			s.dialer = fn
		}
	}
}

// WithProbeTimeout 覆盖用户显式扫描的单端点超时（测试用）。
func WithProbeTimeout(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.probeTimeout = d
		}
	}
}

// WithProbeDial 替换用户显式扫描的拨号实现（测试用）。
func WithProbeDial(fn func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(s *Service) {
		if fn != nil {
			s.dialer = fn
		}
	}
}

func (s *Service) targets() []DiscoveryTarget {
	if len(s.discoveryTargets) > 0 {
		return s.discoveryTargets
	}
	return defaultDiscoveryTargets
}

func (s *Service) probeDeadline() time.Duration {
	if s.probeTimeout > 0 {
		return s.probeTimeout
	}
	return providerProbeTimeout
}

func (s *Service) dialTimeout() time.Duration {
	if s.discoveryTimeout > 0 {
		return s.discoveryTimeout
	}
	return discoveryTimeout
}

// DiscoverModels 并行探测默认的回环端点集合。
//
// 返回顺序与目标顺序一致（不让 map 迭代顺序变成界面上的随机排布）。
func (s *Service) DiscoverModels(ctx context.Context) ([]DiscoveredEndpoint, error) {
	targets := s.targets()
	out := make([]DiscoveredEndpoint, len(targets))
	// 上限就是 len(targets)（默认 4 个）。显式信号量是为了让"以后加端点"
	// 也不会变成一次开几十个连接。
	limit := len(targets)
	if limit > 8 {
		limit = 8
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, tg := range targets {
		wg.Add(1)
		go func(i int, tg DiscoveryTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.discoverOne(ctx, tg)
		}(i, tg)
	}
	wg.Wait()
	return out, nil
}

func (s *Service) discoverOne(ctx context.Context, tg DiscoveryTarget) DiscoveredEndpoint {
	res := DiscoveredEndpoint{Kind: tg.Kind, BaseURL: tg.BaseURL, API: tg.API, Models: []DiscoveredModel{}}
	u, err := url.Parse(tg.ProbeURL)
	if err != nil || u.Host == "" {
		res.Error = failure(CodeDiscoveryBadResponse, "探测地址无法解析", tg.ProbeURL)
		return res
	}
	// 自动发现只碰回环：这一条在发起请求之前挡住，拨号层再挡一次。
	if !isLoopbackHost(u.Hostname()) {
		res.Error = failure(CodeDiscoveryNotLoopback,
			"自动发现只探测本机端点，已跳过 "+u.Hostname(),
			"只有用户显式扫描才能访问非回环地址")
		return res
	}

	flavor := "openai"
	if tg.Kind == "ollama" {
		flavor = "ollama"
	}
	start := time.Now()
	models, f := s.fetchModels(ctx, tg.ProbeURL, flavor, s.discoveryClient(s.dialTimeout(), nil))
	res.LatencyMS = time.Since(start).Milliseconds()
	res.Models = models
	if f != nil {
		res.Error = f
		return res
	}
	res.Reachable = true
	return res
}

// ProbeProviderModels 扫描用户**显式**输入的端点。
//
// 与自动发现的区别不是"能不能访问外网"，而是"用户点过按钮"。
// 明文 HTTP 的规则见 endpoint_security.go：私网放行并标 insecure，
// 公网要么 HTTPS，要么用户显式确认。
func (s *Service) ProbeProviderModels(ctx context.Context, baseURL, api, credentialRef string, allowInsecure bool) (ProviderProbe, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return ProviderProbe{}, contracts.NewAppError(contracts.CodeUnsupported, "baseUrl 为空", false)
	}
	insecure, err := parseEndpoint(raw, allowInsecure)
	if err != nil {
		return ProviderProbe{}, err
	}
	candidates, resolvedAPI, err := probeCandidates(raw, api)
	if err != nil {
		return ProviderProbe{}, err
	}
	if err := validateBaseURL(raw); err != nil {
		return ProviderProbe{}, err
	}

	header := http.Header{}
	if ref := strings.TrimSpace(credentialRef); ref != "" {
		if s.creds == nil {
			return ProviderProbe{}, contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
				"没有可用的 OS 凭证库，无法用凭证扫描端点", false)
		}
		secret, err := s.creds.Get(ctx, ref)
		if err != nil {
			return ProviderProbe{}, contracts.NewAppError(contracts.CodeDependencyMissing,
				"凭证引用 "+ref+" 不存在: "+err.Error(), false)
		}
		header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
	}

	resolvedBase := strings.TrimRight(raw, "/")
	if probeFlavor(api) == "ollama" {
		resolvedBase = normalizeV1(raw)
	}
	out := ProviderProbe{BaseURL: resolvedBase, API: resolvedAPI, Insecure: insecure, Models: []DiscoveredModel{}}
	client := s.discoveryClient(s.probeDeadline(), header)

	var last *DiscoveryFailure
	for _, candidate := range candidates {
		start := time.Now()
		models, f := s.fetchModels(ctx, candidate, probeFlavor(api), client)
		latency := time.Since(start).Milliseconds()
		if f != nil {
			last = f
			continue
		}
		out.Reachable = true
		out.LatencyMS = latency
		out.Models = models
		return out, nil
	}
	if last == nil {
		last = failure(CodeDiscoveryBadResponse, "端点没有返回可用的模型清单", raw)
	}
	out.Error = last
	return out, nil
}

// probeFlavor 决定解析哪种响应形状。
func probeFlavor(api string) string {
	if strings.EqualFold(strings.TrimSpace(api), "ollama") {
		return "ollama"
	}
	return "openai"
}

// probeCandidates 生成要尝试的 URL 列表与最终应写进模型定义的 api。
//
// Ollama 比较特殊：它自己的清单是 /api/tags，但 Pi 只有 openai-completions
// 可用，所以最终 baseUrl 必须落到 /v1。
func probeCandidates(raw, api string) ([]string, string, error) {
	base := strings.TrimRight(raw, "/")
	trimmedAPI := strings.TrimSpace(api)
	flavor := probeFlavor(trimmedAPI)
	if flavor == "ollama" {
		return []string{
			base + "/api/tags",
			normalizeV1(base) + "/models",
		}, "openai-completions", nil
	}
	if trimmedAPI != "" && !apiExists(trimmedAPI) {
		return nil, "", contracts.NewAppError(contracts.CodeUnsupported,
			"不支持的 API 类型: "+trimmedAPI, false)
	}
	if trimmedAPI == "" {
		trimmedAPI = "openai-completions"
	}
	if strings.HasSuffix(base, "/v1") {
		return []string{base + "/models"}, trimmedAPI, nil
	}
	return []string{base + "/v1/models", base + "/models"}, trimmedAPI, nil
}

// normalizeV1 把 OpenAI 兼容端点规范成 .../v1 结尾。
//
// 只用于 Ollama：它自己的清单在 /api/tags，而 Pi 走 OpenAI 兼容的 /v1。
// 其它协议（anthropic-messages 等）的 baseUrl 是用户输入原样保存——
// 给它们统一加 /v1 会把请求打到不存在的路径。
func normalizeV1(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	base = strings.TrimSuffix(base, "/models")
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

// discoveryClient 构造一个带超时与拨号约束的 HTTP 客户端。
func (s *Service) discoveryClient(timeout time.Duration, header http.Header) *http.Client {
	dial := s.dialer
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
		// 本地端点常是自签证书（HTTPS 的 NAS 网关），探测只关心"能不能读清单"。
		// 这里不做证书校验，因为这不用于传输密钥之外的数据，且用户显式触发。
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 见上
		MaxIdleConnsPerHost: 2,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &headerTransport{
			base:   transport,
			header: header,
		},
		// 不跟随重定向：端点被劫持/改写到别处时，探测结果不该悄悄变成另一个主机。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type headerTransport struct {
	base   http.RoundTripper
	header http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.header) > 0 {
		r := req.Clone(req.Context())
		for k, vs := range t.header {
			for _, v := range vs {
				r.Header.Set(k, v)
			}
		}
		req = r
	}
	return t.base.RoundTrip(req)
}

// fetchModels 发一次 GET 并解析模型清单；失败返回稳定错误。
func (s *Service) fetchModels(ctx context.Context, rawURL, flavor string, client *http.Client) ([]DiscoveredModel, *DiscoveryFailure) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, failure(CodeDiscoveryBadResponse, "构造探测请求失败", err.Error())
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		if f := classifyTransportError(err); f != nil {
			return nil, f
		}
		return nil, failure(CodeDiscoveryBadResponse, "读取端点响应失败", err.Error())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, failure(CodeDiscoveryHTTPStatus,
			fmt.Sprintf("端点返回 HTTP %d", resp.StatusCode), truncate(string(body), 300))
	}

	models, f := parseModelList(body, flavor)
	if f != nil {
		return nil, f
	}
	return models, nil
}

// parseModelList 解析两种真实形状：Ollama 的 {models:[{name}]} 与
// OpenAI 的 {data:[{id}]}。也接受 {models:[{id}]} 这种兼容网关变体。
func parseModelList(body []byte, flavor string) ([]DiscoveredModel, *DiscoveryFailure) {
	var doc struct {
		Data   []discoveredModelJSON `json:"data"`
		Models []discoveredModelJSON `json:"models"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, failure(CodeDiscoveryBadResponse, "端点响应不是预期的 JSON 模型清单", truncate(string(body), 300))
	}
	entries := doc.Data
	if flavor == "ollama" || len(entries) == 0 {
		if len(doc.Models) > 0 {
			entries = doc.Models
		}
	}
	out := make([]DiscoveredModel, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = strings.TrimSpace(e.Name)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = id
		}
		out = append(out, DiscoveredModel{ID: id, Name: name, Size: e.Size})
	}
	if len(out) == 0 {
		// 端点通了但一条模型都没有：这是可解释的事实，不是错误。
		return []DiscoveredModel{}, nil
	}
	return out, nil
}

type discoveredModelJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// classifyTransportError 把 net/http 的错误映射成稳定码。
func classifyTransportError(err error) *DiscoveryFailure {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return failure(CodeDiscoveryTimeout, "探测被取消", err.Error())
	}
	if isTimeout(err) {
		return failure(CodeDiscoveryTimeout, "端点超时未响应", err.Error())
	}
	return failure(CodeDiscoveryUnreachable, "端点不可达", err.Error())
}

func failure(code, message, detail string) *DiscoveryFailure {
	return &DiscoveryFailure{Code: code, Message: message, Detail: detail}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
