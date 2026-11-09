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
	"fmt"

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

	// EnvAttachmentURL / EnvAttachmentToken / EnvAttachmentSession 是本次 run 的
	// 只读附件 broker（C06f）。字节只能经这个端点取，且只能经**同一个打开句柄**
	// 校验过 size/sha256 之后才交出去；worker 永远拿不到可写附件路径。
	//
	// 令牌每 run 一枚、只经环境变量交付（与 policybridge 同一口径）：写在 argv
	// 会被进程列表看到，写进 DTO 会进前端与日志。
	EnvAttachmentURL     = "ANVIL_ATTACHMENT_URL"
	EnvAttachmentToken   = "ANVIL_ATTACHMENT_TOKEN"
	EnvAttachmentSession = "ANVIL_ATTACHMENT_SESSION"

	// EnvClarificationToken 是本次 run 的澄清身份令牌（C06f）。
	//
	// 产品自带的澄清扩展把它放在每个 extension UI 请求的标题前缀里，Go 侧据此
	// 确认"这个 UI 请求来自产品自带的澄清工具"——按**身份**而不是按工具名。
	// 第三方同名工具发出的 UI 请求没有这枚令牌，不会被当成澄清（见 clarification.go）。
	EnvClarificationToken = "ANVIL_CLARIFICATION_TOKEN"
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

// PolicyCeiling 是系统/工作区/Profile 三层合并出的权限上限。
//
// Ref 是这个上限的引用名（例如 "ceiling:workspace"）。请求里的 PermissionRef
// 只有在能解析到"不超过上限"的快照时才被接受。
type PolicyCeiling struct {
	Permissions config.Permissions `json:"permissions"`
	Revision    uint64             `json:"revision,omitempty"`
	Ref         string             `json:"ref,omitempty"`
}

// PermissionResolver 把 PermissionRef 解析成不可变快照。
//
// 由绑定层提供（C06b 之后的策略目录、C08/C09 的 capability 都从这里取同一条事实）。
// 为空表示服务只认识自己的上限引用：解析不了的 ref 一律结构化失败，
// 不猜、不回退到"最宽的那一份"。
type PermissionResolver interface {
	ResolvePermission(ref string) (PolicySnapshot, error)
}

// AttachmentRef 是一次 run 请求携带的附件引用（C06f）。
//
// 它只含**元数据**：没有路径、没有字节。字节只能经 app 托管的只读 broker 按 ID
// 取，并且 broker 会在交付前用同一个打开句柄重新校验 size/sha256。
// 这四项在 admission 时随 workspace/policy 一起冻结：界面不能在 run 期间把它们
// 换成另一份内容。
type AttachmentRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"mediaType,omitempty"`
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
	// PermissionMode 是这次 run 的审批档位（ask / full_access），**admission 时钉住**。
	//
	// 它与 PolicySnapshot 是两件事：快照说"这次 run 最多能做什么"，档位说"这些动作
	// 要不要逐次找人批准"。分开的理由见规格 2026-11-08 权限模式：把「已开完全访问」
	// 写成更宽的 Permissions，就再也回不到"范围不变、只是不弹卡"这个状态。
	//
	// 钉在这里而不是每次判定时读设置：运行中切开关只影响下一条 run，否则同一条 run
	// 的前后步骤会适用不同规则，回放时无法解释。策略服务里的 per-run 登记
	// （policy.Service.PinRunPermissionMode）只是这份事实的执行侧镜像。
	PermissionMode string `json:"permissionMode"`
	// Attachments 是这次 run 冻结的附件引用（C06f）。空表示没有附件。
	// 它只带元数据，因此可以安全地进快照与事件；路径与令牌不在这里。
	Attachments []AttachmentRef `json:"attachments,omitempty"`
}

// AutoApproves 报告这次 run 是否跳过人工审批（只读快照上的档位）。
func (b ExecutionBinding) AutoApproves() bool {
	return config.PermissionModeAutoApproves(b.PermissionMode)
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

// normalizePermissions 归一权限值；无法识别的等级是错误，不是"按最严处理"。
//
// 静默降级会把"界面里写的是 X、实际跑的是 Y"变成常态，所以这里直接失败。
func normalizePermissions(p config.Permissions) (config.Permissions, error) {
	fs, ok := config.NormalizeFilesystem(p.Filesystem)
	if !ok {
		return config.Permissions{}, contracts.NewAppError(
			contracts.CodeUnsupported, "未知的文件系统权限: "+p.Filesystem, false)
	}
	return config.Permissions{Filesystem: fs, Network: p.Network, Shell: p.Shell}, nil
}

// filesystemRankOf 给出文件系统权限的宽严序（未知值按 none）。
func filesystemRankOf(p config.Permissions) int {
	name, _ := config.NormalizeFilesystem(p.Filesystem)
	switch name {
	case "unrestricted":
		return 3
	case "workspace":
		return 2
	case "read":
		return 1
	}
	return 0
}

// Narrow 把请求的快照收进上限之内；比上限宽就是结构化错误，绝不静默收窄。
//
// 为什么不能"顺手改小"：调用方（前端、调度任务）以为自己拿到了 shell/网络，
// 实际被悄悄降级——后果比拒绝严重得多，因为没有任何地方会告诉它被改了。
func (c PolicyCeiling) Narrow(want PolicySnapshot) (PolicySnapshot, error) {
	ceiling, err := normalizePermissions(c.Permissions)
	if err != nil {
		return PolicySnapshot{}, fmt.Errorf("策略上限不合法: %w", err)
	}
	got, err := normalizePermissions(want.Permissions)
	if err != nil {
		return PolicySnapshot{}, err
	}
	if filesystemRankOf(got) > filesystemRankOf(ceiling) {
		return PolicySnapshot{}, contracts.NewAppError(
			contracts.CodeUnauthorized,
			fmt.Sprintf("请求的文件系统权限 %s 超过上限 %s：权限不能被前端或调度扩大",
				got.Filesystem, ceiling.Filesystem),
			false,
		)
	}
	if got.Shell && !ceiling.Shell {
		return PolicySnapshot{}, contracts.NewAppError(
			contracts.CodeUnauthorized,
			"请求的 shell 权限超过上限（上限未授予 shell）：权限不能被前端或调度扩大",
			false,
		)
	}
	if got.Network && !ceiling.Network {
		return PolicySnapshot{}, contracts.NewAppError(
			contracts.CodeUnauthorized,
			"请求的网络权限超过上限（上限未授予网络）：权限不能被前端或调度扩大",
			false,
		)
	}
	return PolicySnapshot{Permissions: got, Revision: want.Revision}, nil
}
