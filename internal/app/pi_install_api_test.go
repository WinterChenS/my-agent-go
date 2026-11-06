package app

// F06 绑定层测试：InstallPi / CancelPiInstall / RollbackPi。
//
// 这里守的是"绑定层不是转发器"的四件事：
//  1. UI 只能给版本，不能给 URL/路径——版本字段塞地址必须直接失败，一个请求都不发；
//  2. 进度事件只带阶段，权威状态只从返回值读；
//  3. 安装只写 MYAGENT_PI_HOME 指向的托管根，不碰 PATH、不改 settings.json；
//  4. 回滚只认当前 active 的 installationID，未知 ID 报 NOT_FOUND。
//
// 载荷用**本测试二进制**冒充 pi 可执行体：F06 只要求"解出来的包里有目标平台的
// 真实可执行格式"，而探针是注入的（app 层测的是接线，不是 Pi 契约）。

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/runtime/pi"
)

// appPiExeName 返回宿主目标平台的 Pi 可执行体名。
func appPiExeName(t *testing.T) string {
	t.Helper()
	name, err := piinstall.ExpectedExecutable(pi.HostTarget())
	if err != nil {
		t.Fatalf("宿主目标 %q 不在六目标内: %v", pi.HostTarget(), err)
	}
	return name
}

