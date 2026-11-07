package harnessmcp

// B05：把"右侧原生浏览器"的读写动作注册成自有 MCP 工具。
//
// 三条边界必须同时成立，缺一条这条工具就是错的：
//
//  1. **身份不由模型决定**。session/run/configRevision/controlGeneration 全部来自
//     服务端绑定的 contracts.ExecutionContext；参数里出现 sessionId/runId/approvalId/
//     confirmed 之类的键一律拒绝（不是"忽略"，是明确报错），否则一次提示注入就能
//     把动作打到别的会话上，或者自己给自己批准。
//  2. **授权在服务层，不在工具层**。工具只把动作交给 browser.Service；写动作在那里
//     走 Authorize（入队前）与 Recheck（派发前）两次检查。工具自己不做第二次判断，
//     否则两处口径必然分叉。
//  3. **不可用的能力不注册工具**。CapabilitySet 说 Act=false 时就不该有 browser_act，
//     让模型"调用后拿到 NOT_IMPLEMENTED"是把能力缺口伪装成运行时错误。
//
// OutcomeUnknown 的文案必须让模型看见"结果未知、不要重试"：把已派发的超时写成
// 普通失败，会诱导模型再写一次，而那正是非幂等动作最危险的行为。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

// BrowserController 是浏览器工具需要的最小能力面（由 *browser.Service 实现）。
//
// 用接口而不是具体类型：工具测试可以注入"只计数"的实现来证明工具层没有自己
// 另开一条派发路径（例如绕过 Service 直接调 backend）。
type BrowserController interface {
	Capabilities() browser.CapabilitySet
	OpenTab(ctx context.Context, exec contracts.ExecutionContext, opts browser.TabOptions) (browser.TabID, error)
	Observe(ctx context.Context, exec contracts.ExecutionContext, tab browser.TabID, opts browser.ObserveOptions) (browser.Snapshot, error)
	Execute(ctx context.Context, exec contracts.ExecutionContext, tab browser.TabID, action browser.Action) (browser.ActionResult, error)
	CloseTab(ctx context.Context, exec contracts.ExecutionContext, tab browser.TabID) error
	TabInfo(exec contracts.ExecutionContext, tab browser.TabID) (browser.TabInfo, error)
	Control(exec contracts.ExecutionContext, tab browser.TabID) (browser.ControlState, error)
}

// BrowserToolsOptions 构造浏览器工具。
type BrowserToolsOptions struct {
	// Exec 是服务端绑定的执行身份；工具参数覆盖不了它。
	Exec contracts.ExecutionContext
	// MaxObserveElements 是 observe 工具允许模型指定的元素上限（0 表示用基线默认）。
	// 它只能往下调：模型不能靠参数把 300 改成 3000。
	MaxObserveElements int
}

// 工具名。它们出现在 models 的工具表里，改名等于改协议。
const (
	BrowserToolOpenTab  = "browser_open_tab"
	BrowserToolTabs     = "browser_tabs"
	BrowserToolObserve  = "browser_observe"
	BrowserToolAct      = "browser_act"
	BrowserToolCloseTab = "browser_close_tab"
)

// forbiddenArgKeys 是**绝不允许**出现在浏览器工具参数里的键。
//
// 两类：
//   - 执行身份（sessionId/runId/workspaceId/controlGeneration…）：身份只能来自绑定；
//   - 审批结论（approvalId/confirmed/approved/allow/approve/decision…）：
//     模型不能给自己批准任何东西——"确认"这件事只有策略桥（用户）能做。
//
// 这条清单是"模型不能自证"的落地检查：命中即报错，不是静默忽略。
var forbiddenArgKeys = []string{
	"sessionid", "runid", "workspaceid", "configrevision", "controlgeneration", "generation",
	"approvalid", "approval", "confirmed", "confirm", "approved", "approve", "allow", "allowed",
	"decision", "grant", "permission", "consent", "bypass", "skipapproval",
}

