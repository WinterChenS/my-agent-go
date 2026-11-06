// Command fakeworker 是一个可脚本化的假 Pi RPC 进程，只用于 F03 的负例与边界测试。
//
// 它不是 Pi 的替代品：真实协议基线来自 F02 对 Pi 1.5.0 的捕获
// （internal/runtime/pi/testdata/v1.5.0/）。凡是"Pi 是否真的这样行为"的问题，
// 以真实捕获为准；fakeworker 只负责稳定复现异常时序（乱序、洪泛、畸形记录、孙进程）。
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	mode := flag.String("mode", "echo", "echo|outoforder|stderrflood|stdoutflood|malformed|grandchild|hang|crashafterwrite|contract")
	expect := flag.Int("expect", 0, "outoforder：先收满 N 条命令再倒序回复")
	floodBytes := flag.Int("flood-bytes", 4<<20, "洪泛总字节数")
	marker := flag.String("marker", "", "grandchild 子进程的心跳文件")
	pidFile := flag.String("pidfile", "", "把自身 pid 写入该文件")
	child := flag.Bool("child", false, "以孙进程身份运行：只写心跳，不读 stdin")
	counter := flag.String("counter", "", "crashafterwrite：副作用计数器文件，每执行一次写入追加一行")
	wait := flag.String("wait", "", "crashafterwrite：回应 prompt 后等这个文件出现（上限 10s）再制造崩溃")
	recordArgv := flag.String("record-argv", "", "把本进程实际收到的 argv 写成 JSON（观察用）")
	recordEnv := flag.String("record-env", "", "把本进程实际收到的环境变量写成 JSON（观察用）")
	recordFrames := flag.String("record-frames", "", "把本进程实际收到的每一帧追加写成 JSONL（观察用）")
	recordIO := flag.String("record-io", "", "把本进程的每次读写（方向/字节数/时间戳）追加写成 JSONL（观察用）")
	// 真 Pi 的隔离参数（-na/-nc/--no-session/--tools read …）不是 fakeworker 的参数。
	// CFG03/RES04 要求把**同一批**参数原样交给子进程，所以这里只把 fakeworker 自己
	// 认识的参数交给 flag 包；其余原样留在 os.Args 里，由 -record-argv 落盘。
	// 以前的做法是"干脆不传隔离参数"，那测出来的 argv 不是产品真正会发出的那一条。
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	if err := flag.CommandLine.Parse(ownArgs(os.Args[1:])); err != nil {
		fmt.Fprintf(os.Stderr, "fakeworker 参数错误: %v\n", err)
		os.Exit(2)
	}

	// 也允许从环境变量给路径：这样 -record-argv 不必出现在 argv 里，
	// 观察到的 argv 就是产品真正会发出的那一条（CFG03 要求 argv 不被观察装置污染）。
	if *recordArgv == "" {
		*recordArgv = os.Getenv("FAKEWORKER_RECORD_ARGV")
	}
	if *recordEnv == "" {
		*recordEnv = os.Getenv("FAKEWORKER_RECORD_ENV")
	}
	if *recordFrames == "" {
		*recordFrames = os.Getenv("FAKEWORKER_RECORD_FRAMES")
	}
	if *recordIO == "" {
		*recordIO = os.Getenv("FAKEWORKER_RECORD_IO")
	}

	if *recordArgv != "" {
		if err := writeJSONFile(*recordArgv, os.Args); err != nil {
			fmt.Fprintf(os.Stderr, "写 -record-argv 失败: %v\n", err)
			os.Exit(5)
		}
	}
	if *recordEnv != "" {
		env := os.Environ()
		sort.Strings(env)
		if err := writeJSONFile(*recordEnv, env); err != nil {
			fmt.Fprintf(os.Stderr, "写 -record-env 失败: %v\n", err)
			os.Exit(5)
		}
	}

	if *pidFile != "" {
		_ = os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}

	if *child {
		runChildHeartbeat(*marker)
		return
	}

	if *mode == "crashafterwrite" {
		runCrashAfterWrite(*counter, *wait)
		return
	}

	// IO 记录器必须早于任何洪泛 goroutine 启动，否则前几块字节就漏记了。
	var ioRec *ioRecorder
	if *recordIO != "" {
		var err error
		ioRec, err = newIORecorder(*recordIO)
		if err != nil {
			fmt.Fprintf(os.Stderr, "打开 -record-io 失败: %v\n", err)
			os.Exit(5)
		}
		defer func() { _ = ioRec.close() }()
	}

	switch *mode {
	case "stderrflood":
		go floodStderr(*floodBytes, ioRec)
	case "stdoutflood":
		go floodStdout(*floodBytes, ioRec)
	case "malformed":
		emitMalformed()
	}

	// 帧记录器必须在读取循环之前就绪：漏掉第一帧就等于没观察。
	var rec *frameRecorder
	if *recordFrames != "" {
		var err error
		rec, err = newFrameRecorder(*recordFrames)
		if err != nil {
			fmt.Fprintf(os.Stderr, "打开 -record-frames 失败: %v\n", err)
			os.Exit(5)
		}
		defer func() { _ = rec.close() }()
	}

	serve(*mode, *expect, *marker, rec, ioRec)
}