// appPiPackage 用本测试二进制拼一个最小发行包（zip）。
func appPiPackage(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径失败: %v", err)
	}
	body, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("读测试二进制失败: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: appPiExeName(t), Method: zip.Deflate})
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("写 zip 条目失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// appVersionFromDir 从托管版本目录名（<version>-<target>）解析版本。
func appVersionFromDir(dir string) (string, bool) {
	for _, target := range piinstall.TargetIDs {
		if strings.HasSuffix(dir, "-"+target) {
			v := strings.TrimSuffix(dir, "-"+target)
			if v != "" {
				return v, true
			}
		}
	}
	return "", false
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recordingProbe 记录 probe 调用次数（PI_INS01 要求校验失败时一次都不调）。
type recordingProbe struct {
	mu    sync.Mutex
	calls int
	ok    bool
}

func (p *recordingProbe) fn() pi.ProbeFunc {
	return func(context.Context, pi.PiCandidate) (bool, []string) {
		p.mu.Lock()
		p.calls++
		ok := p.ok
		p.mu.Unlock()
		if ok {
			return true, []string{"kind:\"contract-probe-passed\" 注入的 probe 通过"}
		}
		return false, []string{"kind:\"contract-probe-failed\" 注入的 probe 失败"}
	}
}

func (p *recordingProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// piInstallHarness 是 app 层的安装装置：本地 TLS 服务器 + 注入清单/探针。
type piInstallHarness struct {
	d          *Desktop
	managed    string
	srv        *httptest.Server
	probe      *recordingProbe
	mu         sync.Mutex
	entries    []piinstall.ManifestEntry
	bodies     map[string][]byte
	requests   int
	events     []contracts.Event
	eventsLock sync.Mutex
}

func newPiInstallHarness(t *testing.T) *piInstallHarness {
	t.Helper()
	h := &piInstallHarness{probe: &recordingProbe{ok: true}, bodies: map[string][]byte{}}
	h.managed = t.TempDir()

	h.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.requests++
		var body []byte
		for _, e := range h.entries {
			if "/"+e.AssetName == r.URL.Path {
				body = h.bodies[e.AssetName]
			}
		}
		h.mu.Unlock()
		if body == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(h.srv.Close)

	d, _ := newTestDesktop(t)
	// newTestDesktop 会把托管根隔离到它自己的临时目录；这里改成本装置的实际安装根，
	// 让 pi.ManagedRoot() 与安装服务用的目录是同一个（F06b 的发现要读它）。
	t.Setenv("MYAGENT_PI_HOME", h.managed)
	h.d = d
	d.piInstallOverride = func(svc *piinstall.InstallService) {
		h.mu.Lock()
		svc.Manifest = piinstall.Manifest{
			Schema:      piinstall.ManifestSchema,
			GeneratedAt: "2026-11-05T00:00:00Z",
			Source:      "https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",
			Entries:     append([]piinstall.ManifestEntry{}, h.entries...),
		}
		h.mu.Unlock()
		svc.ManagedRoot = h.managed
		svc.HTTPClient = h.srv.Client()
		svc.HostAllowlist = []string{h.srv.Listener.Addr().String()}
		svc.Probe = h.probe.fn()
		svc.VersionProbe = func(_ context.Context, inst pi.PiInstallation) (string, []string) {
			if v, ok := appVersionFromDir(filepath.Base(filepath.Dir(inst.Executable))); ok {
				return v, nil
			}
			return "", []string{"kind:\"version-unreadable\" 目录名不含版本"}
		}
		svc.Now = func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }
	}
	d.SetEmitter(func(ev contracts.Event) {
		h.eventsLock.Lock()
		h.events = append(h.events, ev)
		h.eventsLock.Unlock()
	})
	return h
}

// addPackage 为某版本注册一个可下载的包（哈希/大小按真实字节算）。
func (h *piInstallHarness) addPackage(t *testing.T, version string) {
	t.Helper()
	body := appPiPackage(t)
	asset := "pi-" + version + "-" + pi.HostTarget() + ".zip"
	h.mu.Lock()
	h.bodies[asset] = body
	h.entries = append(h.entries, piinstall.ManifestEntry{
		Version:   version,
		TargetID:  pi.HostTarget(),
		AssetName: asset,
		URL:       h.srv.URL + "/" + asset,
		SizeBytes: int64(len(body)),
		SHA256:    sha256Hex(body),
	})
	h.mu.Unlock()
}

func (h *piInstallHarness) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

func (h *piInstallHarness) stageSequence(t *testing.T) []string {
	t.Helper()
	h.eventsLock.Lock()
	defer h.eventsLock.Unlock()
	out := make([]string, 0, len(h.events))
	for _, ev := range h.events {
		if ev.Type != PiInstallEventName {
			t.Fatalf("安装事件类型 = %q，期望 %q", ev.Type, PiInstallEventName)
		}
		var payload piInstallProgressEvent
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("事件 payload 解析失败: %v", err)
		}
		// 事件里不允许出现状态结论：权威状态只从返回值读。
		var raw map[string]any
		if err := json.Unmarshal(ev.Payload, &raw); err != nil {
			t.Fatalf("事件 payload 解析失败: %v", err)
		}
		if _, bad := raw["state"]; bad {
			t.Fatalf("进度事件不允许携带 state: %s", string(ev.Payload))
		}
		out = append(out, payload.Stage)
	}
	return out
}

// ── 用例 ────────────────────────────────────────────────────────────────────

// TestInstallPiWritesOnlyManagedRootAndEmitsStageOnlyEvents 覆盖 PI_INS02 的
// 绑定层一半：装成功、状态来自返回值、事件只有阶段、PATH 与设置文件不变。
func TestInstallPiWritesOnlyManagedRootAndEmitsStageOnlyEvents(t *testing.T) {
	h := newPiInstallHarness(t)
	h.addPackage(t, "1.5.0")

	settingsPath := filepath.Join(h.d.root, "settings.json")
	settingsBefore, _ := os.ReadFile(settingsPath)
	pathBefore := os.Getenv("PATH")

	res := h.d.InstallPi("1.5.0")
	if res.Error != "" {
		t.Fatalf("安装失败: %s（阶段=%s）", res.Error, res.FailedStage)
	}
	if res.State != string(pi.StateReadyCompatible) {
		t.Fatalf("state = %q，期望 %q", res.State, pi.StateReadyCompatible)
	}
	if res.ManagedRoot != h.managed {
		t.Fatalf("结果里的托管根 = %q，期望 %q（证据必须对得上）", res.ManagedRoot, h.managed)
	}
	if !strings.HasPrefix(res.Executable, h.managed) {
		t.Fatalf("安装不在托管根内: %s", res.Executable)
	}
	if res.InstallationID == "" {
		t.Fatal("结果必须带 installationID（UI 回滚要用）")
	}
	if res.EventName != PiInstallEventName {
		t.Fatalf("EventName = %q，期望 %q", res.EventName, PiInstallEventName)
	}

	// 事件：五个阶段按序，且只带阶段。
	got := h.stageSequence(t)
	want := []string{"download", "verify", "extract", "probe", "activate"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("阶段序列 = %v，期望 %v", got, want)
	}

	// 只写托管根：PATH 与 settings.json 都不许变。
	if after := os.Getenv("PATH"); after != pathBefore {
		t.Fatal("安装改变了 PATH")
	}
	settingsAfter, _ := os.ReadFile(settingsPath)
	if !bytes.Equal(settingsBefore, settingsAfter) {
		t.Fatalf("安装改动了 settings.json:\nbefore=%s\nafter=%s", settingsBefore, settingsAfter)
	}
}

// TestInstallPiRejectsURLOrPathInVersionField 守"UI 只能给版本"。
//
// 版本字段塞地址/路径必须在校验闸门直接失败：不发任何 HTTP 请求、不调用 probe、
// 不创建版本目录。这条一旦失守，受信清单就等于被绕过了。
func TestInstallPiRejectsURLOrPathInVersionField(t *testing.T) {
	h := newPiInstallHarness(t)
	h.addPackage(t, "1.5.0")

	for _, bad := range []string{
		"https://evil.example/pi-1.5.0.zip",
		"http://127.0.0.1:1/pi.zip",
		`..\..\Windows\System32\cmd.exe`,
		"/etc/passwd",
		"file:///C:/pi.zip",
	} {
		before := h.requestCount()
		res := h.d.InstallPi(bad)
		if res.State != string(pi.StateFailed) {
			t.Fatalf("版本字段 %q 必须失败，实际 state=%q", bad, res.State)
		}
		if res.Error == "" {
			t.Fatalf("版本字段 %q 的失败必须带错误说明", bad)
		}
		if res.Executable != "" {
			t.Fatalf("版本字段 %q 失败后不该有可执行体: %s", bad, res.Executable)
		}
		if got := h.requestCount(); got != before {
			t.Fatalf("版本字段 %q 触发了 %d 次下载请求（必须是 0）", bad, got-before)
		}
		if got := h.probe.count(); got != 0 {
			t.Fatalf("版本字段 %q 触发了 %d 次 probe（必须是 0）", bad, got)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(h.managed, "versions")); err == nil && len(entries) != 0 {
		t.Fatalf("非法版本不应产生版本目录: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(h.managed, "active.json")); err == nil {
		t.Fatal("非法版本不应写 active.json")
	}
}

// TestInstallPiThenRollbackPi 覆盖 PI_UPG02 的绑定层一半：
// 升级后 rollbackAvailable=true，RollbackPi 用 installationID 回到旧版；
// 未知 installationID 报 NOT_FOUND；取消幂等。
func TestInstallPiThenRollbackPi(t *testing.T) {
	h := newPiInstallHarness(t)
	h.addPackage(t, "1.5.0")
	if res := h.d.InstallPi("1.5.0"); res.Error != "" {
		t.Fatalf("装 v1 失败: %s", res.Error)
	}
	// 服务是单例，新版本条目要重新登记进同一个清单（真实产品里清单是固定的，
	// 这里只是让"再装一个版本"可测）。
	h.addPackage(t, "1.6.0")
	h.d.piInstallMu.Lock()
	h.d.piInstallSvc = nil // 重建服务以加载新条目（等价于清单里本来就有两个版本）
	h.d.piInstallMu.Unlock()

	up := h.d.InstallPi("1.6.0")
	if up.Error != "" {
		t.Fatalf("升级失败: %s", up.Error)
	}
	if up.State != string(pi.StateRollbackAvailable) || !up.RollbackAvailable {
		t.Fatalf("升级后应报告 rollback_available: %+v", up)
	}
	if up.PreviousVersion != "1.5.0" {
		t.Fatalf("previousVersion = %q，期望 1.5.0", up.PreviousVersion)
	}

	// 未知 ID：结构化 NOT_FOUND，不静默回滚。
	bad := h.d.RollbackPi("pi-does-not-exist")
	if bad.State != string(pi.StateFailed) {
		t.Fatalf("未知 installationID 的回滚必须失败: %+v", bad)
	}
	if !strings.Contains(bad.Error, contracts.CodeNotFound) {
		t.Fatalf("未知 installationID 的错误码应含 %q，实际 %q", contracts.CodeNotFound, bad.Error)
	}

	rb := h.d.RollbackPi(up.InstallationID)
	if rb.Error != "" {
		t.Fatalf("回滚失败: %s", rb.Error)
	}
	if rb.Version != "1.5.0" {
		t.Fatalf("回滚后版本 = %q，期望 1.5.0", rb.Version)
	}
	// 幂等：再点一次不报错、不改变状态。
	rb2 := h.d.RollbackPi(rb.InstallationID)
	if rb2.Error != "" || rb2.Version != "1.5.0" || rb2.RollbackAvailable {
		t.Fatalf("重复回滚必须幂等: %+v", rb2)
	}

	// 取消是幂等的。
	if err := h.d.CancelPiInstall("pii-unknown"); err != nil {
		t.Fatalf("取消一个不在跑的安装必须幂等（不报错）: %v", err)
	}
}
