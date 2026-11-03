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

// activeCount 用 kill(-pgid, 0) 探测进程组是否还有成员。
// 返回 1 表示"可能仍有进程"（保守），0 表示确认清空。
func (p *processTree) activeCount() (int, bool) {
	if p.pid == 0 {
		return 0, false
	}
	err := syscall.Kill(-p.pid, 0)
	switch {
	case err == nil:
		return 1, true
	case errors.Is(err, syscall.ESRCH):
		return 0, true
	default:
		// EPERM 等：进程存在但无权限探测，按"仍存在"处理，不能宣称清理成功。
		return 1, true
	}
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
