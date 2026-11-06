package pi

// SEC02 / SEC03.runtime / SEC08 的外部观察装置。
//
// 观察链（结论全部落在磁盘上，不来自断言变量）：
//
//	Pi 子进程 → 策略扩展 → HTTP → 记录代理（wire.jsonl）→ 策略端点
//	工具到底跑没跑            → 工作区磁盘遍历（tree.json）
//	通道的拒绝/允许是否成立    → channels.json（由上两份文件派生）
//	项目级资源有没有生效       → project-resources.json（磁盘 + 模型请求日志）
//
// 为什么在端点前面再加一层记录代理，而不是改端点：端点是产品代码，本次任务不改它；
// 而且"从端点外面记录"恰好是我们要的视角——记下来的是扩展真实发出的请求和端点
// 真实给出的结论。代理只做两件事：转发、把请求/响应写成一行 JSON。
//
// 为什么 facts 不直接写 pending 计数：pending 是进程内变量，等于自证。
// 这里一律先落盘，再从盘上读回来算。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── 记录代理（策略端点的服务端观察） ──────────────────────────────────────

// bridgeWireRecord 是一次真实的授权请求在服务端留下的记录。
type bridgeWireRecord struct {
	TS      string `json:"ts"`
	Test    string `json:"test"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Host    string `json:"host"`
	Session string `json:"session,omitempty"`
	// HasToken 只记录"带没带令牌"，绝不记录令牌本体。
	HasToken bool   `json:"hasToken"`
	Status   int    `json:"status"`
	ToolName string `json:"toolName,omitempty"`
	Source   string `json:"source,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// captureWriter 既写下游，也留一份响应体副本（截断到 8 KiB）。
type captureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if room := 8<<10 - c.body.Len(); room > 0 {
		if len(b) < room {
			c.body.Write(b)
		} else {
			c.body.Write(b[:room])
		}
	}
	return c.ResponseWriter.Write(b)
}

// policyWireLogPath 返回本次 case 的请求日志路径（未配置观察目录时为空）。
func policyWireLogPath() string {
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "wire.jsonl")
}

var bridgeWireMu sync.Mutex

func appendBridgeWire(path string, rec bridgeWireRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	bridgeWireMu.Lock()
	defer bridgeWireMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, 0x0a))
	_ = f.Close()
}

// readBridgeWire 从磁盘读回请求日志。
func readBridgeWire(t *testing.T, path string) []bridgeWireRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回策略端点请求日志 %s 失败: %v", path, err)
	}
	var out []bridgeWireRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec bridgeWireRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("请求日志记录不是合法 JSON: %v（原文 %s）", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// bridgeWireLen 返回日志当前条数；文件还没被创建时算 0（首次运行前的基线）。
func bridgeWireLen(t *testing.T, path string) int {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("检查请求日志 %s 失败: %v", path, err)
	}
	return len(readBridgeWire(t, path))
}

// startBridgeWireProxy 在策略端点前面起一层记录代理，返回代理 URL。
//
// 端点校验 Host（防 DNS rebinding），所以 Director 里必须把 Host 改回端点自己的
// host:port——否则代理转发的请求会被端点以 403 拒掉，看起来像"策略拒绝"。
func startBridgeWireProxy(t *testing.T, upstream, logPath, testName string) *httptest.Server {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("解析端点 URL %s 失败: %v", upstream, err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	inner := rp.Director
	rp.Director = func(req *http.Request) {
		inner(req)
		req.Host = target.Host
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))

		rec := &captureWriter{ResponseWriter: w}
		rp.ServeHTTP(rec, r)

		var req struct {
			ToolName string `json:"toolName"`
			Source   string `json:"source"`
		}
		_ = json.Unmarshal(raw, &req)
		var resp struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &resp)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		appendBridgeWire(logPath, bridgeWireRecord{
			TS:       time.Now().UTC().Format(time.RFC3339Nano),
			Test:     testName,
			Method:   r.Method,
			Path:     r.URL.Path,
			Host:     r.Host,
			Session:  r.Header.Get("X-MyAgent-Session"),
			HasToken: strings.TrimSpace(r.Header.Get("Authorization")) != "",
			Status:   status,
			ToolName: req.ToolName,
			Source:   req.Source,
			Decision: resp.Decision,
			Reason:   resp.Reason,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// envWithPolicyURL 把端点 URL 换成代理 URL（令牌与会话不变）。
func envWithPolicyURL(env []string, proxyURL string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "MYAGENT_POLICY_URL=") {
			out = append(out, "MYAGENT_POLICY_URL="+proxyURL)
			continue
		}
		out = append(out, kv)
	}
	return out
}

