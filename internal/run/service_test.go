package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// fakeWorker 是进程内的假 Pi：只负责按脚本吐事件，不实现协议。
//
// 这里测的是 RunService 的状态机与事件编排，不是 Pi 协议——真实协议由
// internal/runtime/pi 的真机集成测试负责。假 worker 让时序可控可复现。
type fakeWorker struct {
	events chan pi.RawEvent
	once   sync.Once

	mu      sync.Mutex
	calls   []json.RawMessage
	closed  bool
	closeEr error
}

func newFakeWorker() *fakeWorker {
	return &fakeWorker{events: make(chan pi.RawEvent, 64)}
}

func (f *fakeWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, command)
	f.mu.Unlock()
	return json.RawMessage(`{"type":"response","command":"prompt","success":true}`), nil
}

func (f *fakeWorker) Events() <-chan pi.RawEvent { return f.events }

func (f *fakeWorker) Close(ctx context.Context) error {
	f.once.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.events)
	})
	return f.closeEr
}

func (f *fakeWorker) PID() int                   { return 4321 }
func (f *fakeWorker) StderrTail() (string, bool) { return "", false }
func (f *fakeWorker) ExitCode() (int, bool)      { return 0, false }
func (f *fakeWorker) Limitations() []string      { return nil }

func (f *fakeWorker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// testOptions 构造一套能通过前置校验的最小选项。
func testOptions(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	ext := filepath.Join(dir, "harness-policy.ts")
	if err := os.WriteFile(ext, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展桩失败: %v", err)
	}
	return Options{
		WorkspaceID:  "ws-test",
		SessionID:    "sess-test",
		WorkspaceDir: dir,
		AgentDir:     filepath.Join(dir, "agent"),
		Installation: pi.PiInstallation{ID: "test:pi", Executable: filepath.Join(dir, "pi.exe")},
		Isolation:    pi.IsolationSpec{PolicyExtension: ext},
		Now:          time.Now,
	}
}

// eventually 轮询直到条件成立或超时。用于等异步启动/收尾。
func eventually(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestStateTableRejectsIllegalTransitions 覆盖状态迁移表本体。
func TestStateTableRejectsIllegalTransitions(t *testing.T) {
	if CanTransition(contracts.RunStateCompleted, contracts.RunStateRunning) {
		t.Fatal("终态 completed 不得迁移回 running")
	}
	if CanTransition(contracts.RunStateQueued, contracts.RunStateCompleted) {
		t.Fatal("queued 不得直接 completed（跳过执行会让 UI 谎报完成）")
	}
	if !CanTransition(contracts.RunStateRunning, contracts.RunStateCompleted) {
		t.Fatal("running → completed 必须是合法边")
	}
	if !CanTransition(contracts.RunStateStopping, contracts.RunStateCancelled) {
		t.Fatal("stopping → cancelled 必须是合法边")
	}
	if IsTerminal(contracts.RunStateRunning) {
		t.Fatal("running 不是终态")
	}
}

// TestThirdRunQueuesAndQueuedCancelNeverStarts 覆盖 RUN03。
func TestThirdRunQueuesAndQueuedCancelNeverStarts(t *testing.T) {
	opts := testOptions(t)
	workers := make([]*fakeWorker, 0, 3)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w := newFakeWorker()
		workers = append(workers, w)
		return w, nil
	}
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	var third contracts.RunSnapshot
	var err error
	if _, err = svc.Start(context.Background(), StartRunRequest{Message: "一"}); err != nil {
		t.Fatalf("第一个 run 启动失败: %v", err)
	}
	if _, err = svc.Start(context.Background(), StartRunRequest{Message: "二"}); err != nil {
		t.Fatalf("第二个 run 启动失败: %v", err)
	}
	if third, err = svc.Start(context.Background(), StartRunRequest{Message: "三"}); err != nil {
		t.Fatalf("第三个 run 应排队而不是报错: %v", err)
	}

	if third.State != contracts.RunStateQueued {
		t.Fatalf("第三个 run 应为 queued，实际 %s", third.State)
	}
	if got := svc.ActiveCount(); got != 2 {
		t.Fatalf("并活跃 run 应为 2，实际 %d", got)
	}
	if got := len(workers); got != 2 {
		t.Fatalf("排队中的 run 不得启动 worker，实际启动了 %d 个", got)
	}

	// 取消排队中的第三个 run：不得因此把它启动起来。
	if err := svc.Abort(context.Background(), third.RunID); err != nil {
		t.Fatalf("取消排队 run 失败: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(workers); got != 2 {
		t.Fatalf("取消排队 run 后不得新增 worker，实际 %d 个", got)
	}
	if snap, _ := svc.Get(third.RunID); snap.State != contracts.RunStateCancelled {
		t.Fatalf("排队 run 取消后状态应为 cancelled，实际 %s", snap.State)
	}
}

// TestSettledCompletesRun 验证只有 agent_settled 才 completed。
func TestSettledCompletesRun(t *testing.T) {
	opts := testOptions(t)
	var w *fakeWorker
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w = newFakeWorker()
		return w, nil
	}
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if snap.State != contracts.RunStateRunning {
		t.Fatalf("prompt 被接受后应为 running，实际 %s", snap.State)
	}

	w.events <- pi.RawEvent{Type: "message_update", Record: json.RawMessage(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"你好"}}`)}
	if !eventually(t, 2*time.Second, func() bool {
		evs, _ := svc.Replay(snap.RunID, 0)
		for _, e := range evs {
			if e.Type == EventMessageDelta {
				return true
			}
		}
		return false
	}) {
		t.Fatal("文本增量应转成 message.delta 事件")
	}

	w.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}
	if !eventually(t, 2*time.Second, func() bool {
		s, _ := svc.Get(snap.RunID)
		return s.State == contracts.RunStateCompleted
	}) {
		s, _ := svc.Get(snap.RunID)
		t.Fatalf("agent_settled 后应为 completed，实际 %s", s.State)
	}
}

// TestStreamCloseWithoutSettledIsInterrupted 验证"事件流提前关闭"不得冒充完成。
func TestStreamCloseWithoutSettledIsInterrupted(t *testing.T) {
	opts := testOptions(t)
	var w *fakeWorker
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w = newFakeWorker()
		return w, nil
	}
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	_ = w.Close(context.Background()) // 事件流关闭，但从未发出 agent_settled

	if !eventually(t, 2*time.Second, func() bool {
		s, _ := svc.Get(snap.RunID)
		return s.State == contracts.RunStateInterrupted
	}) {
		s, _ := svc.Get(snap.RunID)
		t.Fatalf("没有最终 idle 的 run 必须记 interrupted，实际 %s", s.State)
	}
}

// TestReplayReturnsOnlyNewEvents 验证重连补发不重复、不丢。
func TestReplayReturnsOnlyNewEvents(t *testing.T) {
	opts := testOptions(t)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	all, err := svc.Replay(snap.RunID, 0)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("应至少有 run.state 事件")
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("seq 必须单调递增：%d 出现在 %d 之后", all[i].Seq, all[i-1].Seq)
		}
	}
	tail, err := svc.Replay(snap.RunID, all[len(all)-1].Seq)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("afterSeq 之后不应有事件，实际 %d 条", len(tail))
	}
}

// TestAbortReachesVisibleStateQuickly 验证 stop 在 2 秒内可见（RUN04）。
func TestAbortReachesVisibleStateQuickly(t *testing.T) {
	opts := testOptions(t)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}
	svc := New(opts)
	defer func() { _ = svc.Close(context.Background()) }()

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	start := time.Now()
	if err := svc.Abort(context.Background(), snap.RunID); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	var seen time.Duration
	if !eventually(t, 2*time.Second, func() bool {
		s, _ := svc.Get(snap.RunID)
		if s.State == contracts.RunStateStopping || s.State == contracts.RunStateCancelled {
			seen = time.Since(start)
			return true
		}
		return false
	}) {
		t.Fatal("停止后 2 秒内必须可见 stopping/cancelled")
	}
	t.Logf("停止状态可见耗时: %v", seen)
}

// TestStartRejectsIncompleteIsolation 验证缺隔离参数的启动被拒绝（SEC08 的启动侧）。
func TestStartRejectsIncompleteIsolation(t *testing.T) {
	opts := testOptions(t)
	// 故意换成空 Isolation：策略扩展缺失 → 没有执行前授权 → 必须拒绝启动。
	opts.Isolation = pi.IsolationSpec{}
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		t.Fatal("隔离参数不完整时不得启动 worker")
		return nil, nil
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"}); err == nil {
		t.Fatal("隔离参数不完整时必须拒绝启动")
	}
}

// TestSnapshotRecordsModelAndConfigRevision 覆盖 MOD04 的"记录真实
// provider/model 与配置 revision"：快照里必须是这次实际交给 Pi 的那一套，
// 而不是设置里后来被改成的值。
func TestSnapshotRecordsModelAndConfigRevision(t *testing.T) {
	opts := testOptions(t)
	opts.Provider = "demo"
	opts.Model = "demo-model"
	opts.ConfigRevision = 7
	opts.ModelEnv = []string{"MYAGENT_CRED_TEST=secret-value"}

	var gotEnv []string
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		gotEnv = append([]string{}, spec.Env...)
		return newFakeWorker(), nil
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if snap.Provider != "demo" || snap.Model != "demo-model" {
		t.Fatalf("快照未记录实际模型: %+v", snap)
	}
	if snap.ConfigRevision != 7 {
		t.Fatalf("快照应记录配置 revision 7，实际 %d", snap.ConfigRevision)
	}

	// 凭证只经子进程环境交付，不写进 models.json。
	found := false
	for _, kv := range gotEnv {
		if kv == "MYAGENT_CRED_TEST=secret-value" {
			found = true
		}
	}
	if !found {
		t.Fatalf("模型凭证没有通过环境变量传给 worker: %v", gotEnv)
	}
}

// decodeSnapshotJSON 把快照转成"字段名 → 原始 JSON"的表。
//
// 直接断言原始 JSON 而不是结构体：omitempty 的取舍本身就是契约的一部分
// （哪些字段缺失等于"没记录"、哪些必须显式为空数组），只用结构体断言看不到。
func decodeSnapshotJSON(t *testing.T, snap contracts.RunSnapshot) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("快照不能序列化: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("快照不是合法 JSON 对象: %v\n%s", err, raw)
	}
	return doc
}

// TestSnapshotSeparatesManagedSkillsFromPiAutoDiscovery 守 C03 的运行记录口径。
//
// 2026-11-04 起产品不再用 -ns 抑制 Pi 的本地技能发现：Pi 会自己加载
// $HOME/.agents/skills 等位置里它发现的技能，而那份清单只存在于 Pi 进程内。
// 所以快照必须把两件事分开写：
//   - managedSkills：这一轮通过 --skill 显式加载的托管技能（空 = 确实没显式加载）；
//   - skillAutoDiscovery：自动发现开着还是关着（清单未知，产品不能编造）。
//
// 旧实现只有一个 skills 列表，空数组被界面读成"未加载任何技能"——
// 自动发现开着时这是谎话，这条测试就是钉住那个谎话。
func TestSnapshotSeparatesManagedSkillsFromPiAutoDiscovery(t *testing.T) {
	opts := testOptions(t)
	opts.SkillAutoDiscovery = contracts.SkillDiscoveryEnabled
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if len(snap.ManagedSkills) != 0 {
		t.Fatalf("没有显式 --skill 时 managedSkills 必须为空，实际 %v", snap.ManagedSkills)
	}
	if snap.SkillAutoDiscovery != contracts.SkillDiscoveryEnabled {
		t.Fatalf("自动发现开着时必须如实记录 enabled，实际 %q", snap.SkillAutoDiscovery)
	}

	doc := decodeSnapshotJSON(t, snap)
	// 空 managedSkills 是"确实没显式加载"这条事实，不能靠字段缺失表达。
	if got, ok := doc["managedSkills"]; !ok || string(got) != "[]" {
		t.Fatalf("managedSkills 应显式序列化成 []，实际 %s（存在=%v）", got, ok)
	}
	if got := string(doc["skillAutoDiscovery"]); got != `"enabled"` {
		t.Fatalf("skillAutoDiscovery 应在 JSON 里可见，实际 %s", got)
	}
	// 旧字段必须消失：留着它，界面又会把它当"实际加载清单"。
	if got, ok := doc["skills"]; ok {
		t.Fatalf("旧的 skills 字段不应再出现（会被误读成实际加载清单），实际 %s", got)
	}
}

// TestSnapshotRecordsManagedSkillIDs 确认托管显式加载的 ID 照实进快照，
// 而不是被"自动发现开着"盖过去。
func TestSnapshotRecordsManagedSkillIDs(t *testing.T) {
	opts := testOptions(t)
	skillPath := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: demo-skill\ndescription: 快照用\n---\n"), 0o644); err != nil {
		t.Fatalf("写技能桩失败: %v", err)
	}
	opts.Isolation.Skills = []string{skillPath}
	opts.ManagedSkillIDs = []string{"demo-skill"}
	opts.SkillAutoDiscovery = contracts.SkillDiscoveryEnabled
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if len(snap.ManagedSkills) != 1 || snap.ManagedSkills[0] != "demo-skill" {
		t.Fatalf("托管技能 ID 未进快照: %v", snap.ManagedSkills)
	}
	if snap.SkillAutoDiscovery != contracts.SkillDiscoveryEnabled {
		t.Fatalf("自动发现状态丢了: %q", snap.SkillAutoDiscovery)
	}
}

// TestSnapshotLeavesAutoDiscoveryUnknownWhenNotProvided 守"不替调用方猜"：
// 没提供自动发现状态时快照留空（界面显示未知），不能默认成 enabled。
func TestSnapshotLeavesAutoDiscoveryUnknownWhenNotProvided(t *testing.T) {
	opts := testOptions(t)
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newFakeWorker(), nil
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if snap.SkillAutoDiscovery != "" {
		t.Fatalf("调用方没提供时不能编造自动发现状态，实际 %q", snap.SkillAutoDiscovery)
	}
	if _, ok := decodeSnapshotJSON(t, snap)["skillAutoDiscovery"]; ok {
		t.Fatal("未知状态不应出现在 JSON 里：缺失=未知，界面据此显示未知")
	}
}
