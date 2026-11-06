package pi

// F08-B 的契约探测测试（PI_COMP01–04）。
//
// 纪律：
//   - 全部探测都跑在 fakeworker 上，配临时 AgentDir/WorkspaceDir；绝不执行用户真实 Pi，
//     也绝不读写 ~/.pi、.tools/pi。本机 PATH 上的 Pi 是 1.4.0，不是基线 1.5.0。
//   - 每个 case 自己写观察值（契约同 internal/run/observation_test.go：首次清空 → 合并/追加）。
//   - 负例必须真的失败并被**具名**记录，不允许"部分通过就放行"。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// contractInstallation 是被测安装件：fakeworker 二进制 + 一个已探测到的版本。
func contractInstallation(t *testing.T, version string) PiInstallation {
	t.Helper()
	return PiInstallation{
		Executable: fakeWorkerExe(t),
		Version:    version,
		Origin:     OriginExternal,
		Runnable:   true,
	}
}

// contractStarter 返回注入 fakeworker 的 WorkerStarter。
// fakeworker 不是 Pi：它只复现协议时序，因此下面的探测结论都是"对 fakeworker 成立"。
func contractStarter(t *testing.T, mode string, extra ...string) WorkerStarter {
	t.Helper()
	exe := fakeWorkerExe(t)
	return func(ctx context.Context, spec WorkerSpec) (Worker, error) {
		spec.Installation = PiInstallation{Executable: exe, Origin: OriginExternal, Runnable: true}
		spec.Args = append(append([]string{"-mode", mode}, extra...), spec.Args...)
		// fakeworker 退出时不会触发 libuv 断言，无需宽限。
		spec.CloseGrace = -1
		spec.KillTimeout = 3 * time.Second
		spec.CleanupLimit = 5 * time.Second
		return StartWorker(ctx, spec)
	}
}

// contractOptions 构造隔离探测参数。Timeout/CheckTimeout 都调短，负例不会拖慢套件。
func contractOptions(t *testing.T, starter WorkerStarter) ContractOptions {
	t.Helper()
	iso := t.TempDir()
	agentDir := filepath.Join(iso, "agent")
	workspaceDir := filepath.Join(iso, "workspace")
	for _, d := range []string{agentDir, workspaceDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("创建隔离目录 %s: %v", d, err)
		}
	}
	return ContractOptions{
		AgentDir:      agentDir,
		WorkspaceDir:  workspaceDir,
		Timeout:       15 * time.Second,
		CheckTimeout:  4 * time.Second,
		WorkerStarter: starter,
	}
}