// wireDecisions 统计某次运行里针对某个工具名的结论（从磁盘读回的记录里数）。
func wireDecisions(recs []bridgeWireRecord, toolName string) (deny, allow int) {
	for _, rec := range recs {
		if rec.Path != "/v1/authorize" || rec.ToolName != toolName {
			continue
		}
		switch rec.Decision {
		case "deny":
			deny++
		case "allow":
			allow++
		}
	}
	return deny, allow
}

// ── 工作区磁盘快照 ────────────────────────────────────────────────────────

// treeFileSHA 在磁盘遍历结果里找一个文件的 sha256；不存在返回 ""。
func treeFileSHA(tree []treeEntry, base string) (string, bool) {
	for _, e := range tree {
		if e.Size < 0 {
			continue
		}
		if filepath.ToSlash(filepath.Base(e.Path)) == base || filepath.ToSlash(e.Path) == base {
			return e.SHA256, true
		}
	}
	return "", false
}

// sentinelBaseline 在运行**之前**从磁盘取哨兵文件的 sha256（不存在返回 ""）。
func sentinelBaseline(t *testing.T, work, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(work, name))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("读哨兵文件 %s 失败: %v", name, err)
	}
	return sha256Of(b)
}

// treePresent 判断哨兵文件是否出现在磁盘遍历结果里。
func treePresent(tree []treeEntry, base string) bool {
	_, ok := treeFileSHA(tree, base)
	return ok
}

// channelRunFrom 把一次运行归一成 channelRun：sha 来自磁盘遍历，
// 结论条数来自请求日志切片，present 由调用方按"副作用算不算发生"给定。
func channelRunFrom(run policyRun, toolName, sentinel string, present bool, note string) channelRun {
	sha, _ := treeFileSHA(run.tree, sentinel)
	deny, allow := wireDecisions(run.wire, toolName)
	return channelRun{
		SentinelSHA:     sha,
		SentinelPresent: present,
		WireDenied:      deny,
		WireAllowed:     allow,
		Note:            note,
	}
}

// ── channels.json（通道证据） ──────────────────────────────────────────────

// channelRun 是某次运行结束后从磁盘上读到的哨兵状态。
type channelRun struct {
	// SentinelSHA 是运行结束后哨兵文件的 sha256（"" = 文件不存在）。
	SentinelSHA string `json:"sentinelSha"`
	// SentinelPresent 是"哨兵表达的副作用发生了"。
	// 对写文件类通道就是文件存在；对 edit 是内容被改；对 MCP 日志是出现 tools/call。
	SentinelPresent bool `json:"sentinelPresent"`
	// WireDenied / WireAllowed 是从请求日志里数出来的结论条数。
	WireDenied  int `json:"wireDenied"`
	WireAllowed int `json:"wireAllowed"`
	// Note 说明这条记录怎么来的（哪种副作用算"发生了"）。
	Note string `json:"note,omitempty"`
}

type channelEvidence struct {
	Channel    string     `json:"channel"`
	ToolName   string     `json:"toolName"`
	Sentinel   string     `json:"sentinel"`
	Baseline   string     `json:"baselineSha"`
	Deny       channelRun `json:"deny"`
	Allow      channelRun `json:"allow"`
	Blocked    bool       `json:"blocked"`
	Allowed    bool       `json:"allowed"`
	RecordedAt string     `json:"recordedAt"`
}

var channelEvidenceMu sync.Mutex

func channelsPath() string {
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "channels.json")
}

// recordChannelEvidence 把一条通道的证据合并写进 channels.json，并同步写入观察值。
//
// 判定只用两样东西：运行前/后的磁盘 sha256（Baseline/Deny.SentinelSHA/Allow.SentinelSHA）
// 与请求日志里的结论条数。两者都已在调用方从磁盘读回。
func recordChannelEvidence(t *testing.T, ev channelEvidence) {
	t.Helper()
	ev.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	ev.Blocked = !ev.Deny.SentinelPresent || ev.Deny.SentinelSHA == ev.Baseline
	ev.Allowed = ev.Allow.SentinelPresent && ev.Allow.SentinelSHA != ev.Baseline
	// 证据自洽：说"被拦住"就必须在请求日志里有一条 deny，反之亦然。
	if ev.Blocked && ev.Deny.WireDenied == 0 {
		t.Fatalf("通道 %s 记为 blocked，但请求日志里没有 deny 记录（%+v）", ev.Channel, ev.Deny)
	}
	if ev.Allowed && ev.Allow.WireAllowed == 0 {
		t.Fatalf("通道 %s 记为 allowed，但请求日志里没有 allow 记录（%+v）", ev.Channel, ev.Allow)
	}
	if !ev.Blocked && !ev.Allowed {
		t.Fatalf("通道 %s 既没被拦住也没有正向对照，不构成证据（%+v）", ev.Channel, ev)
	}

	path := channelsPath()
	if path == "" {
		return
	}
	channelEvidenceMu.Lock()
	merged := map[string]channelEvidence{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			channelEvidenceMu.Unlock()
			t.Fatalf("已有 channels.json 不是合法 JSON: %v", err)
		}
	}
	merged[ev.Channel] = ev
	channelEvidenceMu.Unlock()

	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 channels.json 失败: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("写 channels.json 失败: %v", err)
	}
}