// runChildHeartbeat 持续向 marker 文件追加行；测试通过比对前后行数判断它是否还活着。
func runChildHeartbeat(marker string) {
	if marker == "" {
		os.Exit(2)
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = f.Close() }()
	for i := 0; ; i++ {
		if _, err := fmt.Fprintf(f, "%d\n", i); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// runCrashAfterWrite 复现 RUN05 的场景：工具已经写完，进程随即崩掉，没有 agent_settled。
//
// 顺序不能换：
//  1. 先回复 prompt——否则上层 Call 会把它判成「命令结果未知」，run 直接 failed，
//     测出来的就不是「写入后崩溃」而是「启动失败」；
//  2. 等测试侧的信号文件（-wait）出现——不在崩溃前 sleep：sleep 是在和上层
//     launch 的状态迁移赛跑，信号文件把「先 running、后崩溃」变成确定性时序；
//  3. 发 tool_execution_start、执行真实写入（追加计数器）、发 tool_execution_end；
//  4. 退出码 1 且不发 agent_settled：真实崩溃就是「没有最终 idle」。
//
// 副作用写在子进程里，不在测试进程里：这是 RUN05 能用 fixture-counter 作为
// 外部观察源的前提。
func runCrashAfterWrite(counter, wait string) {
	if counter == "" {
		fmt.Fprint(os.Stderr, "crashafterwrite 需要 -counter\n")
		os.Exit(2)
	}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			var cmd struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(s), &cmd) == nil && cmd.Type != "" {
				fmt.Fprint(os.Stdout, makeResponse(cmd.ID, cmd.Type)+"\n")
				break
			}
		}
		if err != nil {
			os.Exit(2)
		}
	}

	// 等测试侧信号文件：测试先确认 run 已进入 running，再落这个文件。
	// 超时就直接报错退出（不静默继续）——宁可让 case 在结构上失败，也不要
	// 让崩溃发生在 running 之前而把「写入后崩溃」测成「启动失败」。
	if wait != "" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, statErr := os.Stat(wait); statErr == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintf(os.Stderr, "crashafterwrite: 等待信号文件 %s 超时\n", wait)
				os.Exit(4)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// 形状取自真实捕获 internal/runtime/pi/testdata/v1.5.0/tool.jsonl。
	fmt.Fprint(os.Stdout, `{"type":"tool_execution_start","toolCallId":"call_crash_1","toolName":"fixture_write","args":{"path":"out.txt","content":"payload"}}`+"\n")
	f, err := os.OpenFile(counter, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open counter: %v\n", err)
		os.Exit(3)
	}
	_, _ = fmt.Fprintln(f, "fixture_write")
	_ = f.Close()
	fmt.Fprint(os.Stdout, `{"type":"tool_execution_end","toolCallId":"call_crash_1","toolName":"fixture_write","result":{"content":[{"type":"text","text":"wrote out.txt"}],"structuredContent":{"exit_code":0,"output":""}},"isError":false,"durationMs":7}`+"\n")

	// 故意不发 agent_settled：崩溃就是「没有最终 idle」。
	os.Exit(1)
}

