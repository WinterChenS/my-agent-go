package browser

import (
	"context"
	"fmt"
	"strings"

	"my-agent-go/internal/contracts"
)

// BrowserBackend 是"在某个真实引擎里开 tab、看页面、动手"的接口。
//
// 三个平台各实现一份（B01 WebView2 / B02 WKWebView / B03 WebKitGTK），
// 上层（BrowserService、MCP 工具）只依赖本接口，不 import 任何平台 native 包。
//
// 所有方法都必须尊重 ctx：超时返回错误而不是无限等待；已经派发出去的输入
// 超时后只能标记结果未知（OutcomeUnknown），不得自动重放（基线 §7）。
type BrowserBackend interface {
	// Capabilities 返回实测能力，不能按 GOOS 写死。
	Capabilities() CapabilitySet
	// CreateTab 新建 tab。调用方负责先做 tab 限额检查（CheckTabLimit）。
	CreateTab(ctx context.Context, opts TabOptions) (TabID, error)
	// Navigate 导航到 url，返回时至少已发起导航；不保证页面加载完成。
	Navigate(ctx context.Context, tab TabID, url string) error
	// Observe 取页面结构与受限文本，返回的 ref 只在该 Snapshot 内有效。
	Observe(ctx context.Context, tab TabID, opts ObserveOptions) (Snapshot, error)
	// Act 按 ref 执行一次动作。Delivered 不等于业务成功，见 ActionResult。
	Act(ctx context.Context, tab TabID, action Action) (ActionResult, error)
	// Screenshot 截取可见区域并带上坐标元数据。
	Screenshot(ctx context.Context, tab TabID) (Image, error)
	// CloseTab 释放 tab；关闭后该 tab 的 ref 与队列动作一并失效。
	CloseTab(ctx context.Context, tab TabID) error
}

// TabOptions 是新建 tab 的参数。
//
// ProfileID 决定持久登录态归属（基线 §7：workspace 级 profile）。
// OwnerSessionID 是授权归属，B05 用它拒绝跨 session 操作。
type TabOptions struct {
	URL            string              `json:"url"`
	ProfileID      contracts.ProfileID `json:"profileId,omitempty"`
	OwnerSessionID contracts.SessionID `json:"ownerSessionId,omitempty"`
}

// Validate 只做形状检查：URL 必须以 http(s) 开头，避免把 file:// 和 JS 伪协议
// 塞进原生引擎（远程页面本来就是不可信输入）。
func (o TabOptions) Validate() error {
	u := strings.TrimSpace(o.URL)
	if u == "" {
		return appErr(contracts.CodeUnsupported, "创建 tab 需要 url")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return appErr(contracts.CodeUnsupported, "只接受 http(s) URL，收到 "+u)
	}
	return nil
}

// ObserveOptions 是观察参数。
//
// 上限只能往下调，不能往上调：0 表示用基线默认值，超过基线值会被夹到基线值。
type ObserveOptions struct {
	MaxElements  int  `json:"maxElements,omitempty"`
	MaxTextChars int  `json:"maxTextChars,omitempty"`
	IncludeText  bool `json:"includeText,omitempty"`
}

// EffectiveMaxElements 返回实际生效的元素上限。
func (o ObserveOptions) EffectiveMaxElements() int {
	return clampLimit(o.MaxElements, MaxSnapshotElements)
}

// EffectiveMaxTextChars 返回实际生效的文本字符上限。
func (o ObserveOptions) EffectiveMaxTextChars() int {
	return clampLimit(o.MaxTextChars, MaxSnapshotTextChars)
}

func clampLimit(v, max int) int {
	if v <= 0 || v > max {
		return max
	}
	return v
}

// UnimplementedBackend 是 B00 的占位 backend：每个方法都返回 NOT_IMPLEMENTED。
//
// 它存在的意义是让 spike 真实记录"还没有原生视图"，而不是让调用方拿到零值
// 结构体后误以为成功。B01 起由真正的 native 实现替换它。
type UnimplementedBackend struct {
	// Reason 是给人看的具体原因，会进错误消息。
	Reason string
}

