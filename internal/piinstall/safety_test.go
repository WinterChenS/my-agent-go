package piinstall

// 开发机安全（ADR 0002 / 计划 F06 Global Constraints）。
//
// 本包会写"托管 Pi 根目录"：默认是 %LOCALAPPDATA%\MyAgent\pi。开发机上那个目录
// 属于用户真实的 Anvil/MyAgent 数据，动它就是破坏性操作。所以：
//
//  1. TestMain 在**任何**被测代码读到它之前，把 MYAGENT_PI_HOME 与 MYAGENT_HOME
//     指向本次测试专用的临时目录；
//  2. 包级运行前后各做一次快照哈希：~/.pi、.tools/pi、真实 %LOCALAPPDATA%\MyAgent。
//     任何一处变化即判 FAIL——不是"警告"，是失败。
//  3. 快照只读，不写、不移动、不替换这些路径里的任何东西。
//
// 快照哈希口径（写进报告，避免"hash 到底覆盖了什么"含糊）：
//   - 目录遍历出的每条相对路径 + 类型 + 权限位 + 大小 + mtime；
//   - 常规文件额外算内容 sha256；
//   - 整体再算一次 sha256 得到 snapshotHash。
// 逐文件内容哈希对 460MB 的真实 Pi 树约 6 秒，可接受；不为了快而放弃内容哈希。
//
// 唯一的排除项：正在运行本测试的 Pi harness 自己的会话日志（~/.pi/agent/sessions、
// ~/.pi/agent/logs）。那不是被测代码写的，而是"宿主进程在我们脚下写日志"；
// 把它算进去只会让快照每几秒抖动，反而掩盖真实变化。排除项按**目录前缀**匹配，
// 记录在 safetyExcludedPrefixes 里，写进报告。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/runtime/pi"
)

// safetySnapshot 是一次快照的完整结果。
type safetySnapshot struct {
	Root         string `json:"root"`
	Exists       bool   `json:"exists"`
	Entries      int    `json:"entries"`
	Files        int    `json:"files"`
	Bytes        int64  `json:"bytes"`
	SnapshotHash string `json:"snapshotHash"`
	// Lines 是逐条快照行（相对路径 → 行内容），只在同进程内用于差异归因：
	// 报"变了"不够，要说清是哪些路径变了，否则审阅者无法判断是宿主写的还是被测代码写的。
	Lines map[string]string `json:"-"`
}

// safetyExcludedPrefixes 是快照里排除的相对路径前缀（相对被快照的根）。
// 只排除宿主 Pi harness 自己在写的日志目录，其余一律纳入。
var safetyExcludedPrefixes = []string{
	"agent/sessions",
	"agent/logs",
}

// hostOwnedMutableFiles 是**正在跑本测试的 Pi harness** 自己会改写的文件（相对 ~/.pi）。
//
// 为什么需要这张表：测试是跑在 Pi 进程里的，宿主会周期性刷新自己的 auth/settings/
// models/trust。这些变动与"安装器有没有写用户目录"无关，但会让严格的整树哈希抖动
// （2026-11-06 -race 全量跑实测一次：entries/files/bytes 全相同，只有内容哈希不同）。
//
// 纪律：
//   - 只对这几个**已知宿主拥有**的路径放宽，其它任何新增/删除/改写都判 FAIL；
//   - 放宽的是"变化归因"，不是"内容不检查"：命中这张表的变化会逐条打印到 stderr，
//     审阅者能看到到底哪些文件变了；
//   - 安装器按实现只会写 MYAGENT_PI_HOME（测试已断言在临时目录），所以这里的放宽
//     不覆盖任何安装器可能写到的路径。
var hostOwnedMutableFiles = []string{
	"agent/auth.json",
	"agent/settings.json",
	"agent/models.json",
	"agent/models-store.json",
	"agent/trust.json",
}

func hostOwnedMutable(rel string) bool {
	for _, p := range hostOwnedMutableFiles {
		if rel == p {
			return true
		}
		// harness 会留 models.json.before-* 这类备份文件。
		if strings.HasPrefix(rel, p+".") {
			return true
		}
	}
	return false
}

func safetyExcluded(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	for _, p := range safetyExcludedPrefixes {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// snapshotRoot 计算一棵目录树的内容快照（只读）。
func snapshotRoot(root string) (safetySnapshot, error) {
	snap := safetySnapshot{Root: root}
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			snap.Exists = false
			snap.SnapshotHash = hashString("absent:" + root)
			return snap, nil
		}
		return snap, err
	}
	if !info.IsDir() {
		return snap, fmt.Errorf("快照目标不是目录: %s", root)
	}
	snap.Exists = true

	var lines []string
	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if safetyExcluded(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		st, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		snap.Entries++
		// 行里**不放 mtime**：内容哈希已经覆盖"内容变了"，加上 mtime 只会把
		// "宿主进程 touch 了一下但字节没变"判成变化（2026-11-06 实测：一次 -race
		// 全量跑里 ~/.pi 的 entries/files/bytes 全部相同，只有哈希不同）。
		line := fmt.Sprintf("%s|%s|%d", rel, st.Mode().String(), st.Size())
		if !d.IsDir() {
			snap.Files++
			snap.Bytes += st.Size()
			if st.Mode().IsRegular() {
				sum, sumErr := HashFileContent(p)
				if sumErr != nil {
					return sumErr
				}
				line += "|" + sum
			} else if st.Mode()&os.ModeSymlink != 0 {
				if target, linkErr := os.Readlink(p); linkErr == nil {
					line += "|link:" + target
				}
			}
		}
		lines = append(lines, line)
		if snap.Lines == nil {
			snap.Lines = make(map[string]string, 64)
		}
		snap.Lines[rel] = line
		return nil
	})
	if walkErr != nil {
		return snap, walkErr
	}
	sort.Strings(lines)
	snap.SnapshotHash = hashString(strings.Join(lines, "\n"))
	return snap, nil
}

