package run

// 自动放行的留痕（规格 2026-11-08 权限模式第 3 条）。
//
// 策略服务判定完“这次允许”之后回调这里，把结论发成一条 run 事件；
// 事件与其它 run 事件走同一个 emit（订阅者 / 诊断 recorder / 前端 emitter），
// 不另开记录通道——另开一条的结果一定是“诊断页看不到”。
//
// 为什么回调里只做记录：
//   - 结论已经定了，这里再判一次会变成第二个决策者；
//   - 策略包不认识事件与 sink，依赖方向仍然是 run → policy。

import (
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// noteAutoAllowed 是 policy.Service 的自动放行观察者实现。
//
// 找不到对应的 run（理论上不会发生：档位是随 run 登记/摘除的）只丢弃，不返回错误：
// 留痕失败不能反过来把一条已经放行的调用变成失败，那会让“记录”变成新的否决权。
func (s *Service) noteAutoAllowed(exec contracts.ExecutionContext, call policy.ToolCall, mode string) {
	s.mu.Lock()
	rs := s.runs[exec.RunID]
	s.mu.Unlock()
	if rs == nil {
		return
	}
	s.emit(rs, EventPolicyAutoAllowed, autoAllowedPayload{
		RunID:          string(exec.RunID),
		SessionID:      string(exec.SessionID),
		ToolName:       call.Name,
		ToolCallID:     call.ID,
		Source:         string(call.Source),
		ResourceScope:  call.ResourceScope,
		PermissionMode: mode,
		Reason:         policy.ReasonAutoAllowed,
	})
}