// BrowserTools 返回当前能力集下应注册的浏览器工具。
//
// 能力缺失的工具直接不出现；这比"注册了再在运行时返回 NOT_IMPLEMENTED"诚实，
// 也避免模型反复尝试一个永远不可能成功的路径。
func BrowserTools(ctl BrowserController, opts BrowserToolsOptions) []Tool {
	if ctl == nil {
		return nil
	}
	caps := ctl.Capabilities()
	exec := opts.Exec
	tools := []Tool{
		browserTabsTool(ctl, exec),
	}
	if caps.Navigate {
		tools = append(tools, browserOpenTabTool(ctl, exec))
	}
	if caps.Observe {
		tools = append(tools, browserObserveTool(ctl, exec, opts.MaxObserveElements))
	}
	if caps.Act {
		tools = append(tools, browserActTool(ctl, exec))
	}
	tools = append(tools, browserCloseTabTool(ctl, exec))
	return tools
}

// decodeBrowserArgs 解参数并拒绝身份/审批字段。
//
// 用 DisallowUnknownFields：多出来的键必须是错误。否则 `{"confirmed":true}`
// 会被静默丢掉，调用方（模型）以为"我确认过了"，而实际什么都没发生——
// 这种沉默比报错危险得多。
func decodeBrowserArgs(tool string, raw json.RawMessage, dst any) error {
	if len(raw) > 0 {
		if keys := forbiddenKeysIn(raw); len(keys) > 0 {
			return contracts.NewAppError(contracts.CodeUnauthorized, fmt.Sprintf(
				"%s 参数里不允许出现执行身份/审批字段（%s）：身份来自服务端绑定，"+
					"确认只能由用户在策略桥上给出，模型不能自行确认或切换会话",
				tool, strings.Join(keys, "/")), false)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			return contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
				"%s 参数不合法（不接受未声明的字段）: %v", tool, err), false)
		}
	}
	return nil
}

// forbiddenKeysIn 扫原始 JSON 的顶层键（大小写不敏感）。
func forbiddenKeysIn(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	seen := make([]string, 0, 2)
	for key := range obj {
		lower := strings.ToLower(key)
		for _, bad := range forbiddenArgKeys {
			if lower == bad {
				seen = append(seen, key)
				break
			}
		}
	}
	sort.Strings(seen)
	return seen
}

// tabFacts 是每个浏览器工具输出里都要带的一段事实。
//
// 它存在的理由只有一条：工具说"哪个 tab、哪个 URL、哪个文档"，必须与面板显示
// 的完全一致。所以这段事实来自 Service 记录的 (URL, navigationID, generation)，
// 不是页面自报值，也不是模型参数。
type tabFacts struct {
	TabID        browser.TabID       `json:"tabId"`
	URL          string              `json:"url"`
	NavigationID string              `json:"navigationId"`
	Mode         browser.ControlMode `json:"mode"`
	Generation   uint64              `json:"controlGeneration"`
	Queued       int                 `json:"queued"`
	InFlight     int                 `json:"inFlight"`
}

func browserTabFacts(ctl BrowserController, exec contracts.ExecutionContext, tab browser.TabID) (tabFacts, error) {
	info, err := ctl.TabInfo(exec, tab)
	if err != nil {
		return tabFacts{}, err
	}
	return tabFacts{
		TabID:        info.TabID,
		URL:          info.URL,
		NavigationID: info.NavigationID,
		Mode:         info.Mode,
		Generation:   info.ControlGeneration,
		Queued:       info.Queued,
		InFlight:     info.InFlight,
	}, nil
}

// renderFacts 把事实加成一行尾部说明，让模型在文本里也能看到 tab/文档身份。
func renderFacts(f tabFacts) string {
	return fmt.Sprintf("tab=%s url=%s navigationId=%s mode=%s generation=%d queued=%d inFlight=%d",
		f.TabID, f.URL, orDash(f.NavigationID), f.Mode, f.Generation, f.Queued, f.InFlight)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// marshalTool 把结构化结果编成工具文本（模型与证据读同一份 JSON）。
func marshalTool(v any) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "序列化工具结果失败: "+err.Error(), false)
	}
	return string(raw), nil
}

