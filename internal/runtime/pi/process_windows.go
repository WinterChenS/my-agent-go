//go:build windows

package pi

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobObjectBasicAccountingInformation 是 JobObjectBasicAccountingInformation 的信息类编号。
// x/sys/windows 没有导出对应的 Go 结构体（只导出了 extended 版本），这里按 Win32 定义补齐。
// 字段顺序与大小必须与 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION 完全一致。
const jobObjectBasicAccountingInformation = 1

type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// processTree 在 Windows 上用 Job Object 表示"本 worker 拥有的进程树"。
//
// 之所以不用"按可执行文件名杀进程"：那会误杀用户自己的 Pi、其他会话的 Pi 和系统 Node。
// Job Object 只包含被显式 assign 进来的进程及其子孙，ActiveProcesses 就是精确的 owned 计数，
// 既用于强杀，也用于判定"清理是否真的完成"（RPC05）。
type processTree struct {
	job      windows.Handle
	pid      int
	assigned bool
	note     string
}

func configureSysProcAttr(attr *syscall.SysProcAttr) {
	// 独立进程组：Ctrl+C / Ctrl+Break 不会波及宿主控制台。
	// 强杀与子孙清理靠 Job Object，不靠控制台事件。
	attr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

func newProcessTree() (*processTree, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Job Object 失败: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// KILL_ON_JOB_CLOSE：job 句柄关闭（含宿主异常退出）时内核会终结剩余进程，
	// 避免客户端崩溃后留下孤儿 Pi / MCP 进程。
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("设置 Job Object 限制失败: %w", err)
	}
	return &processTree{job: h}, nil
}

// attach 把刚启动的进程放进 job。
//
// 已知竞态：进程在 Start 之后、attach 之前有一个极短窗口，理论上可以抢先派生出
// 逃出 job 的子孙。Pi 启动后要加载配置与扩展才会拉起 MCP，实践中该窗口远小于
// 启动耗时；但这是需要如实记录的限制，不能声称"绝无可能"。
func (p *processTree) attach(proc *os.Process) error {
	p.pid = proc.Pid
	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION,
		false,
		uint32(proc.Pid),
	)
	if err != nil {
		p.note = "OpenProcess 失败: " + err.Error()
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()

	if err := windows.AssignProcessToJobObject(p.job, h); err != nil {
		p.note = "AssignProcessToJobObject 失败（当前进程可能位于不允许嵌套的 Job 中）: " + err.Error()
		return err
	}
	p.assigned = true
	return nil
}

func (p *processTree) kill() error {
	if p.job == 0 {
		return fmt.Errorf("job 句柄已关闭")
	}
	return windows.TerminateJobObject(p.job, 1)
}

// activeCount 返回 job 内仍存活的进程数。
// ok=false 表示无法判定（此时绝不能对外宣称清理成功）。
func (p *processTree) activeCount() (int, bool) {
	if p.job == 0 {
		return 0, false
	}
	var info jobBasicAccounting
	if err := windows.QueryInformationJobObject(
		p.job,
		jobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return 0, false
	}
	return int(info.ActiveProcesses), true
}

func (p *processTree) close() error {
	if p.job == 0 {
		return nil
	}
	err := windows.CloseHandle(p.job)
	p.job = 0
	return err
}

func (p *processTree) limitation() string {
	if p.note != "" {
		return p.note
	}
	if !p.assigned {
		return "进程未被加入 Job Object，子孙清理不可保证"
	}
	return ""
}
