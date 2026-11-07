package browser

// B06：window.open 的受控接管（BR16）。
//
// 基线要求"弹窗新标签"，而实现里只有一条正确的做法：**把 window.open 变成同一个
// session 的另一个 tab**。任何"再开一个原生窗口"的路径都会让窗口脱离宿主：
// 它没有 owner、不受 tab 限额约束、面板看不见它、接管也管不到它。
//
// 因此这里没有"创建窗口"的代码，只有：
//  1. 校验请求来自一个真实归属本会话的父 tab（跨 session 一律拒绝）；
//  2. 只接受 http(s)：`javascript:` / `file:` 之类的弹窗连 tab 都不该有；
//  3. 走**同一条** OpenTab 路径，于是 4/session、12/total 的限额在创建 native 实例
//     之前判定——被拒绝的弹窗不会留下任何实例（BR12 的判据在此复用，没有第二套）；
//  4. 继承父 tab 的 profile 与 owner：弹窗是"同一个人的同一份登录态"，不是新身份；
//  5. 把父子关系记在服务里（Popups 可查），供面板收起子 tab、也供证据核对
//     "服务登记的 tab 数 == 引擎存活实例数"。

import (
	"context"
	"fmt"
	"strings"

	"my-agent-go/internal/contracts"
)

// NewWindowRequest 是原生宿主报上来的 window.open 请求。
//
// WindowFeatures 原样保留但不解释：V1 不实现"按 features 摆窗口"，因为弹窗一律
// 落成宿主内的 tab；把它丢掉又会让"页面确实请求了尺寸"这条事实无法在证据里核对。
// 它只是记录，不参与任何判定。
type NewWindowRequest struct {
	ParentTab      TabID  `json:"parentTab"`
	URL            string `json:"url"`
	WindowFeatures string `json:"windowFeatures,omitempty"`
}

// HandleNewWindow 把一次 window.open 变成同 session 的受控 tab。
//
// 返回的 TabID 与 OpenTab 的返回完全同源：它已经登记在服务里、受限额约束、
// 属于 exec 的会话。失败时返回结构化错误，且**不会**创建 native 实例。
func (s *Service) HandleNewWindow(ctx context.Context, exec contracts.ExecutionContext, req NewWindowRequest) (TabID, error) {
	url := strings.TrimSpace(req.URL)
	s.mu.Lock()
	parent, err := s.ownedLocked(exec, req.ParentTab)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	if url == "" || (!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")) {
		// 页面可以请求任何协议的弹窗；我们只把 http(s) 变成受控 tab，其余拒绝，
		// 并且明确写下"没有创建任何实例"，避免调用方把它当成"稍后重试就好"。
		denyErr := appErr(contracts.CodeUnsupported, fmt.Sprintf(
			"window.open 只接受 http(s) URL（收到 %q）：没有创建任何窗口或 tab", url))
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelinePopupDenied, TabID: req.ParentTab, SessionID: exec.SessionID,
			Generation: parent.generation, Mode: parent.mode, ErrorCode: errorCode(denyErr),
			Queued: parent.queued, Inflight: parent.inflight, Detail: url,
		})
		s.mu.Unlock()
		return "", denyErr
	}
	opts := TabOptions{
		URL:            url,
		ProfileID:      parent.profileID,
		OwnerSessionID: parent.owner,
	}
	parentGeneration := parent.generation
	s.mu.Unlock()

	// 复用 OpenTab：限额在 CreateTab 之前判定、owner 只能是自己、时间线落盘一次。
	// 这里**不**另写一套创建逻辑，否则"弹窗不受限额"会成为一条只有弹窗能走的旁路。
	tab, err := s.OpenTab(ctx, exec, opts)
	if err != nil {
		s.mu.Lock()
		if st, ok := s.tabs[req.ParentTab]; ok {
			s.appendTimelineLocked(TimelineEntry{
				Kind: TimelinePopupDenied, TabID: req.ParentTab, SessionID: exec.SessionID,
				Generation: st.generation, Mode: st.mode, ErrorCode: errorCode(err),
				Queued: st.queued, Inflight: st.inflight, Detail: url,
			})
		}
		s.mu.Unlock()
		return "", err
	}

	s.mu.Lock()
	st, ok := s.tabs[req.ParentTab]
	if !ok || st.closed {
		// 父 tab 在创建期间关闭：新 tab 不能变成"没有父的孤儿窗口"，直接关掉。
		s.mu.Unlock()
		_ = s.backend.CloseTab(context.WithoutCancel(ctx), tab)
		s.mu.Lock()
		delete(s.tabs, tab)
		s.limits.Release(exec.SessionID)
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineTabClosed, TabID: tab, SessionID: exec.SessionID,
			Detail: "父 tab 已关闭，弹窗一并关闭（不留孤儿窗口）",
		})
		s.mu.Unlock()
		return "", revokedError("父 tab 在 window.open 期间已关闭，弹窗已关闭")
	}
	child := s.tabs[tab]
	if child == nil {
		s.mu.Unlock()
		return "", appErr(contracts.CodeInternal, "弹窗 tab 未登记：拒绝留下不受控窗口")
	}
	if child.owner != parent.owner {
		// 不可能发生，但必须显式失败而不是继续：归属错乱的 tab 就是跨 session 泄漏。
		s.mu.Unlock()
		return "", appErr(contracts.CodeInternal, "弹窗归属与父 tab 不一致")
	}
	st.popups = append(st.popups, tab)
	s.popupParent[tab] = st.id
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelinePopupOpened, TabID: tab, SessionID: exec.SessionID,
		Generation: child.generation, Mode: child.mode, Detail: fmt.Sprintf(
			"parent=%s profile=%s url=%s parentGeneration=%d", st.id, st.profileID, url, parentGeneration),
	})
	s.mu.Unlock()
	return tab, nil
}

// Popups 返回某个父 tab 通过 window.open 打开的子 tab（按打开顺序）。
func (s *Service) Popups(exec contracts.ExecutionContext, parent TabID) ([]TabID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ownedLocked(exec, parent)
	if err != nil {
		return nil, err
	}
	out := make([]TabID, 0, len(st.popups))
	for _, child := range st.popups {
		if childState, ok := s.tabs[child]; ok && !childState.closed {
			out = append(out, child)
		}
	}
	return out, nil
}

// PopupParent 返回某 tab 的父 tab（不是弹窗时返回空与 false）。
func (s *Service) PopupParent(exec contracts.ExecutionContext, tab TabID) (TabID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.ownedLocked(exec, tab); err != nil {
		return "", false, err
	}
	parent, ok := s.popupParent[tab]
	return parent, ok, nil
}
