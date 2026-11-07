package pi

// 隔离 agentDir 里的 mcp.json 写入（含 app-owned 连接器入口）。
//
// 为什么放在 runtime/pi 而不是 app：mcp.json 的**格式与保留名**是 Pi 侧的契约，
// 与"谁调用"无关。绑定层和外部的写入方共用同一份实现，才不会出现
// "app 写的条目 Pi 不认"或"两个写者互相覆盖"。
//
// 三条约束（与 F03/C04 的既有约定一致）：
//  1. 令牌只写 `${MYAGENT_HARNESS_TOKEN}` 引用，绝不写明文——配置文件会落盘、
//     会被备份、会被贴进 issue；
//  2. 合并而不是整份覆盖：同一目录下还有用户配置的外部 MCP；
//  3. 已有文件不是合法 JSON 时直接报错，不做"先修好再写"（静默丢配置）。

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ReservedHarnessServerName 是 app-owned MCP 的保留名。
//
// 必须叫 harness：Pi 把 MCP 工具改写成 `mcp__<server>__<tool>`，而策略扩展用
// `mcp__harness__*` 前缀识别"自有 MCP"。连接器工具就走这条通道——它们不是
// 第二个服务，因此也不需要第二枚令牌。
const ReservedHarnessServerName = "harness"

// HarnessTokenEnv 是 mcp.json 里引用的令牌环境变量名。
const HarnessTokenEnv = "MYAGENT_HARNESS_TOKEN"

// HarnessMCPEntry 返回写进 mcp.json 的 app-owned 条目。
//
// url 必须是回环端点：把自有工具端点指向非回环地址等于把工具通道暴露给网络。
func HarnessMCPEntry(endpointURL, description string) (map[string]any, error) {
	if err := validateLoopbackEndpoint(endpointURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(description) == "" {
		description = "Anvil 自有工具：执行前由策略扩展拦截"
	}
	return map[string]any{
		"url": endpointURL,
		"headers": map[string]any{
			"Authorization": "Bearer ${" + HarnessTokenEnv + "}",
		},
		// Pi 1.5.0 的默认 exposure 是 codemode：工具注册了但模型看不到，
		// 只能通过 codemode 脚本或 tool_search 间接调用。自有工具是小规模、
		// 常驻能力，用 direct 让它像内置工具一样直接声明。
		"exposure":    "direct",
		"description": description,
	}, nil
}

// WriteHarnessMCPConfig 把 app-owned 入口（自有工具 + 连接器动作）写进
// agentDir/mcp.json，并保持其它外部服务不变。
func WriteHarnessMCPConfig(agentDir, endpointURL, description string) error {
	if strings.TrimSpace(agentDir) == "" {
		return fmt.Errorf("写入 mcp.json 失败: agentDir 为空")
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return fmt.Errorf("创建 agentDir 失败: %w", err)
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
	entry, err := HarnessMCPEntry(endpointURL, description)
	if err != nil {
		return err
	}
	servers[ReservedHarnessServerName] = entry
	root["mcpServers"] = servers

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 mcp.json 失败: %w", err)
	}
	data = append(data, '\n')

	// 原子写：先写临时文件再 rename，避免 Pi 正好读到写了一半的文件。
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

// validateLoopbackEndpoint 只接受回环 http(s) 端点。
func validateLoopbackEndpoint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("写入 mcp.json 失败: 端点 URL 为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("MCP 端点不是合法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("MCP 端点必须是 http(s): %s", raw)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("MCP 端点必须绑定回环地址（当前 %s）", host)
}
