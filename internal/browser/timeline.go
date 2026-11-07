package browser

// B05：动作时间线。
//
// 为什么它属于产品代码而不只属于测试：BR11/BR14 的验收口径是**顺序**
// （"接管后 queued=0，且 Human 显示之后没有 agent 输入"），顺序只能由一份
// 追加写的流水证明。把流水放在服务里，UI 诊断（B07）与证据（BR11–BR14）
// 看的是同一份事实，不会出现"测试自己记了一份、产品另跑一套"的分叉。
//
// 流水只记录元数据：动作类型、ref、快照/代数、状态、队列长度、错误码。
// 不记录 fill 的文本、不记录页面内容——时间线会被写进证据文件，不能因此泄漏
// 表单值（BR09 的口径是零命中）。

import (
	"errors"

	"my-agent-go/internal/contracts"
)

// 时间线事件类型。取值进 JSONL 证据，改名等于改证据口径。
const (
	TimelineTabCreated         = "tab_created"
	TimelineTabClosed          = "tab_closed"
	TimelineTabLimitExceeded   = "tab_limit_exceeded"
	TimelineObserved           = "observed"
	TimelineNavigated          = "navigated"
	TimelineAuthorizeRequested = "authorize_requested"
	TimelineAuthorizeDenied    = "authorize_denied"
	TimelineEnqueued           = "enqueued"
	TimelineDispatched         = "dispatched"
	TimelineCompleted          = "completed"
	TimelineRevoked            = "revoked"
	TimelineCanceled           = "canceled"
	TimelineOutcomeUnknown     = "outcome_unknown"
	TimelineWriteRejected      = "write_rejected"
	TimelineTakeOver           = "takeover"
	TimelineResume             = "resume"
	TimelineServiceClosed      = "service_closed"
	// B06（BR16）：window.open 被接管成同 session 的 tab / 被拒绝。
	// 两个都进 JSONL 证据：只记成功会让「被拒的弹窗有没有创建实例」无法核对。
	TimelinePopupOpened = "popup_opened"
	TimelinePopupDenied = "popup_denied"
)

// TimelineEntry 是时间线的一行。
type TimelineEntry struct {
	Seq          uint64              `json:"seq"`
	Kind         string              `json:"kind"`
	TabID        TabID               `json:"tabId,omitempty"`
	SessionID    contracts.SessionID `json:"sessionId,omitempty"`
	Action       ActionKind          `json:"action,omitempty"`
	Ref          string              `json:"ref,omitempty"`
	SnapshotID   string              `json:"snapshotId,omitempty"`
	NavigationID string              `json:"navigationId,omitempty"`
	Generation   uint64              `json:"controlGeneration"`
	Mode         ControlMode         `json:"mode,omitempty"`
	Queued       int                 `json:"queued"`
	Inflight     int                 `json:"inFlight"`
	ErrorCode    string              `json:"errorCode,omitempty"`
	Detail       string              `json:"detail,omitempty"`
}

// appendTimelineLocked 追加一行；调用方必须持有 s.mu。
func (s *Service) appendTimelineLocked(e TimelineEntry) {
	e.Seq = uint64(len(s.timeline) + 1)
	s.timeline = append(s.timeline, e)
}

// Timeline 返回时间线副本（含所有 tab）。
func (s *Service) Timeline() []TimelineEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TimelineEntry, len(s.timeline))
	copy(out, s.timeline)
	return out
}

// TimelineFor 返回某个 tab 的时间线。
func (s *Service) TimelineFor(tab TabID) []TimelineEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TimelineEntry, 0, len(s.timeline))
	for _, e := range s.timeline {
		if e.TabID == tab {
			out = append(out, e)
		}
	}
	return out
}

// errorCode 取错误里的机器可解析码；不是 AppError 时返回 INTERNAL_ERROR。
//
// nil 返回空串（"没有错误"），避免时间线里出现一段自由文本被当成错误码解析。
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var ae *contracts.AppError
	if errors.As(err, &ae) && ae != nil {
		return ae.Code
	}
	return contracts.CodeInternal
}
