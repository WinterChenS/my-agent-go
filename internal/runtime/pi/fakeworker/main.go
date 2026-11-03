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
	mode := flag.String("mode", "echo", "echo|outoforder|stderrflood|stdoutflood|malformed|grandchild|hang")
	expect := flag.Int("expect", 0, "outoforder：先收满 N 条命令再倒序回复")
	floodBytes := flag.Int("flood-bytes", 4<<20, "洪泛总字节数")
	marker := flag.String("marker", "", "grandchild 子进程的心跳文件")
	pidFile := flag.String("pidfile", "", "把自身 pid 写入该文件")
	child := flag.Bool("child", false, "以孙进程身份运行：只写心跳，不读 stdin")
	flag.Parse()

	if *pidFile != "" {
		_ = os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}

	if *child {
		runChildHeartbeat(*marker)
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
