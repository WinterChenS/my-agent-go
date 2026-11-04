package harnessmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// Tool 是一个自有工具的定义。
//
// 工具的 Handler 只负责"参数合法时怎么做"，不负责授权。授权在执行前由 Pi 侧的
// 策略扩展强制（harness-policy.ts 覆盖 builtin/扩展/MCP/嵌套四条通道），
// 服务端如果再做一次会与"允许一次"的语义打架：一次性批准已被上游消费，
// 这里再问一次必然变成新的待审批，用户会被要求批准同一个调用两次。
//
// 因此本服务的边界是：认证（谁能调）＋ 参数校验（能调什么范围），
// 而不是"这个调用该不该被允许"。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Handler     func(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// ToolResult 是 MCP tools/call 的返回内容。
type ToolResult struct {
	Text    string
	IsError bool
}

// ProbeTool 返回一个可观测副作用的自有工具：在 workspace 内写一个 marker 文件。
//
// 存在理由：SEC03 要求"自有 MCP 通道被拒绝时副作用不发生"，判定必须落在文件系统上，
// 不能靠"界面显示失败了"。工具参数只允许 workspace 内的相对路径——
// 工具自身也不该成为跳出工作区的通道，否则策略层一次漏判就等于任意写。
func ProbeTool(workspace string) Tool {
	return Tool{
		Name:        "harness_probe",
		Description: "Write a marker file inside the workspace to prove the tool actually ran.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Workspace-relative path of the marker file, e.g. harness-ran.txt",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "File content, defaults to a fixed marker",
				},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var in struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					return ToolResult{IsError: true}, contracts.NewAppError(contracts.CodeUnsupported, "参数不是合法 JSON: "+err.Error(), false)
				}
			}
			target, err := resolveInside(workspace, in.Path)
			if err != nil {
				return ToolResult{IsError: true}, err
			}
			if err := ctx.Err(); err != nil {
				return ToolResult{IsError: true}, err
			}
			content := in.Content
			if content == "" {
				content = "ran by harness mcp"
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return ToolResult{IsError: true}, contracts.NewAppError(contracts.CodeInternal, "创建目录失败: "+err.Error(), false)
			}
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				return ToolResult{IsError: true}, contracts.NewAppError(contracts.CodeInternal, "写入 marker 失败: "+err.Error(), false)
			}
			return ToolResult{Text: "wrote " + filepath.Base(target)}, nil
		},
	}
}

// resolveInside 把相对路径钉死在 workspace 内。
//
// 先 Clean 再比 Rel：`a/../..` 这类写法在 Clean 之后才会露出真实的越界层级。
// 只接受相对路径——绝对路径（含盘符与 UNC）一律拒绝，避免"看起来在根内"。
func resolveInside(root, rel string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "自有 MCP 工具缺少工作区根", false)
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "path 不能为空", false)
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, `\\`) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "path 必须是工作区相对路径", false)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "工作区根无法绝对化: "+err.Error(), false)
	}
	target := filepath.Join(base, filepath.Clean(rel))
	relToRoot, err := filepath.Rel(base, target)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "path 越出工作区: "+rel, false)
	}
	return target, nil
}

// toolByName 在注册表里查工具。大小写敏感：MCP 工具名是契约的一部分，
// 放宽匹配会让"调错名字"变成"调了个别的工具"。
func toolByName(tools []Tool, name string) (Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// describeTool 把工具转成 MCP tools/list 的条目。
func describeTool(t Tool) map[string]any {
	schema := t.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"name":        t.Name,
		"description": t.Description,
		"inputSchema": schema,
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	return b
}
