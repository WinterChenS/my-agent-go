package run

// C06a：一个应用级 admission queue + per-run 身份与策略。
//
// 这一组测试只关心"提交者之间的边界"：身份/策略是不是每个 run 自己的、全局是不是
// 只有一个队列、排队与取消有没有偷偷启动 worker。状态机与事件顺序由既有测试守。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// startRunWith 用一条显式请求启动 run（per-run 身份由请求携带）。
func startRunWith(t *testing.T, svc *Service, req StartRunRequest) contracts.RunSnapshot {
	t.Helper()
	snap, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("启动 run 失败: %v", err)
	}
	return snap
}

// envValue 从形如 NAME=value 的列表里取一个值。
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"="), true
		}
	}
	return "", false
}

// queuedPosition 从事件日志里读出这个 run 上报过的排队位次（最后一次为准）。
//
// 位次必须来自事件：这是前端唯一能看到的队列事实，服务内部字段不算证据。
func queuedPosition(t *testing.T, svc *Service, id contracts.RunID) (int, bool) {
	t.Helper()
	pos, ok := 0, false
	for _, ev := range eventsOfType(t, svc, id, EventRunState) {
		var p statePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 run.state 载荷失败: %v", err)
		}
		if p.State == contracts.RunStateQueued && p.QueuePosition > 0 {
			pos, ok = p.QueuePosition, true
		}
	}
	return pos, ok
}

// TestGlobalAdmissionTwoActiveOneQueued 覆盖 C06a 的全局 admission：
// 两个交互会话 + 一个调度风格请求共用同一个队列，第 3 个只能排队。
func TestGlobalAdmissionTwoActiveOneQueued(t *testing.T) {
	opts := testOptions(t)
	var mu sync.Mutex
	var workers []*fakeWorker
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w := newFakeWorker()
		mu.Lock()
		workers = append(workers, w)
		mu.Unlock()
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	first := startRunWith(t, svc, StartRunRequest{
		Message:      "交互会话一",
		SessionID:    "sess-interactive-a",
		WorkspaceID:  "ws-a",
		WorkspaceDir: t.TempDir(),
	})
	second := startRunWith(t, svc, StartRunRequest{
		Message:      "交互会话二",
		SessionID:    "sess-interactive-b",
		WorkspaceID:  "ws-b",
		WorkspaceDir: t.TempDir(),
	})
	third := startRunWith(t, svc, StartRunRequest{
		Message:      "调度提交的自动化任务",
		SessionID:    "sess-scheduled",
		WorkspaceID:  "ws-automation",
		WorkspaceDir: t.TempDir(),
		ProfileID:    "profile-automation",
	})

	if first.State != contracts.RunStateRunning || second.State != contracts.RunStateRunning {
		t.Fatalf("前两个应进入 running，实际 %s / %s", first.State, second.State)
	}
	if third.State != contracts.RunStateQueued {
		t.Fatalf("第三个必须在同一个队列里排队，实际 %s", third.State)
	}
	if got := svc.ActiveCount(); got != 2 {
		t.Fatalf("全局活跃数必须固定为 2，实际 %d", got)
	}
	if got := svc.QueuedCount(); got != 1 {
		t.Fatalf("排队数应为 1，实际 %d", got)
	}
	mu.Lock()
	started := len(workers)
	mu.Unlock()
	if started != 2 {
		t.Fatalf("排队中的 run 不得启动 worker，实际启动了 %d 个", started)
	}
	if pos, ok := queuedPosition(t, svc, third.RunID); !ok || pos != 1 {
		t.Fatalf("排队 run 必须上报 queued + 位次 1，实际 pos=%d ok=%v", pos, ok)
	}
	// 每个 run 的身份是它自己的，不是队列头的。
	if first.SessionID != "sess-interactive-a" || second.SessionID != "sess-interactive-b" ||
		third.SessionID != "sess-scheduled" {
		t.Fatalf("快照的 session 身份没有按请求走: %s / %s / %s",
			first.SessionID, second.SessionID, third.SessionID)
	}

	// 容量只在有 run 结算时变化：第一个结算后，排队的第三个才启动。
	mu.Lock()
	head := workers[0]
	mu.Unlock()
	head.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

	if !eventually(t, 3*time.Second, func() bool {
		s, err := svc.Get(first.RunID)
		return err == nil && s.State == contracts.RunStateCompleted
	}) {
		s, _ := svc.Get(first.RunID)
		t.Fatalf("第一个 run 结算后应为 completed，实际 %s", s.State)
	}
	if !eventually(t, 3*time.Second, func() bool {
		s, err := svc.Get(third.RunID)
		return err == nil && s.State == contracts.RunStateRunning
	}) {
		s, _ := svc.Get(third.RunID)
		t.Fatalf("空出槽位后排队 run 应启动，实际 %s", s.State)
	}
	mu.Lock()
	started = len(workers)
	mu.Unlock()
	if started != 3 {
		t.Fatalf("第三个 run 启动后 worker 数应为 3，实际 %d", started)
	}
	if got := svc.ActiveCount(); got != 2 {
		t.Fatalf("结算后活跃数仍必须是 2，实际 %d", got)
	}
	if got := svc.QueuedCount(); got != 0 {
		t.Fatalf("结算后队列应为空，实际 %d", got)
	}
}

