//go:build darwin

package pi

import (
	"os/exec"
	"strconv"
	"strings"
)

// liveCountInGroup 统计进程组内的**非僵尸**成员数。
//
// macOS 没有 /proc，因此用系统自带的 ps 取全表再过滤。
// 为什么不能只靠 kill(-pgid, 0)：僵尸进程仍在进程表里，探针对它返回成功，
// 已经被杀、只等父进程 reap 的进程会被永久算成"仍然存活"，
// 让 Close 把"已清干净"误报成 CLEANUP_FAILED（macOS CI 实测踩过）。
//
// `-o pgid=,stat=` 的等号形式是 BSD ps 的"只要值、不要表头"写法，
// 输出形如 "  456 S"、"  457 Z"，解析时按空白切分即可。
func liveCountInGroup(pgid int) (int, bool) {
	out, err := exec.Command("/bin/ps", "-A", "-o", "pgid=,stat=").Output()
	if err != nil {
		return 0, false
	}
	live := 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		group, err := strconv.Atoi(fields[0])
		if err != nil || group != pgid {
			continue
		}
		// stat 的首字符是状态：Z = 僵尸，X = 已死。
		state := fields[1]
		if state == "" || state[0] == 'Z' || state[0] == 'X' {
			continue
		}
		live++
	}
	return live, true
}
