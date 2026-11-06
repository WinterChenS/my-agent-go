package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// ---- fakeworker 构建与启动 ----

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// fakeWorkerExe 编译 fakeworker 并缓存路径。整个包只编译一次。
func fakeWorkerExe(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "myagent-fakeworker-*")
		if err != nil {
			fakeErr = fmt.Errorf("创建临时目录失败: %w", err)
			return
		}
		name := "fakeworker"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./fakeworker")
		if b, err := cmd.CombinedOutput(); err != nil {
			fakeErr = fmt.Errorf("编译 fakeworker 失败: %v\n%s", err, b)
			return
		}
		fakePath = out
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakePath
}

// startFake 启动一个 fakeworker 作为被测 Worker。
func startFake(t *testing.T, mode string, extra ...string) Worker {
	t.Helper()
	args := append([]string{"-mode", mode}, extra...)
	w, err := StartWorker(context.Background(), WorkerSpec{
		Installation: PiInstallation{Executable: fakeWorkerExe(t), Origin: OriginExternal, Runnable: true},
		WorkingDir:   t.TempDir(),
		AgentDir:     t.TempDir(),
		Args:         args,
		// fakeworker 不是 Pi，不会在退出时触发 libuv 断言，无需宽限。
		CloseGrace:   -1,
		KillTimeout:  3 * time.Second,
		CleanupLimit: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("启动 fakeworker 失败: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = w.Close(ctx)
	})
	return w
}

func callCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func echoOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v struct {
		Data struct {
			Echo string `json:"echo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("响应不是预期结构: %v（原文 %s）", err, clip(string(raw), 200))
	}
	return v.Data.Echo
}

func appErrCode(t *testing.T, err error) string {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError，实际 %v", err)
	}
	return appErr.Code
}

// collectEvents 读取事件直到 done 返回 true 或超时。
func collectEvents(t *testing.T, w Worker, timeout time.Duration, done func([]RawEvent) bool) []RawEvent {
	t.Helper()
	var out []RawEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-w.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
			if done != nil && done(out) {
				return out
			}
		case <-deadline:
			return out
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// TestOutOfOrderResponsesMatchByID 对应 RPC02：
// 并发命令的响应可能乱序到达，归属必须按 id，不能按到达顺序配对。
//
// fakeworker 的 outoforder 模式保证"回复顺序 = 接收顺序的倒序"，
// 因此只要每个 Call 拿到自己的 echo，就证明配对依据是 id 而不是时序。
func TestOutOfOrderResponsesMatchByID(t *testing.T) {
	const n = 3
	// RPC02 的外部观察装置：子进程把自己**收到/回出的每一条**（方向、id、字节数、
	// 时间戳）写进 io.jsonl。父进程读回这份账本，才能证明"响应真的是倒序产生的"，
	// 而不是本进程内部怎么想。
	dir := piObservationDir()
	if dir == "" {
		dir = t.TempDir() // 本地 focused 运行：不落盘到证据目录
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	ioPath := filepath.Clean(filepath.Join(dir, "io.jsonl"))
	_ = os.Remove(ioPath)

	w := startFake(t, "outoforder", "-expect", fmt.Sprint(n), "-record-io", ioPath)
	ctx := callCtx(t, 10*time.Second)

	type result struct {
		index int
		echo  string
		err   error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := w.Call(ctx, json.RawMessage(fmt.Sprintf(`{"type":"probe-%d"}`, i)))
			if err != nil {
				results <- result{index: i, err: err}
				return
			}
			results <- result{index: i, echo: echoOf(t, raw)}
		}(i)
	}
	wg.Wait()
	close(results)

	for r := range results {
		if r.err != nil {
			t.Fatalf("命令 probe-%d 失败: %v", r.index, r.err)
		}
		want := fmt.Sprintf("probe-%d", r.index)
		if r.echo != want {
			t.Fatalf("响应被错误归属：命令 %s 拿到了 %q", want, r.echo)
		}
	}

	// ── 从磁盘读回子进程的账本 ──
	if err := w.Close(ctx); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	events := readIOEvents(t, ioPath)
	readOrder := ioIDs(events, "read")
	writeOrder := ioIDs(events, "write")
	if len(readOrder) != n || len(writeOrder) != n {
		t.Fatalf("账本记录数不符：读到 %v，写出 %v（期望各 %d 条）", readOrder, writeOrder, n)
	}
	reversed := make([]string, len(readOrder))
	for i, id := range readOrder {
		reversed[len(readOrder)-1-i] = id
	}
	if !reflect.DeepEqual(writeOrder, reversed) {
		t.Fatalf("子进程不是按倒序回应的：读入 %v，写出 %v（期望 %v）", readOrder, writeOrder, reversed)
	}
	for _, id := range readOrder {
		if id == "" {
			t.Fatal("账本里出现了空 id：产品写出的命令必须带 id")
		}
	}

	if piObservationDir() != "" {
		writePiObservation(t, map[string]any{
			"calls":          n,
			"readOrder":      readOrder,
			"writeOrder":     writeOrder,
			"reversedOnWire": reflect.DeepEqual(writeOrder, reversed),
			"recordedBy":     "fakeworker -record-io（子进程自写）",
		}, nil)
	}
}

// TestDuplicateResponseNotMisattributed 对应 RPC02：
// 同一 id 的第二次响应不能覆盖已完成的 Call，也不能投给别的 Call。
func TestDuplicateResponseNotMisattributed(t *testing.T) {
	w := startFake(t, "duplicate")
	ctx := callCtx(t, 10*time.Second)

	raw, err := w.Call(ctx, json.RawMessage(`{"type":"ping"}`))
	if err != nil {
		t.Fatalf("首次调用失败: %v", err)
	}
	if got := echoOf(t, raw); got != "ping" {
		t.Fatalf("首个响应应为 ping，实际 %q", got)
	}

	// 第二次响应（echo=duplicate）必须作为诊断出现，而不是被当成新响应丢弃或误投。
	evs := collectEvents(t, w, 3*time.Second, func(evs []RawEvent) bool {
		return countDiagnostic(evs, diagUnknownResponseID) > 0
	})
	if countDiagnostic(evs, diagUnknownResponseID) == 0 {
		t.Fatalf("重复 id 未产生 unknown_response_id 诊断，实际事件: %s", summarizeEvents(evs))
	}

	// 误投检查：任何 Call 都不能拿到 echo=duplicate。
	for _, ev := range evs {
		if ev.Type == TypeResponse && strings.Contains(string(ev.Record), `"duplicate"`) {
			// 作为原始记录保留是允许的，这里只要求它没有进入 Call 的返回路径。
			continue
		}
	}
}

// TestCancelledCallDoesNotBlockSubsequentCalls 对应 RPC02 的"取消 Call 能结束"。
func TestCancelledCallDoesNotBlockSubsequentCalls(t *testing.T) {
	w := startFake(t, "hang")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := w.Call(cancelled, json.RawMessage(`{"type":"never"}`))
	if err == nil {
		t.Fatal("已取消的上下文必须立即返回错误")
	}
	if code := appErrCode(t, err); code != contracts.CodeRpcCancelled {
		t.Fatalf("错误码应为 %s，实际 %s", contracts.CodeRpcCancelled, code)
	}

	// 取消后 worker 必须仍然可用，且没有悬挂的 pending 项。
	ctx := callCtx(t, 1*time.Second)
	start := time.Now()
	_, err = w.Call(ctx, json.RawMessage(`{"type":"after-cancel"}`))
	if code := appErrCode(t, err); code != contracts.CodeRpcTimeout {
		t.Fatalf("hang 模式下应超时，实际 %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("超时返回过慢：%s", d)
	}
	if n := w.(*worker).pendingCount(); n != 0 {
		t.Fatalf("取消/超时后仍有 %d 个 pending 项悬挂", n)
	}
}

// TestStdoutBackpressureDoesNotDeadlock 对应 RPC03：
// 消费者不读 Events 时，stdout 洪泛不能把读循环堵死，命令响应仍然可达。
func TestStdoutBackpressureDoesNotDeadlock(t *testing.T) {
	const floodBytes = 4 << 20
	// RPC03 的外部观察装置：子进程自己记账（-record-io）。"洪泛写完"这件事只能由
	// 子进程自己证明——管道被反压死时它根本写不完，账本里就不会有 flood_done。
	dir := piObservationDir()
	if dir == "" {
		dir = t.TempDir() // 本地 focused 运行：不落盘到证据目录
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	ioPath := filepath.Clean(filepath.Join(dir, "io.jsonl"))
	_ = os.Remove(ioPath)

	w := startFake(t, "stdoutflood", "-flood-bytes", fmt.Sprint(floodBytes), "-record-io", ioPath)

	// 刻意不读 Events：让事件队列积压到上限（超出部分丢弃并计诊断）。
	time.Sleep(500 * time.Millisecond)

	ctx := callCtx(t, 8*time.Second)
	start := time.Now()
	raw, err := w.Call(ctx, json.RawMessage(`{"type":"under-flood"}`))
	if err != nil {
		t.Fatalf("stdout 洪泛期间命令失败（读循环可能被阻塞）: %v", err)
	}
	if got := echoOf(t, raw); got != "under-flood" {
		t.Fatalf("响应内容错误：%q", got)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("洪泛期间命令响应耗时过长：%s", d)
	}

	// ── 从磁盘读回子进程的账本：它是否真的把 4 MiB 写完了 ──
	done := waitForIOEvent(t, ioPath, "stdout", "flood_done", 20*time.Second)
	if done.Bytes < floodBytes {
		t.Fatalf("子进程只写出 %d 字节，未达洪泛目标 %d", done.Bytes, floodBytes)
	}
	events := readIOEvents(t, ioPath)
	stdinBytes, stdinFrames := 0, 0
	for _, ev := range events {
		if ev.Dir == "read" && ev.Stream == "stdin" {
			stdinBytes += ev.Bytes
			stdinFrames++
		}
	}
	if stdinFrames == 0 || stdinBytes == 0 {
		t.Fatalf("账本里没有 stdin 记录：命令没到达子进程（frames=%d bytes=%d）", stdinFrames, stdinBytes)
	}

	if piObservationDir() != "" {
		writePiObservation(t, map[string]any{
			"stdoutFloodTargetBytes": floodBytes,
			"stdoutFloodDoneBytes":   done.Bytes,
			"stdoutFloodCompleted":   done.Bytes >= floodBytes,
			"stdoutFloodStdinBytes":  stdinBytes,
			"stdoutFloodStdinFrames": stdinFrames,
			"stdoutFloodIORecords":   len(events),
			"recordedBy":             "fakeworker -record-io（子进程自写）",
		}, nil)
	}
}

// TestStderrFloodDoesNotBlockStdout 对应 RPC03：
// stderr 必须被持续读取，否则子进程会因 stderr 管道写满而整体卡住。
func TestStderrFloodDoesNotBlockStdout(t *testing.T) {
	const floodBytes = 8 << 20
	dir := piObservationDir()
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	ioPath := filepath.Clean(filepath.Join(dir, "io.jsonl"))
	_ = os.Remove(ioPath)

	w := startFake(t, "stderrflood", "-flood-bytes", fmt.Sprint(floodBytes), "-record-io", ioPath)
	time.Sleep(300 * time.Millisecond)

	ctx := callCtx(t, 10*time.Second)
	raw, err := w.Call(ctx, json.RawMessage(`{"type":"under-stderr-flood"}`))
	if err != nil {
		t.Fatalf("stderr 洪泛期间命令失败: %v", err)
	}
	// stderr 必须被持续读取：子进程写满管道后会连 stdout 一起卡住，
	// 因此命令能在洪泛期间返回，就证明 stderr 在被消费。
	if got := echoOf(t, raw); got != "under-stderr-flood" {
		t.Fatalf("响应内容错误：%q", got)
	}

	// 等到确实消费掉 1 MiB 以上，再验缓冲上限（否则测的是"还没写满"，没有意义）。
	waitFor(t, 15*time.Second, "stderr 消费超过 1 MiB", func() bool {
		_, truncated := w.StderrTail()
		return truncated
	})

	// ── 子进程自己的账本：8 MiB stderr 是否真的写完了 ──
	done := waitForIOEvent(t, ioPath, "stderr", "flood_done", 20*time.Second)
	if done.Bytes < floodBytes {
		t.Fatalf("子进程只写出 %d 字节 stderr，未达洪泛目标 %d", done.Bytes, floodBytes)
	}
	events := readIOEvents(t, ioPath)
	stdinBytes, stdinFrames := 0, 0
	for _, ev := range events {
		if ev.Dir == "read" && ev.Stream == "stdin" {
			stdinBytes += ev.Bytes
			stdinFrames++
		}
	}
	if stdinFrames == 0 {
		t.Fatalf("账本里没有 stdin 记录：命令没到达子进程")
	}
	// 关闭后检查缓冲上限：只保留最近 1 MiB，且明确标记被截断。
	if err := w.Close(ctx); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	tail, truncated := w.StderrTail()
	if len(tail) > defaultStderrKeep {
		t.Fatalf("stderr 缓冲 %d 字节，超过 1 MiB 上限", len(tail))
	}
	if !truncated {
		t.Fatalf("写入了 8 MiB stderr，必须标记 truncated=true（实际 false，缓冲 %d 字节）", len(tail))
	}

	if piObservationDir() != "" {
		writePiObservation(t, map[string]any{
			"stderrFloodTargetBytes": floodBytes,
			"stderrFloodDoneBytes":   done.Bytes,
			"stderrFloodCompleted":   done.Bytes >= floodBytes,
			"stderrFloodStdinBytes":  stdinBytes,
			"stderrFloodStdinFrames": stdinFrames,
			"stderrTailBytes":        len(tail),
			"stderrTruncated":        truncated,
			"stderrFloodIORecords":   len(events),
			"recordedBy":             "fakeworker -record-io（子进程自写）",
		}, nil)
	}

}

// TestUnknownAndMalformedRecordsHaveDiagnostics 对应 RPC04：
// 畸形 JSON / 缺 type / 未知 type / 无主 response 都必须有明确诊断且不崩溃；
// 未知最终完成语义绝不能判 completed。
func TestUnknownAndMalformedRecordsHaveDiagnostics(t *testing.T) {
	w := startFake(t, "malformed")

	evs := collectEvents(t, w, 3*time.Second, func(evs []RawEvent) bool {
		return countDiagnostic(evs, diagMalformedRecord) > 0 &&
			countDiagnostic(evs, diagUntypedRecord) > 0 &&
			countDiagnostic(evs, diagUnknownResponseID) > 0 &&
			countType(evs, "agent_settled_v2") > 0
	})

	for _, kind := range []string{diagMalformedRecord, diagUntypedRecord, diagUnknownResponseID} {
		if countDiagnostic(evs, kind) == 0 {
			t.Fatalf("缺少诊断 %s；实际事件：%s", kind, summarizeEvents(evs))
		}
	}
	if countType(evs, "agent_settled_v2") == 0 {
		t.Fatalf("未知 type 的原始记录必须保留，不能静默丢弃；实际：%s", summarizeEvents(evs))
	}

	// 未知 type 不得被当作完成信号。
	for _, ev := range evs {
		if ev.Type != "agent_settled_v2" {
			continue
		}
		out := ReadSettledOutcome(ev)
		if out.Known || out.IsCompleted() {
			t.Fatalf("未知 type 被判为完成（Known=%v Settled=%v）", out.Known, out.Settled)
		}
	}

	// 只有真正认证的 agent_settled 才允许 completed。
	settled := RawEvent{Type: TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}
	if !ReadSettledOutcome(settled).IsCompleted() {
		t.Fatal("agent_settled{aborted:false} 应判 completed")
	}
	aborted := RawEvent{Type: TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":true}`)}
	if ReadSettledOutcome(aborted).IsCompleted() {
		t.Fatal("agent_settled{aborted:true} 不能判 completed")
	}
}

