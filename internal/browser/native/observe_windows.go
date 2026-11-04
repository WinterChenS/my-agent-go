//go:build windows

package native

// 本文件放 B01a 的页面侧脚本与 Observe/Act 的实现。
//
// 两条原则：
//   - 观察与动作都跑在 Page.createIsolatedWorld 建的隔离世界里，页面脚本改不到；
//   - 只返回有界数据（元素 ≤300、文本 ≤40000 字符、字段截断），
//     超限时如实截断并在证据里说明，而不是把整页 JSON 抛给上层。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

// observeJS 返回一个 JSON 字符串，字段与 browser.Snapshot 对齐。
//
// 元素身份用 data-myagent-ref 属性标记：隔离世界与主世界共享同一个 DOM，
// 所以动作阶段能按属性找回**同一个元素**，而不是按坐标猜。
const observeJS = `(() => {
  try {
    const MAXE = 300, MAXT = 40000, MAXNAME = 120;
    document.querySelectorAll('[data-myagent-ref]').forEach(el => el.removeAttribute('data-myagent-ref'));
    const sel = 'a[href],button,input,select,textarea,[role="button"],[role="link"],[contenteditable="true"]';
    const els = [];
    let n = 0;
    for (const el of document.querySelectorAll(sel)) {
      if (els.length >= MAXE) break;
      const r = el.getBoundingClientRect();
      const st = getComputedStyle(el);
      if (st.visibility === 'hidden' || st.display === 'none') continue;
      n += 1;
      const ref = 'e' + n;
      el.setAttribute('data-myagent-ref', ref);
      const label = (el.getAttribute('aria-label') || el.innerText || el.value ||
        el.getAttribute('placeholder') || el.getAttribute('name') || el.id || '')
        .trim().replace(/\s+/g, ' ').slice(0, MAXNAME);
      els.push({
        ref: ref,
        role: el.getAttribute('role') || '',
        name: label,
        tag: el.tagName.toLowerCase(),
        inputType: (el.getAttribute('type') || '').toLowerCase(),
        disabled: !!el.disabled,
        bounds: { x: r.left, y: r.top, width: r.width, height: r.height }
      });
    }
    const frames = [];
    document.querySelectorAll('iframe').forEach((f, i) => {
      let reason = '';
      try { if (!f.contentDocument) reason = 'cross-origin'; } catch (e) { reason = 'cross-origin'; }
      if (reason) frames.push({ frameId: 'iframe-' + i, url: f.src || '', reason: 'cross-origin（B01a 不读取子 frame）' });
    });
    const text = (document.body ? document.body.innerText : '').slice(0, MAXT);
    return JSON.stringify({
      ready: document.readyState,
      title: document.title,
      url: location.href,
      viewport: { width: window.innerWidth, height: window.innerHeight, scale: window.devicePixelRatio || 1 },
      elements: els,
      text: text,
      unsupportedFrames: frames
    });
  } catch (e) {
    return JSON.stringify({ error: String(e) });
  }
})()`

// resolveRefJS 把 ref 解析成"当前的位置 + 身份校验信息"。
//
// 先 scrollIntoView 再取 rect：元素在视口外时坐标会落到负数，直接点会点空。
const resolveRefJS = `(() => {
  const el = document.querySelector('[data-myagent-ref="%s"]');
  if (!el) return null;
  el.scrollIntoView({ block: 'center', inline: 'center' });
  const r = el.getBoundingClientRect();
  return JSON.stringify({
    x: r.left + r.width / 2,
    y: r.top + r.height / 2,
    tag: el.tagName.toLowerCase(),
    inputType: (el.getAttribute('type') || '').toLowerCase(),
    disabled: !!el.disabled
  });
})()`

// focusRefJS 把元素聚焦并全选（fill 之前的准备）。
//
// 聚焦/全选是 DOM 操作、不是输入事件；真正写值走 CDP Input.insertText，
// 那条路径产生的是可信输入。
const focusRefJS = `(() => {
  const el = document.querySelector('[data-myagent-ref="%s"]');
  if (!el) return null;
  el.focus();
  try { if (typeof el.select === 'function') el.select(); } catch (e) {}
  return JSON.stringify({ tag: el.tagName.toLowerCase(), value: String(el.value ?? '') });
})()`

