package harnessmcp

// C08a：把连接器动作注册成自有 MCP 工具。
//
// 授权边界（与计划一致）：MCP handler 是 connector namespace 的唯一权威策略
// 执行点——它在调用 driver 之前调统一 policy。工具参数里不允许出现
// session/run/workspace/connector 等执行身份字段：身份来自**服务端绑定**的
// contracts.ExecutionContext（本文件的 binding 参数），不是模型说了算。
//
// OutcomeUnknown 的文案必须让模型看见"结果未知，不要自动重试"：把它写成
// 普通失败会诱导模型再发一次，而那正是非幂等动作最危险的行为。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/contracts"
)

// ConnectorInvoker 是连接器工具需要的最小能力。
//
// 用接口而不是 *connectors.Service：工具测试可以注入"只计数"的实现来证明
// 工具层没有自己另开一条派发路径。
type ConnectorInvoker interface {
	ListDefinitions(ctx context.Context) ([]connectors.Definition, error)
	Actions(ctx context.Context, connectorID string) ([]connectors.ActionDescriptor, error)
	Invoke(ctx context.Context, req connectors.InvokeRequest) (connectors.Result, error)
}

// ConnectorTools 返回已启用连接器的全部动作工具。
//
// 每次（每个 run）重新构造：禁用或撤权的连接器不会出现在工具表里（CONN05）。
// binding 是服务端绑定的执行身份，工具参数覆盖不了它。
func ConnectorTools(inv ConnectorInvoker, binding contracts.ExecutionContext) ([]Tool, error) {
	if inv == nil {
		return nil, nil
	}
	defs, err := inv.ListDefinitions(context.Background())
	if err != nil {
		return nil, err
	}
	tools := []Tool{}
	for _, def := range defs {
		if !def.Enabled {
			continue
		}
		actions, aerr := inv.Actions(context.Background(), def.ID)
		if aerr != nil {
			return nil, aerr
		}
		for _, action := range actions {
			tools = append(tools, connectorActionTool(inv, binding, def, action))
		}
	}
	return tools, nil
}

// connectorActionTool 把一条 catalog 动作包成 MCP 工具。
func connectorActionTool(inv ConnectorInvoker, binding contracts.ExecutionContext, def connectors.Definition, action connectors.ActionDescriptor) Tool {
	name := connectors.ToolName(def, action)
	description := fmt.Sprintf("%s（连接器 %s，风险=%s，幂等=%s）。%s",
		action.Title, def.DisplayName, action.Risk, action.Idempotency, action.Description)
	return Tool{
		Name:        name,
		Description: description,
		InputSchema: action.InputSchema,
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			// 身份字段的最终拒绝在 connections.Service.validateArguments（唯一实现），
			// 这里不重复一套规则：重复就会出现两边不一致的"允许/拒绝"。
			res, err := inv.Invoke(ctx, connectors.InvokeRequest{
				Execution:   binding,
				ConnectorID: def.ID,
				ActionID:    action.ID,
				Arguments:   raw,
			})
			if err != nil {
				return ToolResult{IsError: true}, err
			}
			return connectorToolResult(action, res), nil
		},
	}
}

// connectorToolResult 把结果翻译成模型能正确处理的文本。
func connectorToolResult(action connectors.ActionDescriptor, res connectors.Result) ToolResult {
	head := ""
	if len(res.Data) > 0 {
		head = string(res.Data) + "\n"
	}
	detail := strings.TrimSpace(res.Detail)
	switch res.Outcome {
	case connectors.OutcomeDone:
		if detail == "" {
			detail = string(action.Title) + " 已完成"
		}
		return ToolResult{Text: head + detail}
	case connectors.OutcomeUnknown:
		return ToolResult{
			IsError: true,
			Text: head + "结果未知（请求已发出但没有收到确认）：" + detail +
				"。不要自动重试该动作；如确需重发，必须由用户显式再次批准。",
		}
	default:
		if detail == "" {
			detail = string(action.Title) + " 未执行"
		}
		return ToolResult{IsError: true, Text: head + detail}
	}
}

// 说明：工具参数里**没有** idempotencyKey 字段。catalog 的 schema 就是全部
// 可接受参数，多一个隐式字段会让"参数校验通过"变成"其实还接受别的键"。
// 幂等键由服务侧调用方（C09 调度等）在 InvokeRequest 上给出，不由模型决定。
