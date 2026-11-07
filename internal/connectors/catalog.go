package connectors

// Anvil action catalog（CONN04）。
//
// 这张表是风险的**权威**来源：驱动返回的动作必须逐项与它一致，风险不得更低，
// 幂等声明不得更宽。驱动说"这条 send 其实是幂等的"时不会被采纳——平台的幂等
// 语义由产品认定，不能由适配器自行升级。

import (
	"encoding/json"
	"fmt"
	"strings"

	"my-agent-go/internal/contracts"
)

// mailActions 是 mail 的 V1 动作集合。
//
// 与计划相比缺 draft：草稿需要 IMAP APPEND 的完整交互，C08a 的驱动范围明确为
// "list/read/fetch attachment/send/health"，草稿留给 C08b，不在这里放空实现。
func mailActions() []ActionDescriptor {
	return []ActionDescriptor{
		{
			ID:          "search",
			Kind:        KindMail,
			Title:       "搜索邮件",
			Description: "Search the configured mailbox headers and return matching messages (no server-side index).",
			Risk:        RiskRead,
			Capability:  CapabilityMailRead,
			Idempotency: IdempotencyReadOnly,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Substring matched against subject and sender."},
					"limit": map[string]any{"type": "integer", "description": "Maximum number of matches, default 20."},
				},
				"required": []string{"query"},
			},
		},
		{
			ID:          "read",
			Kind:        KindMail,
			Title:       "读取邮件",
			Description: "Read one message body by IMAP UID.",
			Risk:        RiskRead,
			Capability:  CapabilityMailRead,
			Idempotency: IdempotencyReadOnly,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uid": map[string]any{"type": "integer", "description": "IMAP UID of the message."},
				},
				"required": []string{"uid"},
			},
		},
		{
			ID:          "attachment",
			Kind:        KindMail,
			Title:       "获取附件",
			Description: "Fetch one attachment of a message by file name. The bytes are returned inline, base64 encoded.",
			Risk:        RiskRead,
			Capability:  CapabilityMailRead,
			Idempotency: IdempotencyReadOnly,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uid":  map[string]any{"type": "integer", "description": "IMAP UID of the message."},
					"name": map[string]any{"type": "string", "description": "Attachment file name as shown in the message."},
				},
				"required": []string{"uid", "name"},
			},
		},
		{
			ID:          "send",
			Kind:        KindMail,
			Title:       "发送邮件",
			Description: "Send an email through SMTP. Non-idempotent: an unknown outcome is reported, never replayed automatically.",
			Risk:        RiskExternalSend,
			Capability:  CapabilityMailSend,
			Idempotency: IdempotencyNone,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":      map[string]any{"type": "array", "description": "Recipient addresses."},
					"subject": map[string]any{"type": "string", "description": "Subject line."},
					"body":    map[string]any{"type": "string", "description": "Plain text body."},
				},
				"required": []string{"to", "subject", "body"},
			},
		},
	}
}

// catalog 是 kind → 动作表的唯一注册点。
var catalog = map[Kind][]ActionDescriptor{
	KindMail: mailActions(),
}

// Catalog 返回某个 kind 的动作副本。
func Catalog(kind Kind) []ActionDescriptor {
	src := catalog[kind]
	out := make([]ActionDescriptor, len(src))
	copy(out, src)
	return out
}

// LookupAction 在 catalog 里查一条动作。
func LookupAction(kind Kind, actionID string) (ActionDescriptor, bool) {
	for _, a := range catalog[kind] {
		if a.ID == actionID {
			return a, true
		}
	}
	return ActionDescriptor{}, false
}

// KnownCapability 判定某能力是否属于该 kind 的 catalog。
func KnownCapability(kind Kind, c Capability) bool {
	for _, a := range catalog[kind] {
		if a.Capability == c {
			return true
		}
	}
	return false
}

// identityKeys 是绝对不能由调用参数决定的执行身份字段。
//
// 允许它们出现等于允许模型用参数换一个 session/workspace 或直接引用别的凭证
// （CONN08）。这里做硬拒绝而不是忽略：静默忽略会让"为什么我的参数没生效"
// 变成长期谜团，也让伪造尝试不留痕。
var identityKeys = map[string]bool{
	"sessionid":     true,
	"runid":         true,
	"workspaceid":   true,
	"connectorid":   true,
	"credentialref": true,
	"credential":    true,
	"token":         true,
	"secret":        true,
}

