package browser

// B05：控制权、授权与"每个 tab 一条串行队列"的类型与不变量。
//
// 基线 §7（2026-11-03）对这件事的口径是逐字的：
//   - 每个 session 最多 4 个 tab，总计 12 个；
//   - 控制状态 Agent/Pausing/Human/Resuming，接管递增 controlGeneration，
//     排队动作失效，恢复强制新 snapshot；
//   - 取消保证的是**不再派发新的动作**，不能撤回已提交给 native 引擎/外部服务的
//     输入；已提交但超时/未知结果标记 OutcomeUnknown，不得自动重放；
//   - 若不能确认当前动作已静止，保持 Pausing/ControlUncertain，不显示已完成接管。
//
// 因此这里刻意把"状态机"写成数据而不是散落在各调用点：
//   - 任何写动作都必须先证明自己站在 Agent 态、且快照代数等于当前代数；
//   - 接管**先**递增代数并清空队列，再决定显示 Pausing 还是 Human；
//   - 队列里没派发的动作在接管后一律拿到结构化撤销结论（没有任何输入发出）；
//   - 只有"已派发且还在飞"的那一个才允许让状态停留在 Pausing。

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// 控制状态。
//
// 与前端（B07）的协议：前端只按 Mode 分支，不理解队列细节。数值/字符串一旦
// 上线就是协议，改名等于改前端，所以取值刻意用直白的小写词。
const (
	// ControlAgent：agent 拥有控制权，写动作可以入队派发。
	ControlAgent ControlMode = "agent"
	// ControlPausing：接管已请求，但还有已派发、无法撤回的写操作没静止。
	// 这个状态下**不接受** agent 写动作，也不显示"已完成接管"。
	ControlPausing ControlMode = "pausing"
	// ControlHuman：人工拥有控制权，agent 写动作一律拒绝。
	ControlHuman ControlMode = "human"
	// ControlResuming：人工已交还控制权，但 agent 还没有用**新快照**重新观察。
	// 这期间写动作仍然被拒绝（"恢复强制新 snapshot"）。
	ControlResuming ControlMode = "resuming"
	// ControlUncertain：无法确认动作是否静止（例如已派发输入超时）。
	// 只能由用户显式接管/停止该 tab 来收敛，不能自动猜成 Agent 或 Human。
	ControlUncertain ControlMode = "control_uncertain"
)

// ControlMode 是控制状态机的一档。
type ControlMode string

// AllControlModes 是 ControlMode 的完整集合（前端枚举与测试共用）。
var AllControlModes = []ControlMode{
	ControlAgent, ControlPausing, ControlHuman, ControlResuming, ControlUncertain,
}

// IsKnown 报告该状态是否是契约里的一档。
func (m ControlMode) IsKnown() bool {
	for _, v := range AllControlModes {
		if v == m {
			return true
		}
	}
	return false
}

// 浏览器控制/队列专用错误码。
//
// 与 CodeTabLimitExceeded / CodeTargetObscured 一样，contracts 尚未登记的先在本包
// 定义；将来收进 contracts 必须保持字符串值不变（前端按 Code 分支）。
const (
	// CodeQueueFull 表示该 tab 的待派发队列已满，动作没有被接受。
	CodeQueueFull = "QUEUE_FULL"
	// CodeActionRevoked 表示动作已入队但没有派发，被接管/导航/取消撤销。
	// 它与 OUTCOME_UNKNOWN 的区别是决定性的：撤销 = 输入**没有**发出。
	CodeActionRevoked = "TAB_ACTION_REVOKED"
)

// MaxQueuedActionsPerTab 是单个 tab 的待派发队列上限。
//
// 有界是刻意的：无限队列会让"接管后 queued=0"变成一句空话（队列可以无限长，
// 用户等不到它归零），也会让一次提示注入把内存和 click 预算一起吃光。
// 4 与"每 session 4 个 tab"无关，是队列长度。
const MaxQueuedActionsPerTab = 4

