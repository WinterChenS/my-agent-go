package harnessmcp

// B05：浏览器写动作的授权适配器 —— 把 browser.Authorizer 接到策略服务上。
//
// 为什么需要这一层而不是让工具自己判断：
//   - 工具是"模型能碰到的面"，它不应该是授权点（模型能改的东西不能当判据）；
//   - 策略服务（internal/policy，由 policybridge 对 Pi 扩展暴露）是唯一权威，
//     它已经实现了 session/参数指纹/configRevision/controlGeneration 的绑定与
//     一次性许可的原子领取。
//
// 两次检查的分工（对应 Service.Execute 的两次调用）：
//
//	Authorize（入队前）: 判"现在允不允许排队"。
//	  deny  → 立刻拒绝，没有任何输入发出；
//	  ask   → 推给用户（OnPending），等答复；超时/撤销 → 拒绝；
//	  allow → 命中会话/站点许可。
//	Recheck（派发前，就在把输入交给引擎之前）:
//	  TokenGuard 复核 会话 / 站点 origin / 控制代 / 有效期；
//	  再用 ValidateBinding **原子领取**一次性执行权（once 只领取一次）。
//
// 关键设计：once 许可的"领取"放在 Recheck 而不是 Authorize。放前面的话，
// 排队期间的接管/导航/过期就没有第二次机会拦住它，而"排队期间被接管后仍写入"
// 正是 BR11 要挡的失败模式。
//
// 参数指纹里刻意包含 origin 与 navigationId：站点或文档变了，同一条审批不再成立
// （对应 BR13 的"改 origin 后 counter 为 0"）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// BrowserPolicyGate 是浏览器授权需要的最小策略面（*policy.Service 实现它）。
//
// 用接口而不是具体类型，理由和 connectors 的 PolicyGate 一样：工具测试可以
// 注入"只拒绝"的实现，证明拒绝路径确实没有派发任何输入。
type BrowserPolicyGate interface {
	Authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Result, error)
	Pending(ctx context.Context, id string) (policy.ApprovalRequest, error)
	ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call policy.ToolCall) error
	RevokeApproval(ctx context.Context, id string) error
}

// BrowserPolicyAuthorizer 把策略服务适配成 browser.Authorizer。
type BrowserPolicyAuthorizer struct {
	// Gate 是唯一授权权威，不可为空。
	Gate BrowserPolicyGate
	// Guard 负责 token 的签发与派发前复核（会话/origin/代数/过期）。
	Guard browser.TokenGuard
	// ApprovalWait 是等待人工答复的上限，默认与基线一致（60s）。
	ApprovalWait time.Duration
	// PollInterval 是等待期间的轮询间隔（默认 20ms）。
	PollInterval time.Duration
	// OnPending 把待审批请求推给 UI；第二个参数是该调用的原始形参，
	// 答复时用它校验指纹（与 policybridge 的 handleResolve 同一口径）。
	OnPending func(policy.ApprovalRequest, policy.ToolCall)
	// Now 便于测试注入时钟。
	Now func() time.Time

	// bound 是本次 run 的执行身份（来自 run binding）。非空时授权器只认这一份：
	// 调用方拿别的 session/run/workspace 来调 Authorize/Recheck 都会被拒。
	//
	// 为什么要有它：工具参数、调用方都可能被提示注入影响，而身份只能来自服务端
	// 绑定的 run。零值（nil）表示调用方自己保证 exec 的来源（B05 的既有写法），
	// 生产装配用 NewBrowserPolicyAuthorizer 把它钉住。
	bound *contracts.ExecutionContext
}

// BrowserAuthorizationBinding 是一次 run 的执行身份（run binding 的授权视图）。
//
// 就是 RunService 在 admission 时冻结的那份 contracts.ExecutionContext：
// workspace/session/run/configRevision/controlGeneration 全部来自它，授权器不另造
// 身份来源，也不接受调用方自报。
type BrowserAuthorizationBinding struct {
	Exec contracts.ExecutionContext
}

// Validate 检查绑定是否完整。缺会话的绑定不能用来装配授权器。
func (b BrowserAuthorizationBinding) Validate() error {
	if strings.TrimSpace(string(b.Exec.SessionID)) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing,
			"浏览器写动作授权缺少会话绑定：不能在没有 run 身份的情况下装配授权器", false)
	}
	return nil
}

// BrowserAuthorizerOptions 是生产装配入口的参数。
type BrowserAuthorizerOptions struct {
	// Binding 是本次 run 的执行身份；非空时授权器只认这一份身份。
	Binding *BrowserAuthorizationBinding
	// Gate 是唯一授权权威（*policy.Service），不可为空。
	Gate BrowserPolicyGate
	// Guard 负责 token 的生命周期与派发前复核。
	Guard browser.TokenGuard
	// ApprovalWait 是等待人工答复的上限（默认 60s）。
	ApprovalWait time.Duration
	// PollInterval 是等待期间的轮询间隔（默认 20ms）。
	PollInterval time.Duration
	// OnPending 把待审批请求推给产品层（审批队列/UI）。
	OnPending func(policy.ApprovalRequest, policy.ToolCall)
	// Now 便于测试注入时钟。
	Now func() time.Time
}