// validateArguments 按 action catalog 的 schema 校验参数。
//
// 只做这个仓库真正需要的那部分 JSON Schema：对象、已知属性、必填、基本类型、
// 枚举。不引入通用 schema 引擎——那会让"接受什么"变得更难预测。
func validateArguments(action ActionDescriptor, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, action.ID+" 参数不是合法 JSON 对象: "+err.Error(), false)
	}
	if obj == nil {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, action.ID+" 参数必须是 JSON 对象", false)
	}
	props, _ := action.InputSchema["properties"].(map[string]any)
	for key := range obj {
		if identityKeys[strings.ToLower(key)] {
			return nil, contracts.NewAppError(
				contracts.CodeUnauthorized,
				"connector 参数不得指定执行身份字段: "+key,
				false,
			)
		}
		if _, ok := props[key]; !ok {
			return nil, contracts.NewAppError(contracts.CodeUnsupported, action.ID+" 不支持参数: "+key, false)
		}
	}
	required, _ := action.InputSchema["required"].([]string)
	for _, key := range required {
		if _, ok := obj[key]; !ok {
			return nil, contracts.NewAppError(contracts.CodeUnsupported, action.ID+" 缺少必填参数: "+key, false)
		}
	}
	for key, val := range obj {
		spec, _ := props[key].(map[string]any)
		if spec == nil {
			continue
		}
		if err := checkType(action.ID, key, spec, val); err != nil {
			return nil, err
		}
		if enum, ok := spec["enum"].([]string); ok && len(enum) > 0 {
			s, _ := val.(string)
			okEnum := false
			for _, want := range enum {
				if s == want {
					okEnum = true
					break
				}
			}
			if !okEnum {
				return nil, contracts.NewAppError(contracts.CodeUnsupported,
					fmt.Sprintf("%s 参数 %s 取值不在允许范围", action.ID, key), false)
			}
		}
	}
	return raw, nil
}

// checkType 校验单个参数的基本类型。
func checkType(actionID, key string, spec map[string]any, val any) error {
	want, _ := spec["type"].(string)
	switch want {
	case "", "object":
		if want == "object" {
			if _, ok := val.(map[string]any); !ok {
				return typeError(actionID, key, want)
			}
		}
	case "string":
		if _, ok := val.(string); !ok {
			return typeError(actionID, key, want)
		}
	case "integer":
		num, ok := val.(float64)
		if !ok || num != float64(int64(num)) {
			return typeError(actionID, key, want)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return typeError(actionID, key, want)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return typeError(actionID, key, want)
		}
	}
	return nil
}

func typeError(actionID, key, want string) error {
	return contracts.NewAppError(
		contracts.CodeUnsupported,
		fmt.Sprintf("%s 参数 %s 类型必须是 %s", actionID, key, want),
		false,
	)
}

// verifyDriverDescriptor 校验驱动自述的动作与 catalog 一致。
//
// 规则是"只能更严，不能更宽"：风险更低、幂等声明更宽（例如非幂等说成带键幂等）
// 一律拒绝。catalog 里有而驱动没有的动作视为驱动能力缺失，同样拒绝——否则
// UI 会展示一个调用必然失败的动作。
func verifyDriverDescriptor(catalogued, reported ActionDescriptor) error {
	if reported.Risk.Rank() < catalogued.Risk.Rank() {
		return contracts.NewAppError(
			contracts.CodeUnsupported,
			fmt.Sprintf("%s 动作风险不得低于 catalog（%s < %s）", catalogued.ID, reported.Risk, catalogued.Risk),
			false,
		)
	}
	if reported.Capability != catalogued.Capability {
		return contracts.NewAppError(
			contracts.CodeUnsupported,
			fmt.Sprintf("%s 动作能力范围与 catalog 不一致", catalogued.ID),
			false,
		)
	}
	if reported.Idempotency != catalogued.Idempotency {
		return contracts.NewAppError(
			contracts.CodeUnsupported,
			fmt.Sprintf("%s 动作幂等声明与 catalog 不一致", catalogued.ID),
			false,
		)
	}
	return nil
}
