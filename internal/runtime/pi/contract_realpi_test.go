package pi

// F08-D：契约探测对**真实 Pi** 的端到端验证。
//
// 为什么必须有这个用例：PI_COMP01–04 之前的证据全部来自 fakeworker。fakeworker 复现的是
// 协议时序，不是 Pi 本身——探测没带 `--mode rpc` 这件事它看不出来，因为它自己解析的是
// `-mode`。于是"探测能跑"只在 fixture 上成立，真实 Pi 反而被判 incompatible。
//
// 纪律：
//   - 真实二进制只读引用（.tools/pi/standalone/<target>/pi.exe），绝不写入；
//   - AgentDir / workspace / provider 全部在 t.TempDir() 下，模型只指向本地 fixture 端点；
//   - 缺包不是 skip，而是带修复步骤的失败。走 integration runner 时由 case 级
//     Blocked 拦在执行之前（见 internal/devtools/integration.go），不会用 fixture 冒充。
//   - 断言只有一条：报告里六项检查全过。任何一项失败都算失败，没有"容忍超时"分支。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/fixtures/openaisse"
)

// realPiStandaloneTarget 把宿主 GOOS/GOARCH 映射成 .tools/pi/standalone 的目录后缀。
func realPiStandaloneTarget() string {
	goos := runtime.GOOS
	switch goos {
	case "windows":
		goos = "win"
	case "darwin":
		goos = "mac"
	}
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x64"
	}
	return goos + "-" + arch
}

// realPiStandalonePath 返回本机官方独立包路径，并给出缺失时的修复步骤。
func realPiStandalonePath(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	dir := "v1.5.0-" + realPiStandaloneTarget()
	name := "pi"
	if runtime.GOOS == "windows" {
		name = "pi.exe"
	}
	exe := filepath.Join(root, ".tools", "pi", "standalone", dir, name)
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("本机没有官方独立包（%s）。修复：下载官方 Pi 1.5.0 独立包并解包到 "+
			".tools/pi/standalone/%s/（只读引用，不覆盖任何外部 Pi），然后重跑 "+
			"scripts/devtools.ps1 verify --suite=integration --target=%s --gate=G0 --case=PI_COMP01。"+
			"注意：这条 case 在缺包时不是 skip，也不能用 fakeworker 冒充真实 Pi。",
			exe, dir, realPiStandaloneTarget())
	}
	return exe
}

// realPiVersionLine 在隔离 env 下运行真实二进制的 --version，返回原始输出行与解析出的版本。
//
// 隔离方式与 Probe 一致（HOME/USERPROFILE/APPDATA/PI_CODING_AGENT_DIR 全部重定向），
// 但这里必须拿到**原始输出**：用例要证明"状态机用的版本号来自二进制自己"。
func realPiVersionLine(t *testing.T, exe string) (string, string) {
	t.Helper()
	iso := t.TempDir()
	cmd := exec.Command(exe, "--version")
	cmd.Env = overrideEnv(os.Environ(),
		"PI_CODING_AGENT_DIR", iso,
		"HOME", iso,
		"USERPROFILE", iso,
		"APPDATA", iso,
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("真实 Pi 的 --version 执行失败: %v", err)
	}
	line := firstLine(string(out))
	version := parseVersion(string(out))
	if version == "" {
		t.Fatalf("无法从 --version 输出解析版本：%q", string(out))
	}
	return line, version
}

func hashRealPi(t *testing.T, path string) string {
	t.Helper()
	// 复用产品里的流式哈希（lifecycle.go）：128MB 的独立包不读进内存。
	sum, err := sha256File(path)
	if err != nil {
		t.Fatalf("计算 %s 的 sha256 失败: %v", path, err)
	}
	return "sha256:" + sum
}