func browserTabsTool(ctl BrowserController, exec contracts.ExecutionContext) Tool {
	return Tool{
		Name: BrowserToolTabs,
		Description: "List the browser tabs owned by this session (browser panel tabs). " +
			"Use it to find the tabId before observing. Only tabs of the current session are visible.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			if err := decodeBrowserArgs(BrowserToolTabs, raw, &struct{}{}); err != nil {
				return ToolResult{IsError: true}, err
			}
			tabs, err := listBrowserTabs(ctl, exec)
			if err != nil {
				return ToolResult{IsError: true}, err
			}
			text, merr := marshalTool(map[string]any{"tabs": tabs})
			if merr != nil {
				return ToolResult{IsError: true}, merr
			}
			return ToolResult{Text: text}, nil
		},
	}
}

// listBrowserTabs 通过 controller 的 TabInfo 收集（不带 backend 直连：tab 的归属
// 只有 Service 知道）。
func listBrowserTabs(ctl BrowserController, exec contracts.ExecutionContext) ([]tabFacts, error) {
	lister, ok := ctl.(interface {
		Tabs(exec contracts.ExecutionContext) []browser.TabID
	})
	if !ok {
		return []tabFacts{}, nil
	}
	ids := lister.Tabs(exec)
	out := make([]tabFacts, 0, len(ids))
	for _, id := range ids {
		facts, err := browserTabFacts(ctl, exec, id)
		if err != nil {
			continue
		}
		out = append(out, facts)
	}
	return out, nil
}

func browserOpenTabTool(ctl BrowserController, exec contracts.ExecutionContext) Tool {
	return Tool{
		Name: BrowserToolOpenTab,
		Description: "Open a new browser tab in the right-hand panel and navigate it to an http(s) URL. " +
			"Limits: 4 tabs per session, 12 in total. Opening beyond the limit fails instead of creating a tab.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "http(s) URL to open."},
			},
			"required": []string{"url"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var in struct {
				URL string `json:"url"`
			}
			if err := decodeBrowserArgs(BrowserToolOpenTab, raw, &in); err != nil {
				return ToolResult{IsError: true}, err
			}
			tab, err := ctl.OpenTab(ctx, exec, browser.TabOptions{URL: in.URL})
			if err != nil {
				return ToolResult{IsError: true}, err
			}
			facts, ferr := browserTabFacts(ctl, exec, tab)
			if ferr != nil {
				return ToolResult{IsError: true}, ferr
			}
			text, merr := marshalTool(facts)
			if merr != nil {
				return ToolResult{IsError: true}, merr
			}
			return ToolResult{Text: text}, nil
		},
	}
}

func browserObserveTool(ctl BrowserController, exec contracts.ExecutionContext, maxElements int) Tool {
	limit := maxElements
	if limit <= 0 || limit > browser.MaxSnapshotElements {
		limit = browser.MaxSnapshotElements
	}
	return Tool{
		Name: BrowserToolObserve,
		Description: "Observe the page of a browser tab: returns a snapshot with element refs, text and the " +
			"document identity (navigationId). Refs are only valid for THIS snapshot and THIS control " +
			"generation; after a takeover, resume or navigation you must observe again.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tabId":       map[string]any{"type": "string", "description": "Tab id from browser_tabs."},
				"includeText": map[string]any{"type": "boolean", "description": "Include the page text (default true)."},
				"maxElements": map[string]any{
					"type": "integer", "minimum": 1, "maximum": limit,
					"description": fmt.Sprintf("Optional element cap, at most %d.", limit),
				},
			},
			"required": []string{"tabId"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var in struct {
				TabID       string `json:"tabId"`
				IncludeText *bool  `json:"includeText"`
				MaxElements int    `json:"maxElements"`
			}
			if err := decodeBrowserArgs(BrowserToolObserve, raw, &in); err != nil {
				return ToolResult{IsError: true}, err
			}
			includeText := true
			if in.IncludeText != nil {
				includeText = *in.IncludeText
			}
			snap, err := ctl.Observe(ctx, exec, browser.TabID(in.TabID), browser.ObserveOptions{
				IncludeText:  includeText,
				MaxElements:  in.MaxElements,
				MaxTextChars: 0,
			})
			if err != nil {
				return ToolResult{IsError: true}, err
			}
			facts, ferr := browserTabFacts(ctl, exec, snap.TabID)
			if ferr != nil {
				return ToolResult{IsError: true}, ferr
			}
			payload := map[string]any{
				"tab":        facts,
				"snapshot":   snap,
				"truncated":  snap.Truncated,
				"refScope":   "ref 只对本次快照与控制代有效：接管/恢复/导航之后必须重新 observe",
				"generation": snap.ControlGeneration,
			}
			text, merr := marshalTool(payload)
			if merr != nil {
				return ToolResult{IsError: true}, merr
			}
			return ToolResult{Text: text}, nil
		},
	}
}

