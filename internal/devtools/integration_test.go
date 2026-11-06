package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// TestObservationHelperProcess 是"外部命令"用的辅助进程，不是被测对象。
//
// 集成 runner 需要一条真的会写 $ANVIL_OBSERVATION_DIR 的命令。这里用同一个测试
// 二进制按 Go 标准的 helper-process 手法扮演它：跨平台一致、不依赖 cmd /c 或 sh -c，
// 也不用往仓库里写临时源码。真正的断言在下面两个 TestIntegration* 里。
func TestObservationHelperProcess(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_HELPER"))
	if mode == "" {
		return // 普通测试运行：它是空测试，不做任何断言
	}
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		os.Exit(2)
	}
	if mode == "nowrite" {
		os.Exit(0) // 负例：声明了 observation 却什么都不写
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		os.Exit(3)
	}
	facts := []byte(`{"runId":"run-fixture-1","state":"interrupted","counterLines":1,"observationDirAbsolute":` +
		map[bool]string{true: "true", false: "false"}[filepath.IsAbs(dir)] + `}`)
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), facts, 0o644); err != nil {
		os.Exit(3)
	}
	events := []byte(`{"type":"run.state","payload":{"state":"interrupted"}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), events, 0o644); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// observationHelperCommand 返回以 helper-process 身份重启本测试二进制的命令行。
func observationHelperCommand(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制失败: %v", err)
	}
	return []string{exe, "-test.run=^TestObservationHelperProcess$"}
}

// TestIntegrationWritesExternalObservationOracle 覆盖 runner 的
// external-observation 路径：case 自己在 $ANVIL_OBSERVATION_DIR 里写出
// observation.json 与 artifact，runner 必须据它生成
// kind=external-observation 的 oracle.json，而不是退回 test-process 自证。
func TestIntegrationWritesExternalObservationOracle(t *testing.T) {
	root := t.TempDir()
	// 故意用相对观察目录：runner 必须把它换成绝对路径再交给子进程，
	// 因为子进程的 cwd 是自己的包目录。artifacts/ 在 .gitignore 里，
	// 即便测试中断也不会把仓库弄脏。
	_ = os.MkdirAll(filepath.Join("..", "..", "artifacts"), 0o755)
	if rel, err := os.MkdirTemp(filepath.Join("..", "..", "artifacts"), "f05c-oracle-"); err == nil {
		root = rel
		t.Cleanup(func() { _ = os.RemoveAll(rel) })
	} else {
		t.Logf("无法构造相对观察目录，退回绝对路径（本用例仍验证 oracle 结构与封印）: %v", err)
	}
	c := integrationCase{
		ID:          "ORACLE_FIXTURE",
		External:    observationHelperCommand(t),
		Env:         []string{"ANVIL_OBSERVATION_HELPER=write"},
		Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"events.jsonl"}},
	}
	res := runIntegrationCase(c, "G2", "win-x64", root, platformMinimumEnvironment(t, "win-x64"))
	if !res.passed {
		t.Fatalf("声明了 observation 且写出了 artifact 的 case 应 PASS，实际 passed=%v blocked=%v", res.passed, res.blocked)
	}

	dir := filepath.Join(root, c.ID)
	raw, err := os.ReadFile(filepath.Join(dir, "oracle.json"))
	if err != nil {
		t.Fatalf("runner 没有写 oracle.json: %v", err)
	}
	var oracle externalOracle
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatalf("oracle.json 不是合法 JSON: %v", err)
	}
	if oracle.Kind != "external-observation" {
		t.Fatalf("oracle kind 应为 external-observation，实际 %q", oracle.Kind)
	}
	if oracle.SchemaVersion != 1 {
		t.Fatalf("oracle schemaVersion 应为 1，实际 %d", oracle.SchemaVersion)
	}
	if oracle.Source != "filesystem" {
		t.Fatalf("oracle source 应保留 case 声明的 filesystem，实际 %q", oracle.Source)
	}
	if len(oracle.Artifacts) != 1 || oracle.Artifacts[0] != "events.jsonl" {
		t.Fatalf("oracle artifacts 不符: %v", oracle.Artifacts)
	}
	var facts struct {
		RunID string `json:"runId"`
		Abs   bool   `json:"observationDirAbsolute"`
	}
	if err := json.Unmarshal(oracle.Observation, &facts); err != nil {
		t.Fatalf("oracle observation 不是合法 JSON: %v", err)
	}
	if facts.RunID != "run-fixture-1" {
		t.Fatalf("oracle observation 未原样保留 case 的观察值: %s", oracle.Observation)
	}
	// 子进程的 cwd 是它自己的包目录，观察目录必须是绝对路径：否则测试会写进
	// internal/run/docs/... 之类的位置，既拿不到证据又把仓库弄脏。
	if !facts.Abs {
		t.Fatalf("交给 case 的 ANVIL_OBSERVATION_DIR 必须是绝对路径: %s", oracle.Observation)
	}
	// 门禁自己的校验也必须接受这份 oracle。
	if err := validateExternalOracle(dir, "oracle.json"); err != nil {
		t.Fatalf("external oracle 未通过门禁校验: %v", err)
	}
	// result.json 里的 oraclePath 必须指向它。
	if path := readCaseOraclePath(t, dir); path != "oracle.json" {
		t.Fatalf("result.json 的 oraclePath 应指向 oracle.json，实际 %q", path)
	}
}

// TestIntegrationRejectsMissingObservation：声明了 observation 却什么都没写，
// 必须判 FAIL 并给出可定位的信息，且绝不能退回 test-process oracle。
func TestIntegrationRejectsMissingObservation(t *testing.T) {
	root := t.TempDir()
	c := integrationCase{
		ID:          "ORACLE_MISSING_FIXTURE",
		External:    observationHelperCommand(t),
		Env:         []string{"ANVIL_OBSERVATION_HELPER=nowrite"},
		Observation: &observationSpec{Source: "fixture-counter", Artifacts: []string{"counter.txt"}},
	}
	res := runIntegrationCase(c, "G2", "win-x64", root, platformMinimumEnvironment(t, "win-x64"))
	if res.passed {
		t.Fatal("没有产出 observation.json 的 case 不能被判 PASS：那是 runner 替 case 自证")
	}
	dir := filepath.Join(root, c.ID)
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("读取 result.json 失败: %v", err)
	}
	var ev contracts.GateEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("result.json 不是合法 JSON: %v", err)
	}
	if len(ev.CaseResults) != 1 || ev.CaseResults[0].Status != contracts.StatusFail {
		t.Fatalf("case 状态应为 FAIL，实际 %+v", ev.CaseResults)
	}
	if actual := ev.CaseResults[0].Actual; !strings.Contains(actual, "observation.json") {
		t.Fatalf("失败原因必须点名缺失的 observation.json，实际 %q", actual)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "oracle.json")); statErr == nil {
		t.Fatal("失败的 external case 不得写任何 oracle（尤其不能退回 test-process 自证）")
	}
}

func readCaseOraclePath(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("读取 result.json 失败: %v", err)
	}
	var ev contracts.GateEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("result.json 不是合法 JSON: %v", err)
	}
	if len(ev.CaseResults) != 1 {
		t.Fatalf("result.json 应只有一条 caseResult，实际 %d 条", len(ev.CaseResults))
	}
	return ev.CaseResults[0].OraclePath
}
