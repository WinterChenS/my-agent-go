package policy

// 审批档位：询问（ask）/ 允许完全访问（full_access）。
//
// 规格：docs/specs/2026-11-08-permission-mode-full-access.md。
//
// 一句话边界：**full_access 只免除人工审批，不免除权限边界**。
// 文件系统越界、未授予 shell、未知工具通道都在 Authorize 的前几步被 deny，
// 与档位无关；档位只在"本来要签一条审批"的那个位置改判为 allow。
//
// 为什么档位挂在服务上而不是拼进调用参数：
//   - 判定必须发生在**服务端**：档位若由调用方（策略扩展/前端）随请求带来，
//     一条伪造的请求就能给自己开免审，等于没有档位；
//   - per-run 钉住（PinRunPermissionMode）让同一条 run 的前后步骤永远适用同一档位：
//     运行中切开关只影响下一条 run，回放时能解释每一步为什么被放行。

import (
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// ReasonAutoAllowed 是自动放行的原因串（可识别、稳定）。
//
// 前端/诊断按它区分"人批的"与"档位放行的"：判定结论同为 allow，
// 但两者在审计上是不同的事。
const ReasonAutoAllowed = "允许完全访问：跳过人工审批"

// SetAutoAllow 设置服务级默认档位（Options.AutoAllow 的可变版本）。
//
// 它只影响**未登记 per-run 档位**的调用。交互 run 走 PinRunPermissionMode，
// 不会因为这里改了一下就中途变宽或变严。
func (s *Service) SetAutoAllow(on bool) {
	s.mu.Lock()
	s.autoAllow = on
	s.mu.Unlock()
}

// AutoAllow 返回当前服务级默认档位（诊断/测试用；判定请用 Authorize）。
func (s *Service) AutoAllow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoAllow
}

// PinRunPermissionMode 在 admission 钉住一条 run 的审批档位。
//
// 调用点是 run.Service.Start：权限档位是"这次 run 以什么规则跑"的一部分，
// 与策略快照一起在受理时冻结。无法识别的档位返回错误，而不是猜一个——
// 猜成 full_access 会让一条写错的配置变成"永久免审"。
func (s *Service) PinRunPermissionMode(runID contracts.RunID, mode string) error {
	normalized, ok := config.NormalizePermissionMode(mode)
	if !ok {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"未知的权限档位: "+mode, false)
	}
	if strings.TrimSpace(string(runID)) == "" {
		return contracts.NewAppError(contracts.CodeInternal, "钉住权限档位缺少 run id", false)
	}
	s.mu.Lock()
	if s.runModes == nil {
		s.runModes = map[contracts.RunID]string{}
	}
	s.runModes[runID] = normalized
	s.mu.Unlock()
	return nil
}

// ReleaseRunPermissionMode 在 run 释放时清掉登记的档位。
//
// run 终止后不会再有权威的授权请求（策略端点随 run 关闭），留着只会让
// 进程内 map 随 run 数量无界增长。未登记的 run 用服务级默认值（ask）。
func (s *Service) ReleaseRunPermissionMode(runID contracts.RunID) {
	s.mu.Lock()
	delete(s.runModes, runID)
	s.mu.Unlock()
}

// RunPermissionMode 返回一条 run 钉住的档位（second 为 false 表示未登记）。
//
// 只读查询，供诊断/测试核对"这条 run 当时按哪个档位跑"，不参与判定。
func (s *Service) RunPermissionMode(runID contracts.RunID) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode, ok := s.runModes[runID]
	return mode, ok
}

// SetAutoAllowObserver 安装自动放行留痕回调。
//
// run.Service 用它把每次自动放行发成 policy.auto_allowed 事件（与其他 run 事件
// 同一个 sink）。回调里不做判定、不改状态，也不允许在持锁时被调用。
func (s *Service) SetAutoAllowObserver(fn func(exec contracts.ExecutionContext, call ToolCall, mode string)) {
	s.mu.Lock()
	s.onAutoAllow = fn
	s.mu.Unlock()
}

// autoAllowFor 判定一条 run 当前的有效档位，并给出是否自动放行。
//
// 未登记的 run（含空 run id）用服务级默认值 —— 默认是 ask，fail-closed。
func (s *Service) autoAllowFor(runID contracts.RunID) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode, ok := s.runModes[runID]; ok {
		return mode, config.PermissionModeAutoApproves(mode)
	}
	if s.autoAllow {
		return config.PermissionModeFullAccess, true
	}
	return config.PermissionModeAsk, false
}

// notifyAutoAllowed 通知留痕回调。必须在**释放锁之后**调用。
func (s *Service) notifyAutoAllowed(exec contracts.ExecutionContext, call ToolCall, mode string) {
	s.mu.Lock()
	fn := s.onAutoAllow
	s.mu.Unlock()
	if fn == nil {
		return
	}
	fn(exec, call, mode)
}
