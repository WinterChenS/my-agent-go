// Package contracts 是 V1 共享契约的唯一 Go 定义源。
// 前端类型从应用 DTO 生成/导出，不在此手写第二套同名协议。
package contracts

// ID 别名：统一以字符串承载领域标识，避免基础类型散落各处导致串味。
type (
	WorkspaceID string
	SessionID   string
	RunID       string
	TabID       string
	ProfileID   string
)
