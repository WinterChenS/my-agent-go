package devtools

// 开发期单 case 取证过滤（verify --case）的测试。
//
// 为什么需要这个 flag：F01 要把 38 条 case 从 test-process 迁到
// external-observation，每迁一条都要跑一次真实证据。全量 G0/G2 一次十几分钟，
// 逐 case 提交时根本跑不动，结果一定是"跳过取证直接说通过"。
//
// 纪律（由下面的测试钉住）：
//   - 只允许缩小到该 gate 的**必需** case，未知/别的 gate 的 case → 退出 2；
//   - 校验发生在执行任何命令之前（不留半截证据）；
//   - 不带过滤时行为与旧路径完全一致（选择的 case 集合一模一样）；
//   - 过滤运行**不构成** gate 结论：gate 永远按完整矩阵评估。

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// hostTargetID 返回当前宿主对应的 target id（集成测试只能在目标本机跑）。
func hostTargetID(t *testing.T) string {
	t.Helper()
	for id, pair := range targetPlatforms {
		if pair[0] == runtime.GOOS && pair[1] == runtime.GOARCH {
			return id
		}
	}
	t.Fatalf("当前宿主 %s/%s 不是六目标之一，无法跑集成取证", runtime.GOOS, runtime.GOARCH)
	return ""
}

// chdirRepoRoot 让测试进程的 cwd 回到仓库根（runner 用相对路径读用例索引），
// 并在测试结束后还原，避免影响同包其它测试的相对路径假设。
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前目录: %v", err)
	}
	if err := EnsureRepoRoot(); err != nil {
		t.Fatalf("定位仓库根: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

// TestCaseFilterOnlyNarrowsRequiredCases 覆盖过滤的接受/拒绝边界。
func TestCaseFilterOnlyNarrowsRequiredCases(t *testing.T) {
	idx := loadRealIndex(t)
	g2, err := selectedIntegrationCases(idx, "G2")
	if err != nil {
		t.Fatalf("G2: %v", err)
	}

	// 不带过滤：必须与旧路径完全一致（同序、同集合）。
	same, err := filterIntegrationCases(g2, nil)
	if err != nil {
		t.Fatalf("空过滤应等价于不过滤: %v", err)
	}
	if len(same) != len(g2) {
		t.Fatalf("空过滤改变了 case 集合: %d != %d", len(same), len(g2))
	}
	for i := range same {
		if same[i] != g2[i] {
			t.Fatalf("空过滤改变了顺序: [%d] %s != %s", i, same[i], g2[i])
		}
	}

	// 只留下点名的必需 case，顺序按点名顺序，重复项去重。
	got, err := filterIntegrationCases(g2, []string{"RES01", "MEM01", "RES01"})
	if err != nil {
		t.Fatalf("必需 case 过滤被拒: %v", err)
	}
	if len(got) != 2 || got[0] != "RES01" || got[1] != "MEM01" {
		t.Fatalf("过滤结果不对: %v", got)
	}

	// 未知 case / 别的 gate 的 case / 空片段都必须判配置错误。
	for _, bad := range [][]string{{"NOPE"}, {"ENV01"}, {""}, {"MEM01", ""}} {
		if _, err := filterIntegrationCases(g2, bad); err == nil {
			t.Fatalf("过滤 %v 应被拒绝", bad)
		}
	}
}

// TestCaseFilterRunsOnlySelectedCase：过滤后只产出这一条 case 的证据目录。
func TestCaseFilterRunsOnlySelectedCase(t *testing.T) {
	chdirRepoRoot(t)
	root := t.TempDir()
	target := hostTargetID(t)

	if code := runIntegrationForGate(target, "G2", []string{"MEM01"}, root); code != 0 {
		t.Fatalf("单 case 取证应成功（MEM01），实际退出码 %d", code)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取证据根: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "MEM01" {
		t.Fatalf("过滤运行只应写 MEM01 的证据目录，实际 %v", namesOf(entries))
	}
	dir := filepath.Join(root, "MEM01")
	for _, f := range []string{"result.json", "command.txt", "stdout.log", "oracle.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, f)); statErr != nil {
			t.Fatalf("MEM01 证据缺 %s: %v", f, statErr)
		}
	}
}

// TestCaseFilterRejectsBeforeRunning：参数错误时一条命令都不许跑（不留半截证据）。
func TestCaseFilterRejectsBeforeRunning(t *testing.T) {
	chdirRepoRoot(t)
	root := t.TempDir()
	target := hostTargetID(t)

	if code := runIntegrationForGate(target, "G2", []string{"NOPE"}, root); code != 2 {
		t.Fatalf("未知 case 应退出 2，实际 %d", code)
	}
	if code := runIntegrationForGate(target, "G2", []string{"ENV01"}, root); code != 2 {
		t.Fatalf("别的 gate 的 case 应退出 2，实际 %d", code)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取证据根: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("参数错误时不应产出任何证据，实际 %v", namesOf(entries))
	}
}

func namesOf(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
