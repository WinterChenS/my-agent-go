package contracts

import (
	"encoding/json"
	"time"
)

// EventSchemaVersion 是事件 envelope 的当前 schema 版本（基线 §6）。
const EventSchemaVersion = 1

// Event 是跨进程/UI 的事件信封。
// seq 按 run 从 1 单调递增；UI 据 eventId 去重。
// 未知事件类型必须保留原始类型但不崩溃（基线 §6）。
type Event struct {
	SchemaVersion int             `json:"schemaVersion"`
	EventID       string          `json:"eventId"`
	WorkspaceID   WorkspaceID     `json:"workspaceId"`
	SessionID     SessionID       `json:"sessionId"`
	RunID         RunID           `json:"runId"`
	Seq           uint64          `json:"seq"`
	Type          string          `json:"type"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Payload       json.RawMessage `json:"payload"`
}