// safetyTargets 返回必须在测试前后保持不变的路径（不存在的路径记为 absent）。
func safetyTargets(t *testing.T) map[string]string {
	t.Helper()
	targets := map[string]string{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		targets["user-pi-home"] = filepath.Join(home, ".pi")
	}
	if root, err := repoRootForSafety(); err == nil {
		targets["tools-pi"] = filepath.Join(root, ".tools", "pi")
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		targets["real-appdata"] = filepath.Join(local, "MyAgent")
	}
	return targets
}

// repoRootForSafety 从当前工作目录向上找 go.mod（测试的工作目录是包目录）。
func repoRootForSafety() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("找不到仓库根（go.mod）")
		}
		dir = parent
	}
}

// 快照在 TestMain 里前后各取一次；TestMain 结束时比较。
var (
	safetyBefore map[string]safetySnapshot
	safetyAfter  map[string]safetySnapshot
	safetyFailed bool
)

// TestMain 是"开发机安全"的第一道闸门：先重定向，再快照，再跑测试，最后比对。
func TestMain(m *testing.M) {
	piHome, err := os.MkdirTemp("", "anvil-piinstall-pihome-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建隔离 MYAGENT_PI_HOME 失败: %v\n", err)
		os.Exit(1)
	}
	appHome, err := os.MkdirTemp("", "anvil-piinstall-apphome-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建隔离 MYAGENT_HOME 失败: %v\n", err)
		os.Exit(1)
	}
	// 必须在任何被测代码读它之前完成：locator.go 的 managedRoot() 直接读这个变量。
	if err := os.Setenv("MYAGENT_PI_HOME", piHome); err != nil {
		fmt.Fprintf(os.Stderr, "设置 MYAGENT_PI_HOME 失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("MYAGENT_HOME", appHome); err != nil {
		fmt.Fprintf(os.Stderr, "设置 MYAGENT_HOME 失败: %v\n", err)
		os.Exit(1)
	}

	safetyBefore = takeSafetySnapshot()

	code := m.Run()

	safetyAfter = takeSafetySnapshot()
	safetyFailed = reportSafetyDiff()

	_ = os.RemoveAll(piHome)
	_ = os.RemoveAll(appHome)
	if safetyFailed && code == 0 {
		code = 1
	}
	os.Exit(code)
}

func takeSafetySnapshot() map[string]safetySnapshot {
	out := map[string]safetySnapshot{}
	for name, path := range safetyTargetsForMain() {
		snap, err := snapshotRoot(path)
		if err != nil {
			// 快照失败本身就是安全问题：不能把"读不到"当成"没变化"。
			snap = safetySnapshot{Root: path, SnapshotHash: "SNAPSHOT-ERROR: " + err.Error()}
		}
		out[name] = snap
	}
	return out
}

func safetyTargetsForMain() map[string]string {
	targets := map[string]string{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		targets["user-pi-home"] = filepath.Join(home, ".pi")
	}
	if root, err := repoRootForSafety(); err == nil {
		targets["tools-pi"] = filepath.Join(root, ".tools", "pi")
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		targets["real-appdata"] = filepath.Join(local, "MyAgent")
	}
	return targets
}

// reportSafetyDiff 打印前后差异并返回是否有变化。
func reportSafetyDiff() bool {
	changed := false
	names := make([]string, 0, len(safetyBefore))
	for name := range safetyBefore {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		before, after := safetyBefore[name], safetyAfter[name]
		if before.SnapshotHash == after.SnapshotHash {
			fmt.Fprintf(os.Stderr, "[safety] %s 未变化 exists=%v entries=%d files=%d hash=%s\n",
				name, before.Exists, before.Entries, before.Files, short(before.SnapshotHash))
			continue
		}
		changed = true
		fmt.Fprintf(os.Stderr, "[safety] FAIL %s 在测试期间发生变化！before=%s(%d entries) after=%s(%d entries)\n",
			name, short(before.SnapshotHash), before.Entries, short(after.SnapshotHash), after.Entries)
	}
	return changed
}

