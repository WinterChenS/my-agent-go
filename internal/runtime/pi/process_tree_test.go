package pi

import (
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 本文件直接测 processTree，不经过 Worker 状态机。
//
// 为什么要单独测一层：原先 RPC05 只有 TestWorkerTerminatesGrandchild 一条用例，
// 它在非 Windows 上 t.Skip，而 Unix 的进程组回收代码因此**一次都没被执行过**——
// 就算 process_unix.go 里的 kill(-pgid) 写错，CI 六个目标也全绿。
// 那是"覆盖看起来全，实际零执行"的典型假绿。
//
// 这里用 fakeworker 构造一棵真实的进程树（父 + 孙），断言：
//  1. 树被强杀后，本树内的孙进程确实停止；
//  2. 树外同样形态的进程仍然存活（证明不是"按可执行文件名杀"）。
// 两个断言合起来才有意义：只查第 1 条的话，一个"杀掉系统上所有 fakeworker"的实现也能过。

// startOwnedTree 启动一个会产生孙进程的 fakeworker，并把它交给进程树管理。
func startOwnedTree(t *testing.T, exe, marker string) (*processTree, io.WriteCloser, *exec.Cmd, func()) {
	t.Helper()

	tree, err := newProcessTree()
	if err != nil {
		t.Fatalf("创建进程树失败: %v", err)
	}

	cmd := exec.Command(exe, "-mode", "grandchild", "-marker", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	configureSysProcAttr(cmd.SysProcAttr)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = tree.close()
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		_ = tree.close()
		t.Fatalf("启动 fakeworker: %v", err)
	}
	if err := tree.attach(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = tree.close()
		t.Fatalf("把进程加入进程树失败: %v", err)
	}

	return tree, stdin, cmd, func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		// 已经被 Wait 过时这里会返回 "os: process already finished"，忽略即可。
		_, _ = cmd.Process.Wait()
		_ = tree.close()
	}
}

// TestProcessTreeKillsOwnedOnly 对应 RPC05 的跨平台部分：
// 强杀进程树只带走它自己拥有的进程，不动系统上形态相同的其他进程。
func TestProcessTreeKillsOwnedOnly(t *testing.T) {
	exe := fakeWorkerExe(t)

	dir := t.TempDir()
	ownedMarker := filepath.Join(dir, "owned.hb")
	outsideMarker := filepath.Join(dir, "outside.hb")

	// 树外进程：同样形态（同一个二进制、同样参数），唯一区别是**没有**被 attach。
	outside := exec.Command(exe, "-child", "-marker", outsideMarker)
	if err := outside.Start(); err != nil {
		t.Fatalf("启动树外进程失败: %v", err)
	}
	t.Cleanup(func() {
		_ = outside.Process.Kill()
		_, _ = outside.Process.Wait()
	})
	waitFor(t, 10*time.Second, "树外进程开始写心跳", func() bool { return fileSize(outsideMarker) > 0 })

	tree, stdin, _, stop := startOwnedTree(t, exe, ownedMarker)
	defer stop()

	// 触发孙进程：协议格式与真实 worker 一致（一行一条 JSON）。
	if _, err := io.WriteString(stdin, `{"id":"1","type":"spawn"}`+"\n"); err != nil {
		t.Fatalf("触发孙进程失败: %v", err)
	}
	waitFor(t, 10*time.Second, "孙进程开始写心跳", func() bool { return fileSize(ownedMarker) > 0 })

	// 测试前提：孙进程必须**持续**写心跳，否则"心跳停了"可能只是它本来就没跑起来。
	time.Sleep(200 * time.Millisecond)
	before := fileSize(ownedMarker)
	time.Sleep(400 * time.Millisecond)
	if fileSize(ownedMarker) <= before {
		t.Fatalf("测试前提不成立：孙进程没有持续写心跳（%d 字节未增长）", before)
	}

	if err := tree.kill(); err != nil {
		t.Fatalf("强杀进程树失败: %v", err)
	}

	// 允许一点收敛时间，但只比对"是否还在增长"，不假设某个具体字节数。
	time.Sleep(300 * time.Millisecond)
	stopped := fileSize(ownedMarker)
	time.Sleep(1 * time.Second)
	if after := fileSize(ownedMarker); after != stopped {
		t.Fatalf("owned 孙进程在强杀进程树后仍存活（心跳 %d → %d 字节）", stopped, after)
	}

	// 树外进程必须还活着。
	outsideBefore := fileSize(outsideMarker)
	time.Sleep(700 * time.Millisecond)
	if after := fileSize(outsideMarker); after <= outsideBefore {
		t.Fatalf("树外进程被误杀（心跳停在 %d 字节）—— 清理逻辑可能在按名字杀进程", outsideBefore)
	}
}

