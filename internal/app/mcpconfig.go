package app

import (
	"my-agent-go/internal/runtime/pi"
)

// harnessServerName 是写进 Pi mcp.json 的服务器名。
//
// 必须叫 `harness`：Pi 把 MCP 工具改写成 `mcp__<server>__<tool>`，而策略扩展
// 用 `mcp__harness__*` 前缀识别"自有 MCP"（其余 `mcp__*` 归外部 MCP）。
// 连接器动作也注册在这同一个 app-owned 服务上，因此不需要第二枚令牌。
const harnessServerName = pi.ReservedHarnessServerName

// writeMCPConfig 把自有 MCP 端点写进隔离 agentDir 的 mcp.json。
//
// 实现落在 internal/runtime/pi：mcp.json 的格式与保留名是 Pi 侧契约，
// 装配层只是调用方。这样连接器入口（C08a）与自带工具入口共用同一个写入者，
// 不会出现两个写者互相覆盖。
func writeMCPConfig(agentDir, url string) error {
	return pi.WriteHarnessMCPConfig(agentDir, url, "Anvil 自有工具与连接器动作：执行前由策略扩展拦截")
}