func browserActTool(ctl BrowserController, exec contracts.ExecutionContext) Tool {
	return Tool{
		Name: BrowserToolAct,
		Description: "Run ONE browser action (click / fill / key / scroll) on a tab using a ref from a snapshot. " +
			"Each call is authorized before dispatch: the default is to ask the user. A denied, expired or " +
			"otherwise invalid authorization means NO input is sent. If the result says outcome unknown, do " +
			"NOT retry automatically — ask the user.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tabId":      map[string]any{"type": "string", "description": "Tab id from browser_tabs."},
				"kind":       map[string]any{"type": "string", "enum": []string{"click", "fill", "key", "scroll"}},
				"snapshotId": map[string]any{"type": "string", "description": "snapshotId from browser_observe."},
				"ref":        map[string]any{"type": "string", "description": "Element ref from that same snapshot."},
				"text":       map[string]any{"type": "string", "description": "fill: the value to type."},
				"key":        map[string]any{"type": "string", "description": "key: Enter/Tab/Escape/Backspace/Delete/arrows/Home/End/PageUp/PageDown/Space or a single character."},
				"deltaX":     map[string]any{"type": "number", "description": "scroll: horizontal offset in CSS px."},
				"deltaY":     map[string]any{"type": "number", "description": "scroll: vertical offset in CSS px."},
				"expectedCondition": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":      map[string]any{"type": "string", "enum": []string{"url_equals", "element_text", "field_value", "element_present"}},
						"url":       map[string]any{"type": "string"},
						"ref":       map[string]any{"type": "string"},
						"value":     map[string]any{"type": "string"},
						"timeoutMs": map[string]any{"type": "integer", "minimum": 0, "maximum": int(browser.ActionTimeout / time.Millisecond)},
					},
					"required":             []string{"kind"},
					"additionalProperties": false,
				},
			},
			"required": []string{"tabId", "kind", "snapshotId"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var in struct {
				TabID             string                     `json:"tabId"`
				Kind              string                     `json:"kind"`
				SnapshotID        string                     `json:"snapshotId"`
				Ref               string                     `json:"ref"`
				Text              string                     `json:"text"`
				Key               string                     `json:"key"`
				DeltaX            float64                    `json:"deltaX"`
				DeltaY            float64                    `json:"deltaY"`
				ExpectedCondition *browser.ExpectedCondition `json:"expectedCondition"`
			}
			if err := decodeBrowserArgs(BrowserToolAct, raw, &in); err != nil {
				return ToolResult{IsError: true}, err
			}
			action := browser.Action{
				Kind:       browser.ActionKind(in.Kind),
				SnapshotID: in.SnapshotID,
				Ref:        in.Ref,
				Text:       in.Text,
				Key:        in.Key,
				DeltaX:     in.DeltaX,
				DeltaY:     in.DeltaY,
			}
			if in.ExpectedCondition != nil {
				action.ExpectedCondition = *in.ExpectedCondition
			}
			tab := browser.TabID(in.TabID)
			res, err := ctl.Execute(ctx, exec, tab, action)
			facts, ferr := browserTabFacts(ctl, exec, tab)
			if ferr != nil {
				// tab 可能已被关闭/不属于本会话：用动作本身的结论优先。
				facts = tabFacts{TabID: tab}
			}
			if err != nil {
				return ToolResult{IsError: true, Text: browserActionErrorText(err, facts)}, nil
			}
			payload := map[string]any{
				"tab":          facts,
				"action":       action.Kind,
				"delivered":    res.Delivered,
				"verified":     res.Verified,
				"verification": res.Verification,
				"navigationId": res.NavigationID,
				"note": "Delivered 不等于业务成功：没有 verified=true 时必须重新 browser_observe，" +
					"不能把 delivered 当成完成。",
			}
			text, merr := marshalTool(payload)
			if merr != nil {
				return ToolResult{IsError: true}, merr
			}
			return ToolResult{Text: text}, nil
		},
	}
}

