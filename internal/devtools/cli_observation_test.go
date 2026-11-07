package devtools

// GOV01–GOV04 的外部观察装置：把**真实的 devtools 二进制**当子进程跑。
//
// 为什么必须这样：GOV01–04 断言的是「CLI 的退出码语义」（0 通过 / 1 断言失败 /
// 2 配置缺失，见 docs/adr/0001-devtools-exit-code-fidelity.md）。在测试进程里直接
// 调 RunGate/RunVerify 只能证明「函数返回值」，证明不了「二进制真的把那个码交给
// 了调用方」——参数解析、os.Exit、包装脚本都可能把它改掉。所以这里 build 一次
// ./cmd/devtools，然后以子进程方式执行，并把**子进程的退出码与原始输出**落成
// artifact；observation.json 里的值从落盘 artifact 读回，不是内存里的返回值。
//
// 纪律：这些测试只观察，不改变任何门禁语义/阈值/退出码。它们也无法让某个 gate
// 变绿——gate 判定仍然只看真实证据树，这里跑的是临时证据树。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

// devtoolsCLI 每次测试进程只 build 一次（sync.Once）：四条 GOV case 共用同一个
// 二进制。临时目录不随单个测试清理——它在进程内被后续测试复用；由操作系统
// 的临时目录清理回收。
var (
	devtoolsCLIOnce sync.Once
	devtoolsCLIPath string
	devtoolsCLIErr  error
)

func devtoolsRepoRoot(t *testing.T) string {
	t.Helper()
	mod, err := repoFile("go.mod")
	if err != nil {
		t.Fatalf("定位仓库根失败: %v", err)
	}
	return filepath.Dir(mod)
}

func devtoolsBinary(t *testing.T) string {
	t.Helper()
	devtoolsCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "anvil-devtools-cli-")
		if err != nil {
			devtoolsCLIErr = fmt.Errorf("创建 CLI 临时目录失败: %w", err)
			return
		}
		bin := filepath.Join(dir, "devtools")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/devtools")
		cmd.Dir = devtoolsRepoRootNoFail()
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			devtoolsCLIErr = fmt.Errorf("go build ./cmd/devtools 失败: %v\n%s", buildErr, out)
			return
		}
		devtoolsCLIPath = bin
	})
	if devtoolsCLIErr != nil {
		t.Fatalf("%v", devtoolsCLIErr)
	}
	return devtoolsCLIPath
}

// devtoolsRepoRootNoFail 供 sync.Once 内部使用（那里拿不到 *testing.T）。
func devtoolsRepoRootNoFail() string {
	mod, err := repoFile("go.mod")
	if err != nil {
		return "."
	}
	return filepath.Dir(mod)
}