// NewBrowserPolicyAuthorizer 装配生产用的浏览器授权器。
//
// 这是**唯一**允许缺省参数的生产入口：没有策略服务（Gate）时直接返回错误，
// 而不是返回一个“看起来能用、实际默认放行”的授权器。调用方拿到错误后应当
// 把 Authorizer 留空（browser.Service 会拒绝所有写动作）。
func NewBrowserPolicyAuthorizer(opts BrowserAuthorizerOptions) (*BrowserPolicyAuthorizer, error) {
	if opts.Gate == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing,
			"浏览器写动作授权器缺少策略服务：拒绝装配（不能退化成默认放行）", false)
	}
	a := &BrowserPolicyAuthorizer{
		Gate:         opts.Gate,
		Guard:        opts.Guard,
		ApprovalWait: opts.ApprovalWait,
		PollInterval: opts.PollInterval,
		OnPending:    opts.OnPending,
		Now:          opts.Now,
	}
	if opts.Binding != nil {
		if err := opts.Binding.Validate(); err != nil {
			return nil, err
		}
		exec := opts.Binding.Exec
		a.bound = &exec
	}
	return a, nil
}

// 默认值。
const (
	defaultBrowserApprovalWait = 60 * time.Second
	defaultBrowserPollInterval = 20 * time.Millisecond
)

// browserCallArguments 是浏览器写动作的授权参数（进审批指纹）。
//
// 它必须是**确定性**的：Authorize 与 Recheck 用同一份参数算指纹，否则同一条
// 审批永远对不上。字段全部显式，不用 map（map 迭代顺序会破坏确定性）。
type browserCallArguments struct {
	TabID        string                     `json:"tabId"`
	Kind         string                     `json:"kind"`
	SnapshotID   string                     `json:"snapshotId"`
	Ref          string                     `json:"ref"`
	Key          string                     `json:"key"`
	Text         string                     `json:"text"`
	DeltaX       float64                    `json:"deltaX"`
	DeltaY       float64                    `json:"deltaY"`
	Origin       string                     `json:"origin"`
	URL          string                     `json:"url"`
	NavigationID string                     `json:"navigationId"`
	Condition    *browser.ExpectedCondition `json:"condition,omitempty"`
}

// buildBrowserCall 把一次授权请求变成策略服务认识的 ToolCall。
//
// 名字固定为 browser_act：策略按工具名做权限判断（例如没有网络权限时
// 任何含 "browser" 的工具都被拒），按参数做指纹绑定。
func buildBrowserCall(req browser.AuthorizationRequest) (policy.ToolCall, error) {
	args := browserCallArguments{
		TabID:        string(req.TabID),
		Kind:         string(req.Action.Kind),
		SnapshotID:   req.Action.SnapshotID,
		Ref:          req.Action.Ref,
		Key:          req.Action.Key,
		Text:         req.Action.Text,
		DeltaX:       req.Action.DeltaX,
		DeltaY:       req.Action.DeltaY,
		Origin:       req.Origin,
		URL:          req.URL,
		NavigationID: req.NavigationID,
	}
	if req.Action.ExpectedCondition.Kind != "" {
		cond := req.Action.ExpectedCondition
		args.Condition = &cond
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return policy.ToolCall{}, contracts.NewAppError(contracts.CodeInternal,
			"浏览器动作参数无法序列化: "+err.Error(), false)
	}
	return policy.ToolCall{
		ID:            fmt.Sprintf("browser-%s-%s", req.TabID, req.Action.SnapshotID),
		Name:          BrowserToolAct,
		Source:        policy.SourceHarnessMCP,
		Arguments:     raw,
		Origin:        "user",
		ResourceScope: req.Origin,
	}, nil
}