// buildFakeWorkerAs 编译一份名字唯一的 fakeworker。
//
// 映像名唯一是为了能从**操作系统进程表**里只数本用例的进程：同包里其它用例、
// 以及别的包也在编 fakeworker，撞名会让"有没有多出一个进程"这个问题没有答案。
func buildFakeWorkerAs(t *testing.T, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "my-agent-go/internal/runtime/pi/fakeworker")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译 fakeworker 失败: %v\n%s", err, b)
	}
	return out
}

// readChildRecord 读子进程自己写下的观察文件（父进程只读，不代写）。
func readChildRecord(t *testing.T, path string) []byte {
	t.Helper()
	var raw []byte
	if !eventually(t, 15*time.Second, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		raw = b
		return true
	}) {
		t.Fatalf("等不到子进程写出的观察文件 %s", path)
	}
	return raw
}

// snapshotB 是 run B 自己显式给的策略快照（等于上限）。
var snapshotB = PolicySnapshot{
	Permissions: config.Permissions{Filesystem: "workspace", Shell: true},
	Revision:    11,
}

// TestPerRunBindingDoesNotLeakAcrossRuns 覆盖 per-run 身份与策略：
// 两个 run 用不同 workspace/profile/policy snapshot，各自 worker 收到的身份与策略
// 必须是它自己那一份。断言全部读子进程写下的 argv/env，不读服务内部字段。
func TestPerRunBindingDoesNotLeakAcrossRuns(t *testing.T) {
	exe := buildFakeWorkerAs(t, "fakeworker-binding")
	recDir := t.TempDir()
	dirA, dirB := t.TempDir(), t.TempDir()

	ceiling := PolicyCeiling{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: true, Network: false},
		Revision:    11,
		Ref:         "ceiling:workspace",
	}
	narrow := PolicySnapshot{
		Permissions: config.Permissions{Filesystem: "read"},
		Revision:    3,
	}

	opts := testOptions(t)
	opts.Installation = pi.PiInstallation{ID: "fixture:fakeworker", Executable: exe}
	opts.Provider = "fixture"
	opts.Model = "fixture-model"
	opts.PolicyCeiling = ceiling
	opts.PolicySnapshot = PolicySnapshot{Permissions: ceiling.Permissions, Revision: ceiling.Revision}
	opts.PermissionResolver = stubPermissionResolver{refs: map[string]PolicySnapshot{"policy:readonly@3": narrow}}

	var (
		specMu sync.Mutex
		specs  []pi.WorkerSpec
	)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		sessionID, _ := envValue(spec.Env, EnvSessionID)
		spec.Env = append(spec.Env,
			"FAKEWORKER_RECORD_ARGV="+filepath.Join(recDir, sessionID+"-argv.json"),
			"FAKEWORKER_RECORD_ENV="+filepath.Join(recDir, sessionID+"-env.json"),
		)
		specMu.Lock()
		specs = append(specs, spec)
		specMu.Unlock()
		return pi.StartWorker(ctx, spec)
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	runA := startRunWith(t, svc, StartRunRequest{
		Message:       "run A",
		SessionID:     "sess-a",
		WorkspaceID:   "ws-a",
		WorkspaceDir:  dirA,
		ProfileID:     "prof-a",
		PermissionRef: "policy:readonly@3",
	})
	bindingB := snapshotB
	runB := startRunWith(t, svc, StartRunRequest{
		Message:        "run B",
		SessionID:      "sess-b",
		WorkspaceID:    "ws-b",
		WorkspaceDir:   dirB,
		ProfileID:      "prof-b",
		PolicySnapshot: &bindingB,
	})

	specMu.Lock()
	gotSpecs := append([]pi.WorkerSpec{}, specs...)
	specMu.Unlock()
	if len(gotSpecs) != 2 {
		t.Fatalf("两个 run 应各启动一个 worker，实际 %d 个", len(gotSpecs))
	}
	// worker 的工作目录就是这次 run 自己的工作区，不是服务上的某个默认值。
	if gotSpecs[0].WorkingDir != dirA || gotSpecs[1].WorkingDir != dirB {
		t.Fatalf("worker 工作目录不是 per-run 的: %q / %q", gotSpecs[0].WorkingDir, gotSpecs[1].WorkingDir)
	}

	envA := readChildEnv(t, recDir, "sess-a")
	envB := readChildEnv(t, recDir, "sess-b")
	argvA := readChildArgv(t, recDir, "sess-a")
	argvB := readChildArgv(t, recDir, "sess-b")

	assertChildEnv(t, "run A", envA, map[string]string{
		EnvRunID:          string(runA.RunID),
		EnvSessionID:      "sess-a",
		EnvWorkspaceID:    "ws-a",
		EnvWorkspaceDir:   dirA,
		EnvProfileID:      "prof-a",
		EnvPermissionRef:  "policy:readonly@3",
		EnvPolicySnapshot: mustPolicyJSON(t, narrow),
	})
	assertChildEnv(t, "run B", envB, map[string]string{
		EnvRunID:          string(runB.RunID),
		EnvSessionID:      "sess-b",
		EnvWorkspaceID:    "ws-b",
		EnvWorkspaceDir:   dirB,
		EnvProfileID:      "prof-b",
		EnvPermissionRef:  "",
		EnvPolicySnapshot: mustPolicyJSON(t, snapshotB),
	})

	// 交叉污染检查：A 的进程里不能出现 B 的任何身份事实（反之亦然）。
	if got, _ := envValue(envA, EnvSessionID); got == "sess-b" {
		t.Fatalf("run A 的子进程带上了 run B 的 session: %q", got)
	}
	if got, _ := envValue(envA, EnvWorkspaceID); got == "ws-b" {
		t.Fatalf("run A 的子进程带上了 run B 的 workspace: %q", got)
	}
	if got, _ := envValue(envA, EnvProfileID); got == "prof-b" {
		t.Fatalf("run A 的子进程带上了 run B 的 profile: %q", got)
	}
	if got, _ := envValue(envA, EnvPolicySnapshot); got == mustPolicyJSON(t, snapshotB) {
		t.Fatalf("run A 的子进程带上了 run B 的策略快照: %q", got)
	}
	for key, bad := range map[string]string{
		EnvSessionID:   "sess-a",
		EnvWorkspaceID: "ws-a",
		EnvProfileID:   "prof-a",
	} {
		if got, _ := envValue(envB, key); got == bad {
			t.Fatalf("run B 的子进程带上了 run A 的 %s=%q", key, got)
		}
	}

	// argv：两个 run 共用同一个 Pi 会话根目录（身份走环境变量，不靠改 Pi 的会话布局），
	// 且 argv 里不能出现另一个 run 的身份。
	wantSessionDir := filepath.Join(opts.AgentDir, "sessions")
	for _, argv := range [][]string{argvA, argvB} {
		if !containsArgPair(argv, "--session-dir", wantSessionDir) {
			t.Fatalf("argv 里的 --session-dir 不是 %q: %v", wantSessionDir, argv)
		}
		if !containsArg(argv, "--mode") {
			t.Fatalf("argv 缺少 --mode rpc: %v", argv)
		}
	}
	for _, leaked := range []string{"sess-b", "ws-b", "prof-b"} {
		if strings.Contains(strings.Join(argvA, " "), leaked) {
			t.Fatalf("run A 的 argv 泄漏了 run B 的身份 %q: %v", leaked, argvA)
		}
	}
	for _, leaked := range []string{"sess-a", "ws-a", "prof-a"} {
		if strings.Contains(strings.Join(argvB, " "), leaked) {
			t.Fatalf("run B 的 argv 泄漏了 run A 的身份 %q: %v", leaked, argvB)
		}
	}
}

