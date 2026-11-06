// Command executor 是 SEC01/SEC05 外部观察用的**独立执行进程**。
//
// 为什么要有它：策略服务（internal/policy）本身不执行副作用，只回答"允不允许"。
// 如果"执行"仍由测试进程里的闭包完成，"被拒路径没有副作用"就只是同一个进程的
// 自述。这里让真正的写入发生在**另一个进程**里，并由它自己把每次执行追加进
// executions.jsonl；测试只读回账本与磁盘，不参与记录。
//
// 它只做两件事：
//  1. 以 O_APPEND 追加一行 JSON（时间戳、pid、标签、工具名、目标路径、argv）；
//  2. 写出目标文件（真正的副作用）。
//
// 参数由调用方给出，本进程不判断策略——判断是策略服务的事。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type record struct {
	TS     string   `json:"ts"`
	PID    int      `json:"pid"`
	Label  string   `json:"label"`
	Tool   string   `json:"tool"`
	Target string   `json:"target"`
	Argv   []string `json:"argv"`
}

func main() {
	logPath := flag.String("log", "", "执行账本（JSONL，追加写）")
	label := flag.String("label", "", "本次执行在用例里的标签")
	tool := flag.String("tool", "", "工具名")
	target := flag.String("path", "", "被写出的目标文件")
	content := flag.String("content", "", "目标文件内容")
	flag.Parse()

	if *logPath == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "executor: -log 与 -path 必填")
		os.Exit(2)
	}

	targetAbs, err := filepath.Abs(*target)
	if err != nil {
		targetAbs = *target
	}
	rec := record{
		TS:     time.Now().UTC().Format(time.RFC3339Nano),
		PID:    os.Getpid(),
		Label:  *label,
		Tool:   *tool,
		Target: targetAbs,
		Argv:   os.Args,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "executor: 编码记录失败: %v\n", err)
		os.Exit(3)
	}

	f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "executor: 打开账本失败: %v\n", err)
		os.Exit(4)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		fmt.Fprintf(os.Stderr, "executor: 追加账本失败: %v\n", err)
		os.Exit(5)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "executor: 关闭账本失败: %v\n", err)
		os.Exit(6)
	}

	// 副作用：真正写出目标文件。父目录不存在即失败——不替调用方创建目录，
	// 否则"越界写"可能因为自动建目录而变成"成功"。
	if err := os.WriteFile(targetAbs, []byte(*content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "executor: 写目标文件失败: %v\n", err)
		os.Exit(7)
	}
}
