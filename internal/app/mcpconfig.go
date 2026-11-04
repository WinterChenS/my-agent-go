package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// harnessServerName 是写进 Pi mcp.json 的服务器名。
//
// 必须叫 `harness`：Pi 把 MCP 工具改写成 `mcp__<server>__<tool>`，而策略扩展
// 用 `mcp__harness__*` 前缀识别"自有 MCP"（其余 `mcp__*` 归外部 MCP）。
// 起别的名字会让自有工具被当成外部通道——功能上仍受策略约束，但审计时
// 两条通道会混在一起，SEC03 分不出是哪一条被拒。
const harnessServerName = "harness"

// writeMCPConfig 把自有 MCP 端点写进隔离 agentDir 的 mcp.json。
//
// 三点约束：
//  1. 令牌只写引用 `${MYAGENT_HARNESS_TOKEN}`，不写明文——配置文件会落盘、
//     会被备份、会被贴进 issue，明文令牌等于长期泄露。
//  2. 合并而不是覆盖：同一目录下可能已有别的条目（例如后续 C04 要支持用户配置
//     外部 MCP），整份重写会把它们抹掉。
//  3. 已有文件不是合法 JSON 时直接报错，不"修好再写"：那等于替用户丢掉
//     他自己的配置，而且丢失是静默的。
func writeMCPConfig(agentDir, url string) error {
	if strings.TrimSpace(agentDir) == "" {
		return fmt.Errorf("写入 mcp.json 失败: agentDir 为空")
	}
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("写入 mcp.json 失败: 端点 URL 为空")
	}
	path := filepath.Join(agentDir, "mcp.json")

	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("已有 mcp.json 不是合法 JSON，拒绝覆盖以免丢失原配置: %w", err)
		}
		if root == nil {
			root = map[string]any{}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 mcp.json 失败: %w", err)
	}

	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		if root["mcpServers"] != nil {
			return fmt.Errorf("已有 mcp.json 的 mcpServers 不是对象，拒绝覆盖")
		}
		servers = map[string]any{}
	}
	servers[harnessServerName] = map[string]any{
		"url": url,
		"headers": map[string]any{
			// Pi 会从 worker 环境展开 ${...}；不写 Authorization 它会转去走 OAuth。
			"Authorization": "Bearer ${MYAGENT_HARNESS_TOKEN}",
		},
		// Pi 1.5.0 的默认 exposure 是 codemode：工具注册了但模型看不到，
		// 只能通过 codemode 脚本或 tool_search 间接调用。实测直接调用会得到
		// "MCP tools are only reachable from the codemode or tool_search tool"。
		// 自有工具是小规模、常驻能力，用 direct 让它像内置工具一样直接声明。
		"exposure":    "direct",
		"description": "MyAgent 自有工具：执行前由策略扩展拦截",
	}
	root["mcpServers"] = servers

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 mcp.json 失败: %w", err)
	}
	data = append(data, '\n')

	// 原子写：先写临时文件再 rename，避免 Pi 正好在读到写了一半的文件。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入 mcp.json 临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交 mcp.json 失败: %w", err)
	}
	return nil
}
