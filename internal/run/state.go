// Package run 是任务状态、事件与停止/恢复的编排层（F05）。
//
// 定位：它不实现 agent loop（那是 Pi 的事），也不实现授权判定（那是 policy 的事）。
// 它负责把两者串成一条可观察、可中断、可重放的 run：状态只能按合法边迁移，
// 事件 seq 单调递增且可重放，停止有明确的时限与升级路径。
//
// 纪律：
//   - prompt 被接受（accepted）不等于完成；只有 Pi 的 agent_settled 才算 completed。
//   - 未知完成语义（未来版本换了事件名）不得猜 completed，只能记 interrupted/失败。
//   - 取消排队中的 run 不得顺带把它启动起来。
package run

import "my-agent-go/internal/contracts"

// transitions 是合法状态迁移表。不在表内的迁移一律拒绝并返回错误。
//
// 为什么要把表写死：状态误迁移（例如 accepted 直接 completed）会让 UI 显示
// "已完成"而实际工具还在跑，是最难排查的一类问题。宁可拒绝，也不"尽量迁就"。
var transitions = map[string][]string{
	contracts.RunStateQueued: {
		contracts.RunStateStarting,
		contracts.RunStateCancelled,
		contracts.RunStateFailed,
	},
	contracts.RunStateStarting: {
		contracts.RunStateRunning,
		contracts.RunStateWaitingApproval,
		contracts.RunStateFailed,
		contracts.RunStateCancelled,
		contracts.RunStateInterrupted,
	},
	contracts.RunStateRunning: {
		contracts.RunStateWaitingApproval,
		contracts.RunStateStopping,
		contracts.RunStateCompleted,
		contracts.RunStateFailed,
		contracts.RunStateCancelled,
		contracts.RunStateInterrupted,
	},
	contracts.RunStateWaitingApproval: {
		contracts.RunStateRunning,
		contracts.RunStateStopping,
		contracts.RunStateFailed,
		contracts.RunStateCancelled,
		contracts.RunStateInterrupted,
	},
	contracts.RunStateStopping: {
		contracts.RunStateCancelled,
		contracts.RunStateCompleted,
		contracts.RunStateFailed,
		contracts.RunStateInterrupted,
	},
	// 终态不再迁出。
	contracts.RunStateCompleted:   {},
	contracts.RunStateFailed:      {},
	contracts.RunStateCancelled:   {},
	contracts.RunStateInterrupted: {},
}

// IsTerminal 报告状态是否为终态（终态不再产生写请求）。
func IsTerminal(state string) bool {
	next, ok := transitions[state]
	return ok && len(next) == 0
}

// CanTransition 判定 from → to 是否合法。
func CanTransition(from, to string) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}
