package piinstall

// 安装事务的测试装置：TLS fixture server + 清单 + 可记录的 probe + 固定时间。
//
// 这里刻意不 mock 服务本体：下载、校验、解压、激活、指针替换都走真实代码路径，
// 只有"包从哪来"（本地 TLS 服务器）和"probe 怎么判"（注入）是测试替身。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/runtime/pi"
)

// recordingProbe 记录 probe 调用次数与被执行体路径，并按注入结果作答。
type recordingProbe struct {
	mu      sync.Mutex
	calls   int
	paths   []string
	verdict bool
	failed  []string
}

func (p *recordingProbe) fn() pi.ProbeFunc {
	return func(ctx context.Context, cand pi.PiCandidate) (bool, []string) {
		p.mu.Lock()
		p.calls++
		p.paths = append(p.paths, cand.Installation.Executable)
		verdict, failed := p.verdict, p.failed
		p.mu.Unlock()
		return verdict, failed
	}
}

func (p *recordingProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *recordingProbe) recordedPaths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.paths...)
}

func (p *recordingProbe) set(verdict bool, failed ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verdict, p.failed = verdict, failed
}

// harness 是一条 case 的安装环境。
type harness struct {
	t    *testing.T
	Root string

	srv    *httptest.Server
	mu     sync.Mutex
	bodies map[string][]byte
	redir  map[string]string
	status map[string]int
	slow   map[string]chan struct{}

	entries  []ManifestEntry
	probe    *recordingProbe
	versionP pi.VersionProbe
	svc      *InstallService
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		Root:     t.TempDir(),
		bodies:   map[string][]byte{},
		redir:    map[string]string{},
		status:   map[string]int{},
		slow:     map[string]chan struct{}{},
		probe:    &recordingProbe{verdict: true},
		versionP: managedVersionProbe,
		now:      time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}
	h.srv = httptest.NewTLSServer(http.HandlerFunc(h.serveHTTP))
	t.Cleanup(h.srv.Close)
	h.rebuild()
	return h
}