// ControlState 是 TakeOver/Resume/Control 的返回值。
//
// NavigationID 与面板/工具输出里的同一个值来自同一处（Service 记录的当前文档），
// 所以"工具说 tab 是哪个文档"和"面板显示哪个文档"不会各说一套。
type ControlState struct {
	TabID        TabID       `json:"tabId"`
	Mode         ControlMode `json:"mode"`
	Generation   uint64      `json:"generation"`
	NavigationID string      `json:"navigationId"`
	// Queued 是已入队但没有派发的动作数；接管后必须为 0。
	Queued int `json:"queued"`
	// InFlight 是已派发但还没静止的动作数；>0 时 Mode 只能是 Pausing/Uncertain。
	InFlight int `json:"inFlight"`
	// Detail 是给人看的具体原因（例如"还有 1 个已派发动作未静止"）。
	Detail string `json:"detail,omitempty"`
}

// TabInfo 是"面板与工具输出必须一致"的那组事实。
//
// URL/NavigationID 由 Service 记录（OpenTab/Navigate/Observe 时更新），不是页面自报值：
// 页面自报 URL 属不可信输入，只能用于显示参考，不能作为授权/一致性判据。
type TabInfo struct {
	TabID             TabID               `json:"tabId"`
	URL               string              `json:"url"`
	NavigationID      string              `json:"navigationId"`
	ControlGeneration uint64              `json:"controlGeneration"`
	OwnerSessionID    contracts.SessionID `json:"ownerSessionId"`
	Mode              ControlMode         `json:"mode"`
	Queued            int                 `json:"queued"`
	InFlight          int                 `json:"inFlight"`
}

// AuthorizationRequest 是一次浏览器写动作的授权请求。
//
// 字段全部来自**服务端状态**（会话归属、当前文档、当前代数），不接受模型自报：
// 参数里能改的只有动作本身。
type AuthorizationRequest struct {
	TabID        TabID               `json:"tabId"`
	SessionID    contracts.SessionID `json:"sessionId"`
	Action       Action              `json:"action"`
	Origin       string              `json:"origin"`
	URL          string              `json:"url"`
	NavigationID string              `json:"navigationId"`
	Generation   uint64              `json:"generation"`
}

// AuthorizationToken 是一次已获授权动作的凭据，派发前要再核一遍。
//
// 它不含任何"批准"能力：只是把 append 时的判据抄一份，好在派发前发现
// 过期/换会话/换站点/换控制权这些**排队期间**才发生的变化。
type AuthorizationToken struct {
	ApprovalID string              `json:"approvalId,omitempty"`
	SessionID  contracts.SessionID `json:"sessionId"`
	Origin     string              `json:"origin"`
	Generation uint64              `json:"generation"`
	ExpiresAt  time.Time           `json:"expiresAt"`
	Reason     string              `json:"reason,omitempty"`
}

// Authorizer 是浏览器写动作的唯一授权点。
//
// 为什么是两次调用而不是一次：写动作会先入队再派发，中间可能发生接管、导航、
// 换站点、审批过期、会话关闭。Authorize 回答"现在能不能排队"，Recheck 回答
// "就在把输入交给引擎之前，这条许可是否仍然成立"。
//
// 实现方**不要**在 Recheck 里再走一遍会消费一次性许可的路径（那会让第二次
// 复查必然失败，或让用户被要求批准同一次调用两次）；Recheck 只复核 token 上的
// 判据是否还成立。
type Authorizer interface {
	Authorize(ctx context.Context, exec contracts.ExecutionContext, req AuthorizationRequest) (AuthorizationToken, error)
	Recheck(ctx context.Context, exec contracts.ExecutionContext, req AuthorizationRequest, token AuthorizationToken) error
}

// OriginOf 把 URL 归一到 scheme://host（没有路径的站点身份）。
//
// 授权许可绑定的是站点 origin（基线：可授"站点范围"许可），不是整条 URL：
// 同一站点不同路径不应各自弹一次审批，但换站点必须重新授权。
func OriginOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		// 解析不出来就原样返回：宁可让"两个不同的坏 URL"看起来不同，
		// 也不要把它们都归一成空串而互相冒充。
		return raw
	}
	return u.Scheme + "://" + u.Host
}

// revokedError 构造"已入队但未派发即被撤销"的错误。
//
// 文案刻意写明"没有任何输入发出"：调用方（模型）最危险的反应是把撤销当成
// 失败后重试，而重试恰恰会在人工控制期间再写入一次。
func revokedError(reason string) error {
	msg := "动作已入队但未派发（没有任何输入发出）：" + reason
	return appErr(CodeActionRevoked, msg)
}

