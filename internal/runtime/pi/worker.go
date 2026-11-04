package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// 默认参数。均可由 WorkerSpec 覆盖，测试用较小时限避免拖慢套件。
const (
	defaultCloseGrace   = 800 * time.Millisecond
	defaultKillTimeout  = 5 * time.Second
	defaultCleanupLimit = 10 * time.Second
	defaultQueueLimit   = 4096
	defaultEventBuffer  = 256
	defaultStderrKeep   = 1 << 20
)

// WorkerSpec 描述一个 worker 进程的启动参数。
type WorkerSpec struct {
	// Installation 是 F02 定位/认证得到的 Pi 安装件。
	Installation PiInstallation
	// WorkingDir 是 workspace 根目录。
	WorkingDir string
	// AgentDir 是隔离的 PI_CODING_AGENT_DIR，绝不指向用户的 ~/.pi/agent。
	AgentDir string
	// Env 追加到宿主环境之后，同名变量覆盖前面的值。
	Env []string
	// SessionPath 非空时追加 --session <path>。
	SessionPath string
	// Args 是追加在 BuildLaunch 之后的命令行参数（--mode rpc 等）。
	Args []string

	// CloseGrace 是关 stdin 之前等待 stdout 静默的宽限期。
	// 默认 800ms 来自 F02 的实测对照：0s 宽限 4/4 触发 Pi 退出阶段 libuv 断言崩溃，
	// 800ms 与 3000ms 各 8/8 正常。调到 0 会重现崩溃，仅用于验证该结论。
	CloseGrace time.Duration
	// KillTimeout 是等待进程自行退出的时限，超时即强杀进程树。
	KillTimeout time.Duration
	// CleanupLimit 是清理 owned 子孙的总时限，超时记 CleanupFailed。
	CleanupLimit time.Duration
	// QueueLimit 是事件队列上限（超过则丢弃最旧事件并计诊断）。
	QueueLimit int
}

// Worker 是一个 Pi RPC 子进程的监督者。
type Worker interface {
	// Call 发送一条命令并等待同 id 的响应。
	//
	// 不会自动重放：写失败、超时、进程退出后重发都可能造成重复副作用，
	// 一律返回错误由上层决定（基线 §6）。
	Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error)
	// Events 返回 Pi 原生事件流。关闭 worker 后 channel 会关闭。
	Events() <-chan RawEvent
	// Close 有序关闭：静默宽限 → 关 stdin → 等退出 → 强杀进程树 → 校验 owned 子孙清空。
	// 可重复调用，返回同一个结果。
	Close(ctx context.Context) error

	// 以下为诊断/验收用的扩展，不属于 RPC 协议契约。
	PID() int
	StderrTail() (text string, truncated bool)
	ExitCode() (code int, known bool)
	Limitations() []string
}

type worker struct {
	spec WorkerSpec

	cmd   *exec.Cmd
	stdin io.WriteCloser

	framer *Framer
	events chan RawEvent
	queue  *eventQueue

	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	writeMu sync.Mutex

	ids commandIDs

	stderr *stderrRing
	tree   *processTree

	lastActivity atomic.Int64 // UnixNano
	closing      atomic.Bool

	stdoutDone   chan struct{}
	stderrDone   chan struct{}
	dispatchDone chan struct{}
	stopDispatch chan struct{}
	exited       chan struct{}

	waitOnce sync.Once
	exitCode int
	exitOK   bool

	closeOnce sync.Once
	closeErr  error
}