// Capabilities 返回全 false 的能力集，并逐项说明缺失原因。
func (b UnimplementedBackend) Capabilities() CapabilitySet {
	reason := b.reason("没有平台 native 宿主")
	set := CapabilitySet{
		Platform: "none",
		Engine:   "none",
	}
	for _, name := range []string{"navigate", "observe", "act", "screenshot",
		"trustedInput", "crossOriginFrames", "closedShadowRoots", "downloads", "popups"} {
		set.Unsupported = append(set.Unsupported, UnsupportedCapability{
			Capability: name,
			Reason:     reason,
		})
	}
	return set
}

// CreateTab 永远失败：没有原生视图可创建。
func (b UnimplementedBackend) CreateTab(context.Context, TabOptions) (TabID, error) {
	return "", b.notImplemented("CreateTab")
}

// Navigate 永远失败。
func (b UnimplementedBackend) Navigate(context.Context, TabID, string) error {
	return b.notImplemented("Navigate")
}

// Observe 永远失败，返回空快照。
func (b UnimplementedBackend) Observe(context.Context, TabID, ObserveOptions) (Snapshot, error) {
	return Snapshot{}, b.notImplemented("Observe")
}

// Act 永远失败。返回 Delivered=false，不能声称输入已发出。
func (b UnimplementedBackend) Act(context.Context, TabID, Action) (ActionResult, error) {
	return ActionResult{Verification: Verification{
		Status: VerificationUnknown,
		Detail: "backend 未实现，没有任何输入被发出",
	}}, b.notImplemented("Act")
}

// Screenshot 永远失败，返回空图。
func (b UnimplementedBackend) Screenshot(context.Context, TabID) (Image, error) {
	return Image{}, b.notImplemented("Screenshot")
}

// CloseTab 永远失败：没有 tab 可关也说明没实现，不能返回 nil 假装清理成功。
func (b UnimplementedBackend) CloseTab(context.Context, TabID) error {
	return b.notImplemented("CloseTab")
}

func (b UnimplementedBackend) notImplemented(method string) error {
	return appErr(CodeNotImplemented, "browser backend 未实现（B00）："+method+" 需要 B01/B02/B03 的平台宿主；"+b.Reason)
}

func (b UnimplementedBackend) reason(fallback string) string {
	if strings.TrimSpace(b.Reason) == "" {
		return fallback
	}
	return b.Reason
}

// 编译期断言：占位实现必须满足接口，否则 B01 会拿到一个半实现的类型才发现问题。
var _ BrowserBackend = UnimplementedBackend{}

// String 便于日志里一眼看出当前是占位实现。
func (b UnimplementedBackend) String() string {
	return fmt.Sprintf("UnimplementedBackend(reason=%q)", b.reason(""))
}

// SessionTabs 统计每个 session 的活跃 tab 数，用来执行 4/session 的限额。
//
// 它只记账不创建实例：B05 的真机用例要求第 5 个 tab 被拒绝时"没有新 native
// 实例"，所以限额判断必须发生在 native 创建之前。
type SessionTabs struct {
	perSession map[contracts.SessionID]int
	total      int
}

// NewSessionTabs 创建空的 tab 记账表。
func NewSessionTabs() *SessionTabs {
	return &SessionTabs{perSession: map[contracts.SessionID]int{}}
}

// Reserve 在创建 native 实例之前占一个名额；超限返回 TAB_LIMIT_EXCEEDED。
//
// 超限时不做任何记账：B05 要求"第 5/第 13 个 tab 被拒绝且没有新 native 实例"，
// 失败后计数必须原样，否则下一次判断会继续被拖偏。
func (t *SessionTabs) Reserve(session contracts.SessionID) error {
	if t.perSession == nil {
		t.perSession = map[contracts.SessionID]int{}
	}
	if err := CheckTabLimit(t.perSession[session], t.total); err != nil {
		return err
	}
	t.perSession[session]++
	t.total++
	return nil
}

// Release 归还名额；关 tab 或创建失败回滚时调用。
func (t *SessionTabs) Release(session contracts.SessionID) {
	if t.perSession == nil {
		return
	}
	if n := t.perSession[session]; n > 0 {
		t.perSession[session] = n - 1
		if t.perSession[session] == 0 {
			delete(t.perSession, session)
		}
		t.total--
	}
}

// CountFor 返回某 session 的活跃 tab 数。
func (t *SessionTabs) CountFor(session contracts.SessionID) int {
	return t.perSession[session]
}

// Total 返回全部 session 的活跃 tab 数。
func (t *SessionTabs) Total() int {
	return t.total
}