// TestProcessTreeActiveCountTracksOwned 验证 activeCount 的语义：
// attach 后至少能看见 1 个进程，强杀后必须收敛到 0。
//
// 这一条不能省：Worker.Close 用 activeCount 判定"是否真的清干净"，
// 如果它恒返回 0，CLEANUP_FAILED 就永远不会触发；如果它恒返回非 0，
// 每次关闭都会被判成清理失败。两种错都必须被这条用例抓住。
func TestProcessTreeActiveCountTracksOwned(t *testing.T) {
	exe := fakeWorkerExe(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "owned.hb")

	tree, stdin, cmd, stop := startOwnedTree(t, exe, marker)
	defer stop()

	if _, err := io.WriteString(stdin, `{"id":"1","type":"spawn"}`+"\n"); err != nil {
		t.Fatalf("触发孙进程失败: %v", err)
	}
	waitFor(t, 10*time.Second, "孙进程开始写心跳", func() bool { return fileSize(marker) > 0 })

	n, ok := tree.activeCount()
	if !ok {
		t.Fatalf("attach 之后仍无法判定进程数（limitation=%q）", tree.limitation())
	}
	if n < 1 {
		t.Fatalf("attach 之后应有至少 1 个 owned 进程，实际 %d", n)
	}

	if err := tree.kill(); err != nil {
		t.Fatalf("强杀进程树失败: %v", err)
	}

	// reap 自己的直接子进程。这不是"为了让测试变绿"的补丁：
	// 真实调用方 Worker.supervise 在 Close 之前一定会 cmd.Wait()，
	// 而 activeCount 的职责是数"活着的进程"，不负责回收尸体。
	// 不 Wait 的话主进程会一直是僵尸留在进程组里（必须靠平台实现剔除）。
	_, _ = cmd.Process.Wait()

	// 轮询收敛。Unix 上被杀进程可能短暂处于僵尸态，仍在进程表里，
	// 因此这里给到 5s；超时仍非 0 就是真实的"清理未完成"，必须判失败。
	deadline := time.Now().Add(5 * time.Second)
	var last int
	var lastOK bool
	for {
		last, lastOK = tree.activeCount()
		if lastOK && last == 0 {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("强杀进程树 5s 后仍有 %d 个 owned 进程（可判定=%v，limitation=%q，GOOS=%s）\n进程组快照：\n%s",
		last, lastOK, tree.limitation(), runtime.GOOS, groupSnapshot(tree.pid))
}

// groupSnapshot 打印指定进程组内的成员，用于失败时定位"到底是谁还活着"。
// 跨平台差异大，因此按 GOOS 选 ps 或 /proc；取不到就返回原因而不是空串——
// 空快照会让失败信息看起来像"没有进程但计数非 0"，反而更难查。
func groupSnapshot(pgid int) string {
	bin := "/bin/ps"
	args := []string{"-A", "-o", "pid=,pgid=,stat=,comm="}
	if runtime.GOOS == "windows" {
		return "(windows 走 Job Object，无进程组概念)"
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return "ps 执行失败: " + err.Error()
	}
	want := strconv.Itoa(pgid)
	var b strings.Builder
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == want {
			b.WriteString("  " + strings.Join(f, " ") + "\n")
		}
	}
	if b.Len() == 0 {
		return "  (ps 未列出该进程组任何成员)\n"
	}
	return b.String()
}

// TestProcessTreeRejectsUnattachedKill 未绑定进程时 kill 必须报错，
// 不能静默成功 —— 否则 Close 会把"根本没接管"当成"已经清干净"。
func TestProcessTreeRejectsUnattachedKill(t *testing.T) {
	// Unix 的实现以 pid==0 表示未绑定；Windows 以 job 句柄为 0 表示已关闭。
	// 两者都要求返回错误，但各自的"未绑定形态"不同，所以这里各测各的自然入口：
	// 先建再关，之后任何 kill 都必须失败。
	tree, err := newProcessTree()
	if err != nil {
		t.Fatalf("创建进程树失败: %v", err)
	}
	if err := tree.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := tree.kill(); err == nil {
		t.Fatal("已关闭的进程树不得返回 kill 成功")
	}
}