// StartWorker 启动 Pi 的 RPC 子进程并接管其 stdio。
func StartWorker(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := ctx.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeRpcCancelled, "启动前上下文已取消: "+err.Error(), false)
	}
	if strings.TrimSpace(spec.Installation.Executable) == "" {
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "未提供 Pi 可执行文件", false)
	}
	if spec.QueueLimit <= 0 {
		spec.QueueLimit = defaultQueueLimit
	}
	if spec.CloseGrace < 0 {
		spec.CloseGrace = 0
	} else if spec.CloseGrace == 0 {
		spec.CloseGrace = defaultCloseGrace
	}
	if spec.KillTimeout <= 0 {
		spec.KillTimeout = defaultKillTimeout
	}
	if spec.CleanupLimit <= 0 {
		spec.CleanupLimit = defaultCleanupLimit
	}

	args := append([]string{}, spec.Args...)
	if spec.SessionPath != "" {
		args = append(args, "--session", spec.SessionPath)
	}
	exe, argv, err := BuildLaunch(spec.Installation, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "解析启动命令失败: "+err.Error(), false)
	}

	cmd := exec.Command(exe, argv...)
	cmd.Dir = spec.WorkingDir
	cmd.Env = buildEnv(spec)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	configureSysProcAttr(cmd.SysProcAttr)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "创建 stdin 管道失败: "+err.Error(), false)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "创建 stdout 管道失败: "+err.Error(), false)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "创建 stderr 管道失败: "+err.Error(), false)
	}

	tree, err := newProcessTree()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "准备进程树失败: "+err.Error(), false)
	}

	if err := cmd.Start(); err != nil {
		_ = tree.close()
		return nil, contracts.NewAppError(contracts.CodeWorkerStartFail, "启动 Pi 失败: "+err.Error(), false)
	}

	w := &worker{
		spec:         spec,
		cmd:          cmd,
		stdin:        stdin,
		framer:       NewFramer(stdout, 0),
		events:       make(chan RawEvent, defaultEventBuffer),
		queue:        newEventQueue(spec.QueueLimit),
		pending:      make(map[string]chan json.RawMessage),
		stderr:       newStderrRing(defaultStderrKeep),
		tree:         tree,
		stdoutDone:   make(chan struct{}),
		stderrDone:   make(chan struct{}),
		dispatchDone: make(chan struct{}),
		stopDispatch: make(chan struct{}),
		exited:       make(chan struct{}),
	}
	w.lastActivity.Store(time.Now().UnixNano())

	// attach 失败不阻止启动：worker 仍可用，但 Close 会带出"子孙清理不可保证"的限制。
	_ = tree.attach(cmd.Process)

	go w.readStdout()
	go w.readStderr(stderr)
	go w.dispatchLoop()
	go w.supervise()
	return w, nil
}

// buildEnv 构造子进程环境：宿主环境 + 显式覆盖。
// 所有 PI_ 前缀变量先剔除，避免宿主已设的 PI_CODING_AGENT_DIR 把子进程带回用户真实配置目录。
func buildEnv(spec WorkerSpec) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if strings.HasPrefix(strings.ToUpper(key), "PI_") {
			continue
		}
		env = append(env, kv)
	}
	if spec.AgentDir != "" {
		env = append(env, "PI_CODING_AGENT_DIR="+spec.AgentDir)
	}
	// spec.Env 放最后：同名变量以最后出现的为准（os/exec 会做 dedup）。
	env = append(env, spec.Env...)
	return env
}

func (w *worker) PID() int {
	if w.cmd == nil || w.cmd.Process == nil {
		return 0
	}
	return w.cmd.Process.Pid
}

func (w *worker) Events() <-chan RawEvent { return w.events }

func (w *worker) StderrTail() (string, bool) { return w.stderr.snapshot() }

func (w *worker) ExitCode() (int, bool) {
	select {
	case <-w.exited:
		return w.exitCode, w.exitOK
	default:
		return 0, false
	}
}

func (w *worker) Limitations() []string {
	var out []string
	if note := w.tree.limitation(); note != "" {
		out = append(out, note)
	}
	return out
}

