package connectors

// 写/外发动作的资源目标（CONN08 的第二层）。
//
// mail 的资源是收件人地址；五个 HTTP 平台的资源是频道/文档/页面/仓库等标识。
// 目标一律从**参数**里取（参数已过 schema 校验与身份字段拒绝），所以这里拿到的
// 一定是"调用方要求触达的资源"，而不是执行身份。
//
// 为什么用一张表而不是让驱动自述：资源目标决定"这次外发会碰到谁"，它是审批
// 指纹的一部分。驱动若能在运行时换一个目标，审批等于批准了一个字符串。

import (
	"encoding/json"
	"strings"

	"my-agent-go/internal/contracts"
)

// writeTargetArgs 是 (kind, action) → 参数名 的唯一表。
//
// 只登记写/外发动作：读动作没有"触达外部资源"的语义（与 mail 的 search/read 一致）。
var writeTargetArgs = map[Kind]map[string]string{
	KindDingtalk: {
		"send_group": "conversationId",
	},
	KindFeishu: {
		"update_doc":   "documentId",
		"send_message": "chatId",
	},
	KindSlack: {
		"reply_thread": "channel",
	},
	KindNotion: {
		"create_page": "parentId",
		"update_page": "pageId",
	},
	KindGitHub: {
		"create_issue": "repo",
		"comment_pr":   "repo",
	},
}

// writeTargets 返回一条写动作要触达的资源标识。
//
// 返回空表示"这条动作没有声明资源目标"：调用方据此拒绝派发，而不是放行。
func writeTargets(kind Kind, actionID string, args json.RawMessage) []string {
	argName, ok := writeTargetArgs[kind][actionID]
	if !ok {
		return nil
	}
	var in map[string]any
	if err := json.Unmarshal(args, &in); err != nil {
		return nil
	}
	raw, ok := in[argName]
	if !ok {
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return []string{value}
}

// matchAnyResource 判定目标是否在允许范围内。
//
// 规则与 mail 的 matchResource 兼容（* 任意、@domain 域、完整地址精确），
// 另外支持"非地址标识"的精确匹配（仓库 acme/app、频道 C0123）——
// 把 acme/app 当成域名去比较永远不会命中，那会让范围检查变成永远失败。
func matchAnyResource(patterns []string, target string) bool {
	want := strings.ToLower(strings.TrimSpace(target))
	if want == "" {
		return false
	}
	for _, raw := range patterns {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if p == "*" || p == want {
			return true
		}
		if strings.HasPrefix(p, "@") && strings.HasSuffix(want, p) {
			return true
		}
	}
	return false
}

// scopeFor 生成策略指纹里的资源范围字符串。
//
// 写动作带上实际目标：审批绑定的是"发给这个频道/这个仓库"，不是"这个动作"。
func scopeFor(def Definition, action ActionDescriptor, args json.RawMessage) string {
	base := "connector:" + def.ID + ":" + action.ID
	if action.Risk == RiskRead {
		return base
	}
	targets := writeTargets(def.Kind, action.ID, args)
	if len(targets) == 0 {
		return base
	}
	return string(def.Kind) + ":" + action.ID + "=" + strings.Join(targets, ",")
}

// requireWriteTargets 在派发前校验写动作的资源范围。
func requireWriteTargets(def Definition, action ActionDescriptor, args json.RawMessage) error {
	targets := writeTargets(def.Kind, action.ID, args)
	if len(targets) == 0 {
		return contracts.NewAppError(
			contracts.CodeUnauthorized,
			"动作 "+action.ID+" 没有声明可校验的资源目标，拒绝外发",
			false,
		)
	}
	patterns := def.AllowedResources
	if len(patterns) == 0 {
		return contracts.NewAppError(contracts.CodeUnauthorized, "连接器未声明允许的资源范围，拒绝外发", false)
	}
	for _, target := range targets {
		if !matchAnyResource(patterns, target) {
			return contracts.NewAppError(
				contracts.CodeUnauthorized,
				"目标资源不在允许的资源范围内: "+target,
				false,
			)
		}
	}
	return nil
}
