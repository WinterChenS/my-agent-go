package config

import "strings"

// 审批档位（2026-11-08 权限模式增量规格）。
//
// 这是"要不要逐次人工审批"的显式档位，**不是**把审批 TTL 调长：
//   - ask：每次写动作/命令都要人工确认（默认，与既有行为一致）；
//   - full_access：同一 run 内不再逐次弹审批，但每次放行仍逐条留事件
//     （run 包发 policy.auto_allowed），审批卡不出现而"发生了什么"可追溯。
//
// 它不放宽权限本身：filesystem/shell/network 仍是硬边界（见 Permissions、
// policy.violatesPermission）。要放宽范围必须显式改 Permissions——两个开关不合并，
// 免得"允许完全访问"被读成"把 unrestricted 塞进文件系统权限"。
const (
	// PermissionModeAsk 是默认档位：每个写动作/命令都要人工确认。
	PermissionModeAsk = "ask"
	// PermissionModeFullAccess 跳过人工审批这一步，但仍逐条记事件。
	PermissionModeFullAccess = "full_access"
)

// NormalizePermissionMode 归一并校验档位名。
//
// 空串按默认档位 ask 处理（没有设置就是逐次审批）。
// 无法识别的值返回 false：调用方必须自己决定是拒绝还是退回 ask，
// 绝不能把它当成 full_access——"配置写错了反而更宽"是最坏的一种降级。
func NormalizePermissionMode(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", PermissionModeAsk:
		return PermissionModeAsk, true
	case PermissionModeFullAccess:
		return PermissionModeFullAccess, true
	default:
		return "", false
	}
}

// PermissionModeAutoApproves 判定一个档位是否跳过人工审批。
//
// 无法识别的档位**不**自动放行（返回 false）：与 NormalizePermissionMode 同一口径。
func PermissionModeAutoApproves(mode string) bool {
	normalized, ok := NormalizePermissionMode(mode)
	return ok && normalized == PermissionModeFullAccess
}
