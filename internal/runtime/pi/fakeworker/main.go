// Command fakeworker 是一个可脚本化的假 Pi RPC 进程，只用于 F03 的负例与边界测试。
//
// 它不是 Pi 的替代品：真实协议基线来自 F02 对 Pi 1.5.0 的捕获
// （internal/runtime/pi/testdata/v1.5.0/）。凡是"Pi 是否真的这样行为"的问题，
// 以真实捕获为准；fakeworker 只负责稳定复现异常时序（乱序、洪泛、畸形记录、孙进程）。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	mode := flag.String("mode", "echo", "echo|outoforder|stderrflood|stdoutflood|malformed|grandchild|hang|crashafterwrite")
	expect := flag.Int("expect", 0, "outoforder：先收满 N 条命令再倒序回复")
	floodBytes := flag.Int("flood-bytes", 4<<20, "洪泛总字节数")
	marker := flag.String("marker", "", "grandchild 子进程的心跳文件")
	pidFile := flag.String("pidfile", "", "把自身 pid 写入该文件")
	child := flag.Bool("child", false, "以孙进程身份运行：只写心跳，不读 stdin")
	counter := flag.String("counter", "", "crashafterwrite：副作用计数器文件，每执行一次写入追加一行")
	wait := flag.String("wait", "", "crashafterwrite：回应 prompt 后等这个文件出现（上限 10s）再制造崩溃")
	flag.Parse()

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

	switch *mode {
	case "stderrflood":
		go floodStderr(*floodBytes)
	case "stdoutflood":
		go floodStdout(*floodBytes)
	case "malformed":
		emitMalformed()
	}

	serve(*mode, *expect, *marker)
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

func floodStderr(total int) {
	line := strings.Repeat("e", 120)
	written := 0
	for written < total {
		n, err := fmt.Fprintf(os.Stderr, "stderr-flood %08d %s\n", written, line)
		if err != nil {
			return
		}
		written += n
	}
}

func floodStdout(total int) {
	line := `{"type":"flood","pad":"` + strings.Repeat("f", 160) + `"}`
	written := 0
	for written < total {
		n, err := fmt.Fprint(os.Stdout, line+"\n")
		if err != nil {
			return
		}
		written += n
	}
}

// emitMalformed 输出四类异常记录，覆盖 RPC04 与 RPC02 的诊断要求：
// 非法 JSON、缺 type、未来版本才有的未知 type、无主的 response id。
func emitMalformed() {
	fmt.Fprint(os.Stdout, "{\"broken\": \n")
	fmt.Fprint(os.Stdout, "{\"no_type_field\":true}\n")
	fmt.Fprint(os.Stdout, "{\"type\":\"agent_settled_v2\",\"aborted\":false}\n")
	fmt.Fprint(os.Stdout, "{\"type\":\"response\",\"id\":\"ghost-1\",\"success\":true}\n")
}

func serve(mode string, expect int, marker string) {
	in := bufio.NewReader(os.Stdin)
	var mu sync.Mutex
	var pending []string
	var spawnOnce sync.Once

	write := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprint(os.Stdout, s+"\n")
	}

	for {
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			var cmd struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(line), &cmd) == nil && cmd.Type != "" {
				switch {
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