// controlUncertainError 构造"无法确认静止"的错误。
func controlUncertainError(reason string) error {
	return appErr(contracts.CodeControlUncertain, reason)
}

// modeRejectsWrite 说明某档控制状态为什么不允许 agent 写动作。
func modeRejectsWrite(mode ControlMode, detail string) error {
	switch mode {
	case ControlAgent:
		return nil
	case ControlHuman:
		return appErr(contracts.CodeNeedsHuman,
			"该 tab 当前由人工控制（Human）：agent 写操作被拒绝，未派发任何输入")
	case ControlPausing:
		msg := "该 tab 正在接管（Pausing）：还有已派发的写操作未静止，agent 写操作被拒绝"
		if detail != "" {
			msg += "（" + detail + "）"
		}
		return controlUncertainError(msg)
	case ControlResuming:
		return appErr(contracts.CodeStaleSnapshot,
			"该 tab 正在恢复（Resuming）：必须先 observe 取一份新快照，再用新 ref 动作")
	case ControlUncertain:
		msg := "该 tab 的控制状态不确定（ControlUncertain）：无法确认是否有输入仍在生效，agent 写操作被拒绝"
		if detail != "" {
			msg += "（" + detail + "）"
		}
		return controlUncertainError(msg)
	default:
		return appErr(contracts.CodeControlUncertain, fmt.Sprintf(
			"未知控制状态 %q：按最严处理，拒绝一切写操作", mode))
	}
}

// TokenGuard 把"授权结论"固化成 token，并在派发前复核它是否仍然成立。
//
// 放在契约层而不是各自的授权适配器里，是为了让"过期/换会话/换站点/换控制权"
// 这四条规则只有一份实现：浏览器工具（经策略桥）、测试替身、将来的其它入口
// 都复用它，不会出现"某个入口忘了查 origin"这种分叉。
type TokenGuard struct {
	// TTL 是许可的有效期；<=0 用 60 秒（与基线 §6 的审批有效期一致）。
	TTL time.Duration
	// Now 便于测试注入时钟。
	Now func() time.Time
}

// DefaultApprovalTTL 是审批/许可的默认有效期（基线 §6：60 秒）。
const DefaultApprovalTTL = 60 * time.Second

func (g TokenGuard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g TokenGuard) ttl() time.Duration {
	if g.TTL > 0 {
		return g.TTL
	}
	return DefaultApprovalTTL
}

// Issue 生成一次已授权动作的凭据。
func (g TokenGuard) Issue(session contracts.SessionID, req AuthorizationRequest, approvalID, reason string) AuthorizationToken {
	return AuthorizationToken{
		ApprovalID: approvalID,
		SessionID:  session,
		Origin:     req.Origin,
		Generation: req.Generation,
		ExpiresAt:  g.now().Add(g.ttl()),
		Reason:     reason,
	}
}

// Check 在派发前复核 token。
//
// 这四条正好覆盖"排队期间能发生的变化"：换会话（跨 session 操作）、
// 换站点（origin 变了还沿用旧许可）、接管/恢复（控制代数变了）、
// 许可过期。任何一条不成立都拒绝，输入不发出。
func (g TokenGuard) Check(exec contracts.ExecutionContext, req AuthorizationRequest, token AuthorizationToken) error {
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return appErr(contracts.CodeUnauthorized, "缺少会话身份：拒绝派发")
	}
	if token.SessionID != exec.SessionID {
		return appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"许可属于会话 %s，当前会话是 %s：拒绝派发", token.SessionID, exec.SessionID))
	}
	if token.Generation != req.Generation {
		return appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"许可签发于控制代 %d，当前是 %d（接管/恢复已改变控制权）：拒绝派发",
			token.Generation, req.Generation))
	}
	if token.Origin != req.Origin {
		return appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"许可签发给站点 %q，当前站点是 %q：站点变了必须重新授权，拒绝派发",
			token.Origin, req.Origin))
	}
	if !g.now().Before(token.ExpiresAt) {
		return appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"许可已于 %s 过期：拒绝派发", token.ExpiresAt.UTC().Format(time.RFC3339)))
	}
	return nil
}