// stubPermissionResolver 把 permissionRef 解析成测试给的快照。
type stubPermissionResolver struct {
	refs map[string]PolicySnapshot
}

func (s stubPermissionResolver) ResolvePermission(ref string) (PolicySnapshot, error) {
	if snap, ok := s.refs[ref]; ok {
		return snap, nil
	}
	return PolicySnapshot{}, contracts.NewAppError(contracts.CodeNotFound, "未知的 permissionRef: "+ref, false)
}

func mustPolicyJSON(t *testing.T, snap PolicySnapshot) string {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("序列化策略快照失败: %v", err)
	}
	return string(raw)
}

func readChildEnv(t *testing.T, dir, session string) []string {
	t.Helper()
	raw := readChildRecord(t, filepath.Join(dir, session+"-env.json"))
	var env []string
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("子进程写下的 env 不是合法 JSON: %v", err)
	}
	return env
}

func readChildArgv(t *testing.T, dir, session string) []string {
	t.Helper()
	raw := readChildRecord(t, filepath.Join(dir, session+"-argv.json"))
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("子进程写下的 argv 不是合法 JSON: %v", err)
	}
	return argv
}

func assertChildEnv(t *testing.T, label string, env []string, want map[string]string) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		got, ok := envValue(env, k)
		if !ok {
			t.Fatalf("%s 的子进程环境里没有 %s", label, k)
		}
		if got != want[k] {
			t.Fatalf("%s 的 %s=%q，应为 %q", label, k, got, want[k])
		}
	}
}