// Call 发送命令并等待同 id 响应。
func (w *worker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	if w.closing.Load() {
		return nil, contracts.NewAppError(contracts.CodeWorkerClosed, "worker 正在关闭，拒绝新命令", false)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(command, &fields); err != nil {
		return nil, contracts.NewAppError(contracts.CodeRpcInvalidCall, "命令不是合法 JSON 对象: "+err.Error(), false)
	}
	if fields == nil {
		return nil, contracts.NewAppError(contracts.CodeRpcInvalidCall, "命令必须是 JSON 对象", false)
	}

	// id 由 worker 统一分配：调用方传入的 id 会被覆盖，保证不复用、不被跨会话猜中。
	id := w.ids.next()
	fields["id"] = json.RawMessage(strconvQuote(id))
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeRpcInvalidCall, "序列化命令失败: "+err.Error(), false)
	}

	ch := make(chan json.RawMessage, 1)
	w.mu.Lock()
	w.pending[id] = ch
	w.mu.Unlock()
	defer w.forget(id)

	w.writeMu.Lock()
	_, werr := w.stdin.Write(append(payload, '\n'))
	w.writeMu.Unlock()
	if werr != nil {
		// 写失败不自动重放：命令可能已部分送达，重发会造成重复执行。
		return nil, contracts.NewAppError(contracts.CodeRpcWriteFailed,
			"写入命令失败（不自动重放）: "+werr.Error(), false)
	}

	select {
	case resp := <-ch:
		return w.interpretResponse(resp)
	case <-ctx.Done():
		return nil, ctxCallError(ctx)
	case <-w.exited:
		// 进程退出与响应到达可能同时发生，先把已投递的响应取出来。
		select {
		case resp := <-ch:
			return w.interpretResponse(resp)
		default:
		}
		return nil, contracts.NewAppError(contracts.CodeRpcWorkerExited, w.exitDiagnosis(), false)
	}
}

// exitDiagnosis 把"进程没了"变成一句能直接定位原因的话。
//
// 只写"Pi 进程已退出"是不够的：真实的退出原因几乎总在 stderr 里
// （例如 models.json schema 非法导致 provider 不存在），而那正是用户看不到的地方。
// 不带 stderr 的错误会让排查从"读一行报错"变成"猜十分钟"。
func (w *worker) exitDiagnosis() string {
	var b strings.Builder
	b.WriteString("Pi 进程已退出，该命令结果未知")
	if code, ok := w.ExitCode(); ok {
		fmt.Fprintf(&b, "（退出码 %d）", code)
	}
	if tail, truncated := w.StderrTail(); strings.TrimSpace(tail) != "" {
		b.WriteString("；stderr 尾部")
		if truncated {
			b.WriteString("（已截断）")
		}
		b.WriteString("：")
		b.WriteString(tailRunes(strings.TrimSpace(tail), 800))
	}
	return b.String()
}

// tailRunes 取字符串末尾最多 n 个 rune（按字符而非字节，避免把中文切坏）。
func tailRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[len(runes)-n:])
}

// interpretResponse 把 success=false 的响应转成错误，但保留原始 record 供诊断。
func (w *worker) interpretResponse(raw json.RawMessage) (json.RawMessage, error) {
	var h recordHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return raw, nil
	}
	if h.Success != nil && !*h.Success {
		return raw, contracts.NewAppError(contracts.CodeRpcCommandFailed,
			fmt.Sprintf("Pi 返回命令失败（command=%s）", h.Command), false)
	}
	return raw, nil
}

func (w *worker) forget(id string) {
	w.mu.Lock()
	delete(w.pending, id)
	w.mu.Unlock()
}

// pendingCount 返回仍在等待响应的 Call 数（诊断/测试用）。
func (w *worker) pendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// deliverResponse 把响应交给等待者。只有第一次投递成功：
// 重复 id 的第二次响应会走"未知 id"路径，不会被误归属。
func (w *worker) deliverResponse(id string, raw json.RawMessage) bool {
	w.mu.Lock()
	ch, ok := w.pending[id]
	if ok {
		delete(w.pending, id)
	}
	w.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- raw:
	default:
	}
	return true
}