func floodStderr(total int, ioRec *ioRecorder) {
	line := strings.Repeat("e", 120)
	written := 0
	nextMark := 1 << 20
	for written < total {
		n, err := fmt.Fprintf(os.Stderr, "stderr-flood %08d %s\n", written, line)
		if err != nil {
			ioRec.event("write", "stderr", written, "flood_aborted", "")
			return
		}
		written += n
		if written >= nextMark {
			ioRec.event("write", "stderr", written, "flood_progress", "")
			nextMark = written + 1<<20
		}
	}
	// 这条记录就是“stderr 全部写完了”的外部证据：管道没人读时不可能写完。
	ioRec.event("write", "stderr", written, "flood_done", "")
}

func floodStdout(total int, ioRec *ioRecorder) {
	line := `{"type":"flood","pad":"` + strings.Repeat("f", 160) + `"}`
	written := 0
	nextMark := 1 << 20
	for written < total {
		n, err := fmt.Fprint(os.Stdout, line+"\n")
		if err != nil {
			ioRec.event("write", "stdout", written, "flood_aborted", "")
			return
		}
		written += n
		if written >= nextMark {
			ioRec.event("write", "stdout", written, "flood_progress", "")
			nextMark = written + 1<<20
		}
	}
	// 同上：洪泛写完 = 父进程的读循环没有被事件队列反压死。
	ioRec.event("write", "stdout", written, "flood_done", "")
}

// emitMalformed 输出四类异常记录，覆盖 RPC04 与 RPC02 的诊断要求：
// 非法 JSON、缺 type、未来版本才有的未知 type、无主的 response id。
func emitMalformed() {
	fmt.Fprint(os.Stdout, "{\"broken\": \n")
	fmt.Fprint(os.Stdout, "{\"no_type_field\":true}\n")
	fmt.Fprint(os.Stdout, "{\"type\":\"agent_settled_v2\",\"aborted\":false}\n")
	fmt.Fprint(os.Stdout, "{\"type\":\"response\",\"id\":\"ghost-1\",\"success\":true}\n")
}

// contractTurn 记录 contract 模式的回合状态：prompt 之后 500ms 内没收到 abort
// 就正常结束（agent_settled{aborted:false}），收到 abort 就立刻结束并标记 aborted:true。
//
// 时间窗不是"随便 sleep"：契约探测的 abort 检查必须在回合进行中发出 abort，
// 而 settled 检查又必须能在没有 abort 时自然收敛。窗口把两者变成确定性时序。
type contractTurn struct {
	mu      sync.Mutex
	running bool
	aborted bool
}