func failedCheckNaming(report ContractReport, check string) (string, bool) {
	for _, f := range report.FailedChecks {
		if strings.HasPrefix(f, check+":") {
			return f, true
		}
	}
	return "", false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ── PI_COMP01：合规 worker 全部通过 ─────────────────────────────────────────

func TestContractProbePassesOnCompliantWorker(t *testing.T) {
	inst := contractInstallation(t, "1.5.0")
	report, err := ContractProbe(context.Background(), inst, contractOptions(t, contractStarter(t, "contract")))
	if err != nil {
		t.Fatalf("契约探测不应报错: %v", err)
	}

	want := []string{"rpc", "settled", "mcp", "extension", "abort", "cleanup"}
	if !reflect.DeepEqual(report.Capabilities, want) {
		t.Fatalf("Capabilities = %v，期望 %v（failedChecks=%v）", report.Capabilities, want, report.FailedChecks)
	}
	if len(report.FailedChecks) != 0 {
		t.Fatalf("合规 worker 不应有失败项: %v", report.FailedChecks)
	}
	if report.State != StateReadyCompatible {
		t.Fatalf("六项全通过时状态 = %q，期望 ready_compatible", report.State)
	}
	if report.Version != "1.5.0" {
		t.Fatalf("报告版本 = %q", report.Version)
	}
	if report.Environment != "isolated-agentdir" {
		t.Fatalf("Environment = %q，必须声明在隔离 agentDir 里跑", report.Environment)
	}
	if report.FixtureHash == "" {
		t.Fatal("FixtureHash 不能为空：它要证明 testdata fixture 未漂移")
	}
	if len(report.Observed) == 0 {
		t.Fatal("必须记录实际观察到的能力（mcp/extension 的来源命令）")
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("序列化报告: %v", err)
	}
	writePiObservation(t, map[string]any{
		"state":        string(report.State),
		"checks":       report.Capabilities,
		"failedChecks": nonNil(report.FailedChecks),
		"observed":     report.Observed,
	}, map[string][]byte{"contract-report.json": raw})
}

// PI_COMP01 的版本范围负例：2.x 不进探测。
func TestContractProbeRejectsVersionRangeWithoutProbe(t *testing.T) {
	probed := false
	launcher := writePiStub(t, filepath.Join(t.TempDir(), "pi"), nativePiName(), "stub")
	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("2.0.0", ""),
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			probed = true
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if probed {
		t.Fatal("2.x 候选不得执行契约探测（没有签名清单就谈不上兼容）")
	}
	if got := res.Candidates[0].Compatibility; got != StateIncompatible {
		t.Fatalf("2.x 候选状态 = %q，期望 incompatible", got)
	}
	diag := strings.Join(res.Candidates[0].Diagnostics, "\n")
	if !strings.Contains(diag, "未执行") {
		t.Fatalf("诊断必须说明契约探测未执行，实际 %v", res.Candidates[0].Diagnostics)
	}
	if strings.Contains(diag, "contract-probe-failed") {
		t.Fatalf("未探测不能说成探测失败，实际 %v", res.Candidates[0].Diagnostics)
	}

	// 低版本同理：只会是 upgrade_available，也不跑探测。
	upgrade, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.4.0", ""),
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			probed = true
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if probed {
		t.Fatal("低于最低支持版本时不得执行契约探测")
	}
	if got := upgrade.Candidates[0].Compatibility; got != StateUpgradeAvailable {
		t.Fatalf("1.4.0 状态 = %q，期望 upgrade_available", got)
	}
	if diag := strings.Join(upgrade.Candidates[0].Diagnostics, "\n"); !strings.Contains(diag, "未执行") {
		t.Fatalf("升级提示里也要说明未执行探测，实际 %v", upgrade.Candidates[0].Diagnostics)
	}
}

// ── PI_COMP03：逐项失败的具名结论 ──────────────────────────────────────────

func TestContractProbeFailsOnMalformedSettled(t *testing.T) {
	inst := contractInstallation(t, "1.5.0")
	opts := contractOptions(t, contractStarter(t, "malformed"))
	opts.Timeout = 8 * time.Second
	opts.CheckTimeout = time.Second

	report, err := ContractProbe(context.Background(), inst, opts)
	if err != nil {
		t.Fatalf("探测本身不应报错（失败要写进报告）: %v", err)
	}
	if report.State != StateIncompatible {
		t.Fatalf("settled 不合规时状态 = %q，期望 incompatible", report.State)
	}
	settled, ok := failedCheckNaming(report, "settled")
	if !ok {
		t.Fatalf("FailedChecks 必须具名 settled，实际 %v", report.FailedChecks)
	}
	if !strings.Contains(settled, "timeout") && !strings.Contains(settled, "agent_settled") {
		t.Fatalf("settled 失败项要说明原因（超时或负载），实际 %q", settled)
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("序列化报告: %v", err)
	}
	writePiObservation(t, map[string]any{
		"state":        string(report.State),
		"failedChecks": nonNil(report.FailedChecks),
	}, map[string][]byte{"failure-report.json": raw})
}

func TestContractProbeFailsOnHungAbort(t *testing.T) {
	inst := contractInstallation(t, "1.5.0")
	opts := contractOptions(t, contractStarter(t, "hang"))
	opts.Timeout = 10 * time.Second
	opts.CheckTimeout = time.Second

	report, err := ContractProbe(context.Background(), inst, opts)
	if err != nil {
		t.Fatalf("探测本身不应报错: %v", err)
	}
	if report.State != StateIncompatible {
		t.Fatalf("hang 模式状态 = %q，期望 incompatible", report.State)
	}
	abort, ok := failedCheckNaming(report, "abort")
	if !ok {
		t.Fatalf("FailedChecks 必须具名 abort，实际 %v", report.FailedChecks)
	}
	if !strings.Contains(abort, "timeout") {
		t.Fatalf("abort 失败必须是超时语义，实际 %q", abort)
	}
	// 卡住的 worker 不能因为"其它项失败"就把 cleanup 也算失败：close 仍应能收干净。
	if _, ok := failedCheckNaming(report, "cleanup"); ok {
		t.Fatalf("hang 模式的 cleanup 应当仍能完成，实际 %v", report.FailedChecks)
	}
}

// leakyContractWorker 模拟"Close 没把孙进程收干净"的 worker：
// 返回的错误与真实 worker 的 CodeCleanupFailed 完全一致，因为真实清除路径
// 已由 RPC05（TestWorkerTerminatesGrandchild）覆盖，这里要测的是**判定**。
type leakyContractWorker struct {
	Worker
}

func (l leakyContractWorker) Close(context.Context) error {
	return contracts.NewAppError(contracts.CodeCleanupFailed,
		"worker 清理未完成：清理后仍有 1 个 owned 进程存活（孙进程未被回收）", false)
}

func TestContractProbeFailsOnLeakedGrandchild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "grandchild.hb")
	compliant := contractStarter(t, "contract")
	leaking := contractStarter(t, "grandchild", "-marker", marker)

	var mu sync.Mutex
	var started []Worker
	starter := func(ctx context.Context, spec WorkerSpec) (Worker, error) {
		// cleanup 检查才用 grandchild 模式：它会拉起真的孙进程。
		start := compliant
		if strings.Contains(strings.Join(spec.Env, "\n"), "ANVIL_CONTRACT_CHECK=cleanup") {
			start = leaking
		}
		inner, err := start(ctx, spec)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		started = append(started, inner)
		mu.Unlock()
		return leakyContractWorker{Worker: inner}, nil
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		mu.Lock()
		workers := append([]Worker(nil), started...)
		mu.Unlock()
		for _, w := range workers {
			_ = w.Close(ctx)
		}
	})

	inst := contractInstallation(t, "1.5.0")
	opts := contractOptions(t, starter)
	opts.Timeout = 10 * time.Second
	opts.CheckTimeout = time.Second

	report, err := ContractProbe(context.Background(), inst, opts)
	if err != nil {
		t.Fatalf("探测本身不应报错: %v", err)
	}
	if report.State != StateIncompatible {
		t.Fatalf("孙进程泄漏时状态 = %q，期望 incompatible", report.State)
	}
	cleanup, ok := failedCheckNaming(report, "cleanup")
	if !ok {
		t.Fatalf("FailedChecks 必须具名 cleanup，实际 %v", report.FailedChecks)
	}
	if !strings.Contains(cleanup, "清理") {
		t.Fatalf("cleanup 失败项要说清清理未完成，实际 %q", cleanup)
	}
}

