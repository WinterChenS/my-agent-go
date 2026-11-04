package devtools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// npmCmd 是本机基线指定的 npm 入口（AGENTS.md：必须用 npm.cmd，且 PATH 首个
// node 是 v22 会误导，故显式使用 D:\Program Files\nodejs\npm.cmd）。
const npmCmd = `D:\Program Files\nodejs\npm.cmd`

// RunExternal 执行外部命令并保留其真实退出码。
// 命令成功运行但断言失败：返回其真实退出码（通常 1）。
// 命令无法启动（如二进制不存在 / runner 不存在）：返回 2 并附带 error。
// 原始 stdout/stderr 原样返回，绝不静默吞掉。
func RunExternal(name string, args ...string) (int, string, string, error) {
	cmd := exec.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()
	if runErr == nil {
		return 0, out.String(), errb.String(), nil
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return exitErr.ExitCode(), out.String(), errb.String(), nil
	}
	// 命令无法启动（二进制缺失 / runner 不存在）：视为配置缺失，退出 2。
	return 2, out.String(), errb.String(), runErr
}

// RunVerify 是 `verify` 子命令入口。suite 必须属于已知集合；
// 未知 suite 返回 2；F01 未实现的 runner 也返回 2，绝不自动发现空目录判成功。
func RunVerify(suite, target, display, artifact string) int {
	switch suite {
	case "static":
		return runStatic()
	case "unit":
		return runUnit()
	case "integration":
		return RunIntegration(target)
	case "native", "e2e", "package":
		return unimplementedSuite(suite)
	case "":
		fmt.Fprintln(os.Stderr, "verify 必须指定 --suite 参数")
		return 2
	default:
		fmt.Fprintf(os.Stderr, "未知 suite: %s（已知: static,unit,integration,native,e2e,package）\n", suite)
		return 2
	}
}

// RunPackage 是 `package` 子命令入口。打包属 X03/G4 范围，F01 不实现，返回 2。
func RunPackage(target, channel string) int {
	fmt.Println("[FAIL] package 命令在 F01 尚未实现（打包属 X03/G4 范围）；不能冒充通过，退出 2。")
	return 2
}

func unimplementedSuite(suite string) int {
	fmt.Printf("[FAIL] verify --suite=%s 的 runner 在 F01 尚未实现；不能自动发现空目录判成功，退出 2。\n", suite)
	return 2
}

func runStatic() int {
	fmt.Println("== verify --suite=static ==")
	code := 0

	// 1) gofmt -l：列出未格式化文件；非空即失败（退出 1，断言类）。
	c, out, _, _ := RunExternal("gofmt", "-l", ".")
	_ = c
	if strings.TrimSpace(out) != "" {
		fmt.Printf("[FAIL] gofmt 存在未格式化文件:\n%s\n", out)
		if code < 1 {
			code = 1
		}
	} else {
		fmt.Println("[PASS] gofmt 无未格式化文件")
	}

	// 2) go vet ./...
	vc, vout, verr, vrunErr := RunExternal("go", "vet", "./...")
	if vc != 0 {
		fmt.Printf("[FAIL] go vet 失败(退出 %d):\n%s\n%s\n", vc, vout, verr)
		if vrunErr != nil || vc == 2 {
			code = 2
		} else if code < 1 {
			code = 1
		}
	} else {
		fmt.Println("[PASS] go vet ./...")
	}

	// 3) 前端 typecheck（必须真实存在脚本）
	tc, tout, terr, terr2 := runFrontendTypecheck()
	if tc != 0 {
		fmt.Printf("[FAIL] 前端 typecheck 失败(退出 %d):\n%s\n%s\n", tc, tout, terr)
		if terr2 != nil || tc == 2 {
			code = 2
		} else if code < 1 {
			code = 1
		}
	} else {
		fmt.Println("[PASS] 前端 typecheck")
	}

	fmt.Printf("static 退出码: %d\n", code)
	return code
}

func runUnit() int {
	fmt.Println("== verify --suite=unit ==")
	code := 0

	// go test -json ./internal/...
	gc, gout, gerr, gerr2 := RunExternal("go", "test", "-json", "./internal/...")
	if gc != 0 {
		fmt.Printf("[FAIL] go test ./internal/... 失败(退出 %d):\n%s\n%s\n", gc, gout, gerr)
		if gerr2 != nil || gc == 2 {
			code = 2
		} else if code < 1 {
			code = 1
		}
	} else {
		fmt.Println("[PASS] go test ./internal/...")
	}

	// 前端测试 runner：脚本不存在必须明确报 runner 不存在并以 2 退出，不能静默跳过。
	tc, tout, terr, terr2 := runFrontendTest()
	if tc != 0 {
		fmt.Printf("[FAIL] 前端测试未通过(退出 %d):\n%s\n%s\n", tc, tout, terr)
		if terr2 != nil || tc == 2 {
			code = 2
		} else if code < 1 {
			code = 1
		}
	}

	fmt.Printf("unit 退出码: %d\n", code)
	return code
}

// runFrontendTypecheck 真实执行前端 typecheck；脚本缺失→2（runner 不存在）。
func runFrontendTypecheck() (int, string, string, error) {
	const pkgPath = "frontend/package.json"
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return 2, "", fmt.Sprintf("读取 %s 失败: %v", pkgPath, err), err
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return 2, "", fmt.Sprintf("解析 %s 失败: %v", pkgPath, err), err
	}
	if _, ok := pkg.Scripts["typecheck"]; !ok {
		return 2, "", "前端缺少 typecheck 脚本（runner 不存在）", nil
	}
	return RunExternal(npmCmd, "--prefix", "frontend", "run", "typecheck")
}

// runFrontendTest 真实执行前端测试；脚本缺失→明确报 runner 不存在并以 2 退出。
func runFrontendTest() (int, string, string, error) {
	const pkgPath = "frontend/package.json"
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return 2, "", fmt.Sprintf("读取 %s 失败: %v", pkgPath, err), err
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return 2, "", fmt.Sprintf("解析 %s 失败: %v", pkgPath, err), err
	}
	if _, ok := pkg.Scripts["test"]; !ok {
		return 2, "", "前端缺少 test 脚本（runner 不存在），不能静默跳过判成功", nil
	}
	return RunExternal(npmCmd, "--prefix", "frontend", "run", "test", "--", "--run")
}
