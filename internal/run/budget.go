package run

// 每 run 预算（RUN06）：最多 100 次工具调用、最长 30 分钟。
//
// 两条预算的拦截点不同，必须分开实现：
//   - 工具调用次数：在**执行前授权之前**拦（policybridge.Options.Gate），
//     否则第 101 次已经问过策略、甚至已经弹了审批框，预算就只是记账不是限制；
//   - 运行时长：由计时器在 run 外侧触发，停 worker 并记 failed，
//     不能等模型"下一次请求"才生效——那时费用已经发生。
//
// 预算超限是正常终止理由（BUDGET_EXCEEDED），不是清理失败，也不是完成。

import (
	"context"
	"fmt"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// budgetExceededPayload 是 EventBudgetExceeded 的载荷。
//
// kind=tool_calls 时带 limit/used；kind=time 时带 limitMs。
// 两者不同时出现是有意的：界面不需要为"哪一个是有效的"再猜一次。
type budgetExceededPayload struct {
	Kind    string `json:"kind"`
	Limit   int    `json:"limit,omitempty"`
	Used    int    `json:"used,omitempty"`
	LimitMs int64  `json:"limitMs,omitempty"`
}

// budgetGate 是本次 run 的预算闸门，与 runState 一一对应。
//
// 并发安全：Gate 由策略端点从 HTTP 处理协程调用，计时器从另一条协程调用，
// 所有状态都在 mu 下读写。
type budgetGate struct {
	svc *Service
	rs  *runState

	maxToolCalls int
	maxDuration  time.Duration
	started      time.Time

	mu       sync.Mutex
	used     int
	exceeded bool
	reason   string
	notified bool // budget.exceeded 只发一次
}

// newBudgetGate 构造一次 run 的预算闸门。started 取构造时刻，即 run 启动时刻。
func (s *Service) newBudgetGate(rs *runState) *budgetGate {
	return &budgetGate{
		svc:          s,
		rs:           rs,
		maxToolCalls: s.opts.MaxToolCalls,
		maxDuration:  s.opts.MaxRunDuration,
		started:      s.opts.Clock.Now(),
	}
}

// check 是传给 policybridge.Options.Gate 的执行前闸门。
//
// 计数口径是"试图执行的工具调用"：被策略服务拒绝的调用也计数。预算是
// 对 agent 行为的限制，不是对成功副作用的计费；否则一个被反复拒绝的
// 死循环永远撞不到上限。
func (g *budgetGate) check(ctx context.Context, _ policy.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notify, err := g.reserve()
	if notify != nil {
		g.svc.emit(g.rs, EventBudgetExceeded, *notify)
	}
	return err
}

// reserve 在锁内判定这次调用是否放行，并顺带把快照与一次性通知算好。
func (g *budgetGate) reserve() (*budgetExceededPayload, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 已经进入停止/终态的 run 一律不再放行工具调用（RUN04「取消后不发新写请求」）：
	// 用户已经表示不要这次结果，再执行写操作只会产生无人认领的副作用。
	// 这种拒绝不计入预算：被取消的 run 不该"消耗"配额。
	// 只拦 stopping 与终态，不拦 starting——prompt 下发后 Pi 可能在
	// 状态迁到 running 之前就发来授权请求，那一次是合法执行。
	if st := g.rs.snapshot().State; st == contracts.RunStateStopping || IsTerminal(st) {
		return nil, contracts.NewAppError(contracts.CodeRpcCancelled,
			"run 已停止，拒绝新的工具调用", false)
	}

	// 已经超限：直接拒绝，不再重复记账也不再重复发事件。
	if g.exceeded {
		g.publishLocked()
		return nil, g.exceededErrorLocked()
	}

	// 时间先判：已经跑满 30 分钟就不该再放任何调用进来。
	if g.maxDuration > 0 && g.svc.opts.Clock.Now().Sub(g.started) >= g.maxDuration {
		g.exceeded = true
		g.reason = contracts.BudgetReasonTime
	}
	// 工具调用：第 101 次被拒（计数器停在 100，不是记到 101 再报错）。
	if !g.exceeded && g.used+1 > g.maxToolCalls {
		g.exceeded = true
		g.reason = contracts.BudgetReasonToolCalls
	}

	if g.exceeded {
		g.publishLocked()
		return g.takeNotificationLocked(), g.exceededErrorLocked()
	}

	g.used++
	g.publishLocked()
	return nil, nil
}

// markTimeExceeded 由运行计时器调用：记下超限原因并发出一次性事件。
func (g *budgetGate) markTimeExceeded() {
	g.mu.Lock()
	if !g.exceeded {
		g.exceeded = true
		g.reason = contracts.BudgetReasonTime
	} else if g.reason == "" {
		g.reason = contracts.BudgetReasonTime
	}
	g.publishLocked()
	notify := g.takeNotificationLocked()
	g.mu.Unlock()
	if notify != nil {
		g.svc.emit(g.rs, EventBudgetExceeded, *notify)
	}
}

// exceededErrorLocked 构造"预算用尽"错误。文案写清是哪一条预算，
// 前端按 Code 分支，人看文案就知道该改什么。
func (g *budgetGate) exceededErrorLocked() error {
	if g.reason == contracts.BudgetReasonTime {
		return contracts.NewAppError(contracts.CodeBudgetExceeded,
			fmt.Sprintf("预算用尽：每 run 最多运行 %s", g.maxDuration), false)
	}
	return contracts.NewAppError(contracts.CodeBudgetExceeded,
		fmt.Sprintf("预算用尽：每 run 最多 %d 次工具调用", g.maxToolCalls), false)
}

// takeNotificationLocked 返回本次要发的 budget.exceeded 载荷；已经发过就返回 nil。
func (g *budgetGate) takeNotificationLocked() *budgetExceededPayload {
	if g.notified {
		return nil
	}
	g.notified = true
	if g.reason == contracts.BudgetReasonTime {
		return &budgetExceededPayload{
			Kind:    contracts.BudgetReasonTime,
			LimitMs: g.maxDuration.Milliseconds(),
		}
	}
	return &budgetExceededPayload{
		Kind:  contracts.BudgetReasonToolCalls,
		Limit: g.maxToolCalls,
		Used:  g.used,
	}
}

// publishLocked 把预算执行情况写进快照（整体替换指针，读侧拿到的副本不会跟着变）。
func (g *budgetGate) publishLocked() {
	g.rs.setBudget(&contracts.RunBudget{
		MaxToolCalls:   g.maxToolCalls,
		MaxDurationMs:  g.maxDuration.Milliseconds(),
		ToolCalls:      g.used,
		Exceeded:       g.exceeded,
		ExceededReason: g.reason,
	})
}

// setBudget 更新快照里的预算执行情况。
func (rs *runState) setBudget(b *contracts.RunBudget) {
	rs.mu.Lock()
	rs.snap.Budget = b
	rs.mu.Unlock()
}

// startRunBudget 启动运行时长预算。
//
// 计时器在调用方**同步登记**，而不是在 goroutine 里登记：用可控时钟推进时必须
// 确定它已经存在，否则"推进之后才注册"会把到期时刻算到新的 now 上，永远不触发。
func (s *Service) startRunBudget(rs *runState, g *budgetGate) {
	if g.maxDuration <= 0 {
		return
	}
	due := s.after(g.maxDuration)
	go func() {
		select {
		case <-rs.finished:
		case <-due:
			s.enforceTimeBudget(g)
		}
	}()
}

// enforceTimeBudget 在运行时长超限时收尾。
//
// 顺序有讲究：先打"停止是本进程发起的"标记，pump 才不会把随后关闭的事件流
// 报成"Pi 意外中断"——那是谎话，还会多一条 CLEANUP_FAILED 的 run.error。
func (s *Service) enforceTimeBudget(g *budgetGate) {
	rs := g.rs
	if IsTerminal(rs.snapshot().State) {
		return
	}
	g.markTimeExceeded()
	rs.mu.Lock()
	rs.stopRequested = true
	rs.mu.Unlock()
	_ = s.transition(rs, contracts.RunStateStopping)

	budgetErr := contracts.NewAppError(contracts.CodeBudgetExceeded,
		fmt.Sprintf("预算用尽：本次 run 已运行满 %s", g.maxDuration), false)
	go func() {
		if err := s.stopWorkerWait(rs); err != nil {
			// 清理没做完必须留痕并阻止会话重启：预算用尽不等于"进程已经收干净"。
			reason := "预算用尽后清理未完成：" + err.Error()
			s.markCleanupFailed(reason)
			s.emit(rs, EventDiagnostic, map[string]any{
				"kind":    "stop.cleanup_failed",
				"message": reason,
			})
		}
		if IsTerminal(rs.snapshot().State) {
			return
		}
		s.finishFailed(rs, budgetErr)
	}()
}