// Authorize 是入队前的那次授权。
func (a *BrowserPolicyAuthorizer) Authorize(ctx context.Context, exec contracts.ExecutionContext, req browser.AuthorizationRequest) (browser.AuthorizationToken, error) {
	if a == nil || a.Gate == nil {
		return browser.AuthorizationToken{}, contracts.NewAppError(contracts.CodeUnauthorized,
			"没有配置策略服务：浏览器写动作默认拒绝（未经授权不得写入页面）", false)
	}
	if err := a.requireBinding(exec); err != nil {
		return browser.AuthorizationToken{}, err
	}
	if req.SessionID != exec.SessionID {
		return browser.AuthorizationToken{}, contracts.NewAppError(contracts.CodeUnauthorized,
			"授权请求的会话与执行身份不一致：浏览器写动作默认拒绝（未派发任何输入）", false)
	}
	call, err := buildBrowserCall(req)
	if err != nil {
		return browser.AuthorizationToken{}, err
	}
	res, err := a.Gate.Authorize(ctx, exec, call)
	if err != nil {
		return browser.AuthorizationToken{}, err
	}
	switch res.Decision {
	case policy.DecisionDeny:
		msg := "策略拒绝本次浏览器写操作（未派发任何输入）"
		if strings.TrimSpace(res.Reason) != "" {
			msg += "：" + res.Reason
		}
		return browser.AuthorizationToken{}, contracts.NewAppError(contracts.CodeUnauthorized, msg, false)
	case policy.DecisionAllow:
		return a.Guard.Issue(exec.SessionID, req, "", res.Reason), nil
	case policy.DecisionAsk:
		if strings.TrimSpace(res.ApprovalID) == "" {
			return browser.AuthorizationToken{}, contracts.NewAppError(contracts.CodeInternal,
				"策略要求审批但没有给出审批 ID：拒绝派发", false)
		}
		if err := a.awaitApproval(ctx, res.ApprovalID, call); err != nil {
			return browser.AuthorizationToken{}, err
		}
		// 注意：这里**不**领取一次性执行权。领取放在 Recheck（派发前），
		// 否则排队期间被接管/导航的动作会带着一条已消费的许可继续往下走。
		return a.Guard.Issue(exec.SessionID, req, res.ApprovalID, "用户已批准"), nil
	}
	return browser.AuthorizationToken{}, contracts.NewAppError(contracts.CodeInternal,
		"策略返回了未知结论: "+string(res.Decision), false)
}

// awaitApproval 等待人工答复；超时或取消都撤销审批并拒绝。
//
// 不执行是唯一安全的默认：超时后继续执行等于"用户没看也算同意"。
func (a *BrowserPolicyAuthorizer) awaitApproval(ctx context.Context, approvalID string, call policy.ToolCall) error {
	if a.OnPending != nil {
		if p, perr := a.Gate.Pending(ctx, approvalID); perr == nil {
			a.OnPending(p, call)
		}
	}
	wait := a.ApprovalWait
	if wait <= 0 {
		wait = defaultBrowserApprovalWait
	}
	interval := a.PollInterval
	if interval <= 0 {
		interval = defaultBrowserPollInterval
	}
	deadline := a.now().Add(wait)
	for {
		if _, perr := a.Gate.Pending(ctx, approvalID); perr != nil {
			// 不再是待审批：结论已定（allow 会在 Recheck 里原子领取）。
			return nil
		}
		if a.now().After(deadline) {
			_ = a.Gate.RevokeApproval(context.Background(), approvalID)
			return contracts.NewAppError(contracts.CodeUnauthorized,
				"等待人工确认超时：已撤销该审批，未派发任何输入", false)
		}
		select {
		case <-ctx.Done():
			_ = a.Gate.RevokeApproval(context.Background(), approvalID)
			return contracts.NewAppError(contracts.CodeUnauthorized,
				"等待人工确认期间请求被取消：已撤销该审批，未派发任何输入", false)
		case <-time.After(interval):
		}
	}
}

// Recheck 是派发前的那次复查。
func (a *BrowserPolicyAuthorizer) Recheck(ctx context.Context, exec contracts.ExecutionContext, req browser.AuthorizationRequest, token browser.AuthorizationToken) error {
	if a == nil {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"没有配置浏览器授权器：浏览器写动作默认拒绝（未派发任何输入）", false)
	}
	if err := a.requireBinding(exec); err != nil {
		return err
	}
	if err := a.Guard.Check(exec, req, token); err != nil {
		return err
	}
	if strings.TrimSpace(token.ApprovalID) == "" {
		// 命中会话/站点许可：token 上的判据已经复核过，无需再动审批表。
		return nil
	}
	call, err := buildBrowserCall(req)
	if err != nil {
		return err
	}
	if err := a.Gate.ValidateBinding(ctx, exec, token.ApprovalID, call); err != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"派发前复查未通过（许可已撤销/过期/参数或站点已变）：未派发任何输入。原因: "+err.Error(), false)
	}
	return nil
}

func (a *BrowserPolicyAuthorizer) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// requireBinding 检查这次调用的执行身份。
//
// 两条规则都不允许“猜”：没有会话就不授权；钉住了 run binding 时，workspace/
// session/run/configRevision/controlGeneration 任何一项不同都拒绝（换个 run 的 exec
// 来复用许可，等于让调用方自报身份）。
func (a *BrowserPolicyAuthorizer) requireBinding(exec contracts.ExecutionContext) error {
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"缺少执行会话身份：浏览器写动作默认拒绝（身份只能来自服务端绑定的 run）", false)
	}
	if a.bound == nil {
		return nil
	}
	if !sameExecution(exec, *a.bound) {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"执行身份与本次 run 的绑定不一致（workspace/session/run/configRevision/controlGeneration）："+
				"浏览器写动作默认拒绝，未派发任何输入", false)
	}
	return nil
}

func sameExecution(a, b contracts.ExecutionContext) bool {
	return a.WorkspaceID == b.WorkspaceID &&
		a.SessionID == b.SessionID &&
		a.RunID == b.RunID &&
		a.ConfigRevision == b.ConfigRevision &&
		a.ControlGeneration == b.ControlGeneration
}