func (w *worker) readStdout() {
	defer close(w.stdoutDone)
	defer w.queue.close()

	for {
		line, err := w.framer.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// 分帧错误（记录过大 / 流中截断）必须让上层看到，而不是静默停止。
				w.enqueue(RawEvent{Type: TypeProtocolDiagnostic,
					Record: diagnosticRecord(diagStreamError, err.Error())})
			}
			return
		}
		w.handleRecord(line)
	}
}

// handleRecord 按 type 分派一条已分帧的记录。
func (w *worker) handleRecord(line []byte) {
	w.markActivity()
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return
	}

	var h recordHeader
	if err := json.Unmarshal(trimmed, &h); err != nil {
		w.enqueue(RawEvent{Type: TypeProtocolDiagnostic,
			Record: diagnosticRecord(diagMalformedRecord, "无法解析的 JSON："+err.Error()+" 原文："+clip(string(trimmed), 400))})
		return
	}
	if h.Type == "" {
		w.enqueue(RawEvent{Type: TypeProtocolDiagnostic,
			Record: diagnosticRecord(diagUntypedRecord, "记录缺少 type 字段："+clip(string(trimmed), 400))})
		return
	}

	if h.Type == TypeResponse && h.ID != "" {
		if w.deliverResponse(h.ID, cloneBytes(trimmed)) {
			return
		}
		// 未知/重复 id：先给诊断，再保留原始记录，绝不悄悄丢掉也不误归属给别的 Call。
		w.enqueue(RawEvent{Type: TypeProtocolDiagnostic,
			Record: diagnosticRecord(diagUnknownResponseID, "没有等待中的 Call 对应 id="+h.ID)})
		w.enqueue(RawEvent{Type: TypeResponse, Record: cloneBytes(trimmed)})
		return
	}

	w.enqueue(RawEvent{Type: h.Type, Record: cloneBytes(trimmed)})
}

func (w *worker) markActivity() { w.lastActivity.Store(time.Now().UnixNano()) }

func (w *worker) enqueue(ev RawEvent) {
	w.queue.push(ev)
}

func (w *worker) dispatchLoop() {
	defer close(w.dispatchDone)
	defer close(w.events)

	for {
		if n := w.queue.takeDropped(); n > 0 {
			ev := RawEvent{Type: TypeProtocolDiagnostic,
				Record: diagnosticRecord(diagQueueOverflow, fmt.Sprintf("事件队列溢出，丢弃了 %d 条最旧事件", n))}
			if !w.sendEvent(ev) {
				return
			}
		}
		ev, ok := w.queue.pop()
		if !ok {
			return
		}
		if !w.sendEvent(ev) {
			return
		}
	}
}

// sendEvent 把事件投递出去。消费者不读时 Close 可以通过 stopDispatch 解除阻塞，
// 保证关闭流程不会被一个停止读取的 UI 卡死。
func (w *worker) sendEvent(ev RawEvent) bool {
	select {
	case w.events <- ev:
		return true
	case <-w.stopDispatch:
		return false
	}
}

func (w *worker) readStderr(r io.Reader) {
	defer close(w.stderrDone)
	buf := make([]byte, 32<<10)
	redactor := newLineRedactor(w.stderr)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = redactor.Write(buf[:n])
		}
		if err != nil {
			_ = redactor.Flush()
			return
		}
	}
}

func (w *worker) supervise() {
	// 必须等两个管道都读到 EOF 才能调用 Wait：os/exec 会在 Wait 里关闭管道，
	// 提前调用会让仍在读的 goroutine 拿到 "file already closed" 而不是真实数据。
	<-w.stdoutDone
	<-w.stderrDone

	w.waitOnce.Do(func() {
		_ = w.cmd.Wait()
		if w.cmd.ProcessState != nil {
			w.exitCode = w.cmd.ProcessState.ExitCode()
			w.exitOK = true
		}
	})
	close(w.exited)
}