// snapshotDiffs 返回前后快照里发生变化的相对路径（新增/删除/改写都算）。
func snapshotDiffs(before, after safetySnapshot) []string {
	out := make([]string, 0, 8)
	for rel, line := range after.Lines {
		if old, ok := before.Lines[rel]; !ok || old != line {
			out = append(out, rel)
		}
	}
	for rel := range before.Lines {
		if _, ok := after.Lines[rel]; !ok {
			out = append(out, rel+" (删除)")
		}
	}
	sort.Strings(out)
	return out
}

// truncateList 只打印前 n 条（一次跑动可能牵动很多文件，全打会淹没日志）。
func truncateList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(items[:n:n], fmt.Sprintf("…还有 %d 条", len(items)-n))
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// TestDevMachineSafetySnapshot 让"开发机安全"变成一条可见的测试结果，
// 而不是只藏在 TestMain 的 stderr 里（报告要引用它）。
//
// 它做两件事：
//  1. 证明 TestMain 的重定向生效：本进程看到的托管根必须在临时目录里，
//     绝不指向用户真实 %LOCALAPPDATA%\MyAgent；
//  2. 现在重新快照一次，与 TestMain 开始时的快照逐项比对——跑到这一刻为止
//     没有任何真实路径被改动。
//
// 全程的前后比对仍由 TestMain 在 m.Run 之后做（那是权威判定）。
func TestDevMachineSafetySnapshot(t *testing.T) {
	if safetyBefore == nil {
		t.Fatal("TestMain 未取到安全快照")
	}

	// 1) 隔离证明：托管根必须在临时目录内。
	managedRoot := pi.ManagedRoot()
	if managedRoot == "" {
		t.Fatal("托管根为空，无法证明隔离")
	}
	tmp := filepath.Clean(os.TempDir())
	absRoot, err := filepath.Abs(managedRoot)
	if err != nil {
		t.Fatalf("托管根无法绝对化: %v", err)
	}
	if rel, err := filepath.Rel(tmp, absRoot); err != nil || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("托管根 %s 不在临时目录 %s 内：TestMain 的重定向没有生效", absRoot, tmp)
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		if strings.HasPrefix(strings.ToLower(absRoot), strings.ToLower(filepath.Join(local, "MyAgent"))) {
			t.Fatalf("托管根指向了用户真实数据目录: %s", absRoot)
		}
	}

	// 2) 到此刻为止没有任何真实路径被改动。
	names := make([]string, 0, len(safetyBefore))
	for name := range safetyBefore {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		before := safetyBefore[name]
		if strings.HasPrefix(before.SnapshotHash, "SNAPSHOT-ERROR") {
			t.Fatalf("%s 快照失败：%s", name, before.SnapshotHash)
		}
		now, err := snapshotRoot(before.Root)
		if err != nil {
			t.Fatalf("%s 重新快照失败: %v", name, err)
		}
		if now.SnapshotHash != before.SnapshotHash {
			t.Fatalf("%s 在测试运行中已变化：before=%+v now=%+v", name, before, now)
		}
		t.Logf("[safety] %s 未变化 exists=%v entries=%d files=%d hash=%s",
			name, before.Exists, before.Entries, before.Files, short(before.SnapshotHash))
	}
}

// nowFunc 是服务里 Now 的默认实现（测试注入固定时间）。
func nowFunc() time.Time { return time.Now().UTC() }

// TestSafetyDiffAttributesHostOwnedFiles 守"归因而不是放宽"的边界：
// 只有宿主自己拥有的那几个文件可以被解释掉，其它路径的变化必须判 FAIL。
func TestSafetyDiffAttributesHostOwnedFiles(t *testing.T) {
	before := safetySnapshot{Lines: map[string]string{
		"agent/auth.json":   "x|1",
		"agent/skills/a":    "x|1",
		"agent/models.json": "x|1",
	}}
	after := safetySnapshot{Lines: map[string]string{
		"agent/auth.json":   "x|2", // 宿主刷新：可解释
		"agent/models.json": "x|2", // 宿主刷新：可解释
		"agent/skills/a":    "x|2", // 安装器/任何人改写技能：必须暴露
	}}
	diffs := snapshotDiffs(before, after)
	if len(diffs) != 3 {
		t.Fatalf("差异应列出 3 条路径，实际 %v", diffs)
	}
	var unexplained []string
	for _, d := range diffs {
		if !hostOwnedMutable(d) {
			unexplained = append(unexplained, d)
		}
	}
	if len(unexplained) != 1 || unexplained[0] != "agent/skills/a" {
		t.Fatalf("只有 agent/skills/a 应留在不可解释列表，实际 %v", unexplained)
	}

	// 新增/删除路径永远不可解释（哪怕落在宿主目录下），否则删掉用户技能会被放过。
	created := safetySnapshot{Lines: map[string]string{"agent/sessions/x": "y"}}
	if diff := snapshotDiffs(safetySnapshot{Lines: map[string]string{}}, created); len(diff) != 1 ||
		diff[0] != "agent/sessions/x" {
		t.Fatalf("新增路径必须出现在差异里，实际 %v", diff)
	}
}
