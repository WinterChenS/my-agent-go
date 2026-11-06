// C06a：per-run 身份与策略。
//
// 应用进程只有一个 admission queue（见 Service），交互会话与 C08/C09 的自动化
// 提交共用它。所以"这次 run 是谁、以什么权限跑"必须是 per-run 的输入，而不是
// 服务实例上的字段：把身份留在实例上，第二个提交者就只能新建第二个队列，
// 全局活跃上限也就不再是 2。
//
// 解析发生在 admission（Service.Start），随 run 冻结。冻结后 run 期间不再重新
// 解析：中途"变严"会让已经发生的作用失去解释，"变宽"更是等于绕过审批。
package run

import (
	"encoding/json"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// 交给 worker 的 per-run 身份环境变量。
//
// 为什么走环境变量而不是 argv：argv 是 Pi 自己的命令行（--mode/--session-dir/
// --provider…），塞入产品私有参数会让真 Pi 直接报未知参数；而策略令牌早就走
// 环境变量（policybridge），同一份身份不应该有两条投递通道。
const (
	// EnvRunID 是本次 run 的 ID。
	EnvRunID = "ANVIL_RUN_ID"
	// EnvSessionID 是本次 run 所属会话。
	EnvSessionID = "ANVIL_SESSION_ID"
	// EnvWorkspaceID 是本次 run 所属工作区 ID。
	EnvWorkspaceID = "ANVIL_WORKSPACE_ID"
	// EnvWorkspaceDir 是本次 run 的工作目录（worker 的 cwd 与它一致）。
	EnvWorkspaceDir = "ANVIL_WORKSPACE_DIR"
	// EnvProfileID 是本次 run 使用的 Profile；空表示没有指定 Profile。
	EnvProfileID = "ANVIL_PROFILE_ID"
	// EnvPermissionRef 是本次 run 的权限引用；空表示沿用了默认快照。
	EnvPermissionRef = "ANVIL_PERMISSION_REF"
	// EnvPolicySnapshot 是本次 run 冻结的权限快照（JSON）。
	EnvPolicySnapshot = "ANVIL_POLICY_SNAPSHOT"
)

// PolicySnapshot 是一次 run 冻结的权限快照。
//
// Revision 是快照来源的版本（配置 revision 或策略目录 revision），只用于审计：
// 同一个 Permissions 在不同 revision 下对"为什么是这一份"给出不同解释，
// 但判定只看 Permissions。
type PolicySnapshot struct {
	Permissions config.Permissions `json:"permissions"`
	Revision    uint64             `json:"revision,omitempty"`
}

// ExecutionBinding 是一次 run 的执行身份与策略。
//
// ExecutionContext 仍然由 contracts 定义（不改它）：这里只负责把 per-run 的身份
// 与策略拼成一份，供 worker 环境、策略端点和后续 capability 共用同一份事实。
type ExecutionBinding struct {
	ExecutionContext contracts.ExecutionContext `json:"executionContext"`
	WorkspaceID      contracts.WorkspaceID      `json:"workspaceId"`
	WorkspaceDir     string                     `json:"workspaceDir"`
	ProfileID        contracts.ProfileID        `json:"profileId,omitempty"`
	PermissionRef    string                     `json:"permissionRef,omitempty"`
	PolicySnapshot   PolicySnapshot             `json:"policySnapshot"`
}

// SessionID 是本次 run 所属会话。
func (b ExecutionBinding) SessionID() contracts.SessionID { return b.ExecutionContext.SessionID }

// RunID 是本次 run 的 ID。
func (b ExecutionBinding) RunID() contracts.RunID { return b.ExecutionContext.RunID }

// ConfigRevision 是本次 run 依据的配置版本。
func (b ExecutionBinding) ConfigRevision() uint64 { return b.ExecutionContext.ConfigRevision }

// Env 返回交给 worker 的身份与策略环境变量。
//
// 顺序固定：调用方看到的环境是同一份事实的稳定投影，便于对照证据。
func (b ExecutionBinding) Env() []string {
	raw, err := json.Marshal(b.PolicySnapshot)
	if err != nil {
		// PolicySnapshot 只含字符串/布尔/整数，序列化不会失败；真的失败时
		// 宁可不给这条变量，也不给一个"看起来像快照"的残缺 JSON。
		raw = nil
	}
	env := []string{
		EnvRunID + "=" + string(b.RunID()),
		EnvSessionID + "=" + string(b.SessionID()),
		EnvWorkspaceID + "=" + string(b.WorkspaceID),
		EnvWorkspaceDir + "=" + b.WorkspaceDir,
		EnvProfileID + "=" + string(b.ProfileID),
		EnvPermissionRef + "=" + b.PermissionRef,
	}
	if len(raw) > 0 {
		env = append(env, EnvPolicySnapshot+"="+string(raw))
	}
	return env
}