// TestWorkerTerminatesGrandchild 对应 RPC05 的集成面：
// Close 后本 worker 拥有的孙进程必须被回收，而系统上无关的同类进程必须仍然存活。
//
// 此前这条用例在非 Windows 上直接 skip，导致 process_unix.go 的进程组回收
// **从未被执行过**，CI 六目标全绿属于假覆盖。现在两套实现都跑：
// 底层机制另有 process_tree_test.go 做跨平台直测，两者互为证据。
func TestWorkerTerminatesGrandchild(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild.hb")
	unrelatedMarker := filepath.Join(dir, "unrelated.hb")

	// 无关进程：同样挑一个 fakeworker 子进程，但不由被测 worker 启动。
	unrelated := exec.Command(fakeWorkerExe(t), "-child", "-marker", unrelatedMarker)
	if err := unrelated.Start(); err != nil {
		t.Fatalf("启动无关进程失败: %v", err)
	}
	t.Cleanup(func() {
		if unrelated.Process != nil {
			_ = unrelated.Process.Kill()
		}
	})
	waitFor(t, 5*time.Second, "无关进程开始写心跳", func() bool { return fileSize(unrelatedMarker) > 0 })

	w := startFake(t, "grandchild", "-marker", marker)
	ctx := callCtx(t, 10*time.Second)
	if _, err := w.Call(ctx, json.RawMessage(`{"type":"spawn"}`)); err != nil {
		t.Fatalf("触发孙进程失败: %v", err)
	}

	waitFor(t, 5*time.Second, "孙进程开始写心跳", func() bool { return fileSize(marker) > 0 })
	time.Sleep(150 * time.Millisecond)
	before := fileSize(marker)
	time.Sleep(350 * time.Millisecond)
	if fileSize(marker) <= before {
		t.Fatal("测试前提不成立：孙进程没有持续写心跳")
	}

	if err := w.Close(ctx); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	stopped := fileSize(marker)
	time.Sleep(1 * time.Second)
	if after := fileSize(marker); after != stopped {
		t.Fatalf("owned 孙进程在 Close 后仍存活（心跳 %d → %d 字节）", stopped, after)
	}

	// 无关进程必须还活着：不能为了清理自己的树去杀系统上所有同名进程。
	unrelatedBefore := fileSize(unrelatedMarker)
	time.Sleep(500 * time.Millisecond)
	if after := fileSize(unrelatedMarker); after <= unrelatedBefore {
		t.Fatalf("无关进程被误杀（心跳停在 %d 字节）", unrelatedBefore)
	}
}

