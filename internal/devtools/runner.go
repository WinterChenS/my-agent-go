package devtools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	case "native":
		return RunNative(target, display)
	case "e2e", "package":
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
	//
	// 只检查"仓库内、未被 gitignore"的 Go 文件：`gofmt -l .` 会把 .tmp/ 的临时
	// 探针、.tools/ 的构建产物也算进去，于是本地 scratch 代码会让 static 变红——
	// 那测的是工作习惯，不是产品代码。
	files, ferr := goSourceFiles()
	if ferr != nil {
		fmt.Printf("[FAIL] 无法列出 Go 源文件（配置缺失）: %v\n", ferr)
		return 2
	}
	c, out, _, _ := RunExternal("gofmt", append([]string{"-l"}, files...)...)
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
	} else {
		// 通过时必须留一行凭证：没有输出的话，"前端测试跑了并通过"与
		// "前端测试压根没被调度"在日志里长得一模一样。
		fmt.Println("[PASS] 前端测试")
	}

	fmt.Printf("unit 退出码: %d\n", code)
	return code
}

// repoFile 从当前目录向上找 go.mod，返回仓库内相对路径对应的绝对路径。
//
// 为什么需要：go test 的 cwd 是包目录，devtools 被测试调用时 cwd 不是仓库根。
// 直接 os.ReadFile("frontend/package.json") 会在测试里失败并返回 2——
// 那是“路径错了”，不是“runner 缺失”，两者都会让 GOV04 的断言假绿。
func repoFile(rel string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(wd, "go.mod")); statErr == nil {
			return filepath.Join(wd, rel), nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("找不到仓库根（go.mod）：%s", rel)
		}
		wd = parent
	}
}

// frontendScript 校验 package.json 里有指定脚本；不存在→2（runner 不存在）。
// 只做存在性判定，不执行任何命令——测试可以放心调用。
func frontendScript(pkgPath, name string) (int, string, error) {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return 2, fmt.Sprintf("读取 %s 失败: %v", pkgPath, err), err
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return 2, fmt.Sprintf("解析 %s 失败: %v", pkgPath, err), err
	}
	if _, ok := pkg.Scripts[name]; !ok {
		return 2, fmt.Sprintf("前端缺少 %s 脚本（runner 不存在）", name), nil
	}
	return 0, "", nil
}

// runFrontendTypecheck 真实执行前端 typecheck；脚本缺失→2（runner 不存在）。
func runFrontendTypecheck() (int, string, string, error) {
	pkgPath, err := repoFile("frontend/package.json")
	if err != nil {
		return 2, "", err.Error(), err
	}
	if code, msg, err := frontendScript(pkgPath, "typecheck"); code != 0 {
		return code, "", msg, err
	}
	return RunExternal(npmCmd, "--prefix", filepath.Dir(pkgPath), "run", "typecheck")
}

// runFrontendTest 真实执行前端测试；脚本缺失→明确报 runner 不存在并以 2 退出。
func runFrontendTest() (int, string, string, error) {
	pkgPath, err := repoFile("frontend/package.json")
	if err != nil {
		return 2, "", err.Error(), err
	}
	if code, msg, err := frontendScript(pkgPath, "test"); code != 0 {
		return code, "", msg + "，不能静默跳过判成功", err
	}
	return RunExternal(npmCmd, "--prefix", filepath.Dir(pkgPath), "run", "test", "--", "--run")
}

// EnsureRepoRoot 保证当前目录是仓库根。
//
// devtools 的所有相对路径（tests/acceptance/cases.json、build/versions.lock.json、
// docs/evidence/runs/...）都以仓库根为基准。`go run ./cmd/devtools` 天然在根目录，
// 但包装脚本、IDE、或从子目录调用时会落错位置——那种失败会表现为「索引读不到」
// 或「证据写进了别的目录」，比直接报错难查得多。
func EnsureRepoRoot() error {
	if _, err := os.Stat("go.mod"); err == nil {
		return nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := wd
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("找不到仓库根（go.mod），当前目录: %s", wd)
		}
		dir = parent
	}
}

// goSourceFiles 列出仓库内应参与 gofmt 检查的 Go 文件。
//
// 用 git 的 ignore 规则（--cached --others --exclude-standard）而不是自己维护
// 排除列表：.tmp/、.tools/、build/ 的规则已经在 .gitignore 里，重复一份必然分叉。
// 未跟踪但未被忽略的新文件也会被包含——提交前就该被检查。
func goSourceFiles() ([]string, error) {
	// git 默认按当前目录输出相对路径；从 internal/xxx 调用时 gofmt 会找不到文件。
	// 统一用仓库根 + 绝对路径，任何 cwd 都能用。
	rootFile, err := repoFile("go.mod")
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(rootFile)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others",
		"--exclude-standard", "--", "*.go").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		files = append(files, filepath.Join(root, string(p)))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("git ls-files 没有返回任何 .go 文件")
	}
	return files, nil
}