// ── PI_COMP02：注入探测缝的判定 ────────────────────────────────────────────

func TestDiscoverUsesContractProbeForReadyCompatible(t *testing.T) {
	launcher := writePiStub(t, filepath.Join(t.TempDir(), "pi"), nativePiName(), "stub")

	failing, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			return false, []string{"kind:\"contract-probe-failed\" settled: timeout: 未等到 agent_settled"}
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := failing.Candidates[0].Compatibility; got != StateIncompatible {
		t.Fatalf("契约探测失败时状态 = %q，期望 incompatible（不许部分通过就放行）", got)
	}
	if failing.Selected != nil {
		t.Fatalf("探测失败的候选不许被选中：%+v", failing.Selected)
	}
	failDiag := strings.Join(failing.Candidates[0].Diagnostics, "\n")
	for _, want := range []string{"contract-probe-failed", "settled: timeout"} {
		if !strings.Contains(failDiag, want) {
			t.Fatalf("诊断缺少 %q，实际 %v", want, failing.Candidates[0].Diagnostics)
		}
	}

	passing, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			return true, []string{"kind:\"contract-probe-passed\" 隔离契约探测通过"}
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	cand := passing.Candidates[0]
	if cand.Compatibility != StateReadyCompatible {
		t.Fatalf("探测通过但哈希未登记时状态 = %q，期望 ready_compatible（%v）", cand.Compatibility, cand.Diagnostics)
	}
	if diag := strings.Join(cand.Diagnostics, "\n"); !strings.Contains(diag, "unregistered-executable-hash") {
		t.Fatalf("诊断要说明哈希未登记，实际 %v", cand.Diagnostics)
	}
	if passing.Selected == nil || passing.Selected.Installation.ID != cand.Installation.ID {
		t.Fatalf("只读兼容候选应可被选中，实际 %+v", passing.Selected)
	}
}