func containsArg(argv []string, name string) bool {
	for _, a := range argv {
		if a == name {
			return true
		}
	}
	return false
}

func containsArgPair(argv []string, name, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == name && argv[i+1] == value {
			return true
		}
	}
	return false
}

// imageFileName 是某个 fakeworker 构建在进程表里的映像名。
func imageFileName(buildName string, platform string) string {
	if platform == "windows" {
		return buildName + ".exe"
	}
	return buildName
}

// processPIDsByImage 从**操作系统进程表**里数出映像名匹配的进程。
//
// 不用本进程的记账：排队取消之后"没启动"这件事，只有进程表能回答。
func processPIDsByImage(t *testing.T, image string) []int {
	t.Helper()
	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf(`@(Get-CimInstance Win32_Process -Filter "Name='%s'" | Select-Object -ExpandProperty ProcessId) -join ","`, image)
		out, err = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	} else {
		out, err = exec.Command("ps", "-eo", "pid=,comm=").Output()
	}
	if err != nil {
		detail := string(out)
		if ee, ok := err.(*exec.ExitError); ok {
			detail += string(ee.Stderr)
		}
		t.Fatalf("读取 OS 进程表失败: %v\n%s", err, detail)
	}
	var pids []int
	if runtime.GOOS == "windows" {
		for _, field := range strings.Split(strings.TrimSpace(string(out)), ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			pid, convErr := strconv.Atoi(field)
			if convErr != nil {
				t.Fatalf("进程表里的 pid 不是整数: %q", field)
			}
			pids = append(pids, pid)
		}
		return pids
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if filepath.Base(fields[1]) != image {
			continue
		}
		pid, convErr := strconv.Atoi(fields[0])
		if convErr != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// TestQueuedAbortNeverStartsWorker 覆盖"取消排队 run 不得启动 worker"，
// 观察点为 OS 进程表：排队中取消之后，进程表里不能多出这个 run 的进程。
func TestQueuedAbortNeverStartsWorker(t *testing.T) {
	const buildName = "fakeworker-queued-abort"
	exe := buildFakeWorkerAs(t, buildName)
	image := imageFileName(buildName, runtime.GOOS)
	recDir := t.TempDir()

	opts := testOptions(t)
	opts.Installation = pi.PiInstallation{ID: "fixture:fakeworker", Executable: exe}
	opts.Provider = "fixture"
	opts.Model = "fixture-model"

	var (
		specMu sync.Mutex
		specs  []pi.WorkerSpec
	)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		sessionID, _ := envValue(spec.Env, EnvSessionID)
		spec.Env = append(spec.Env,
			"FAKEWORKER_RECORD_ARGV="+filepath.Join(recDir, sessionID+"-argv.json"),
			"FAKEWORKER_RECORD_ENV="+filepath.Join(recDir, sessionID+"-env.json"),
		)
		specMu.Lock()
		specs = append(specs, spec)
		specMu.Unlock()
		return pi.StartWorker(ctx, spec)
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	first := startRunWith(t, svc, StartRunRequest{
		Message: "一", SessionID: "sess-one", WorkspaceID: "ws-one", WorkspaceDir: t.TempDir(),
	})
	second := startRunWith(t, svc, StartRunRequest{
		Message: "二", SessionID: "sess-two", WorkspaceID: "ws-two", WorkspaceDir: t.TempDir(),
	})
	third := startRunWith(t, svc, StartRunRequest{
		Message: "三", SessionID: "sess-three", WorkspaceID: "ws-three", WorkspaceDir: t.TempDir(),
	})
	if first.State != contracts.RunStateRunning || second.State != contracts.RunStateRunning {
		t.Fatalf("前两个应为 running，实际 %s / %s", first.State, second.State)
	}
	if third.State != contracts.RunStateQueued {
		t.Fatalf("第三个应排队，实际 %s", third.State)
	}
	var runningPIDs []int
	if !eventually(t, 30*time.Second, func() bool {
		runningPIDs = processPIDsByImage(t, image)
		return len(runningPIDs) == 2
	}) {
		t.Fatalf("运行中的两个 run 应在 OS 进程表里各有一个进程，实际 %d 个: %v",
			len(runningPIDs), runningPIDs)
	}

	if err := svc.Abort(context.Background(), third.RunID); err != nil {
		t.Fatalf("取消排队 run 失败: %v", err)
	}
	if snap, err := svc.Get(third.RunID); err != nil || snap.State != contracts.RunStateCancelled {
		t.Fatalf("取消后应为 cancelled，实际 %v / %v", snap.State, err)
	}

	// 排队中取消：worker 从未为它构造过。
	specMu.Lock()
	startedSpecs := append([]pi.WorkerSpec{}, specs...)
	specMu.Unlock()
	if len(startedSpecs) != 2 {
		t.Fatalf("取消排队 run 不得启动 worker，实际构造了 %d 个", len(startedSpecs))
	}
	for _, spec := range startedSpecs {
		if id, _ := envValue(spec.Env, EnvSessionID); id == "sess-three" {
			t.Fatalf("取消的排队 run 竟然拿到了 worker spec")
		}
	}
	// 子进程自己也没为它留下任何痕迹（没有进程 = 没有记录文件）。
	if _, err := os.Stat(filepath.Join(recDir, "sess-three-argv.json")); err == nil {
		t.Fatal("取消的排队 run 竟然有子进程写下了 argv")
	}

	// 空出槽位也不会把它拉起来：队列里已经没它了。
	if err := svc.Abort(context.Background(), first.RunID); err != nil {
		t.Fatalf("停止第一个 run 失败: %v", err)
	}
	if !eventually(t, 20*time.Second, func() bool {
		s, err := svc.Get(first.RunID)
		return err == nil && s.State == contracts.RunStateCancelled
	}) {
		s, _ := svc.Get(first.RunID)
		t.Fatalf("第一个 run 未按预期停止，实际 %s", s.State)
	}
	time.Sleep(300 * time.Millisecond)
	specMu.Lock()
	afterSettle := len(specs)
	specMu.Unlock()
	if afterSettle != 2 {
		t.Fatalf("取消的排队 run 在槽位空出后被启动了：构造 %d 个 spec", afterSettle)
	}
	if snap, _ := svc.Get(third.RunID); snap.State != contracts.RunStateCancelled {
		t.Fatalf("取消的 run 状态被改写成了 %s", snap.State)
	}
	// 进程表里仍然只有那两个（第一个已停，剩第二个）。
	if pids := processPIDsByImage(t, image); len(pids) > 1 {
		t.Fatalf("进程表里多于一个残留进程: %v", pids)
	}
}

// TestWiderPolicySnapshotRejected 覆盖"permissionRef/snapshot 只能收紧，不能扩大"。
func TestWiderPolicySnapshotRejected(t *testing.T) {
	opts := testOptions(t)
	opts.PolicyCeiling = PolicyCeiling{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: false, Network: false},
		Revision:    5,
		Ref:         "ceiling:workspace",
	}
	opts.PolicySnapshot = PolicySnapshot{Permissions: opts.PolicyCeiling.Permissions, Revision: 5}
	opts.PermissionResolver = stubPermissionResolver{refs: map[string]PolicySnapshot{
		"policy:unrestricted": {Permissions: config.Permissions{Filesystem: "unrestricted"}},
		"policy:readonly":     {Permissions: config.Permissions{Filesystem: "read"}},
	}}
	var starts sync.Mutex
	workerStarts := 0
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		starts.Lock()
		workerStarts++
		starts.Unlock()
		return newFakeWorker(), nil
	}

	var (
		evMu  sync.Mutex
		types []string
	)
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	unsub := svc.Subscribe(func(ev contracts.Event) {
		evMu.Lock()
		types = append(types, ev.Type)
		evMu.Unlock()
	})
	t.Cleanup(unsub)

	cases := []struct {
		name      string
		req       StartRunRequest
		wantCode  string
		wantInMsg string
	}{
		{
			name: "filesystem 超过上限",
			req: StartRunRequest{Message: "越权", SessionID: "sess-wide", WorkspaceID: "ws-1",
				WorkspaceDir: t.TempDir(),
				PolicySnapshot: &PolicySnapshot{
					Permissions: config.Permissions{Filesystem: "unrestricted"},
				}},
			wantCode:  contracts.CodeUnauthorized,
			wantInMsg: "unrestricted",
		},
		{
			name: "shell 超过上限",
			req: StartRunRequest{Message: "越权", SessionID: "sess-wide", WorkspaceID: "ws-1",
				WorkspaceDir: t.TempDir(),
				PolicySnapshot: &PolicySnapshot{
					Permissions: config.Permissions{Filesystem: "workspace", Shell: true},
				}},
			wantCode:  contracts.CodeUnauthorized,
			wantInMsg: "shell",
		},
		{
			name: "permissionRef 解析成更宽的快照",
			req: StartRunRequest{Message: "越权", SessionID: "sess-wide", WorkspaceID: "ws-1",
				WorkspaceDir: t.TempDir(), PermissionRef: "policy:unrestricted"},
			wantCode:  contracts.CodeUnauthorized,
			wantInMsg: "unrestricted",
		},
		{
			name: "未知的 permissionRef",
			req: StartRunRequest{Message: "越权", SessionID: "sess-wide", WorkspaceID: "ws-1",
				WorkspaceDir: t.TempDir(), PermissionRef: "policy:does-not-exist"},
			wantCode:  contracts.CodeNotFound,
			wantInMsg: "policy:does-not-exist",
		},
		{
			name: "无法识别的权限等级",
			req: StartRunRequest{Message: "越权", SessionID: "sess-wide", WorkspaceID: "ws-1",
				WorkspaceDir: t.TempDir(),
				PolicySnapshot: &PolicySnapshot{
					Permissions: config.Permissions{Filesystem: "everything"},
				}},
			wantCode:  contracts.CodeUnsupported,
			wantInMsg: "everything",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Start(context.Background(), tc.req)
			if err == nil {
				t.Fatal("越权请求必须返回结构化错误，不能静默收窄后照跑")
			}
			appErr, ok := err.(*contracts.AppError)
			if !ok {
				t.Fatalf("应为 *contracts.AppError，实际 %T: %v", err, err)
			}
			if appErr.Code != tc.wantCode {
				t.Fatalf("错误码应为 %s，实际 %s（%s）", tc.wantCode, appErr.Code, appErr.Message)
			}
			if !strings.Contains(appErr.Message, tc.wantInMsg) {
				t.Fatalf("错误信息应说明哪一项越权（含 %q），实际 %q", tc.wantInMsg, appErr.Message)
			}
			if got := svc.ActiveCount(); got != 0 {
				t.Fatalf("越权请求不得占用槽位，实际活跃 %d", got)
			}
			if got := svc.QueuedCount(); got != 0 {
				t.Fatalf("越权请求不得进入队列，实际排队 %d", got)
			}
		})
	}

	starts.Lock()
	started := workerStarts
	starts.Unlock()
	if started != 0 {
		t.Fatalf("越权请求不得启动 worker，实际启动 %d 个", started)
	}
	// 从未有 run 被创建 → 不可能有审批或授权记录。
	evMu.Lock()
	for _, typ := range types {
		if typ == EventApprovalRequest || typ == EventApprovalResolved {
			evMu.Unlock()
			t.Fatalf("越权请求不得创建审批/授权，却出现了事件 %s", typ)
		}
	}
	evMu.Unlock()

	// 反过来确认收窄是允许的（不是把所有请求都拒掉）。
	okSvc := New(opts)
	t.Cleanup(func() { _ = okSvc.Close(context.Background()) })
	narrowSnap := PolicySnapshot{Permissions: config.Permissions{Filesystem: "read"}, Revision: 1}
	if _, err := okSvc.Start(context.Background(), StartRunRequest{
		Message: "只读", SessionID: "sess-narrow", WorkspaceID: "ws-1",
		WorkspaceDir: t.TempDir(), PermissionRef: "policy:readonly",
		PolicySnapshot: &narrowSnap,
	}); err != nil {
		t.Fatalf("不超过上限的快照必须被接受: %v", err)
	}
}

