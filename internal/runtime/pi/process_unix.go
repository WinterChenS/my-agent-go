//go:build !windows

package pi

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// processTree 在 Unix 上用独立进程组表示"本 worker 拥有的进程树"。
//
// 与 Windows Job Object 的差别必须如实记录：
//   - 只能保证信号发给整个进程组，无法像 job 那样拿到精确的存活计数
//   - 若子进程自行 setsid 脱离进程组，本方案抓不到（Pi 与 MCP server 都不会这么做，
//     但这是方案限制，不是保证）
//
// 本机无 Unix 运行环境，本文件未经实机验证，相关用例在 Linux/macOS 上执行才算数。
type processTree struct {
	pid  int
	note string
}

func configureSysProcAttr(attr *syscall.SysProcAttr) {
	// Setpgid：新进程成为独立进程组组长，之后可以用 kill(-pgid) 一次覆盖整棵树。
	attr.Setpgid = true
}

func newProcessTree() (*processTree, error) {
	return &processTree{}, nil
}

func (p *processTree) attach(proc *os.Process) error {
	if proc == nil {
		return errors.New("进程为空")
	}
	p.pid = proc.Pid
	return nil
}

func (p *processTree) kill() error {
	if p.pid == 0 {
		return fmt.Errorf("未绑定进程")
	}
	return syscall.Kill(-p.pid, syscall.SIGKILL)
}

// activeCount 统计进程组内仍然**活着**的成员数。
//
// 只用 kill(-pgid, 0) 是不够的：僵尸进程仍留在进程表里，探针对它返回成功，
// 于是一个早已被杀死、只等父进程 reap 的进程会一直被算成"仍然存活"。
// 后果是 Close 把"已经清干净"误报成 CLEANUP_FAILED。
// 这不是理论问题——macOS CI 上 TestProcessTreeActiveCountTracksOwned 就是这么红的。
//
// 因此分两步：先用 kill 探针走快路径（组已空直接返回 0），
// 再让平台实现剔除僵尸（Linux 读 /proc，macOS 读 ps）。
func (p *processTree) activeCount() (int, bool) {
	if p.pid == 0 {
		return 0, false
	}
	err := syscall.Kill(-p.pid, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return 0, true
	case err != nil:
		// EPERM 等：进程存在但无权限探测，按"仍存在"处理，不能宣称清理成功。
		return 1, true
	}
	return liveCountInGroup(p.pid)
}

func (p *processTree) close() error {
	p.pid = 0
	return nil
}

func (p *processTree) limitation() string {
	if p.note != "" {
		return p.note
	}
	return "Unix 进程组方案：若子孙自行 setsid 脱离进程组将无法回收"
}