// drainGrace 等待 stdout 静默满一个宽限期。
// 若距最后一条记录已经超过宽限期，则立即返回（进程本来就闲着）。
func (w *worker) drainGrace(ctx context.Context) {
	grace := w.spec.CloseGrace
	if grace <= 0 {
		return
	}
	last := time.Unix(0, w.lastActivity.Load())
	wait := grace - time.Since(last)
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	case <-w.exited:
	}
}

func (w *worker) waitExit(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-w.exited:
		return true
	case <-t.C:
		return false
	}
}

// waitTreeEmpty 轮询 job 内的存活进程数，直到清零或超时。
// 返回 (剩余数, 是否可判定)。
func (w *worker) waitTreeEmpty(limit time.Duration) (int, bool) {
	deadline := time.Now().Add(limit)
	for {
		n, ok := w.tree.activeCount()
		if !ok {
			return -1, false
		}
		if n == 0 {
			return 0, true
		}
		if time.Now().After(deadline) {
			return n, true
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Close 有序关闭 worker。
//
// 顺序不是随意的，每一步都有实测依据：
//  1. 等待 stdout 静默——F02 实测：agent_settled 后立刻关 stdin 会让 Pi 在退出阶段触发
//     libuv 断言崩溃（exit 3221226505），宽限 800ms 后 8/8 正常。
//  2. 关 stdin 请求有序退出。
//  3. 超时才强杀进程树（Job Object / 进程组），不误杀系统上其他 Pi。
//  4. 校验 owned 子孙真的清空；判定不了或仍有残留就返回 CleanupFailed，
//     绝不允许把"没清干净"显示成"清理成功"（RPC05）。
func (w *worker) Close(ctx context.Context) error {
	w.closeOnce.Do(func() {
		w.closing.Store(true)

		w.drainGrace(ctx)
		_ = w.stdin.Close()

		exited := w.waitExit(w.spec.KillTimeout)
		if !exited {
			_ = w.tree.kill()
			exited = w.waitExit(w.spec.KillTimeout)
		}

		// 主进程可能已退出但子孙（MCP server 等）仍持有管道与进程槽位。
		remaining, verifiable := w.waitTreeEmpty(w.spec.CleanupLimit)
		if !verifiable || remaining != 0 {
			_ = w.tree.kill()
			remaining, verifiable = w.waitTreeEmpty(2 * time.Second)
		}

		// 收尾 dispatch：先给正常排空一个窗口，再强制解锁。
		w.queue.close()
		select {
		case <-w.dispatchDone:
		case <-time.After(500 * time.Millisecond):
			close(w.stopDispatch)
			<-w.dispatchDone
		}

		limitation := w.tree.limitation()
		_ = w.tree.close()

		var problems []string
		if !exited {
			problems = append(problems, "主进程在时限内未退出，已强杀进程树（结果未知）")
		}
		if !verifiable {
			problems = append(problems, "无法判定 owned 子孙是否清空（"+limitation+"）")
		} else if remaining != 0 {
			problems = append(problems, fmt.Sprintf("清理后仍有 %d 个 owned 进程存活", remaining))
		}
		if len(problems) > 0 {
			w.closeErr = contracts.NewAppError(contracts.CodeCleanupFailed,
				"worker 清理未完成："+strings.Join(problems, "；"), false)
		}
	})
	return w.closeErr
}

// ---- 小工具 ----

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func cloneBytes(b []byte) json.RawMessage {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// clip 按字节上限截断字符串，且不切断 UTF-8 字符。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(已截断)"
}

func ctxCallError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return contracts.NewAppError(contracts.CodeRpcTimeout,
			"等待 Pi 响应超时；命令是否已执行未知，不自动重放", false)
	}
	return contracts.NewAppError(contracts.CodeRpcCancelled, "调用已取消", false)
}

// ---- 事件队列：有界，读循环永不因消费者变慢而阻塞 ----

type eventQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      []RawEvent
	head     int
	count    int
	dropped  uint64
	reported uint64
	closed   bool
}

