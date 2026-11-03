// 命令 devtools 是“不会说谎的检查编排器”。
// 它只编排真实检查、保留子进程真实退出码与原始 stdout/stderr，
// 绝不写 passed.json 冒充执行。
//
// 用法（仓库根目录运行）：
//
//	go run ./cmd/devtools verify --suite=static
//	go run ./cmd/devtools verify --suite=unit
//	go run ./cmd/devtools verify --suite=integration --target=<targetId>
//	go run ./cmd/devtools verify --suite=native --target=<targetId> --display=<session>
//	go run ./cmd/devtools verify --suite=e2e --target=<targetId>
//	go run ./cmd/devtools package --target=<targetId> --channel=internal
//	go run ./cmd/devtools verify --suite=package --target=<targetId> --artifact=<abs-path>
//	go run ./cmd/devtools gate --id=G0 --evidence=<abs-evidence-root>
//
// 退出码：0 通过；1 断言失败；2 配置/索引/目标/门禁缺失或未知。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"my-agent-go/internal/devtools"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: devtools <verify|package|gate> [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var (
		suite    string
		target   string
		display  string
		artifact string
		channel  string
		id       string
		evidence string
	)
	fs.StringVar(&suite, "suite", "", "verify 的 suite：static|unit|integration|native|e2e|package")
	fs.StringVar(&target, "target", "", "目标 targetId")
	fs.StringVar(&display, "display", "", "native 用的 session 标识")
	fs.StringVar(&artifact, "artifact", "", "package 校验用的产物绝对路径")
	fs.StringVar(&channel, "channel", "internal", "package 渠道")
	fs.StringVar(&id, "id", "", "gate id：G0..G5")
	fs.StringVar(&evidence, "evidence", "", "gate 证据根目录（绝对路径）")

	if err := fs.Parse(args); err != nil {
		// flag 解析失败（如未知 flag）视为配置错误。
		os.Exit(2)
	}

	var code int
	switch cmd {
	case "verify":
		code = devtools.RunVerify(suite, target, display, artifact)
	case "package":
		code = devtools.RunPackage(target, channel)
	case "gate":
		code = devtools.RunGate(id, evidence)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s（已知: verify|package|gate）\n", cmd)
		code = 2
	}
	if code < 0 || code > 2 {
		code = 2
	}
	_ = strings.TrimSpace
	os.Exit(code)
}