// TestUnknownPatchBecomesCompatibleOnlyAfterProbe：范围内新补丁版本不因版本号放行，
// 只有隔离契约探测通过后才是 ready_compatible（哈希未登记 → 不能 ready_verified）。
func TestUnknownPatchBecomesCompatibleOnlyAfterProbe(t *testing.T) {
	launcher := writePiStub(t, filepath.Join(t.TempDir(), "pi"), nativePiName(), "stub")
	opts := contractOptions(t, contractStarter(t, "contract"))

	var report ContractReport
	probe := func(ctx context.Context, cand PiCandidate) (bool, []string) {
		rep, err := ContractProbe(ctx, cand.Installation, opts)
		if err != nil {
			t.Errorf("契约探测报错: %v", err)
			return false, []string{err.Error()}
		}
		report = rep
		return rep.State == StateReadyCompatible, rep.FailedChecks
	}

	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.6.2", ""),
		ProbeFunc:    probe,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	cand := res.Candidates[0]
	if cand.Installation.Version != "1.6.2" {
		t.Fatalf("版本 = %q", cand.Installation.Version)
	}
	if cand.Compatibility != StateReadyCompatible {
		t.Fatalf("1.6.2 探测通过后应为 ready_compatible，实际 %q（%v）", cand.Compatibility, cand.Diagnostics)
	}
	if report.State != StateReadyCompatible {
		t.Fatalf("契约报告状态 = %q，期望 ready_compatible（%v）", report.State, report.FailedChecks)
	}
	if diag := strings.Join(cand.Diagnostics, "\n"); !strings.Contains(diag, "uncertified-version") {
		t.Fatalf("诊断要说明版本未认证，实际 %v", cand.Diagnostics)
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("序列化报告: %v", err)
	}
	writePiObservation(t, map[string]any{
		"state":  string(cand.Compatibility),
		"checks": nonNil(report.Capabilities),
	}, map[string][]byte{"contract-report.json": raw})
}

// TestVersionRangeMapsWithoutProbing：范围外版本按 F08-A 的映射走，且**明确没探测**。
func TestVersionRangeMapsWithoutProbing(t *testing.T) {
	launcher := writePiStub(t, filepath.Join(t.TempDir(), "pi"), nativePiName(), "stub")

	cases := []struct {
		version string
		want    CompatibilityState
	}{
		{"2.0.0", StateIncompatible},
		{"1.4.0", StateUpgradeAvailable},
	}
	for _, tc := range cases {
		probed := false
		res, err := Discover(context.Background(), DiscoveryRequest{
			Preferred:    launcher,
			VersionProbe: fixedVersionProbe(tc.version, ""),
			ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
				probed = true
				return true, nil
			},
		})
		if err != nil {
			t.Fatalf("Discover(%s): %v", tc.version, err)
		}
		if probed {
			t.Fatalf("版本 %s 不得执行契约探测", tc.version)
		}
		if got := res.Candidates[0].Compatibility; got != tc.want {
			t.Fatalf("版本 %s → %q，期望 %q", tc.version, got, tc.want)
		}
		diag := strings.Join(res.Candidates[0].Diagnostics, "\n")
		if !strings.Contains(diag, "未执行") {
			t.Fatalf("版本 %s 的诊断要写明未执行探测，实际 %v", tc.version, res.Candidates[0].Diagnostics)
		}
		if strings.Contains(diag, "contract-probe-failed") {
			t.Fatalf("版本 %s 未探测，不能说成探测失败：%v", tc.version, res.Candidates[0].Diagnostics)
		}
	}

	// 该 case 的观察产物：范围外版本的结论报告，Environment 明确写 not-probed。
	report := ContractReport{
		Version:      "2.0.0",
		State:        StateIncompatible,
		Capabilities: []string{},
		FixtureHash:  protocolFixtureHash("2.0.0"),
		Environment:  "not-probed-version-out-of-range",
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("序列化报告: %v", err)
	}
	writePiObservation(t, map[string]any{
		"state":  string(report.State),
		"checks": report.Capabilities,
		"probed": false,
	}, map[string][]byte{"contract-report.json": raw})
}