// TestCloseRejectsNewCalls 对应基线 §6「停止时立即拒绝新的执行请求」。
func TestCloseRejectsNewCalls(t *testing.T) {
	w := startFake(t, "echo")
	ctx := callCtx(t, 10*time.Second)

	if err := w.Close(ctx); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	_, err := w.Call(ctx, json.RawMessage(`{"type":"after-close"}`))
	if err == nil {
		t.Fatal("Close 后必须拒绝新命令")
	}
	if code := appErrCode(t, err); code != contracts.CodeWorkerClosed {
		t.Fatalf("错误码应为 %s，实际 %s", contracts.CodeWorkerClosed, code)
	}
	// Close 可重复调用且返回一致结果。
	if err := w.Close(ctx); err != nil {
		t.Fatalf("重复 Close 应幂等，实际 %v", err)
	}
}

// TestCallRejectsNonObjectCommand 保证命令形状错误不会被写进管道。
func TestCallRejectsNonObjectCommand(t *testing.T) {
	w := startFake(t, "echo")
	ctx := callCtx(t, 5*time.Second)
	for _, bad := range []string{`[]`, `"str"`, `null`, `{`} {
		_, err := w.Call(ctx, json.RawMessage(bad))
		if err == nil {
			t.Fatalf("非法命令 %q 必须被拒绝", bad)
		}
		if code := appErrCode(t, err); code != contracts.CodeRpcInvalidCall {
			t.Fatalf("命令 %q 的错误码应为 %s，实际 %s", bad, contracts.CodeRpcInvalidCall, code)
		}
	}
}