func serve(mode string, expect int, marker string, rec *frameRecorder, ioRec *ioRecorder) {
	in := bufio.NewReader(os.Stdin)
	var mu sync.Mutex
	var pending []string
	var spawnOnce sync.Once
	var turn contractTurn

	write := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprint(os.Stdout, s+"\n")
		kind, id := lineKindID(s)
		ioRec.event("write", "stdout", len(s)+1, kind, id)
	}

	for {
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			var cmd struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			parsed := json.Unmarshal([]byte(line), &cmd) == nil
			if parsed {
				ioRec.event("read", "stdin", len(line)+1, cmd.Type, cmd.ID)
			}
			if rec != nil {
				// 记录的是**本进程实际收到的那一行**（含 id、含多字节字符），
				// 父进程据此核对“发出去的字节是否原样到达”。
				kind := cmd.Type
				if !parsed || kind == "" {
					kind = "unparsed"
				}
				rec.record(kind, line)
			}
			if parsed && cmd.Type != "" {
				switch {
				case mode == "contract" && cmd.Type == "get_commands":
					// 形状取自真实捕获 internal/runtime/pi/testdata/v1.5.0/extension.jsonl：
					// 扩展与 MCP 都以 get_commands 的 commands 记录暴露。
					write(makeCommandsResponse(cmd.ID))
				case mode == "contract" && cmd.Type == "prompt":
					write(makeResponse(cmd.ID, cmd.Type))
					write(`{"type":"agent_start"}`)
					turn.mu.Lock()
					turn.running = true
					turn.aborted = false
					turn.mu.Unlock()
					time.AfterFunc(500*time.Millisecond, func() {
						turn.mu.Lock()
						if !turn.running {
							turn.mu.Unlock()
							return
						}
						aborted := turn.aborted
						turn.running = false
						turn.mu.Unlock()
						if aborted {
							write(`{"type":"agent_settled","aborted":true}`)
							return
						}
						write(`{"type":"agent_settled","aborted":false}`)
					})
				case mode == "contract" && cmd.Type == "abort":
					write(makeResponse(cmd.ID, cmd.Type))
					turn.mu.Lock()
					wasRunning := turn.running
					turn.running = false
					turn.aborted = true
					turn.mu.Unlock()
					if wasRunning {
						write(`{"type":"agent_settled","aborted":true}`)
					}
				case mode == "hang":
					// 故意不响应：让上层走到超时/取消分支。
				case mode == "outoforder" && expect > 0:
					pending = append(pending, makeResponse(cmd.ID, cmd.Type))
					if len(pending) >= expect {
						for i := len(pending) - 1; i >= 0; i-- {
							write(pending[i])
						}
						pending = nil
					}
				case mode == "grandchild":
					spawnOnce.Do(func() { spawnChild(marker) })
					write(makeResponse(cmd.ID, cmd.Type))
				case mode == "duplicate":
					// 同一 id 连发两次：第二次必须是"未知 id"，不能被当成新命令的响应。
					write(makeResponse(cmd.ID, cmd.Type))
					write(makeDuplicateResponse(cmd.ID, cmd.Type))
				default:
					write(makeResponse(cmd.ID, cmd.Type))
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func makeResponse(id, command string) string {
	if id == "" {
		return `{"type":"response","command":"` + command + `","success":false,"error":"missing id"}`
	}
	return `{"id":` + strconv.Quote(id) + `,"type":"response","command":` +
		strconv.Quote(command) + `,"success":true,"data":{"echo":` + strconv.Quote(command) + `}}`
}

// makeCommandsResponse 复现 get_commands 的真实响应形状（扩展来源命令 + MCP 命令）。
func makeCommandsResponse(id string) string {
	return `{"id":` + strconv.Quote(id) + `,"type":"response","command":"get_commands","success":true,"data":{"commands":[` +
		`{"name":"fixture-ext","description":"F08-B contract probe extension","source":"extension"},` +
		`{"name":"mcp","description":"Manage MCP servers: sign in, reconnect, enable or disable, and change exposure","source":"extension"}` +
		`]}}`
}

func makeDuplicateResponse(id, command string) string {
	return `{"id":` + strconv.Quote(id) + `,"type":"response","command":` +
		strconv.Quote(command) + `,"success":true,"data":{"echo":"duplicate"}}`
}

func spawnChild(marker string) {
	if marker == "" {
		return
	}
	cmd := exec.Command(os.Args[0], "-child", "-marker", marker)
	// 不把父的 stdio 传给它：既避免污染协议流，也让测试专注于"进程是否被回收"。
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "spawn grandchild failed: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "spawned grandchild pid=%d\n", cmd.Process.Pid)
	// 故意不 Wait：让孙进程成为孤儿，只有进程树回收机制能带走它。
}

// ownArgs 从收到的参数里挑出 fakeworker 自己认识的参数，其余（真 Pi 的隔离参数、
// 未来新增的 Pi 开关）原样忽略——不认识的参数不能把子进程直接顶死，
// 否则"子进程收到了什么"这件事永远观察不到。
func ownArgs(args []string) []string {
	// 需要取值的参数；-child 是布尔开关，不能吞掉后面的 token。
	valueFlags := map[string]bool{
		"-mode": true, "-expect": true, "-flood-bytes": true, "-marker": true,
		"-pidfile": true, "-counter": true, "-wait": true,
		"-record-argv": true, "-record-env": true, "-record-frames": true, "-record-io": true,
	}
	boolFlags := map[string]bool{"-child": true}

	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := a
		hasValue := false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name = a[:eq]
			hasValue = true
		}
		switch {
		case valueFlags[name]:
			out = append(out, a)
			if !hasValue && i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			}
		case boolFlags[name]:
			out = append(out, a)
		default:
			// 不属于 fakeworker：丢掉，让隔离参数原样出现在 -record-argv 里。
		}
	}
	return out
}