// conditionJS 按后置条件返回一个字符串；null 表示元素不存在。
const conditionURLJS = `location.href`
const conditionTextJS = `(() => { const el = document.querySelector('[data-myagent-ref="%s"]'); return el ? (el.innerText || '') : null; })()`
const conditionValueJS = `(() => { const el = document.querySelector('[data-myagent-ref="%s"]'); return el ? String(el.value ?? '') : null; })()`
const conditionPresentJS = `(() => { return !!document.querySelector('[data-myagent-ref="%s"]'); })()`

type observeResult struct {
	Ready    string `json:"ready"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Viewport struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Scale  float64 `json:"scale"`
	} `json:"viewport"`
	Elements []struct {
		Ref       string       `json:"ref"`
		Role      string       `json:"role"`
		Name      string       `json:"name"`
		Tag       string       `json:"tag"`
		InputType string       `json:"inputType"`
		Disabled  bool         `json:"disabled"`
		Bounds    browser.Rect `json:"bounds"`
	} `json:"elements"`
	Text              string                     `json:"text"`
	UnsupportedFrames []browser.UnsupportedFrame `json:"unsupportedFrames"`
	Error             string                     `json:"error"`
}

// observe 执行"取 frame → 建隔离世界 → 求值"三段。
func (h *host) observe(ctx context.Context, tab browser.TabID, opts browser.ObserveOptions) (browser.Snapshot, error) {
	if err := h.requireTab(ctx, tab); err != nil {
		return browser.Snapshot{}, err
	}
	frameID, err := h.mainFrameID(ctx)
	if err != nil {
		return browser.Snapshot{}, err
	}
	execCtx, err := h.isolatedWorld(ctx, frameID)
	if err != nil {
		return browser.Snapshot{}, err
	}
	res, err := h.cdpEvaluate(ctx, execCtx, observeJS, 10*time.Second)
	if err != nil {
		return browser.Snapshot{}, err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return browser.Snapshot{}, err
	}
	var parsed observeResult
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return browser.Snapshot{}, appErr(contracts.CodeInternal, "解析 observe 结果失败: %v", err)
	}
	if parsed.Error != "" {
		return browser.Snapshot{}, appErr(contracts.CodeInternal, "页面侧 observe 抛异常: %s", parsed.Error)
	}

	// 上限只能往下调：ObserveOptions 里的值超过基线时按基线夹紧。
	maxEls := opts.EffectiveMaxElements()
	maxChars := opts.EffectiveMaxTextChars()
	elements := make([]browser.Element, 0, len(parsed.Elements))
	for _, el := range parsed.Elements {
		if len(elements) >= maxEls {
			break
		}
		elements = append(elements, browser.Element{
			Ref:       el.Ref,
			Role:      el.Role,
			Name:      el.Name,
			Tag:       el.Tag,
			InputType: el.InputType,
			Disabled:  el.Disabled,
			Bounds:    el.Bounds,
		})
	}
	text := ""
	if opts.IncludeText {
		text = truncateRunes(parsed.Text, maxChars)
	}

	var snapID string
	navID := h.navigationID()
	if err := h.runOnUI(ctx, func() error {
		h.snapSeq++
		snapID = fmt.Sprintf("snap-%d", h.snapSeq)
		refs := &snapshotRefs{id: snapID, navigation: navID, execCtx: execCtx, refs: map[string]browser.Element{}}
		for _, el := range elements {
			refs.refs[el.Ref] = el
		}
		h.snaps[snapID] = refs
		return nil
	}); err != nil {
		return browser.Snapshot{}, err
	}

	scale := parsed.Viewport.Scale
	if scale <= 0 {
		scale = 1
	}
	return browser.Snapshot{
		ID:                snapID,
		TabID:             tab,
		NavigationID:      navID,
		ControlGeneration: 0, // B05 才有控制权代数
		Viewport: browser.Viewport{
			Width:  parsed.Viewport.Width,
			Height: parsed.Viewport.Height,
			Scale:  scale,
		},
		Elements:          elements,
		Text:              text,
		UnsupportedFrames: parsed.UnsupportedFrames,
	}, nil
}

// mainFrameID 取顶层 frame 的 id。
func (h *host) mainFrameID(ctx context.Context) (string, error) {
	res, err := h.cdpRoundTrip(ctx, "Page.getFrameTree", `{}`, 5*time.Second)
	if err != nil {
		return "", err
	}
	var payload struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		return "", appErr(contracts.CodeInternal, "解析 getFrameTree 失败: %v", err)
	}
	if payload.FrameTree.Frame.ID == "" {
		return "", appErr(contracts.CodeInternal, "getFrameTree 没有返回顶层 frame id")
	}
	return payload.FrameTree.Frame.ID, nil
}

// isolatedWorld 在顶层 frame 建（或复用）一个具名隔离世界，返回执行上下文 id。
func (h *host) isolatedWorld(ctx context.Context, frameID string) (int64, error) {
	params := fmt.Sprintf(`{"frameId":%s,"worldName":"myagent-observe","grantUniveralAccess":false}`,
		jsonString(frameID))
	res, err := h.cdpRoundTrip(ctx, "Page.createIsolatedWorld", params, 5*time.Second)
	if err != nil {
		return 0, err
	}
	var payload struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		return 0, appErr(contracts.CodeInternal, "解析 createIsolatedWorld 失败: %v", err)
	}
	if payload.ExecutionContextID <= 0 {
		return 0, appErr(contracts.CodeInternal, "createIsolatedWorld 没有返回 executionContextId")
	}
	return payload.ExecutionContextID, nil
}

// act 执行 click/fill 并核验后置条件。
func (h *host) act(ctx context.Context, tab browser.TabID, action browser.Action) (browser.ActionResult, error) {
	if err := action.Validate(); err != nil {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
			err
	}
	switch action.Kind {
	case browser.ActionClick, browser.ActionFill:
	case browser.ActionKey, browser.ActionScroll:
		return browser.ActionResult{
			Verification: browser.Verification{
				Status:    browser.VerificationUnsupported,
				Condition: action.ExpectedCondition.Kind,
				Detail:    "B01a 只实现 click/fill；" + string(action.Kind) + " 留给 B01b",
			},
		}, appErr(contracts.CodeUnsupported,
			"B01a 不实现 %s 动作（click/fill 之外的输入尚未接入 CDP），没有发出任何输入",
			action.Kind)
	}

	snap, err := h.lookupSnapshot(ctx, tab, action.SnapshotID)
	if err != nil {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
	}

	el, ok := snap.refs[action.Ref]
	if !ok {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
			appErr(contracts.CodeStaleSnapshot,
				"ref %q 不在快照 %s 里；必须重新 observe", action.Ref, action.SnapshotID)
	}

	target, err := h.resolveRef(ctx, snap, action.Ref)
	if err != nil {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
	}
	// 身份校验：标签/类型变了说明 DOM 已经被换掉，按坐标点会点到别的东西。
	if target.Tag != el.Tag || (el.InputType != "" && target.InputType != el.InputType) {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
			appErr(contracts.CodeStaleSnapshot,
				"ref %q 现在指向 %s(type=%s)，快照里是 %s(type=%s)：文档已变，必须重新 observe",
				action.Ref, target.Tag, target.InputType, el.Tag, el.InputType)
	}
	if target.Disabled {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
			appErr(contracts.CodeUnsupported, "目标元素 %q 处于 disabled 状态，没有发出输入", action.Ref)
	}

	switch action.Kind {
	case browser.ActionClick:
		if err := h.dispatchClick(ctx, target.X, target.Y); err != nil {
			return browser.ActionResult{}, err
		}
	case browser.ActionFill:
		if err := h.dispatchFill(ctx, snap, action.Ref, action.Text); err != nil {
			return browser.ActionResult{}, err
		}
	}
	h.caps.act.Store(true)

	result := browser.DeliveredResult(h.navigationID())
	if action.ExpectedCondition.Kind == "" {
		return result, nil
	}
	verification := h.verifyCondition(ctx, snap, action.ExpectedCondition)
	result.Verification = verification
	result.Verified = verification.Status == browser.VerificationVerified
	if !result.Verified && verification.Status == browser.VerificationFailed {
		result.Error = verification.Detail
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

// lookupSnapshot 找到动作授权的快照，并确认文档没有换过。
func (h *host) lookupSnapshot(ctx context.Context, tab browser.TabID, id string) (*snapshotRefs, error) {
	var snap *snapshotRefs
	err := h.runOnUI(ctx, func() error {
		if !h.tabOpen || h.tab != tab {
			return appErr(contracts.CodeNotFound, "没有活跃 tab %q", string(tab))
		}
		s, ok := h.snaps[id]
		if !ok {
			return appErr(contracts.CodeStaleSnapshot,
				"快照 %q 不存在或已失效（tab 关闭/导航后快照表会被清空）", id)
		}
		current := fmt.Sprintf("nav-%d", h.renderNav.Load())
		if s.navigation != current {
			return appErr(contracts.CodeStaleSnapshot,
				"快照 %q 属于文档 %s，当前文档是 %s：必须重新 observe", id, s.navigation, current)
		}
		snap = s
		return nil
	})
	return snap, err
}

type resolvedTarget struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Tag       string  `json:"tag"`
	InputType string  `json:"inputType"`
	Disabled  bool    `json:"disabled"`
}

// resolveRef 按 ref 找回当前元素位置。
func (h *host) resolveRef(ctx context.Context, snap *snapshotRefs, ref string) (resolvedTarget, error) {
	res, err := h.cdpEvaluate(ctx, snap.execCtx, fmt.Sprintf(resolveRefJS, ref), 5*time.Second)
	if err != nil {
		return resolvedTarget{}, err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return resolvedTarget{}, err
	}
	if value == "null" || value == "" {
		return resolvedTarget{}, appErr(contracts.CodeStaleSnapshot,
			"ref %q 在页面上已经不存在（节点被移除）：必须重新 observe", ref)
	}
	var t resolvedTarget
	if err := json.Unmarshal([]byte(value), &t); err != nil {
		return resolvedTarget{}, appErr(contracts.CodeInternal, "解析 ref 位置失败: %v", err)
	}
	return t, nil
}

// dispatchClick 用 CDP Input.dispatchMouseEvent 发一次可信左键点击。
func (h *host) dispatchClick(ctx context.Context, x, y float64) error {
	for _, ev := range []struct {
		typ     string
		buttons int
	}{{"mousePressed", 1}, {"mouseReleased", 0}} {
		params := fmt.Sprintf(
			`{"type":%s,"x":%.2f,"y":%.2f,"button":"left","buttons":%d,"clickCount":1}`,
			jsonString(ev.typ), x, y, ev.buttons)
		if _, err := h.cdpRoundTrip(ctx, "Input.dispatchMouseEvent", params, 5*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// dispatchFill 聚焦并全选目标元素，再用 Input.insertText 写入文本。
func (h *host) dispatchFill(ctx context.Context, snap *snapshotRefs, ref, text string) error {
	res, err := h.cdpEvaluate(ctx, snap.execCtx, fmt.Sprintf(focusRefJS, ref), 5*time.Second)
	if err != nil {
		return err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return err
	}
	if value == "null" || value == "" {
		return appErr(contracts.CodeStaleSnapshot, "fill 目标 %q 已不存在：必须重新 observe", ref)
	}
	params := fmt.Sprintf(`{"text":%s}`, jsonString(text))
	_, err = h.cdpRoundTrip(ctx, "Input.insertText", params, 5*time.Second)
	return err
}

// verifyCondition 轮询后置条件；只有真的观测到才返回 verified。
func (h *host) verifyCondition(ctx context.Context, snap *snapshotRefs, cond browser.ExpectedCondition) browser.Verification {
	timeout := 5 * time.Second
	if cond.TimeoutMS > 0 {
		timeout = time.Duration(cond.TimeoutMS) * time.Millisecond
	}
	if timeout > browser.ActionTimeout {
		timeout = browser.ActionTimeout
	}
	deadline := time.Now().Add(timeout)
	var last string
	var lastErr error
	for {
		observed, err := h.observeCondition(ctx, snap, cond)
		if err != nil {
			// 点击引起导航时，评估可能正好撞上新旧文档交接（旧 context 已销毁）。
			// 这属于“还没看到结果”，不是“不支持”，所以要继续轮询到超时。
			lastErr = err
		} else {
			lastErr = nil
			last = observed
			if conditionSatisfied(cond, observed) {
				return browser.Verification{
					Condition: cond.Kind,
					Status:    browser.VerificationVerified,
					Detail:    fmt.Sprintf("观测到 %s=%q", string(cond.Kind), observed),
				}
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return browser.Verification{
					Condition: cond.Kind,
					Status:    browser.VerificationUnsupported,
					Detail:    "条件核验通道在 " + timeout.String() + " 内一直失败: " + lastErr.Error(),
				}
			}
			return browser.Verification{
				Condition: cond.Kind,
				Status:    browser.VerificationFailed,
				Detail: fmt.Sprintf("等待 %s 后仍不满足：期望 %q，观测到 %q",
					timeout, expectedValue(cond), last),
			}
		}
		select {
		case <-ctx.Done():
			return browser.Verification{
				Condition: cond.Kind,
				Status:    browser.VerificationUnknown,
				Detail:    "核验被取消：" + ctx.Err().Error(),
			}
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func (h *host) observeCondition(ctx context.Context, snap *snapshotRefs, cond browser.ExpectedCondition) (string, error) {
	var expr string
	switch cond.Kind {
	case browser.ConditionURLEquals:
		expr = conditionURLJS
	case browser.ConditionElementText:
		expr = fmt.Sprintf(conditionTextJS, cond.Ref)
	case browser.ConditionFieldValue:
		expr = fmt.Sprintf(conditionValueJS, cond.Ref)
	case browser.ConditionElementPresent:
		expr = fmt.Sprintf(conditionPresentJS, cond.Ref)
	default:
		return "", appErr(contracts.CodeUnsupported, "未知后置条件 %q", cond.Kind)
	}
	// 核验跑在**默认上下文**而不是快照的隔离世界：
	//   - 隔离世界随文档销毁，点击导致导航后旧 contextId 直接 E_INVALIDARG（实测）；
	//   - url_equals 要看的正是导航后的新文档，拿旧上下文根本测不到。
	// 核验只读 DOM/URL，不执行页面提供的脚本，所以不需要隔离世界保护。
	_ = snap
	res, err := h.cdpEvaluate(ctx, 0, expr, 5*time.Second)
	if err != nil {
		return "", err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return "", err
	}
	return value, nil
}

func conditionSatisfied(cond browser.ExpectedCondition, observed string) bool {
	switch cond.Kind {
	case browser.ConditionElementPresent:
		return observed == "true"
	case browser.ConditionURLEquals:
		return normalizeURL(observed) == normalizeURL(cond.URL)
	default:
		return strings.TrimSpace(observed) == strings.TrimSpace(cond.Value)
	}
}

func expectedValue(cond browser.ExpectedCondition) string {
	if cond.Kind == browser.ConditionURLEquals {
		return cond.URL
	}
	return cond.Value
}

// normalizeURL 只做末尾斜杠归一，不做更激进的改写：把 URL 改得太多会让
// url_equals 变成"差不多相等"，那就不是核验了。
func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if len(u) > 1 && strings.HasSuffix(u, "/") {
		return strings.TrimSuffix(u, "/")
	}
	return u
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
