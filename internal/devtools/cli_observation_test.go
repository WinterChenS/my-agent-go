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
		dir := filepath.Join(root, rev, "G0", target, r.CaseID)
		display := r.Display
		if display != "" {
			dir = filepath.Join(dir, display)
		}
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