// ── tree.json（工作区磁盘快照） ────────────────────────────────────────────

var policyTreeMu sync.Mutex

// recordWorkTree 把一次运行的 work 目录磁盘遍历结果并进 tree.json。
func recordWorkTree(t *testing.T, work string, run policyRun) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "tree.json")
	policyTreeMu.Lock()
	merged := map[string][]treeEntry{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			policyTreeMu.Unlock()
			t.Fatalf("已有 tree.json 不是合法 JSON: %v", err)
		}
	}
	key := t.Name() + "/" + run.label
	if run.label == "" {
		key = t.Name()
	}
	merged[key] = run.tree
	policyTreeMu.Unlock()

	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 tree.json 失败: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("写 tree.json 失败: %v", err)
	}
	_ = work
}

// ── 汇总（case 级 facts） ────────────────────────────────────────────────

// TestPolicyChannelObservationSummary 把 channels.json / wire.jsonl / tree.json
// 从磁盘读回来，算出 case 级的观察值。
//
// 它不跑 Pi，也不做断言性判断（真正的断言在各通道用例里）；它只做两件事：
// 从文件派生 facts、并检查"文件之间不打架"。
// 必须排在 policy_integration_test.go 的用例之后执行（同包内按文件名排序）。
func TestPolicyChannelObservationSummary(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		t.Log("未设置 ANVIL_OBSERVATION_DIR：不产出观察值")
		return
	}
	rawChannels, err := os.ReadFile(filepath.Join(dir, "channels.json"))
	if err != nil {
		t.Logf("没有 channels.json（本 case 未跑策略通道用例）：%v", err)
		return
	}
	channels := map[string]channelEvidence{}
	if err := json.Unmarshal(rawChannels, &channels); err != nil {
		t.Fatalf("channels.json 不是合法 JSON: %v", err)
	}
	if len(channels) == 0 {
		t.Fatal("channels.json 里没有任何通道证据")
	}
	wire := readBridgeWire(t, filepath.Join(dir, "wire.jsonl"))
	if len(wire) == 0 {
		t.Fatal("wire.jsonl 里没有任何请求记录：策略端点可能根本没被问到")
	}

	var blocked, allowed []string
	for name, ev := range channels {
		if ev.Blocked {
			blocked = append(blocked, name)
		}
		if ev.Allowed {
			allowed = append(allowed, name)
		}
		// 交叉核对：channels.json 里的结论条数必须能在 wire.jsonl 里找到同样的计数。
		deny, allow := wireDecisions(wire, ev.ToolName)
		if ev.Deny.WireDenied > deny || ev.Allow.WireAllowed > allow {
			t.Fatalf("通道 %s 的证据与请求日志不一致：channels.json(deny=%d allow=%d) > wire.jsonl(deny=%d allow=%d)",
				name, ev.Deny.WireDenied, ev.Allow.WireAllowed, deny, allow)
		}
	}
	sort.Strings(blocked)
	sort.Strings(allowed)

	// 哨兵是否落在盘上：直接看 tree.json（磁盘遍历结果），不看测试变量。
	rawTree, err := os.ReadFile(filepath.Join(dir, "tree.json"))
	if err != nil {
		t.Fatalf("读回 tree.json 失败: %v", err)
	}
	tree := map[string][]treeEntry{}
	if err := json.Unmarshal(rawTree, &tree); err != nil {
		t.Fatalf("tree.json 不是合法 JSON: %v", err)
	}
	sentinels := map[string][]string{}
	for key, entries := range tree {
		var present []string
		for _, e := range entries {
			if e.Size < 0 {
				continue
			}
			base := filepath.ToSlash(filepath.Base(e.Path))
			if strings.HasSuffix(base, "-ran.txt") || base == "edit-target.txt" {
				present = append(present, base)
			}
		}
		sort.Strings(present)
		if len(present) > 0 {
			sentinels[key] = present
		}
	}

	writePiObservation(t, map[string]any{
		"blockedChannels": blocked,
		"allowedChannels": allowed,
		"bridgeRequests":  len(wire),
		"sentinelsOnDisk": sentinels,
		"channels":        channels,
		"recordedBy": "wire.jsonl=策略端点前的记录代理写的服务端请求日志；" +
			"tree.json=运行结束后对工作区的磁盘遍历；channels.json=上面两份文件派生的通道证据",
	}, nil)
}