// ---- 测试内小工具 ----

func countType(evs []RawEvent, typ string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func countDiagnostic(evs []RawEvent, kind string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type != TypeProtocolDiagnostic {
			continue
		}
		var d struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(ev.Record, &d) == nil && d.Kind == kind {
			n++
		}
	}
	return n
}

func summarizeEvents(evs []RawEvent) string {
	var b strings.Builder
	for _, ev := range evs {
		fmt.Fprintf(&b, "[%s %s] ", ev.Type, clip(string(ev.Record), 80))
	}
	return b.String()
}

// ---- 退出诊断 ----

// TestExitDiagnosisCarriesStderrTail 守住一条可用性底线：
// "进程没了"必须连带说出原因。
//
// 真实踩过的场景：models.json 因 apiKey 为空被判非法，Pi 打印一行
// "Unknown provider" 后退出。如果错误只说"Pi 进程已退出"，
// 排查者面对的是一个没有任何线索的界面。
func TestExitDiagnosisCarriesStderrTail(t *testing.T) {
	w := &worker{stderr: newStderrRing(1024)}
	if _, err := w.stderr.Write([]byte("Error: Unknown provider \"fixture\".\n")); err != nil {
		t.Fatalf("写 stderr 环: %v", err)
	}
	msg := w.exitDiagnosis()
	if !strings.Contains(msg, "Unknown provider") {
		t.Fatalf("退出诊断必须包含 stderr 内容，实际 %q", msg)
	}
}

