// 命令 devtools 是“不会说谎的检查编排器”。
// 它只编排真实检查、保留子进程真实退出码与原始 stdout/stderr，
// 绝不写 passed.json 冒充执行。
//
// 用法（仓库根目录运行）：
//
//	go run ./cmd/devtools verify --suite=static
//	go run ./cmd/devtools verify --suite=unit
//	go run ./cmd/devtools verify --suite=integration --target=<targetId>
//	go run ./cmd/devtools verify --suite=integration --target=<targetId> --gate=G2 --case=MEM01,MEM02
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
	// 所有相对路径（案例索引、版本锁、证据树）都以仓库根为基准。
	// 从子目录调用时先切到仓库根，避免"索引读不到/证据写错地方"的静默错位。
	if err := devtools.EnsureRepoRoot(); err != nil {
		fmt.Fprintf(os.Stderr, "定位仓库根失败: %v\n", err)
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
		gate     string
		caseList string
	)
	fs.StringVar(&suite, "suite", "", "verify 的 suite：static|unit|integration|native|e2e|package")
	fs.StringVar(&target, "target", "", "目标 targetId")
	fs.StringVar(&display, "display", "", "native 用的 session 标识")
	fs.StringVar(&artifact, "artifact", "", "package 校验用的产物绝对路径")
	fs.StringVar(&channel, "channel", "internal", "package 渠道")
	fs.StringVar(&id, "id", "", "gate id：G0..G5")
	fs.StringVar(&evidence, "evidence", "", "gate 证据根目录（绝对路径）")
	fs.StringVar(&gate, "gate", "G0", "integration 要产出的门禁证据（G0..G5），默认 G0")
	// --case 是**开发期**单 case 取证加速器：只缩小本次产出哪些 case 的证据，
	// 不改变 gate 判定，也不能用来宣称某个 gate 通过（gate 永远按完整矩阵评估）。
	// 只对 --suite=integration 有意义；其它 suite 带了它就是配置错误。
	fs.StringVar(&caseList, "case", "", "开发用：只跑指定 case（逗号分隔，必须是该 gate 的必需 case）；不构成 gate 通过结论")

	if err := fs.Parse(args); err != nil {
		// flag 解析失败（如未知 flag）视为配置错误。
		os.Exit(2)
	}

	var code int
	switch cmd {
	case "verify":
		if suite == "integration" {
			// 证据按 gate 分目录：同一个 case 可能同时属于 G0 与 G2，
			// 不分开会让两份报告互相覆盖或串用。
			code = devtools.RunIntegrationForGateWithCases(target, gate, splitCaseFilter(caseList))
		} else {
			if strings.TrimSpace(caseList) != "" {
				// --case 只对 integration 有效：静默忽略会让一次
				// `--suite=unit --case=X` 看起来“跑过了 X”，实际跑的是全量单元测试。
				fmt.Fprintf(os.Stderr, "--case 只支持 --suite=integration（当前 suite=%q）\n", suite)
				os.Exit(2)
			}
			code = devtools.RunVerify(suite, target, display, artifact)
		}
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

// splitCaseFilter 把 --case=MEM01,MEM02 拆成切片；空串返回 nil（不过滤）。
// 空片段不在这里吞掉：交给 devtools.filterIntegrationCases 判配置错误，
// 否则 "MEM01,,MEM02" 会静默变成两条，和用户写的不是一回事。
func splitCaseFilter(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p)
	}
	return out
}
