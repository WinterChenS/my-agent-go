package run

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// buildFakeWorkerBinary 编译可脚本化的假 Pi 进程。
//
// RUN05 需要一个「副作用发生在被测进程边界之外」的 fixture：写入发生在子进程里，
// 测试进程只读结果文件。进程内 fakeWorker 做不到这一点——它没有自己的文件系统
// 写入生命周期，也就无法证明「恢复后没有重放」。
func buildFakeWorkerBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fakeworker")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "my-agent-go/internal/runtime/pi/fakeworker")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译 fakeworker 失败: %v\n%s", err, b)
	}
	return out
}

// counterLines 读副作用计数器文件的行数。
func counterLines(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

// workerCalls 返回假 worker 收到的全部命令（拷贝，供断言）。
func workerCalls(w *fakeWorker) []json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]json.RawMessage{}, w.calls...)
}

// toolStartsNamed 返回某 run 事件日志里指定工具名的 tool.start 次数。
func toolStartsNamed(t *testing.T, svc *Service, id contracts.RunID, name string) int {
	t.Helper()
	n := 0
	for _, ev := range eventsOfType(t, svc, id, EventToolStart) {
		var p struct {
			ToolName string `json:"toolName"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 tool.start 载荷失败: %v", err)
		}
		if p.ToolName == name {
			n++
		}
	}
	return n
}

// TestInterruptedRunDoesNotReplayUnknownWrite 覆盖 RUN05：
// 「工具已写后崩溃，恢复计数仍 1；未知副作用结果不自动重放」。
//
// 场景顺序是重点：子进程先回复 prompt（于是 run 进入 running），然后执行
// fixture_write（真实写文件 = 副作用），再以退出码 1 结束且**不发** agent_settled。
// 上层看到的是「事件流在最终 idle 之前关闭」，只能记 interrupted + CLEANUP_FAILED；
// 这个结果未知，所以同一会话的下一次 run 绝不能把这次写入重放一遍。
func TestInterruptedRunDoesNotReplayUnknownWrite(t *testing.T) {
	exe := buildFakeWorkerBinary(t)
	dir := t.TempDir()
	counter := filepath.Join(dir, "counter.txt")
	// waitFile 是测试给子进程的"状态已到 running"信号：子进程回应 prompt 之后就轮询它，
	// 不靠 sleep。sleep 会与 launch 的状态迁移赛跑，时快时慢会变成偶发红。
	waitFile := filepath.Join(dir, "crash-now.txt")

	opts := testOptions(t)
	opts.Installation = pi.PiInstallation{ID: "fixture:fakeworker", Executable: exe}

	// 第二次启动换成进程内假 worker：它跑的是「继续这个会话」的正常路径。
	normal := newFakeWorker()
	started := 0
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		started++
		if started == 1 {
			// 隔离参数与崩溃场景无关：fakeworker 不认识 --no-* 这类 Pi 参数，
			// 整体替换 Args 才能让它读到 -mode/-counter/-wait。
			// Options.Isolation 仍必须是合法 spec —— Service.validate() 会校验它，
			// 只是它不影响这里真正跑起来的进程。
			//
			// SessionPath 也要清掉：pi.StartWorker 会在 Args 后面追加 --session <path>，
			// 而 fakeworker 的 flag.Parse 不认它，会直接退出。同理 Installation.Args。
			spec.SessionPath = ""
			spec.Installation.Args = nil
			spec.Args = []string{"-mode", "crashafterwrite", "-counter", counter, "-wait", waitFile}
			return pi.StartWorker(ctx, spec)
		}
		return normal, nil
	}

	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	first := startRunForTest(t, svc, "把这个文件写出来")
	// Start 返回时 run 已进入 running（launch 里 prompt 的 Call 成功之后才迁状态）。
	// 这里显式断言一次，再落信号文件——于是"先 running、后崩溃"不再依赖任何 sleep。
	if st := mustRunState(t, svc, first.RunID); st != contracts.RunStateRunning {
		t.Fatalf("崩溃前 run 应处于 running，实际 %s", st)
	}
	if err := os.WriteFile(waitFile, []byte("crash now\n"), 0o644); err != nil {
		t.Fatalf("写崩溃信号文件失败: %v", err)
	}

	if !eventually(t, 15*time.Second, func() bool {
		return mustRunState(t, svc, first.RunID) == contracts.RunStateInterrupted
	}) {
		t.Fatalf("写入后崩溃的 run 必须是 interrupted，实际 %s", mustRunState(t, svc, first.RunID))
	}
	if code := lastRunErrorCode(t, svc, first.RunID); code != contracts.CodeCleanupFailed {
		t.Fatalf("崩断的 run.error 应是 %s，实际 %s", contracts.CodeCleanupFailed, code)
	}
	lines, err := counterLines(counter)
	if err != nil {
		t.Fatalf("读取副作用计数器失败: %v", err)
	}
	if lines != 1 {
		t.Fatalf("崩溃前副作用必须发生且只发生一次，计数器实际 %d 行", lines)
	}
	if n := toolStartsNamed(t, svc, first.RunID, "fixture_write"); n != 1 {
		t.Fatalf("第一次 run 应有且只有 1 次 fixture_write，实际 %d 次", n)
	}

	// —— 继续这个会话 ——
	second := startRunForTest(t, svc, "接着来")
	calls := workerCalls(normal)
	if len(calls) != 1 {
		t.Fatalf("新 run 只应收到 1 条命令（prompt），实际 %d 条: %v", len(calls), calls)
	}
	if !strings.Contains(string(calls[0]), `"type":"prompt"`) {
		t.Fatalf("新 run 的第一条命令必须是 prompt，实际 %s", calls[0])
	}
	if n := toolStartsNamed(t, svc, second.RunID, "fixture_write"); n != 0 {
		t.Fatalf("恢复后的 run 不得重放未知副作用的工具调用，实际重放 %d 次", n)
	}
	lines, err = counterLines(counter)
	if err != nil {
		t.Fatalf("读取副作用计数器失败: %v", err)
	}
	if lines != 1 {
		t.Fatalf("恢复后副作用计数必须仍是 1，实际 %d 行（说明未知写入被重放了）", lines)
	}

	// —— 历史不得被恢复过程改写 ——
	snap, err := svc.Get(first.RunID)
	if err != nil {
		t.Fatalf("恢复后读取崩溃 run 快照失败: %v", err)
	}
	if snap.State != contracts.RunStateInterrupted {
		t.Fatalf("恢复不得改写历史：崩溃 run 仍是 interrupted，实际 %s", snap.State)
	}
	journal, err := svc.Replay(first.RunID, 0)
	if err != nil {
		t.Fatalf("恢复后重放崩溃 run 事件失败: %v", err)
	}
	if len(journal) == 0 {
		t.Fatal("崩溃 run 的事件日志不能为空：恢复不等于删除历史")
	}
	var sawInterrupted bool
	for _, ev := range journal {
		if ev.Type != EventRunState {
			continue
		}
		var p statePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 run.state 载荷失败: %v", err)
		}
		if p.State == contracts.RunStateInterrupted {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Fatal("事件日志里缺少 interrupted 状态：历史被改写了")
	}

	raw, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("读取副作用计数器失败: %v", err)
	}
	writeCaseObservation(t, map[string]any{
		"interrupted":  true,
		"errorCode":    contracts.CodeCleanupFailed,
		"counterLines": lines,
		"replayed":     false,
	}, map[string][]byte{"counter.txt": raw})
}