// cliRun 是一次真实子进程执行的原始记录。
type cliRun struct {
	Label      string   `json:"label"`
	Args       []string `json:"args"`
	WorkDir    string   `json:"workDir"`
	ExitCode   int      `json:"exitCode"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	DurationMS int64    `json:"durationMs"`
}

// runDevtoolsCLI 执行真实的 devtools 二进制。dir 为空表示仓库根。
//
// 子进程环境从父进程继承，但**剥掉** ANVIL_OBSERVATION_DIR：本 case 的观察目录
// 只能由本测试自己写，不能被嵌套调用悄悄改写。
func runDevtoolsCLI(t *testing.T, label, dir string, args ...string) cliRun {
	t.Helper()
	bin := devtoolsBinary(t)
	if dir == "" {
		dir = devtoolsRepoRoot(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(envWithoutKey(os.Environ(), "ANVIL_OBSERVATION_DIR"), "ANVIL_OBSERVATION_DIR=")
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	started := time.Now()
	runErr := cmd.Run()
	duration := time.Since(started).Milliseconds()
	if ctx.Err() != nil {
		t.Fatalf("devtools CLI 超时（%v）：%v", ctx.Err(), args)
	}
	exit := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("无法执行 devtools CLI %v: %v", args, runErr)
		}
	}
	return cliRun{
		Label:      label,
		Args:       args,
		WorkDir:    dir,
		ExitCode:   exit,
		Stdout:     out.String(),
		Stderr:     errOut.String(),
		DurationMS: duration,
	}
}

func envWithoutKey(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// observationDirForTest 返回本 case 的证据目录（集成 runner 注入）。
// 单跑 `go test ./internal/devtools` 时退化成 t.TempDir()：产物仍写出来便于调试，
// 但此时没有 runner 去消费它。
func observationDirForTest(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	return dir
}

type cliArtifact struct {
	SchemaVersion int      `json:"schemaVersion"`
	Runs          []cliRun `json:"runs"`
}

// writeCLIArtifact 落盘原始执行记录并**读回**：后续 observation.json 里的值必须
// 来自磁盘上的 artifact，而不是内存里的返回值（否则又是自证）。
func writeCLIArtifact(t *testing.T, dir, name string, runs []cliRun) []cliRun {
	t.Helper()
	payload := cliArtifact{SchemaVersion: 1, Runs: runs}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", name, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", name, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 %s 失败: %v", name, err)
	}
	var back cliArtifact
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	if len(back.Runs) != len(runs) {
		t.Fatalf("%s 读回 %d 条，写入 %d 条", name, len(back.Runs), len(runs))
	}
	return back.Runs
}

// writeObservationFacts 写出本 case 的观察值（runner 会把它封成 external oracle）。
func writeObservationFacts(t *testing.T, dir string, facts any) {
	t.Helper()
	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// firstLineContaining 返回第一条包含 sub 的行（去空白）；找不到返回空串。
// 用它从 artifact 的原始输出里取「实际报告了什么」，而不是把期望值写死。
func firstLineContaining(s, sub string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// linesContaining 返回所有包含 sub 的行。
func linesContaining(s, sub string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// writeCompleteG0EvidenceTree 在临时目录里写出「当前 HEAD 上完整的 G0 证据树」。
//
// 这是给被测 gate 的 fixture：GOV02/GOV04 要观察的是**单一缺陷**（oracle 被篡改 /
// 某个 case 目录整个缺失）。如果不把其余 130 个必需证据位都写成合法证据，gate 永远
// 会因为「缺一堆 case」退出 2，任何缺陷都会被同一片输出掩盖——那样的观察没有信息量。
//
// 注意：这里的 external oracle 内容是**fixture**（文件名与内容都标了 fixture），
// 它只用于观察 gate 的退出码与报告，不冒充任何真实 case 的证据。
func writeCompleteG0EvidenceTree(t *testing.T, root string) (rev, lockHash string) {
	t.Helper()
	idxPath, err := repoFile("tests/acceptance/cases.json")
	if err != nil {
		t.Fatalf("定位 cases.json 失败: %v", err)
	}
	idx, err := LoadCasesIndex(idxPath)
	if err != nil {
		t.Fatalf("加载用例索引失败: %v", err)
	}
	lockPath, err := repoFile("build/versions.lock.json")
	if err != nil {
		t.Fatalf("定位 versions.lock.json 失败: %v", err)
	}
	lockHash, err = ComputeVersionsLockHash(lockPath)
	if err != nil {
		t.Fatalf("计算 versions.lock 哈希失败: %v", err)
	}
	rev, _ = SourceIdentity()
	if rev == "" {
		t.Fatalf("无法取得当前 revision；gate 的基线比对需要它")
	}
	reqs, problems := ResolveRequiredEvidence(idx, "G0")
	if len(problems) > 0 {
		t.Fatalf("G0 必需证据位解析失败: %v", problems)
	}
	entry := map[string]CaseEntry{}
	for _, c := range idx.Cases {
		entry[c.ID] = c
	}
	host := hostTargetID(t)
	for _, r := range reqs {
		target := r.Target
		if target == "" {
			target = host
		}
		// 叶子目录名必须是 case id：gate 用 filepath.Base(dir) 识别证据属于哪条 case。
		// display 只能作为 case 之前的层级（与 evidenceRoot 的 native 布局一致）。
		segments := []string{root, rev, "G0", target}
		if r.Display == DisplayX11 || r.Display == DisplayWayland {
			segments = append(segments, r.Display)
		}
		dir := filepath.Join(append(segments, r.CaseID)...)
		display := r.Display
		writeEvidenceAt(t, dir, rev, "G0", target, r.CaseID, lockHash, func(e *contracts.GateEvidence) {
			e.Display = display
		})
		if entry[r.CaseID].OracleKind != "external" {
			continue
		}
		// oracleKind=external 的 case 用 test-process 自证会被 gate 拒绝，
		// 所以 fixture 也必须给出 external 形状的 oracle（内容明确标注是 fixture）。
		artifact := filepath.Join(dir, "fixture-artifact.json")
		body, _ := json.MarshalIndent(map[string]any{
			"fixture": true,
			"caseId":  r.CaseID,
			"purpose": "gate-exit-code-observation",
		}, "", "  ")
		if err := os.WriteFile(artifact, body, 0o644); err != nil {
			t.Fatalf("写 fixture artifact 失败: %v", err)
		}
		writeExternalOracle(t, dir, "filesystem", map[string]any{
			"fixture": true,
			"caseId":  r.CaseID,
		}, []string{"fixture-artifact.json"})
		rehashEvidence(t, dir)
	}
	return rev, lockHash
}

// ---- GOV01：缺证据 / 未知 gate 必须是配置类退出码 2 ----

func TestGOV01CLIGateRejectsMissingEvidenceWithExitTwo(t *testing.T) {
	obsDir := observationDirForTest(t)
	empty := t.TempDir()

	runMissing := runDevtoolsCLI(t, "empty-evidence-root", "", "gate", "--id=G0", "--evidence="+empty)
	runUnknown := runDevtoolsCLI(t, "unknown-gate", "", "gate", "--id=BOGUS", "--evidence="+empty)
	if runMissing.ExitCode != 2 {
		t.Fatalf("空证据树应退出 2（配置缺失），实际 %d\nstdout=%s\nstderr=%s",
			runMissing.ExitCode, tail(runMissing.Stdout, 800), tail(runMissing.Stderr, 400))
	}
	if runUnknown.ExitCode != 2 {
		t.Fatalf("未知 gate 应退出 2，实际 %d\nstdout=%s\nstderr=%s",
			runUnknown.ExitCode, tail(runUnknown.Stdout, 400), tail(runUnknown.Stderr, 400))
	}

	// 观察值从落盘 artifact 读回，不从上面的内存变量取。
	runs := writeCLIArtifact(t, obsDir, "gate-run.json", []cliRun{runMissing, runUnknown})
	missingReason := firstLineContaining(runs[0].Stdout, "缺必需证据")
	if missingReason == "" {
		t.Fatalf("gate 的真实输出里没有点名『缺必需证据』，不能编造观察值\nstdout=%s", tail(runs[0].Stdout, 800))
	}
	unknownReason := firstLineContaining(runs[1].Stderr, "未知 gate")
	if unknownReason == "" {
		t.Fatalf("gate 的真实 stderr 里没有点名『未知 gate』\nstderr=%s", runs[1].Stderr)
	}

	writeObservationFacts(t, obsDir, map[string]any{
		"exitCode":          runs[0].ExitCode,
		"reason":            missingReason,
		"unknownGateExit":   runs[1].ExitCode,
		"unknownGateReason": unknownReason,
		"runs":              len(runs),
	})
}

// ---- GOV02：PASS case 缺目录内 oracle 必须被真实 gate 判死 ----

func TestGOV02CLIGateRejectsTamperedOracleWithExitTwo(t *testing.T) {
	obsDir := observationDirForTest(t)
	root := t.TempDir()
	rev, _ := writeCompleteG0EvidenceTree(t, root)

	// 缺陷只做一处：GOV01 的证据目录里删掉 oracle.json 并重算报告哈希。
	// 重算哈希是必要的——否则 gate 会先因为「报告哈希被篡改」判死，
	// 观察到的就不是「缺 oracle」这条规则了。
	dir := filepath.Join(root, rev, "G0", hostTargetID(t), "GOV01")
	if err := os.Remove(filepath.Join(dir, "oracle.json")); err != nil {
		t.Fatalf("删除 oracle.json 失败: %v", err)
	}
	rehashEvidence(t, dir)

	run := runDevtoolsCLI(t, "tampered-oracle", "", "gate", "--id=G0", "--evidence="+root)
	if run.ExitCode != 2 {
		t.Fatalf("缺 oracle 应退出 2，实际 %d\nstdout=%s\nstderr=%s",
			run.ExitCode, tail(run.Stdout, 1500), tail(run.Stderr, 400))
	}

	runs := writeCLIArtifact(t, obsDir, "gate-run.json", []cliRun{run})
	stdout := runs[0].Stdout
	rejected := linesContaining(stdout, "[rejected]")
	// HOST scope 的 case 会同时出现「必需证据位」和「该 case 的额外证据」两行，
	// 两行都属于同一个 case。这里要钉的是「没有别的 case 被拒」，不是行数。
	if len(rejected) == 0 {
		t.Fatalf("gate 没有拒绝任何证据位，缺 oracle 应被判死\nstdout=%s", tail(stdout, 1500))
	}
	for _, line := range rejected {
		if !strings.Contains(line, "GOV01") {
			t.Fatalf("除了 GOV01 以外还有证据位被拒（缺陷不止一处，观察无效）: %v", rejected)
		}
	}
	reason := firstLineContaining(stdout, "oraclePath 不可读")
	if reason == "" {
		t.Fatalf("gate 的真实输出没有点名 oracle 不可读，不能编造观察值\nstdout=%s", tail(stdout, 1500))
	}
	// 反向核对 fixture 本身：除 GOV01 的 oracle 外，没有其它缺陷（没缺证据位），
	// 否则“因为缺 oracle 而拒”可能只是“因为缺一堆 case 而拒”的巧合。
	if missing := linesContaining(stdout, "[missing]"); len(missing) != 0 {
		t.Fatalf("fixture 不完整：还有 %d 个必需证据位缺失，观察不到单一缺陷: %v", len(missing), missing)
	}

	writeObservationFacts(t, obsDir, map[string]any{
		"exitCode":             runs[0].ExitCode,
		"rejected":             "oracle",
		"rejectedCase":         "GOV01",
		"reason":               reason,
		"rejectedRequirements": len(rejected),
	})
}

// writeCLIFixtureRepo 造一个临时「仓库」：go.mod + 一个 Go 文件 + 带 typecheck
// 脚本的前端包，并 git init/add（`verify --suite=static` 用 git ls-files 列文件）。
//
// 这是给被测 CLI 的 fixture，不是产品代码：用来让 gofmt 断言真的失败/成功，
// 从而观察 0/1/2 三种退出码是否被二进制原样交给调用方（ADR-0001）。
func writeCLIFixtureRepo(t *testing.T, goSource string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("写 %s: %v", rel, err)
		}
	}
	writeFile("go.mod", "module anvil-cli-fixture\n\ngo 1.21\n")
	writeFile("fixture.go", goSource)
	writeFile("frontend/typecheck.js", "process.exit(0)\n")
	writeFile("frontend/package.json",
		"{\"name\":\"anvil-cli-fixture\",\"private\":true,\"scripts\":{\"typecheck\":\"node typecheck.js\"}}\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", ".")
	git("add", "-A")
	return dir
}

// ---- GOV03：0 成功 / 1 断言失败 / 2 配置缺失，真实二进制必须原样透传 ----

func TestGOV03CLIExitCodeFidelity(t *testing.T) {
	obsDir := observationDirForTest(t)

	// 配置缺失：未知 suite → 2（不是 1）。
	configRun := runDevtoolsCLI(t, "config-missing", "", "verify", "--suite=doesnotexist")
	if configRun.ExitCode != 2 {
		t.Fatalf("未知 suite 应退出 2，实际 %d\nstdout=%s\nstderr=%s",
			configRun.ExitCode, tail(configRun.Stdout, 400), tail(configRun.Stderr, 400))
	}

	// 断言失败：临时仓库里放一个未格式化的 Go 文件，gofmt 检查真的失败 → 1。
	// 同一次运行里 go vet 与前端 typecheck 都通过，所以 1 只可能来自断言失败。
	assertionRepo := writeCLIFixtureRepo(t, "package fixture\n\nfunc  F( ) int {\n\treturn 1\n}\n")
	assertionRun := runDevtoolsCLI(t, "assertion-failure", assertionRepo, "verify", "--suite=static")
	if assertionRun.ExitCode != 1 {
		t.Fatalf("gofmt 断言失败应退出 1，实际 %d\nstdout=%s\nstderr=%s",
			assertionRun.ExitCode, tail(assertionRun.Stdout, 1500), tail(assertionRun.Stderr, 400))
	}

	// 成功对照：同一个装置换成已格式化的源码 → 0。
	// 没有这一条，上面两条可能只是「永远非零」，观察不到 0 这一档。
	successRepo := writeCLIFixtureRepo(t, "package fixture\n\nfunc F() int {\n\treturn 1\n}\n")
	successRun := runDevtoolsCLI(t, "success", successRepo, "verify", "--suite=static")
	if successRun.ExitCode != 0 {
		t.Fatalf("干净 fixture 应退出 0，实际 %d\nstdout=%s\nstderr=%s",
			successRun.ExitCode, tail(successRun.Stdout, 1500), tail(successRun.Stderr, 400))
	}

	runs := writeCLIArtifact(t, obsDir, "exit-codes.json", []cliRun{configRun, assertionRun, successRun})

	configExit, assertionExit, successExit := runs[0].ExitCode, runs[1].ExitCode, runs[2].ExitCode
	if configExit != 2 || assertionExit != 1 || successExit != 0 {
		t.Fatalf("artifact 读回的退出码不符合 0/1/2 语义: success=%d assertion=%d config=%d",
			successExit, assertionExit, configExit)
	}
	configReason := firstLineContaining(runs[0].Stderr, "未知 suite")
	if configReason == "" {
		t.Fatalf("配置缺失那一次没有点名『未知 suite』\nstderr=%s", runs[0].Stderr)
	}
	assertionReason := firstLineContaining(runs[1].Stdout, "[FAIL] gofmt")
	if assertionReason == "" {
		t.Fatalf("断言失败那一次没有点名 gofmt 失败，1 的来历不可核对\nstdout=%s", tail(runs[1].Stdout, 1500))
	}
	if !strings.Contains(runs[2].Stdout, "static 退出码: 0") {
		t.Fatalf("成功那一次没有给出 static 退出码 0\nstdout=%s", tail(runs[2].Stdout, 800))
	}

	writeObservationFacts(t, obsDir, map[string]any{
		"configExit":      configExit,
		"assertionExit":   assertionExit,
		"successExit":     successExit,
		"configReason":    configReason,
		"assertionReason": assertionReason,
		"note":            "1 来自临时 fixture 仓库里未格式化文件的 gofmt 断言；同一次运行的 go vet 与前端 typecheck 均通过（见 artifact stdout）。",
	})
}

// ---- GOV04：必需 case 目录整个缺失必须被真实 gate 点名并退出 2 ----

func TestGOV04CLIGateReportsMissingCaseWithExitTwo(t *testing.T) {
	obsDir := observationDirForTest(t)
	root := t.TempDir()
	rev, _ := writeCompleteG0EvidenceTree(t, root)

	// 缺陷只做一处：把一条必需 case 的证据目录整个删掉（不是写坏，是不存在）。
	// 不依赖仓库当前的 runner 覆盖：缺陷是在临时证据树里构造出来的。
	missingCase := "RPC04"
	caseDir := filepath.Join(root, rev, "G0", hostTargetID(t), missingCase)
	if _, err := os.Stat(caseDir); err != nil {
		t.Fatalf("fixture 里没有 %s 的证据目录，观察前提不成立: %v", missingCase, err)
	}
	if err := os.RemoveAll(caseDir); err != nil {
		t.Fatalf("删除 %s 证据目录失败: %v", missingCase, err)
	}

	run := runDevtoolsCLI(t, "missing-case", "", "gate", "--id=G0", "--evidence="+root)
	if run.ExitCode != 2 {
		t.Fatalf("必需 case 缺失应退出 2，实际 %d\nstdout=%s\nstderr=%s",
			run.ExitCode, tail(run.Stdout, 1500), tail(run.Stderr, 400))
	}

	runs := writeCLIArtifact(t, obsDir, "gate-run.json", []cliRun{run})
	stdout := runs[0].Stdout
	missing := linesContaining(stdout, "[missing]")
	if len(missing) != 1 {
		t.Fatalf("期望只有一条必需证据位缺失（其余 130 位合法），实际 %d 条: %v", len(missing), missing)
	}
	if !strings.Contains(missing[0], missingCase) {
		t.Fatalf("gate 报告的缺失项不是 %s: %s", missingCase, missing[0])
	}
	reason := firstLineContaining(stdout, "缺必需证据: "+missingCase)
	if reason == "" {
		t.Fatalf("gate 的真实输出没有点名缺失的 %s，不能编造观察值\nstdout=%s", missingCase, tail(stdout, 1500))
	}

	writeObservationFacts(t, obsDir, map[string]any{
		"exitCode":            runs[0].ExitCode,
		"missingCase":         missingCase,
		"missingRequirements": len(missing),
		"reason":              reason,
	})
}

// ---- GOV05：证据必须带真实环境身份，缺失/与 target 不符/低于最低系统都判死 ----

// TestGOV05CLIGateAcceptsAndRejectsEnvironmentIdentity 用**真实 devtools 二进制**观察
// GOV05 的四个分支：恰好等于最低要求通过；缺环境身份、OS 与 target 不符、低于最低 build
// 各自退出 2 并被点名。
//
// 为什么必须走子进程：GOV05 断言的是"证据里的环境身份与 build/platforms.json 的最低要求
// 比对之后，gate 给出什么退出码"。进程内调 EvaluateGate 只能证明返回值，证明不了
// 二进制真的把 2 交给了调用方（ADR-0001）。观察值从落盘 artifact 读回，不从内存变量取。
func TestGOV05CLIGateAcceptsAndRejectsEnvironmentIdentity(t *testing.T) {
	obsDir := observationDirForTest(t)

	// 场景一：最低线证据 → 必须通过（证明 GOV05 不是"一律判死"）。
	okRoot := t.TempDir()
	rev, lockHash := writeCompleteG0EvidenceTree(t, okRoot)
	runOK := runDevtoolsCLI(t, "minimum-environment", "", "gate", "--id=G0", "--evidence="+okRoot)
	if runOK.ExitCode != 0 {
		t.Fatalf("恰好等于最低要求的证据应通过（exit 0），实际 %d\nstdout=%s",
			runOK.ExitCode, tail(runOK.Stdout, 1200))
	}

	// 场景二/三/四：只破坏 win-x64 上一个必需 case 的环境身份，其余保持最低线。
	// 这样拒绝只能来自被破坏的那一项，而不是"整棵树本来就不完整"。
	host := hostTargetID(t)
	broken := []struct {
		label  string
		mutate func(*contracts.GateEvidence)
		expect string // gate 输出里必须出现的点名
	}{
		{
			label: "missing-environment",
			mutate: func(e *contracts.GateEvidence) {
				e.Environment = contracts.Environment{}
			},
			expect: "环境",
		},
		{
			label: "os-target-mismatch",
			mutate: func(e *contracts.GateEvidence) {
				e.Environment.OS = "linux"
			},
			expect: "不一致",
		},
		{
			label: "below-minimum-build",
			mutate: func(e *contracts.GateEvidence) {
				// 最低线减一：低于要求但形状合法，避免把"解析失败"当成"低于最低"。
				build, err := strconv.ParseInt(e.Environment.OSBuild, 10, 64)
				if err != nil || build <= 0 {
					t.Fatalf("最低线 build 不是可解析的数字: %q", e.Environment.OSBuild)
				}
				e.Environment.OSBuild = strconv.FormatInt(build-1, 10)
			},
			expect: "最低",
		},
	}

	runs := []cliRun{runOK}
	facts := map[string]any{"acceptedExit": runOK.ExitCode}
	for _, b := range broken {
		root := t.TempDir()
		// 用完整的 G0 证据树（与上面同一份），再对 win-x64 的一个必需 case 施加破坏。
		writeCompleteG0EvidenceTree(t, root)
		dir := filepath.Join(root, rev, "G0", host, "ENV03")
		writeEvidenceAt(t, dir, rev, "G0", host, "ENV03", lockHash, b.mutate)
		writeExternalOracle(t, dir, "filesystem", map[string]any{"fixture": true}, []string{"fixture-artifact.json"})
		rehashEvidence(t, dir)

		run := runDevtoolsCLI(t, b.label, "", "gate", "--id=G0", "--evidence="+root)
		if run.ExitCode != 2 {
			t.Fatalf("%s 应退出 2，实际 %d\nstdout=%s\nstderr=%s",
				b.label, run.ExitCode, tail(run.Stdout, 1200), tail(run.Stderr, 400))
		}
		combined := run.Stdout + run.Stderr
		if !strings.Contains(combined, b.expect) {
			t.Fatalf("%s 的拒绝原因必须点名 %q，实际输出：\n%s", b.label, b.expect, tail(combined, 1200))
		}
		runs = append(runs, run)
		facts[b.label+"Exit"] = run.ExitCode
		facts[b.label+"Reason"] = firstLineContaining(combined, b.expect)
	}

	// 观察值从 artifact 读回。
	persisted := writeCLIArtifact(t, obsDir, "gate-run.json", runs)
	if persisted[0].ExitCode != 0 {
		t.Fatalf("回读的第一条运行 exit=%d，期望 0（artifact 与断言不一致）", persisted[0].ExitCode)
	}
	for i, r := range persisted[1:] {
		if r.ExitCode != 2 {
			t.Fatalf("回读的第 %d 条破坏运行 exit=%d，期望 2", i+2, r.ExitCode)
		}
	}
	writeObservationFacts(t, obsDir, facts)
}