// contractSlowWriter 在每个 flush 前等待固定时长，把 fixture provider 的流拉长。
//
// 为什么需要：abort 检查要求 abort 在回合**进行中**发出。openaisse 是即时应答的
// 回放端点，回合几十毫秒就结束，abort 只能落在回合之后——那样测出来的是
// "abort 来晚了"，不是"abort 能不能打断回合"。真实模型回合以秒计，所以这里把
// fixture 拉长到同一量级，让 abort 检查测的是它本来要测的东西。
type contractSlowWriter struct {
	http.ResponseWriter
	delay time.Duration
}

func (s *contractSlowWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		time.Sleep(s.delay)
		f.Flush()
	}
}

// TestContractProbeAgainstRealPi 用真实 Pi 1.5.0 跑完整契约探测。
func TestContractProbeAgainstRealPi(t *testing.T) {
	exe := realPiStandalonePath(t)

	// 二进制自报版本必须与状态机用的版本一致：状态机不认识 1.5.0 就不会进入探测，
	// 那时"探测通过"也就无从谈起。
	versionLine, version := realPiVersionLine(t, exe)
	if state, diag := classifyVersion(DefaultPolicy, version); state != StateReady {
		t.Fatalf("真实二进制自报版本 %q（%q）不在认证版本里，状态机会在探测之前拒绝它: %s",
			version, versionLine, strings.Join(diag, "; "))
	}

	work := t.TempDir()
	agentDir := filepath.Join(work, "agent")
	workspaceDir := filepath.Join(work, "workspace")
	for _, dir := range []string{agentDir, workspaceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建隔离目录 %s: %v", dir, err)
		}
	}

	// 受控 provider：只在本地回环上，请求体落进临时目录，供事后核对 Pi 真的发过请求。
	recordDir := filepath.Join(work, "provider-requests")
	provider := openaisse.NewHandler(recordDir)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.ServeHTTP(&contractSlowWriter{ResponseWriter: w, delay: 250 * time.Millisecond}, r)
	}))
	defer srv.Close()
	writeFixtureModels(t, agentDir, srv.URL)

	inst := PiInstallation{
		Executable:   exe,
		Version:      version,
		Origin:       OriginExternal,
		Architecture: runtime.GOARCH,
		Runnable:     true,
		Source:       SourceUserSpecified,
	}

	started := time.Now()
	report, err := ContractProbe(context.Background(), inst, ContractOptions{
		AgentDir:     agentDir,
		WorkspaceDir: workspaceDir,
		// 真实进程每次启动 + 一个慢速回合，六项检查留足余量；
		// 余量不是放行分支：任何一项超时仍然失败。
		Timeout:      3 * time.Minute,
		CheckTimeout: 20 * time.Second,
		Provider:     "fixture",
		Model:        "fixture-model",
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("探测本身不应报错（检查失败要写进报告）: %v", err)
	}
	t.Logf("真实 Pi 契约探测耗时 %v；capabilities=%v；observed=%v；failedChecks=%v",
		elapsed.Round(time.Millisecond), report.Capabilities, report.Observed, report.FailedChecks)

	if len(report.FailedChecks) != 0 {
		// 失败时把观察到的来源与失败项一起打出来：这条 case 的价值就在于
		// "真实二进制到底哪一项不行"，只报 incompatible 没法定位。
		raw, _ := json.MarshalIndent(report, "", "  ")
		t.Fatalf("真实 Pi 契约探测未通过: %v\n报告：\n%s", report.FailedChecks, raw)
	}
	if report.State != StateReadyCompatible {
		t.Fatalf("六项检查全过时状态 = %q，期望 ready_compatible", report.State)
	}
	if report.Version != version {
		t.Fatalf("报告版本 = %q，与二进制自报版本 %q 不一致", report.Version, version)
	}
	if report.Environment != contractProbeEnvironment {
		t.Fatalf("Environment = %q，必须声明在隔离 agentDir 里跑", report.Environment)
	}
	if report.FixtureHash == "" {
		t.Fatal("FixtureHash 不能为空：它要证明 testdata fixture 未漂移")
	}
	want := []string{"rpc", "settled", "mcp", "extension", "abort", "cleanup"}
	if strings.Join(report.Capabilities, ",") != strings.Join(want, ",") {
		t.Fatalf("Capabilities = %v，期望 %v", report.Capabilities, want)
	}
	// 扩展面必须由探测自己加载的扩展证明，而不是碰巧命中了宿主上的用户级技能。
	extensionEvidence := ""
	for _, o := range report.Observed {
		if strings.HasPrefix(o, "extension:") {
			extensionEvidence = o
		}
	}
	if !strings.Contains(extensionEvidence, contractProbeCommand) {
		t.Fatalf("扩展面证据 = %q，必须来自探测自己加载的 %q（不能靠宿主用户级技能凑）",
			extensionEvidence, contractProbeCommand)
	}

	// 隔离性：Pi 的配置与会话都必须落在临时 agentDir 里，不能出现在别处。
	if _, statErr := os.Stat(filepath.Join(agentDir, contractProbeExtensionDir, contractProbeExtensionName)); statErr != nil {
		t.Fatalf("隔离 agentDir 里没有探测扩展，说明探测没用这份配置: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(agentDir, "models.json")); statErr != nil {
		t.Fatalf("隔离 agentDir 里没有 models.json: %v", statErr)
	}
	// 会话也必须落在隔离 agentDir 下（--session-dir）：否则 Pi 会写进用户自己的
	// ~/.pi/agent/sessions，那就不是"探测"而是"跑了一次真实会话"。
	sessions, err := os.ReadDir(filepath.Join(agentDir, "sessions"))
	if err != nil {
		t.Fatalf("隔离 agentDir 下没有 sessions 目录（--session-dir 没生效）: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("隔离 sessions 目录为空：探测回合的会话没有落在隔离 agentDir 里")
	}

	// provider 侧的外部观察：Pi 真的发过请求（否则 settled 是通过假的）。
	requests, _ := filepath.Glob(filepath.Join(recordDir, "request-*.json"))
	if len(requests) == 0 {
		t.Fatalf("fixture provider 没有收到任何请求（%s 为空）：settled 不可能是真的", recordDir)
	}

	binaryInfo, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("stat 真实 Pi 失败: %v", err)
	}
	binaryFacts := map[string]any{
		"executable":   exe,
		"sha256":       hashRealPi(t, exe),
		"sizeBytes":    binaryInfo.Size(),
		"versionLine":  versionLine,
		"versionUsed":  version,
		"target":       realPiStandaloneTarget(),
		"distribution": "standalone",
	}
	reportRaw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("序列化报告: %v", err)
	}
	binaryRaw, err := json.MarshalIndent(binaryFacts, "", "  ")
	if err != nil {
		t.Fatalf("序列化二进制信息: %v", err)
	}

	writePiObservation(t, map[string]any{
		"realPi": map[string]any{
			"executable":  exe,
			"sha256":      binaryFacts["sha256"],
			"versionLine": versionLine,
			"versionUsed": version,
		},
		// 探测结论单独命名：observation.json 是同一个 case 里多条用例合并写的，
		// 用 state/checks/observed 这种通用键会和 fakeworker 那条互相覆盖，
		// 合并之后就说不清"这份结论到底来自真实 Pi 还是 fixture"。
		"realPiContract": map[string]any{
			"state":            string(report.State),
			"checks":           report.Capabilities,
			"failedChecks":     nonNil(report.FailedChecks),
			"observed":         report.Observed,
			"provider":         "local fixture provider（openaisse，慢速流）",
			"providerRequests": len(requests),
			"durationMillis":   elapsed.Milliseconds(),
			"agentDir":         agentDir,
			"workspaceDir":     workspaceDir,
		},
	}, map[string][]byte{
		// 与 fixture 证据分开命名：两条用例写在同一个证据目录里，同名会互相覆盖，
		// 于是"这份报告到底是真实 Pi 还是 fakeworker 的"就说不清了。
		"contract-report-real-pi.json": reportRaw,
		"pi-binary.json":               binaryRaw,
	})

	fmt.Printf("real-pi contract report: state=%s checks=%v duration=%v\n",
		report.State, report.Capabilities, elapsed.Round(time.Millisecond))
}