// browserActionErrorText 把动作失败翻译成模型能正确处理的一段话。
//
// 关键分支只有三个：
//   - OUTCOME_UNKNOWN / CONTROL_UNCERTAIN：不得重试；
//   - TAB_ACTION_REVOKED / STALE_SNAPSHOT：没有输入发出，可以（且必须）重新 observe；
//   - 其它：如实转述。
func browserActionErrorText(err error, facts tabFacts) string {
	code := ""
	var app *contracts.AppError
	if e, ok := err.(*contracts.AppError); ok && e != nil {
		app = e
		code = e.Code
	}
	head := fmt.Sprintf("%s\n%s", renderFacts(facts), err.Error())
	if app == nil {
		return head
	}
	switch code {
	case contracts.CodeOutcomeUnknown:
		return head + "\n结果未知（输入已派发给引擎但没有确认结果）：不要重试，也不要换个 ref 再试一次。" +
			"该 tab 已进入 control_uncertain，需要用户确认页面状态后再继续。"
	case contracts.CodeControlUncertain:
		return head + "\n控制状态不确定：在用户接管/确认之前不得再派发任何写操作（本次没有任何输入发出）。"
	case browser.CodeActionRevoked, contracts.CodeStaleSnapshot:
		return head + "\n本次没有任何输入发出（已入队但被撤销，或引用已失效）：" +
			"重新 browser_observe 取新快照后再决定要不要再做一次。"
	case contracts.CodeNeedsHuman:
		return head + "\n该 tab 现在由人工控制：你不是操作方。等用户把控制权交还（resume）并重新 observe 之后再继续。"
	case browser.CodeQueueFull:
		return head + "\n该 tab 的待派发队列已满：等前一个动作结束后再试，不要并发刷写。"
	case browser.CodeTabLimitExceeded:
		return head + "\n达 tab 限额（4/session、12/总计）：先关闭不用的 tab，不要重复尝试创建。"
	}
	return head
}

func browserCloseTabTool(ctl BrowserController, exec contracts.ExecutionContext) Tool {
	return Tool{
		Name: BrowserToolCloseTab,
		Description: "Close a browser tab owned by this session. Pending (not yet dispatched) actions of that tab " +
			"are revoked and none of them will be sent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tabId": map[string]any{"type": "string", "description": "Tab id from browser_tabs."},
			},
			"required": []string{"tabId"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var in struct {
				TabID string `json:"tabId"`
			}
			if err := decodeBrowserArgs(BrowserToolCloseTab, raw, &in); err != nil {
				return ToolResult{IsError: true}, err
			}
			facts, _ := browserTabFacts(ctl, exec, browser.TabID(in.TabID))
			if err := ctl.CloseTab(ctx, exec, browser.TabID(in.TabID)); err != nil {
				return ToolResult{IsError: true}, err
			}
			return ToolResult{Text: fmt.Sprintf("已关闭 tab %s（%s）：该 tab 未派发的动作已撤销，没有任何输入发出。",
				in.TabID, orDash(facts.URL))}, nil
		},
	}
}