func newEventQueue(limit int) *eventQueue {
	q := &eventQueue{buf: make([]RawEvent, limit)}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *eventQueue) push(ev RawEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if q.count == len(q.buf) {
		// 队列满：丢最旧的，保留最新状态。丢弃数会以诊断事件告知上层。
		q.buf[q.head] = RawEvent{}
		q.head = (q.head + 1) % len(q.buf)
		q.count--
		q.dropped++
	}
	q.buf[(q.head+q.count)%len(q.buf)] = ev
	q.count++
	q.cond.Signal()
}

func (q *eventQueue) pop() (RawEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.count == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.count == 0 {
		return RawEvent{}, false
	}
	ev := q.buf[q.head]
	q.buf[q.head] = RawEvent{}
	q.head = (q.head + 1) % len(q.buf)
	q.count--
	return ev, true
}

func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *eventQueue) takeDropped() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := q.dropped - q.reported
	q.reported = q.dropped
	return n
}

// ---- stderr：持续读取、脱敏、只保留最近 1 MiB ----

type stderrRing struct {
	mu        sync.Mutex
	buf       []byte
	max       int
	truncated bool
}

func newStderrRing(max int) *stderrRing {
	return &stderrRing{max: max}
}

func (r *stderrRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	// 留 2 倍余量再批量裁剪，避免每写一行都搬移 1 MiB。
	if len(r.buf) > 2*r.max {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.max:]...)
		r.truncated = true
	}
	return len(p), nil
}

func (r *stderrRing) snapshot() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	truncated := r.truncated || len(r.buf) > r.max
	b := r.buf
	if len(b) > r.max {
		b = b[len(b)-r.max:]
	}
	return string(b), truncated
}

// lineRedactor 按行脱敏后再落进环形缓冲。
type lineRedactor struct {
	dst *stderrRing
	buf []byte
}

func newLineRedactor(dst *stderrRing) *lineRedactor {
	return &lineRedactor{dst: dst}
}

func (r *lineRedactor) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			break
		}
		_, _ = r.dst.Write([]byte(redactText(string(r.buf[:i+1]))))
		r.buf = append([]byte(nil), r.buf[i+1:]...)
	}
	// 单行超长（无换行的洪泛）时先冲刷，防止内存无界增长。
	if len(r.buf) > 64<<10 {
		_, _ = r.dst.Write([]byte(redactText(string(r.buf))))
		r.buf = r.buf[:0]
	}
	return len(p), nil
}

func (r *lineRedactor) Flush() error {
	if len(r.buf) > 0 {
		_, _ = r.dst.Write([]byte(redactText(string(r.buf))))
		r.buf = r.buf[:0]
	}
	return nil
}

var redactionRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`sk-[A-Za-z0-9_\-]{8,}`), "sk-***"},
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)\S+`), "${1}***"},
	{regexp.MustCompile(`(?i)("?(?:api[_-]?key|token|secret)"?\s*[:=]\s*"?)[A-Za-z0-9_\-.]{8,}`), "${1}***"},
	{regexp.MustCompile(`(?i)([A-Za-z]:\\Users\\)[^\\\s]+`), "${1}<user>"},
	{regexp.MustCompile(`(/home/)[^/\s]+`), "${1}<user>"},
}

// redactText 脱敏 stderr 文本。
//
// 边界：这是"尽力而为"的文本脱敏，不能替代"不把密钥写进日志"。
// 未在规则内的自定义凭证格式仍可能落到 1 MiB 环形缓冲里。
func redactText(s string) string {
	for _, rule := range redactionRules {
		s = rule.re.ReplaceAllString(s, rule.repl)
	}
	return s
}
