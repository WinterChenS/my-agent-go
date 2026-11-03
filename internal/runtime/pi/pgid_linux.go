//go:build linux

package pi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// liveCountInGroup 统计进程组内的**非僵尸**成员数。
//
// 走 /proc 而不是再调一次 kill：僵尸进程仍占着进程表条目，
// kill(-pgid, 0) 对它会返回成功，于是"已经被杀、只等 reap"的进程
// 会被永久算成存活。Linux 上 /proc/<pid>/stat 第 3 个字段就是状态，
// 'Z' / 'X' 即僵尸，直接排除。
func liveCountInGroup(pgid int) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	live := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // 不是进程目录（self、sys 等）
		}
		state, group, ok := readProcStat(pid)
		if !ok || group != pgid {
			continue
		}
		if state == 'Z' || state == 'X' {
			continue // 僵尸 / 已死：不占资源、不执行代码，不该算作"仍在运行"
		}
		live++
	}
	return live, true
}

// readProcStat 解析 /proc/<pid>/stat，返回 (状态字符, 进程组号, 是否解析成功)。
//
// 格式：pid (comm) state ppid pgrp ...
// comm 里可能含空格甚至括号（进程名可以叫 "a) b"），所以必须从**最后一个** ')'
// 之后开始切分，按空格 split 会把这种名字切错位。
func readProcStat(pid int) (byte, int, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	s := string(raw)
	cut := strings.LastIndexByte(s, ')')
	if cut < 0 || cut+2 >= len(s) {
		return 0, 0, false
	}
	fields := strings.Fields(s[cut+2:])
	if len(fields) < 3 || len(fields[0]) == 0 {
		return 0, 0, false
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], group, true
}
