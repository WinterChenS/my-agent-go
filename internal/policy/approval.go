package policy

import (
	"context"
	"encoding/json"
	"time"

	"my-agent-go/internal/contracts"
)

// ApprovalChoice 是用户在审批对话框里的选择（基线 §6）。
type ApprovalChoice string

const (
	ApprovalOnce         ApprovalChoice = "once"
	ApprovalSessionScope ApprovalChoice = "session_scope"
	ApprovalDeny         ApprovalChoice = "deny"
)

// approval 是一条审批请求的内部状态。
//
// 绑定字段一个都不能少：参数指纹防"批准 A 执行 B"，session/run 防跨会话复用，
// configRevision 防"改配置后旧批准继续有效"，controlGeneration 防接管后仍沿用旧许可。
type approval struct {
	ID                string
	SessionID         contracts.SessionID
	RunID             contracts.RunID
	ToolCallID        string
	ToolName          string
	Arguments         json.RawMessage
	ArgsHash          string
	ConfigRevision    uint64
	ControlGeneration uint64
	ExpiresAt         time.Time
	Choice            ApprovalChoice
	Consumed          bool
}

// ApprovalRequest 是发给前端的审批请求 DTO。
//
// 不带 ArgsHash：那是内部绑定用的指纹，前端只需要展示参数本身，不该拿到可用于伪造的绑定值。
type ApprovalRequest struct {
	ID            string              `json:"id"`
	SessionID     contracts.SessionID `json:"sessionId"`
	RunID         contracts.RunID     `json:"runId"`
	ToolCallID    string              `json:"toolCallId"`
	ToolName      string              `json:"toolName"`
	Arguments     json.RawMessage     `json:"arguments"`
	ExpiresAtUnix int64               `json:"expiresAtUnix"`
}

// Pending 返回一条仍可用的审批请求；不存在、已消费或已过期都返回 NOT_FOUND，
// 避免前端把过期请求继续展示成"等待确认"。
func (s *Service) Pending(ctx context.Context, id string) (ApprovalRequest, error) {
	if err := ctx.Err(); err != nil {
		return ApprovalRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.approvals[id]
	if !ok {
		return ApprovalRequest{}, contracts.NewAppError(contracts.CodeNotFound, "审批不存在: "+id, false)
	}
	if a.Consumed || s.now().After(a.ExpiresAt) {
		return ApprovalRequest{}, contracts.NewAppError(contracts.CodeNotFound, "审批已失效: "+id, false)
	}
	return ApprovalRequest{
		ID:            a.ID,
		SessionID:     a.SessionID,
		RunID:         a.RunID,
		ToolCallID:    a.ToolCallID,
		ToolName:      a.ToolName,
		Arguments:     a.Arguments,
		ExpiresAtUnix: a.ExpiresAt.Unix(),
	}, nil
}
