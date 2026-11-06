package app

// C06a 的绑定层口径：整个应用进程只有一个 admission queue。
//
// 这里不重复 run 包的队列语义（那是 internal/run 的测试），只钉住绑定层的一件事：
// Desktop 不会为第二个提交者再建一个带独立 MaxActive 的 RunService，
// 也不会把某个会话的身份写死在服务上。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
)

// c06aWorker 是可控的替身 worker：由测试决定这一轮什么时候结算。
type c06aWorker struct {
	events chan pi.RawEvent
	once   sync.Once
}

func newC06aWorker() *c06aWorker { return &c06aWorker{events: make(chan pi.RawEvent, 8)} }

func (w *c06aWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"type":"response","success":true}`), nil
}
func (w *c06aWorker) Events() <-chan pi.RawEvent { return w.events }
func (w *c06aWorker) Close(ctx context.Context) error {
	w.once.Do(func() { close(w.events) })
	return nil
}
func (w *c06aWorker) PID() int                   { return 0 }
func (w *c06aWorker) StderrTail() (string, bool) { return "", false }
func (w *c06aWorker) ExitCode() (int, bool)      { return 0, false }
func (w *c06aWorker) Limitations() []string      { return nil }

// c06aEnvValue 从 NAME=value 列表里取值。
func c06aEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"="), true
		}
	}
	return "", false
}

// TestDesktopServesEverySubmitterThroughOneAdmissionQueue 守 C06a 的装配约束：
//   - 三次提交（两个交互 + 一个手动"调度风格"调用）全部落到同一个 RunService；
//   - 第 3 个在同一个队列里排队，位置可观察；
//   - 每个 run 的身份来自 Desktop 的设置，而不是服务上写死的会话。
func TestDesktopServesEverySubmitterThroughOneAdmissionQueue(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace

	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}

	var (
		mu      sync.Mutex
		specs   []pi.WorkerSpec
		workers []*c06aWorker
	)
	d.mu.Lock()
	d.workerStarter = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w := newC06aWorker()
		mu.Lock()
		specs = append(specs, spec)
		workers = append(workers, w)
		mu.Unlock()
		return w, nil
	}
	d.mu.Unlock()

	first, err := d.Start("第一条")
	if err != nil {
		t.Fatalf("第一次提交失败: %v", err)
	}
	svc := mustRuntime(t, d)
	second, err := d.Start("第二条")
	if err != nil {
		t.Fatalf("第二次提交失败: %v", err)
	}
	third, err := d.Start("第三条")
	if err != nil {
		t.Fatalf("第三次提交失败: %v", err)
	}

	if first.State != contracts.RunStateRunning || second.State != contracts.RunStateRunning {
		t.Fatalf("前两个应 running，实际 %s / %s", first.State, second.State)
	}
	if third.State != contracts.RunStateQueued {
		t.Fatalf("第三个应排队，实际 %s", third.State)
	}
	if got := svc.ActiveCount(); got != 2 {
		t.Fatalf("全局活跃上限必须是 2，实际 %d", got)
	}
	if got := svc.QueuedCount(); got != 1 {
		t.Fatalf("排队数应为 1，实际 %d", got)
	}

	// 唯一装配：再要一次运行时，拿到的还是同一个对象，不是"每个会话一个服务"。
	again, err := d.ensureRuntime()
	if err != nil {
		t.Fatalf("再次构建运行时失败: %v", err)
	}
	if again != svc {
		t.Fatal("ensureRuntime 为后续提交者新建了第二个 RunService（于是有了第二个 MaxActive）")
	}
	d.mu.Lock()
	servedBy := d.svc
	d.mu.Unlock()
	if servedBy != svc {
		t.Fatal("Desktop 持有的 RunService 被换成了另一个队列")
	}

	// 身份来自 Desktop 的设置：工作区是登记过的那一个，身份经环境变量交给子进程。
	if first.WorkspaceID != ws.Item.ID {
		t.Fatalf("run 的 workspace 应是登记过的工作区 %q，实际 %q", ws.Item.ID, first.WorkspaceID)
	}
	if strings.TrimSpace(string(first.SessionID)) == "" {
		t.Fatal("run 必须带上 session 身份")
	}
	mu.Lock()
	firstSpec := specs[0]
	mu.Unlock()
	if got, ok := c06aEnvValue(firstSpec.Env, run.EnvSessionID); !ok || got != string(first.SessionID) {
		t.Fatalf("worker 环境里的 session 身份应为 %q，实际 %q（存在=%v）", first.SessionID, got, ok)
	}
	if got, ok := c06aEnvValue(firstSpec.Env, run.EnvWorkspaceID); !ok || got != string(first.WorkspaceID) {
		t.Fatalf("worker 环境里的 workspace 身份应为 %q，实际 %q（存在=%v）", first.WorkspaceID, got, ok)
	}
	if got, ok := c06aEnvValue(firstSpec.Env, run.EnvWorkspaceDir); !ok || got != work {
		t.Fatalf("worker 环境里的工作区目录应为 %q，实际 %q（存在=%v）", work, got, ok)
	}

	// 结算第一个：同一个队列按 FIFO 补位，不是另开一个队列去跑第三条。
	mu.Lock()
	head := workers[0]
	mu.Unlock()
	head.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}
	if !eventuallyTrue(5*time.Second, func() bool {
		s, err := svc.Get(third.RunID)
		return err == nil && s.State == contracts.RunStateRunning
	}) {
		s, _ := svc.Get(third.RunID)
		t.Fatalf("空出槽位后排队的 run 应启动，实际 %s", s.State)
	}
	if got := svc.ActiveCount(); got != 2 {
		t.Fatalf("补位后活跃数应仍是 2，实际 %d", got)
	}
	if got := svc.QueuedCount(); got != 0 {
		t.Fatalf("补位后队列应清空，实际 %d", got)
	}
}

// mustRuntime 取出当前 RunService（未构建则失败，不隐式构建）。
func mustRuntime(t *testing.T, d *Desktop) *run.Service {
	t.Helper()
	svc, err := d.runtime()
	if err != nil {
		t.Fatalf("读取运行时失败: %v", err)
	}
	return svc
}