// writeJSONFile 把观察值写到子进程自己的目录（父进程只读，不代写）。
func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// frameRecorder 把收到的每一帧追加成 JSONL：seq / kind / bytes / sha256 / payload。
//
// 只记录"我收到了什么"，不解释语义：父进程从文件里读回事实，
// 而不是让子进程替它下结论。
type frameRecorder struct {
	mu  sync.Mutex
	f   *os.File
	seq int
	err error
}

func newFrameRecorder(path string) (*frameRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &frameRecorder{f: f}, nil
}

func (r *frameRecorder) record(kind, payload string) {
	if r == nil {
		return
	}
	sum := sha256.Sum256([]byte(payload))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	entry := map[string]any{
		"seq":     r.seq,
		"kind":    kind,
		"bytes":   len(payload),
		"sha256":  hex.EncodeToString(sum[:]),
		"payload": payload,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		r.err = err
		return
	}
	if _, err := r.f.Write(append(b, '\n')); err != nil {
		r.err = err
	}
}

func (r *frameRecorder) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		fmt.Fprintf(os.Stderr, "写 -record-frames 出错: %v\n", r.err)
	}
	return r.f.Close()
}

// ioRecorder 把本进程的每次读写追加成 JSONL：
// {seq, dir(read|write), stream(stdin|stdout|stderr), bytes, kind, id, tsNano}。
//
// 对 stdin/stdout 来说，这些字节**就是产品另一端收到/发出的字节**：
// 父进程把自己收到的东西与这份账对比，就能区分"子进程没写"和"写了但丢了"。
// 洪泛只按 1 MiB 打进度点，避免几十万条记录把观察文件本身撑成问题。
type ioRecorder struct {
	mu  sync.Mutex
	f   *os.File
	seq int
	err error
}

func newIORecorder(path string) (*ioRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &ioRecorder{f: f}, nil
}

// event 记一条读写。rec 为 nil 时是空操作（未启用观察的 focused 运行）。
func (r *ioRecorder) event(dir, stream string, bytes int, kind, id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	entry := map[string]any{
		"seq":    r.seq,
		"dir":    dir,
		"stream": stream,
		"bytes":  bytes,
		"kind":   kind,
		"id":     id,
		"tsNano": time.Now().UnixNano(),
	}
	b, err := json.Marshal(entry)
	if err != nil {
		r.err = err
		return
	}
	if _, err := r.f.Write(append(b, '\n')); err != nil {
		r.err = err
	}
}

func (r *ioRecorder) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		fmt.Fprintf(os.Stderr, "写 -record-io 出错: %v\n", r.err)
	}
	return r.f.Close()
}

// lineKindID 从一条协议记录里取 type 与 id（取不到就给空串）：
// 读侧据此把"收到的帧"与"回出去的响应"对上号，不必让子进程解释语义。
func lineKindID(line string) (string, string) {
	var rec struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &rec) != nil {
		return "", ""
	}
	return rec.Type, rec.ID
}