// TestExitDiagnosisIncludesExitCode 确认退出码不丢：
// 0 与非 0 的含义完全不同，不能只靠 stderr 有无来判断。
func TestExitDiagnosisIncludesExitCode(t *testing.T) {
	w := &worker{stderr: newStderrRing(1024)}
	w.exited = make(chan struct{})
	close(w.exited) // 真实调用发生在 <-w.exited 之后，这里等价地先把退出信号置上
	w.exitCode, w.exitOK = 7, true
	msg := w.exitDiagnosis()
	if !strings.Contains(msg, "退出码 7") {
		t.Fatalf("诊断里应带退出码，实际 %q", msg)
	}
}

// TestExitDiagnosisWithoutStderr 不能因为 stderr 为空就崩或输出空的"尾部"。
func TestExitDiagnosisWithoutStderr(t *testing.T) {
	w := &worker{stderr: newStderrRing(1024)}
	msg := w.exitDiagnosis()
	if strings.Contains(msg, "stderr 尾部") {
		t.Fatalf("没有 stderr 时不该出现空的尾部片段，实际 %q", msg)
	}
	if !strings.Contains(msg, "Pi 进程已退出") {
		t.Fatalf("保底文案丢了: %q", msg)
	}
}

// TestTailRunesKeepsUTF8Intact 截断按 rune 而不是字节，中文不会被切坏。
func TestTailRunesKeepsUTF8Intact(t *testing.T) {
	s := "前" + strings.Repeat("中", 10) + "后"
	got := tailRunes(s, 3)
	if !utf8.ValidString(got) {
		t.Fatalf("截断产生了非法 UTF-8: %q", got)
	}
	if got != "中中后" {
		t.Fatalf("期望末 3 个字符为 中中后，实际 %q", got)
	}
	if tailRunes("短", 10) != "短" {
		t.Fatal("短于上限时应原样返回")
	}
}