// TestLegacyStartStillWorks 保证旧入口（只给 message）仍然是一条队列里的 run：
// 身份取自服务默认值（Desktop 的设置），不是"另开一个队列"。
func TestLegacyStartStillWorks(t *testing.T) {
	opts := testOptions(t)
	opts.ConfigRevision = 9
	var specMu sync.Mutex
	var gotSpecs []pi.WorkerSpec
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		specMu.Lock()
		gotSpecs = append(gotSpecs, spec)
		specMu.Unlock()
		return newFakeWorker(), nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "老入口"})
	if err != nil {
		t.Fatalf("兼容入口失败: %v", err)
	}
	if snap.SessionID != opts.SessionID || snap.WorkspaceID != opts.WorkspaceID {
		t.Fatalf("兼容入口应使用服务的默认身份，实际 %s / %s", snap.SessionID, snap.WorkspaceID)
	}
	if snap.ConfigRevision != 9 {
		t.Fatalf("兼容入口应使用服务的默认配置 revision，实际 %d", snap.ConfigRevision)
	}
	if snap.State != contracts.RunStateRunning {
		t.Fatalf("兼容入口应进入 running，实际 %s", snap.State)
	}
	if got := svc.ActiveCount(); got != 1 {
		t.Fatalf("兼容入口只应有 1 个活跃 run，实际 %d", got)
	}
	specMu.Lock()
	firstSpec := gotSpecs[0]
	specMu.Unlock()
	if firstSpec.WorkingDir != opts.WorkspaceDir {
		t.Fatalf("兼容入口的 worker 工作目录应为服务默认值 %q，实际 %q", opts.WorkspaceDir, firstSpec.WorkingDir)
	}
	if got, _ := envValue(firstSpec.Env, EnvSessionID); got != string(opts.SessionID) {
		t.Fatalf("worker 环境里的 session 身份应为 %q，实际 %q", opts.SessionID, got)
	}
	if got, _ := envValue(firstSpec.Env, EnvRunID); got != string(snap.RunID) {
		t.Fatalf("worker 环境里的 run 身份应为 %q，实际 %q", snap.RunID, got)
	}
	// 同一实例继续接别的提交者，不会因为"已经绑了 session"而拒绝。
	second, err := svc.Start(context.Background(), StartRunRequest{
		Message: "另一个会话", SessionID: "sess-other", WorkspaceID: "ws-other",
		WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("同一个服务必须服务多个会话: %v", err)
	}
	if second.SessionID != "sess-other" {
		t.Fatalf("第二个提交者的身份没有生效: %s", second.SessionID)
	}
	if got := svc.ActiveCount(); got != 2 {
		t.Fatalf("两个会话应同时活跃，实际 %d", got)
	}
}