func (h *harness) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	body, hasBody := h.bodies[r.URL.Path]
	target, isRedir := h.redir[r.URL.Path]
	status, hasStatus := h.status[r.URL.Path]
	gate, isSlow := h.slow[r.URL.Path]
	h.mu.Unlock()

	if isRedir {
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if hasStatus {
		http.Error(w, "fixture status", status)
		return
	}
	if !hasBody {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	half := len(body) / 2
	_, _ = w.Write(body[:half])
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if isSlow {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	_, _ = w.Write(body[half:])
}

func (h *harness) allowlist() []string {
	return []string{strings.TrimPrefix(h.srv.URL, "https://")}
}

// rebuild 用当前条目重建清单并更新服务的可注入字段。
func (h *harness) rebuild() {
	h.mu.Lock()
	entries := append([]ManifestEntry{}, h.entries...)
	h.mu.Unlock()
	manifest := Manifest{
		Schema:      ManifestSchema,
		GeneratedAt: "2026-11-05T00:00:00Z",
		Source:      "https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",
		Entries:     entries,
	}
	if h.svc == nil {
		h.svc = &InstallService{
			ManagedRoot:   h.Root,
			HTTPClient:    h.srv.Client(),
			Probe:         h.probe.fn(),
			VersionProbe:  h.versionP,
			Now:           func() time.Time { return h.now },
			HostAllowlist: h.allowlist(),
		}
	}
	h.svc.Manifest = manifest
	h.svc.ManagedRoot = h.Root
	h.svc.HTTPClient = h.srv.Client()
	h.svc.Probe = h.probe.fn()
	h.svc.VersionProbe = h.versionP
	h.svc.HostAllowlist = h.allowlist()
}

// defaultArchive 返回一个"合法"的安装包：顶层含目标可执行体。
func (h *harness) defaultArchive(target string) []byte {
	name := expectedExecutableFor(target)
	if strings.HasPrefix(target, "win-") {
		// 用真实可执行文件（fakeworker）当载荷：PI_UPG01 要真的起进程。
		return zipBytes(h.t, map[string][]byte{name: fakeWorkerBytes(h.t)})
	}
	return zipBytes(h.t, map[string][]byte{name: []byte("#!/bin/sh\nexit 0\n")})
}

// addPackage 注册一个可用版本并返回它的清单条目。
func (h *harness) addPackage(version, target string) ManifestEntry {
	h.t.Helper()
	return h.addPackageBody(version, target, h.defaultArchive(target))
}

// addPackageBody 注册一个指定载荷的版本。
func (h *harness) addPackageBody(version, target string, body []byte) ManifestEntry {
	h.t.Helper()
	asset := fmt.Sprintf("pi-%s-%s.zip", version, target)
	h.mu.Lock()
	h.bodies["/"+asset] = body
	h.mu.Unlock()
	entry := entryFor(version, target, asset, h.srv.URL+"/"+asset, body)
	h.entries = append(h.entries, entry)
	h.rebuild()
	return entry
}

// replacePackageBody 换掉某版本的包体，并**同步**清单条目的哈希与大小。
//
// 用途：让"下载 + 校验"通过、失败发生在**解压**阶段（越界条目/错误架构）。
// 用 addPackageBody 会追加一条重复条目，服务取到的是旧条目、服务器发的却是新
// 字节——那测出来的其实是哈希不符，不是归档边界。
func (h *harness) replacePackageBody(version, target string, body []byte) ManifestEntry {
	h.t.Helper()
	asset := fmt.Sprintf("pi-%s-%s.zip", version, target)
	h.mu.Lock()
	h.bodies["/"+asset] = body
	h.mu.Unlock()
	sum := sha256HexOf(body)
	return h.setEntry(version, target, func(e *ManifestEntry) {
		e.SizeBytes = int64(len(body))
		e.SHA256 = sum
	})
}

// setEntry 替换指定 (version,target) 的条目（用于构造非法清单条目）。
func (h *harness) setEntry(version, target string, mutate func(*ManifestEntry)) ManifestEntry {
	h.t.Helper()
	for i := range h.entries {
		if h.entries[i].Version == version && h.entries[i].TargetID == target {
			mutate(&h.entries[i])
			entry := h.entries[i]
			h.rebuild()
			return entry
		}
	}
	h.t.Fatalf("清单里没有 %s/%s", version, target)
	return ManifestEntry{}
}

// install 用一条新的 stageRecorder 跑一次安装。
func (h *harness) install(version, target string) (Result, error, *stageRecorder) {
	h.t.Helper()
	rec := &stageRecorder{}
	res, err := h.svc.Install(context.Background(), InstallRequest{Version: version, TargetID: target}, rec.progress())
	return res, err, rec
}

// mustInstall 要求安装成功。
func (h *harness) mustInstall(version, target string) Result {
	h.t.Helper()
	res, err, rec := h.install(version, target)
	if err != nil {
		h.t.Fatalf("安装 %s/%s 失败: %v（阶段=%v）", version, target, err, rec.list())
	}
	return res
}

// active 读回 active 指针（不存在时返回 nil）。
func (h *harness) active() *ActivePointer {
	h.t.Helper()
	p, err := ReadActive(h.Root)
	if err != nil {
		h.t.Fatalf("读 active 指针失败: %v", err)
	}
	return p
}

// discoverManaged 用 F08 Discover 扫托管版本目录（与产品同一套发现逻辑）。
func (h *harness) discoverManaged() pi.DiscoveryResult {
	h.t.Helper()
	return h.discoverManagedWith("")
}

// discoverManagedWith 以 preferred（应用传 active.json 的可执行体）扫托管根。
//
// F08 的托管候选按目录名排序、firstUsable 取第一个可用，所以"哪个版本是当前版本"
// 只能靠 Preferred 表达——产品路径必须传（见 InstallService.discover）。
func (h *harness) discoverManagedWith(preferred string) pi.DiscoveryResult {
	h.t.Helper()
	res, err := pi.Discover(context.Background(), pi.DiscoveryRequest{
		Preferred:    preferred,
		ManagedRoot:  filepath.Join(h.Root, versionsDirName),
		Policy:       pi.DefaultPolicy,
		VersionProbe: h.versionP,
		ProbeFunc:    h.probe.fn(),
	})
	if err != nil {
		h.t.Fatalf("Discover 失败: %v", err)
	}
	return res
}

// managedVersionProbe 从托管版本目录名解析版本（<version>-<target>）。
//
// 测试里不执行真实 --version：载荷是 fakeworker，不是 Pi。生产默认走 pi.Probe。
func managedVersionProbe(ctx context.Context, inst pi.PiInstallation) (string, []string) {
	dir := filepath.Base(filepath.Dir(inst.Executable))
	if v, ok := splitVersionDir(dir); ok {
		return v, nil
	}
	return "", []string{"kind:\"version-probe-failed\" 托管目录名不含版本: " + dir}
}

// processImagePath 从 OS 侧读出某个 pid 的可执行文件路径（外部观察）。
func processImagePath(t *testing.T, pid int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf("(Get-CimInstance Win32_Process -Filter \"ProcessId=%d\").ExecutablePath", pid))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("查询进程 %d 的可执行路径失败: %v", pid, err)
		}
		return strings.TrimSpace(string(out))
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("查询进程 %d 的可执行路径失败: %v", pid, err)
	}
	return strings.TrimSpace(string(out))
}

// startLongLived 从指定可执行体起一个长驻进程（模拟"活跃 worker 持有的可执行路径"）。
func startLongLived(t *testing.T, exe string, workdir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Dir = workdir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("创建 stdin 失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动长驻进程失败: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// writeExternalPi 在托管根之外造一个"外部 Pi"（只读引用，不允许被改写）。
func writeExternalPi(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "external-pi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建外部 Pi 目录失败: %v", err)
	}
	p := filepath.Join(dir, expectedExecutableFor(pi.HostTarget()))
	if err := os.WriteFile(p, []byte("external-pi-stub"), 0o755); err != nil {
		t.Fatalf("写外部 Pi 失败: %v", err)
	}
	return p, sha256HexOf([]byte("external-pi-stub"))
}
