//go:build windows

package native

// B04 的页面侧脚本与 Observe/Act 实现。
//
// 三条原则（每条都对应一条验收口径，改之前先读）：
//   - 观察与动作都跑在 Page.createIsolatedWorld 建的隔离世界里，页面脚本改不到
//     辅助逻辑；元素身份（data-anvil-uid）是**节点本体**的不透明标记，不是序号，
//     所以"第二份快照把 e3 编号给另一个节点"这种误点结构上不可能发生（BR08）；
//   - 只返回有界数据（元素 ≤300、文本 ≤40000 字符、JSON ≤256KiB），超限时如实
//     裁剪并在 Snapshot.Truncated / 证据 artifact 里标注（BR09）；
//   - 密码字段的值**从不取**：标签里也不放，正文里出现也抹掉（BR09）。
//
// 看不见的东西必须说出来：cross-origin frame、未被遍历的同源 frame、closed
// shadow root 都进 Snapshot.UnsupportedFrames 并带具体原因（BR10），
// 绝不能因为"读不到"就当成"页面是空的"。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

// observeJSTemplate 返回一个 JSON 字符串，字段与 browser.Snapshot 对齐。
//
// 占位符由 Go 用 json 编码后注入：注入值（nonce / 原因文案）可能是任意字符串，
// 拼进 JS 之前必须转义，否则页面侧文案就能改变脚本语义。
const observeJSTemplate = `(() => {
  try {
    const NONCE = {{NONCE}};
    const MAXE = 4000, MAXNAME = 160, MAXT = 60000;
    const SEL = 'a[href],button,input,select,textarea,[role],[contenteditable="true"],h1,h2,h3,p,strong,code,output,label';
    const nodes = document.querySelectorAll(SEL);
    const secrets = [];
    for (const el of nodes) {
      if (el.tagName === 'INPUT' && (el.getAttribute('type') || '').toLowerCase() === 'password') {
        const v = String(el.value == null ? '' : el.value);
        if (v.length >= 3) secrets.push(v);
      }
    }
    const scrub = (s) => {
      let out = String(s == null ? '' : s);
      for (const v of secrets) { if (v) out = out.split(v).join('[redacted]'); }
      return out;
    };
    // uid 序号写在 <html> 上：同一文档内单调递增，跨观察复用已有节点的 uid。
    const html = document.documentElement;
    let uidSeq = Number(html.getAttribute('data-anvil-uid-seq') || '0');
    const els = [];
    for (const el of nodes) {
      if (els.length >= MAXE) break;
      const st = getComputedStyle(el);
      if (!st || st.visibility === 'hidden' || st.display === 'none') continue;
      let uid = el.getAttribute('data-anvil-uid');
      if (!uid) {
        uidSeq += 1;
        uidSeq += 1;
        uid = NONCE + '-e' + uidSeq;
        el.setAttribute('data-anvil-uid', uid);
      }
      const r = el.getBoundingClientRect();
      const type = (el.getAttribute('type') || '').toLowerCase();
      const isSecret = el.tagName === 'INPUT' && type === 'password';
      let label = el.getAttribute('aria-label') || el.getAttribute('placeholder') ||
        el.getAttribute('title') || '';
      if (!label) {
        if (el.tagName === 'INPUT' || el.tagName === 'SELECT' || el.tagName === 'TEXTAREA') {
          // 密码字段的值绝不参与标签：这是"值不进快照"的第一道闸门。
          label = isSecret ? '' : String(el.value == null ? '' : el.value);
          if (!label) label = el.getAttribute('name') || el.id || '';
        } else {
          label = el.innerText == null ? '' : el.innerText;
        }
      }
      label = scrub(String(label).trim().replace(/\s+/g, ' ')).slice(0, MAXNAME);
      els.push({
        ref: uid,
        role: el.getAttribute('role') || '',
        name: label,
        tag: el.tagName.toLowerCase(),
        inputType: type,
        disabled: !!el.disabled,
        bounds: { x: r.left, y: r.top, width: r.width, height: r.height }
      });
    }
    if (uidSeq > 0) html.setAttribute('data-anvil-uid-seq', String(uidSeq));
    const frames = [];
    document.querySelectorAll('iframe').forEach((f, i) => {
      let reason = {{REASON_SAME_FRAME}};
      try { if (!f.contentDocument) reason = {{REASON_CROSS_FRAME}}; }
      catch (e) { reason = {{REASON_CROSS_FRAME}}; }
      const id = 'iframe-' + i + (f.id ? ('#' + f.id) : '');
      frames.push({ frameId: id, url: f.src || '', reason: reason });
    });
    const body = document.body ? (document.body.innerText || '') : '';
    const textRunes = [...body].length;
    const text = scrub([...body].slice(0, MAXT).join(''));
    return JSON.stringify({
      ready: document.readyState,
      title: document.title,
      url: location.href,
      viewport: { width: window.innerWidth, height: window.innerHeight, scale: window.devicePixelRatio || 1 },
      elements: els,
      totalElements: nodes.length,
      text: text,
      textRunes: textRunes,
      unsupportedFrames: frames
    });
  } catch (e) {
    return JSON.stringify({ error: String(e) });
  }
})()`

// resolveRefJS 把 ref 解析成"当前的位置 + 身份 + 命中事实"。
//
// 三件事必须一起做，缺一条就会退化成按坐标猜：
//   - scrollIntoView 之后再取 rect（视口外的元素坐标是负的，直接点会点空）；
//   - 元素身份（uid）原样带回来，由 Go 侧与快照记录比对（节点被替换时对不上）；
//   - elementFromPoint 命中测试：落点上如果有别的东西（遮罩/浮层），这次点击会
//     点到那个东西上，必须报 TARGET_OBSCURED 而不是"发出去了"。
const resolveRefJS = `(() => {
  const REF = {{REF}};
  const el = document.querySelector('[data-anvil-uid="' + REF + '"]');
  if (!el) return null;
  el.scrollIntoView({ block: 'center', inline: 'center' });
  const r = el.getBoundingClientRect();
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;
  let hit = null;
  try { hit = document.elementFromPoint(cx, cy); } catch (e) { hit = null; }
  const inside = !!hit && (hit === el || el.contains(hit));
  let hitDesc = 'null';
  if (hit) {
    hitDesc = hit.tagName.toLowerCase();
    if (hit.id) hitDesc += '#' + hit.id;
    if (hit.className && typeof hit.className === 'string' && hit.className.trim()) {
      hitDesc += '.' + hit.className.trim().split(/\s+/).join('.');
    }
  }
  return JSON.stringify({
    x: cx, y: cy, width: r.width, height: r.height,
    uid: el.getAttribute('data-anvil-uid'),
    tag: el.tagName.toLowerCase(),
    inputType: (el.getAttribute('type') || '').toLowerCase(),
    disabled: !!el.disabled,
    hit: inside,
    hitDesc: hitDesc,
    inViewport: cx >= 0 && cy >= 0 && cx <= window.innerWidth && cy <= window.innerHeight
  });
})()`

// fillFocusJS 聚焦并全选目标控件（可信输入之前的准备）。
const fillFocusJS = `(() => {
  const REF = {{REF}};
  const el = document.querySelector('[data-anvil-uid="' + REF + '"]');
  if (!el) return null;
  el.focus();
  try { if (typeof el.select === 'function') el.select(); } catch (e) {}
  return JSON.stringify({ tag: el.tagName.toLowerCase() });
})()`

// fillSelectJS 把 <select> 设成指定 option（按 value 或可见文本匹配）。
//
// select 没有键盘等价路径，只能走 DOM 赋值 + input/change 事件；这一点在
// 证据里单独标注，不冒充"可信输入"。
const fillSelectJS = `(() => {
  const REF = {{REF}}, WANT = {{WANT}};
  const el = document.querySelector('[data-anvil-uid="' + REF + '"]');
  if (!el) return null;
  let matched = false;
  for (const opt of el.options) {
    if (opt.value === WANT || (opt.textContent || '').trim() === WANT) {
      el.value = opt.value;
      matched = true;
      break;
    }
  }
  if (!matched) return JSON.stringify({ matched: false, value: String(el.value) });
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return JSON.stringify({ matched: true, value: String(el.value) });
})()`

// fillCheckableJS 把 checkbox/radio 设成目标勾选状态。
//
// 同样走 DOM 赋值 + input/change：CDP 没有"直接勾选"的可信输入，而按坐标点
// 会受 <label> 包裹与自定义控件影响，反而更容易点错。所以这里明确记录
// "用 DOM 赋值"这条路径，并且**读回**校验（Go 侧比对）。
const fillCheckableJS = `(() => {
  const REF = {{REF}}, WANT = {{WANT}};
  const el = document.querySelector('[data-anvil-uid="' + REF + '"]');
  if (!el) return null;
  el.checked = WANT ? true : false;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return JSON.stringify({ checked: !!el.checked, value: String(el.value == null ? '' : el.value) });
})()`

// readBackJS 读回控件当前的值/勾选状态（fill 的读回校验）。
//
// COMPARE 非空时**比较留在页面侧完成**，只回一个布尔：密码字段的值不能为了
// 校验而回到 Go 侧（那会顺手进日志/错误消息）。
const readBackJS = `(() => {
  const REF = {{REF}};
  const COMPARE = {{COMPARE}};
  const el = document.querySelector('[data-anvil-uid="' + REF + '"]');
  if (!el) return null;
  const editable = el.getAttribute && el.getAttribute('contenteditable') === 'true';
  const value = editable ? String(el.innerText == null ? '' : el.innerText)
                         : String(el.value == null ? '' : el.value);
  if (COMPARE !== null) {
    return JSON.stringify({ match: value === COMPARE, checked: (typeof el.checked === 'boolean') ? el.checked : null });
  }
  return JSON.stringify({
    match: null,
    value: value,
    checked: (typeof el.checked === 'boolean') ? el.checked : null,
    tag: el.tagName.toLowerCase()
  });
})()`

// conditionValueMatchJS 在页面侧比较密码字段的值，只回 "true"/"false"/"null"。
const conditionValueMatchJS = `(() => { const el = document.querySelector('[data-anvil-uid="' + %s + '"]'); return el ? String(String(el.value == null ? '' : el.value) === %s) : null; })()`

// conditionValueJS / conditionTextJS 在**默认上下文**里读后置条件，
// 不依赖快照的隔离世界（导航后旧 contextId 会直接失效）。
const (
	conditionURLJS     = `location.href`
	conditionTextJS    = `(() => { const el = document.querySelector('[data-anvil-uid="' + %s + '"]'); return el ? String(el.innerText == null ? '' : el.innerText) : null; })()`
	conditionValueJS   = `(() => { const el = document.querySelector('[data-anvil-uid="' + %s + '"]'); return el ? String(el.value == null ? '' : el.value) : null; })()`
	conditionPresentJS = `(() => { return !!document.querySelector('[data-anvil-uid="' + %s + '"]'); })()`
)

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
	TotalElements     int                        `json:"totalElements"`
	Text              string                     `json:"text"`
	TextRunes         int                        `json:"textRunes"`
	UnsupportedFrames []browser.UnsupportedFrame `json:"unsupportedFrames"`
	Error             string                     `json:"error"`
}

// observe 执行"取 frame → 建隔离世界 → 求值 → 裁剪 → 登记引用"五段。
func (h *host) observe(ctx context.Context, tab browser.TabID, opts browser.ObserveOptions) (browser.Snapshot, error) {
	if err := h.requireTab(ctx, tab); err != nil {
		return browser.Snapshot{}, err
	}
	navID := h.navigationID()
	execCtx, err := h.observeWorld(ctx, navID)
	if err != nil {
		return browser.Snapshot{}, err
	}
	expr := strings.NewReplacer(
		"{{NONCE}}", jsonString(h.refNonce),
		"{{REASON_CROSS_FRAME}}", jsonString(browser.ReasonCrossOriginFrame),
		"{{REASON_SAME_FRAME}}", jsonString(browser.ReasonSameOriginFrameNotWalked),
	).Replace(observeJSTemplate)

	res, err := h.cdpEvaluate(ctx, execCtx, expr, 15*time.Second)
	if err != nil {
		return browser.Snapshot{}, err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return browser.Snapshot{}, err
	}
	var parsed observeResult
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		// 注意：错误消息里不能带原始 JSON——页面正文可能回显了敏感值。
		return browser.Snapshot{}, appErr(contracts.CodeInternal, "解析 observe 结果失败（原始载荷不入日志）")
	}
	if parsed.Error != "" {
		return browser.Snapshot{}, appErr(contracts.CodeInternal, "页面侧 observe 抛异常")
	}

	maxEls := opts.EffectiveMaxElements()
	maxChars := opts.EffectiveMaxTextChars()

	truncated := false
	elements := make([]browser.Element, 0, len(parsed.Elements))
	for _, el := range parsed.Elements {
		if len(elements) >= maxEls {
			truncated = true
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
	if parsed.TotalElements > len(parsed.Elements) {
		// 页面侧也有自己的抓取上限（4000）：连它都超了，就更要说清楚。
		truncated = true
	}

	text := ""
	if opts.IncludeText {
		if parsed.TextRunes > maxChars {
			truncated = true
		}
		var textCut bool
		text, textCut = browser.TruncateText(parsed.Text, maxChars)
		truncated = truncated || textCut
	}

	frames := append([]browser.UnsupportedFrame{}, parsed.UnsupportedFrames...)
	frames = append(frames, h.closedShadowSurfaces(ctx)...)

	scale := parsed.Viewport.Scale
	if scale <= 0 {
		scale = 1
	}
	snap := browser.Snapshot{
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
		UnsupportedFrames: frames,
	}

	// JSON 上限：裁剪在**登记引用之前**做，被丢掉的元素就不该有可用的 ref。
	// FitSnapshotJSON 自己收敛到上限内（或明确报错），这里只累加裁剪事实。
	if _, cut, err := browser.FitSnapshotJSON(&snap, browser.MaxSnapshotJSONBytes); err != nil {
		return browser.Snapshot{}, err
	} else {
		truncated = truncated || cut
	}

	var snapID string
	if err := h.runOnUI(ctx, func() error {
		h.snapSeq++
		snapID = fmt.Sprintf("snap-%d", h.snapSeq)
		table := h.refs.Begin(snapID, navID, 0, execCtx)
		for _, el := range snap.Elements {
			table.Bind(el, el.Ref)
		}
		return nil
	}); err != nil {
		return browser.Snapshot{}, err
	}
	snap.ID = snapID
	snap.Truncated = truncated

	if h.opts.OnEvent != nil {
		h.emit(BrowserEvent{TabID: tab, Type: "snapshot_taken", Payload: map[string]any{
			"snapshotId":        snapID,
			"navigationId":      navID,
			"elements":          len(snap.Elements),
			"truncated":         truncated,
			"unsupportedFrames": len(snap.UnsupportedFrames),
		}})
	}
	return snap, nil
}

// observeWorld 返回（必要时创建）当前文档的隔离世界执行上下文。
//
// 同一文档复用同一个世界：每次观察都新建会让执行上下文无限增长，
// 而且动作阶段要用同一个上下文才能看到同一份 DOM 事实。
func (h *host) observeWorld(ctx context.Context, navID string) (int64, error) {
	var cached int64
	var cachedNav string
	if err := h.runOnUI(ctx, func() error {
		cached, cachedNav = h.isoWorld, h.isoWorldNav
		return nil
	}); err != nil {
		return 0, err
	}
	if cached > 0 && cachedNav == navID {
		return cached, nil
	}
	frameID, err := h.mainFrameID(ctx)
	if err != nil {
		return 0, err
	}
	execCtx, err := h.isolatedWorld(ctx, frameID)
	if err != nil {
		return 0, err
	}
	if err := h.runOnUI(ctx, func() error {
		h.isoWorld = execCtx
		h.isoWorldNav = navID
		return nil
	}); err != nil {
		return 0, err
	}
	return execCtx, nil
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
		return "", appErr(contracts.CodeInternal, "解析 getFrameTree 失败")
	}
	if payload.FrameTree.Frame.ID == "" {
		return "", appErr(contracts.CodeInternal, "getFrameTree 没有返回顶层 frame id")
	}
	return payload.FrameTree.Frame.ID, nil
}

// isolatedWorld 在顶层 frame 建一个具名隔离世界，返回执行上下文 id。
func (h *host) isolatedWorld(ctx context.Context, frameID string) (int64, error) {
	params := fmt.Sprintf(`{"frameId":%s,"worldName":"anvil-observe","grantUniveralAccess":false}`,
		jsonString(frameID))
	res, err := h.cdpRoundTrip(ctx, "Page.createIsolatedWorld", params, 5*time.Second)
	if err != nil {
		return 0, err
	}
	var payload struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		return 0, appErr(contracts.CodeInternal, "解析 createIsolatedWorld 失败")
	}
	if payload.ExecutionContextID <= 0 {
		return 0, appErr(contracts.CodeInternal, "createIsolatedWorld 没有返回 executionContextId")
	}
	return payload.ExecutionContextID, nil
}

// domNode 是 DOM.getDocument(pierce) 返回的节点子集。
type domNode struct {
	NodeType       int       `json:"nodeType"`
	NodeName       string    `json:"nodeName"`
	LocalName      string    `json:"localName"`
	NodeValue      string    `json:"nodeValue"`
	ShadowRootType string    `json:"shadowRootType"`
	ShadowRoots    []domNode `json:"shadowRoots"`
	Children       []domNode `json:"children"`
	ContentDoc     *domNode  `json:"contentDocument"`
	Attributes     []string  `json:"attributes"`
}

// closedShadowSurfaces 用 DOM.getDocument(pierce) 找出 closed shadow root。
//
// 为什么必须走 CDP 而不是页面脚本：closed 模式下宿主脚本看不到内部 DOM，
// 仅凭 el.shadowRoot === null 无法把它和普通 <div> 区分开——那正是
// "把看不见的区域当成空页面"的起点（BR10 明确禁止）。
//
// 探测通道本身不可用时返回一条带原因的声明，而不是假装页面没有 shadow root。
func (h *host) closedShadowSurfaces(ctx context.Context) []browser.UnsupportedFrame {
	res, err := h.cdpRoundTrip(ctx, "DOM.getDocument", `{"depth":-1,"pierce":true}`, 20*time.Second)
	if err != nil {
		return []browser.UnsupportedFrame{browser.UnsupportedSurface(
			"closed-shadow:probe", "", browser.ReasonShadowProbeUnavailable+": "+err.Error())}
	}
	var payload struct {
		Root domNode `json:"root"`
	}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		return []browser.UnsupportedFrame{browser.UnsupportedSurface(
			"closed-shadow:probe", "", browser.ReasonShadowProbeUnavailable+": 返回的 DOM 树无法解析")}
	}
	var out []browser.UnsupportedFrame
	var walk func(n domNode)
	walk = func(n domNode) {
		for _, sr := range n.ShadowRoots {
			if strings.EqualFold(sr.ShadowRootType, "closed") {
				id := "closed-shadow:" + describeDOMNode(n)
				out = append(out, browser.UnsupportedSurface(id, "", browser.ReasonClosedShadowRoot))
			}
			walk(sr)
		}
		for i := range n.Children {
			walk(n.Children[i])
		}
		if n.ContentDoc != nil {
			walk(*n.ContentDoc)
		}
	}
	walk(payload.Root)
	return out
}

// describeDOMNode 给节点一个便于定位的名字（tag#id）。
func describeDOMNode(n domNode) string {
	name := strings.ToLower(n.LocalName)
	if name == "" {
		name = strings.ToLower(n.NodeName)
	}
	for i := 0; i+1 < len(n.Attributes); i += 2 {
		if n.Attributes[i] == "id" {
			return name + "#" + n.Attributes[i+1]
		}
	}
	return name
}

// act 执行一次动作并核验后置条件。
func (h *host) act(ctx context.Context, tab browser.TabID, action browser.Action) (browser.ActionResult, error) {
	if err := action.Validate(); err != nil {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
	}

	table, err := h.lookupSnapshot(ctx, tab, action.SnapshotID)
	if err != nil {
		return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
	}
	execCtx, _ := table.BackendRef.(int64)

	// key/scroll 不需要 ref，其余动作必须先精确解析 ref（找不到就是 STALE）。
	var target resolvedTarget
	if action.Kind != browser.ActionKey && action.Kind != browser.ActionScroll {
		el, _, err := table.Resolve(action.Ref)
		if err != nil {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
		}
		target, err = h.resolveRef(ctx, execCtx, action.Ref)
		if err != nil {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
		}
		if err := table.CheckIdentity(action.Ref, target.UID); err != nil {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
		}
		// 标签/类型变了同样说明节点被替换：按坐标点会点到别的东西。
		if target.Tag != el.Tag || (el.InputType != "" && target.InputType != el.InputType) {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
				appErr(contracts.CodeStaleSnapshot,
					"ref %q 现在指向 %s(type=%s)，快照里是 %s(type=%s)：文档已变，必须重新 observe",
					action.Ref, target.Tag, target.InputType, el.Tag, el.InputType)
		}
		if target.Disabled {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
				appErr(contracts.CodeUnsupported,
					"目标元素 %q 处于 disabled 状态，没有发出输入", action.Ref)
		}
	}

	delivered := false
	var inputPath string
	switch action.Kind {
	case browser.ActionClick:
		if !target.Hit {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}},
				browser.ObscuredError(action.Ref, target.HitDesc)
		}
		if err := h.dispatchClick(ctx, target.X, target.Y); err != nil {
			return browser.ActionResult{}, err
		}
		delivered, inputPath = true, "cdp-mouse"
	case browser.ActionFill:
		path, err := h.dispatchFill(ctx, execCtx, action, target)
		if err != nil {
			return browser.ActionResult{Delivered: false, Verification: browser.Verification{
				Status: browser.VerificationFailed, Detail: err.Error()}}, err
		}
		delivered, inputPath = true, path
	case browser.ActionKey:
		isText, err := browser.ValidateKeyName(action.Key)
		if err != nil {
			return browser.ActionResult{Verification: browser.Verification{Status: browser.VerificationUnknown}}, err
		}
		if err := h.dispatchKey(ctx, action.Key, isText); err != nil {
			return browser.ActionResult{}, err
		}
		delivered, inputPath = true, "cdp-key"
	case browser.ActionScroll:
		if err := h.dispatchWheel(ctx, action.DeltaX, action.DeltaY); err != nil {
			return browser.ActionResult{}, err
		}
		delivered, inputPath = true, "cdp-wheel"
	}
	h.caps.act.Store(true)

	navID := h.navigationID()
	if h.opts.OnEvent != nil {
		h.emit(BrowserEvent{TabID: tab, Type: "action_dispatched", Payload: map[string]any{
			"kind":       string(action.Kind),
			"ref":        action.Ref,
			"path":       inputPath,
			"delivered":  delivered,
			"snapshotId": action.SnapshotID,
			// 注意：fill 的文本不进事件载荷——它可能是密码。
		}})
	}

	result := browser.DeliveredResult(navID)
	if action.ExpectedCondition.Kind == "" {
		return result, nil
	}
	verification := h.verifyCondition(ctx, action.ExpectedCondition, table.IsSecret(action.ExpectedCondition.Ref))
	// 输入已经发出、只是核验通道不可用 → needs_human（不能既不说成功也不说失败）。
	verification = browser.EscalateUnverifiable(verification, delivered)
	result.Verification = verification
	result.Verified = verification.Status == browser.VerificationVerified
	if !result.Verified {
		result.Error = verification.Detail
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

// lookupSnapshot 找到动作授权的快照引用表（并校验文档身份）。
func (h *host) lookupSnapshot(ctx context.Context, tab browser.TabID, id string) (*browser.RefTable, error) {
	if err := h.requireTab(ctx, tab); err != nil {
		return nil, err
	}
	var table *browser.RefTable
	err := h.runOnUI(ctx, func() error {
		if !h.tabOpen || h.tab != tab {
			return appErr(contracts.CodeNotFound, "没有活跃 tab %q", string(tab))
		}
		var err error
		table, err = h.refs.Lookup(id, h.navigationID())
		return err
	})
	return table, err
}

type resolvedTarget struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	UID        string  `json:"uid"`
	Tag        string  `json:"tag"`
	InputType  string  `json:"inputType"`
	Disabled   bool    `json:"disabled"`
	Hit        bool    `json:"hit"`
	HitDesc    string  `json:"hitDesc"`
	InViewport bool    `json:"inViewport"`
}

// resolveRef 按 ref 找回当前元素位置与身份。
func (h *host) resolveRef(ctx context.Context, execCtx int64, ref string) (resolvedTarget, error) {
	expr := strings.Replace(resolveRefJS, "{{REF}}", jsonString(ref), 1)
	res, err := h.cdpEvaluate(ctx, execCtx, expr, 5*time.Second)
	if err != nil {
		return resolvedTarget{}, err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return resolvedTarget{}, err
	}
	if value == "null" || value == "" {
		return resolvedTarget{}, appErr(contracts.CodeStaleSnapshot,
			"ref %q 在页面上已经不存在（节点被移除或已重新导航）：必须重新 observe", ref)
	}
	var t resolvedTarget
	if err := json.Unmarshal([]byte(value), &t); err != nil {
		return resolvedTarget{}, appErr(contracts.CodeInternal, "解析 ref 位置失败")
	}
	if !t.InViewport {
		return resolvedTarget{}, appErr(contracts.CodeUnsupported,
			"目标 %q 不在视口内（滚动后仍不可见），没有发出输入", ref)
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

// dispatchFill 按控件族写入，并读回校验；返回实际使用的输入路径。
func (h *host) dispatchFill(ctx context.Context, execCtx int64,
	action browser.Action, target resolvedTarget) (string, error) {

	control := browser.ClassifyControl(target.Tag, target.InputType)
	secret := strings.EqualFold(target.InputType, "password")
	if !control.Fillable() {
		return "", appErr(contracts.CodeUnsupported,
			"元素 %q（%s）不是可填写控件，没有发出输入", action.Ref, target.Tag)
	}

	switch control {
	case browser.ControlText, browser.ControlTextArea, browser.ControlContentEditable:
		expr := strings.Replace(fillFocusJS, "{{REF}}", jsonString(action.Ref), 1)
		res, err := h.cdpEvaluate(ctx, execCtx, expr, 5*time.Second)
		if err != nil {
			return "", err
		}
		value, err := runtimeValue(res)
		if err != nil {
			return "", err
		}
		if value == "null" || value == "" {
			return "", appErr(contracts.CodeStaleSnapshot, "fill 目标 %q 已不存在：必须重新 observe", action.Ref)
		}
		params := fmt.Sprintf(`{"text":%s}`, jsonString(action.Text))
		if _, err := h.cdpRoundTrip(ctx, "Input.insertText", params, 5*time.Second); err != nil {
			return "", err
		}
	case browser.ControlSelect:
		params := strings.NewReplacer(
			"{{REF}}", jsonString(action.Ref),
			"{{WANT}}", jsonString(action.Text),
		).Replace(fillSelectJS)
		res, err := h.cdpEvaluate(ctx, execCtx, params, 5*time.Second)
		if err != nil {
			return "", err
		}
		value, err := runtimeValue(res)
		if err != nil {
			return "", err
		}
		if value == "null" || value == "" {
			return "", appErr(contracts.CodeStaleSnapshot, "fill 目标 %q 已不存在：必须重新 observe", action.Ref)
		}
		var payload struct {
			Matched bool   `json:"matched"`
			Value   string `json:"value"`
		}
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return "", appErr(contracts.CodeInternal, "解析 select 赋值结果失败")
		}
		if !payload.Matched {
			return "", appErr(contracts.CodeVerificationFailed,
				"select 里没有 %q 这个 option（当前值 %q）：没有改任何东西", action.Text, payload.Value)
		}
		return "dom-select", h.verifyReadBack(ctx, execCtx, action, false, false)
	case browser.ControlCheckbox, browser.ControlRadio:
		want, err := browser.InterpretBool(action.Text)
		if err != nil {
			return "", err
		}
		params := strings.NewReplacer(
			"{{REF}}", jsonString(action.Ref),
			"{{WANT}}", fmt.Sprintf("%t", want),
		).Replace(fillCheckableJS)
		res, err := h.cdpEvaluate(ctx, execCtx, params, 5*time.Second)
		if err != nil {
			return "", err
		}
		value, err := runtimeValue(res)
		if err != nil {
			return "", err
		}
		if value == "null" || value == "" {
			return "", appErr(contracts.CodeStaleSnapshot, "fill 目标 %q 已不存在：必须重新 observe", action.Ref)
		}
		return "dom-checkable", h.verifyReadBack(ctx, execCtx, action, true, false)
	}
	return "cdp-insert-text", h.verifyReadBack(ctx, execCtx, action, false, secret)
}

// verifyReadBack 读回字段值并与请求值比对（fill 的"写进去"证据）。
//
// secret=true 时比较在页面侧完成：请求值与读回值都不出现在 Go 的错误消息里。
func (h *host) verifyReadBack(ctx context.Context, execCtx int64, action browser.Action, checkable, secret bool) error {
	compare := "null"
	if secret {
		compare = jsonString(action.Text)
	}
	expr := strings.NewReplacer(
		"{{REF}}", jsonString(action.Ref),
		"{{COMPARE}}", compare,
	).Replace(readBackJS)
	res, err := h.cdpEvaluate(ctx, execCtx, expr, 5*time.Second)
	if err != nil {
		return err
	}
	value, err := runtimeValue(res)
	if err != nil {
		return err
	}
	if value == "null" || value == "" {
		return appErr(contracts.CodeStaleSnapshot, "fill 目标 %q 读回时已不存在：必须重新 observe", action.Ref)
	}
	var payload struct {
		Match   *bool  `json:"match"`
		Value   string `json:"value"`
		Checked *bool  `json:"checked"`
		Tag     string `json:"tag"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return appErr(contracts.CodeInternal, "解析字段读回结果失败")
	}
	if secret {
		if payload.Match == nil || !*payload.Match {
			return appErr(contracts.CodeVerificationFailed,
				"密码字段的读回值与请求值不一致（值不回显）：写入没有生效")
		}
		return nil
	}
	if checkable {
		want, err := browser.InterpretBool(action.Text)
		if err != nil {
			return err
		}
		if payload.Checked == nil || *payload.Checked != want {
			return appErr(contracts.CodeVerificationFailed,
				"读回勾选状态 %v，期望 %t：写入没有生效", payload.Checked, want)
		}
		return nil
	}
	if payload.Value != action.Text {
		return appErr(contracts.CodeVerificationFailed,
			"读回值 %q 与请求值 %q 不一致：写入没有生效", payload.Value, action.Text)
	}
	return nil
}

// dispatchKey 派发一次按键（Enter/Tab/方向键或单字符）。
func (h *host) dispatchKey(ctx context.Context, key string, isText bool) error {
	type keySpec struct {
		key  string
		code string
		text string
		vk   int
	}
	spec := keySpec{key: key, code: key, vk: 0}
	named := map[string]keySpec{
		"Enter":      {key: "Enter", code: "Enter", text: "\r", vk: 13},
		"Tab":        {key: "Tab", code: "Tab", vk: 9},
		"Escape":     {key: "Escape", code: "Escape", vk: 27},
		"Backspace":  {key: "Backspace", code: "Backspace", vk: 8},
		"Delete":     {key: "Delete", code: "Delete", vk: 46},
		"ArrowUp":    {key: "ArrowUp", code: "ArrowUp", vk: 38},
		"ArrowDown":  {key: "ArrowDown", code: "ArrowDown", vk: 40},
		"ArrowLeft":  {key: "ArrowLeft", code: "ArrowLeft", vk: 37},
		"ArrowRight": {key: "ArrowRight", code: "ArrowRight", vk: 39},
		"Home":       {key: "Home", code: "Home", vk: 36},
		"End":        {key: "End", code: "End", vk: 35},
		"PageUp":     {key: "PageUp", code: "PageUp", vk: 33},
		"PageDown":   {key: "PageDown", code: "PageDown", vk: 34},
		"Space":      {key: " ", code: "Space", text: " ", vk: 32},
	}
	if s, ok := named[key]; ok {
		spec = s
	} else if isText {
		runes := []rune(key)
		vk := int(runes[0])
		if runes[0] >= 'a' && runes[0] <= 'z' {
			vk = int(runes[0]) - 32
		}
		spec = keySpec{key: key, code: "Key" + strings.ToUpper(key), text: key, vk: vk}
	}
	for _, typ := range []string{"keyDown", "keyUp"} {
		params := fmt.Sprintf(`{"type":%s,"key":%s,"code":%s,"windowsVirtualKeyCode":%d,"nativeVirtualKeyCode":%d`,
			jsonString(typ), jsonString(spec.key), jsonString(spec.code), spec.vk, spec.vk)
		if typ == "keyDown" && spec.text != "" {
			params += fmt.Sprintf(`,"text":%s,"unmodifiedText":%s`, jsonString(spec.text), jsonString(spec.text))
		}
		params += `}`
		if _, err := h.cdpRoundTrip(ctx, "Input.dispatchKeyEvent", params, 5*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// dispatchWheel 在视口中心派发滚轮事件（可信输入）。
//
// 落点取视口中心而不是 (0,0)：CDP 要求坐标在视口内，而滚动作用于该点所在的
// 滚动容器；用左上角会落到可能存在的固定头部上。
func (h *host) dispatchWheel(ctx context.Context, dx, dy float64) error {
	w, hgt, _, err := h.viewportMetrics(ctx)
	if err != nil {
		return err
	}
	params := fmt.Sprintf(
		`{"type":"mouseWheel","x":%.1f,"y":%.1f,"deltaX":%.1f,"deltaY":%.1f,"button":"none","buttons":0}`,
		w/2, hgt/2, dx, dy)
	_, err = h.cdpRoundTrip(ctx, "Input.dispatchMouseEvent", params, 5*time.Second)
	return err
}

// verifyCondition 轮询后置条件；只有真的观测到才返回 verified。
func (h *host) verifyCondition(ctx context.Context, cond browser.ExpectedCondition, secret bool) browser.Verification {
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
		observed, err := h.observeCondition(ctx, cond, secret)
		if err != nil {
			// 点击引起导航时，评估可能正好撞上新旧文档交接（旧 context 已销毁）。
			// 这属于"还没看到结果"，不是"不支持"，所以要继续轮询到超时。
			lastErr = err
		} else {
			lastErr = nil
			last = observed
			if conditionSatisfiedObserved(cond, observed, secret) {
				return browser.Verification{
					Condition: cond.Kind,
					Status:    browser.VerificationVerified,
					Detail:    describeObservation(cond, observed, secret),
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
				Detail:    describeFailure(cond, last, timeout, secret),
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

// observeCondition 读取一次后置条件的观测值。
//
// 跑在**默认上下文**而不是快照的隔离世界：隔离世界随文档销毁，点击导致导航后
// 旧 contextId 直接失效（实测），而 url_equals 要看的正是导航后的新文档。
// 核验只读 DOM/URL，不执行页面提供的脚本。
func (h *host) observeCondition(ctx context.Context, cond browser.ExpectedCondition, secret bool) (string, error) {
	var expr string
	switch cond.Kind {
	case browser.ConditionURLEquals:
		expr = conditionURLJS
	case browser.ConditionElementText:
		expr = fmt.Sprintf(conditionTextJS, jsonString(cond.Ref))
	case browser.ConditionFieldValue:
		if secret {
			// 密码字段：比较在页面侧做，只回 "true"/"false"，值不回 Go。
			expr = fmt.Sprintf(conditionValueMatchJS, jsonString(cond.Ref), jsonString(cond.Value))
			break
		}
		expr = fmt.Sprintf(conditionValueJS, jsonString(cond.Ref))
	case browser.ConditionElementPresent:
		expr = fmt.Sprintf(conditionPresentJS, jsonString(cond.Ref))
	default:
		return "", appErr(contracts.CodeUnsupported, "未知后置条件 %q", string(cond.Kind))
	}
	res, err := h.cdpEvaluate(ctx, 0, expr, 5*time.Second)
	if err != nil {
		return "", err
	}
	return runtimeValue(res)
}

// describeObservation 生成"核验通过"的说明；敏感字段不回显任何值。
func describeObservation(cond browser.ExpectedCondition, observed string, secret bool) string {
	if secret {
		return "密码字段的值与请求一致（值不回显）"
	}
	return fmt.Sprintf("观测到 %s=%q", string(cond.Kind), observed)
}

// describeFailure 生成"核验失败"的说明；敏感字段不写期望值也不写观测值。
func describeFailure(cond browser.ExpectedCondition, observed string, timeout time.Duration, secret bool) string {
	if secret {
		return fmt.Sprintf("等待 %s 后密码字段的值仍与请求不一致（值不回显）", timeout)
	}
	return fmt.Sprintf("等待 %s 后仍不满足：期望 %q，观测到 %q", timeout, expectedValue(cond), observed)
}

// conditionSatisfiedObserved 判断一次观测是否满足条件。
//
// 密码字段的 field_value 在页面侧比较，观测值是 "true"/"false"/"null"，
// 不能拿去和请求值做字符串比较（那会永远不相等）。
func conditionSatisfiedObserved(cond browser.ExpectedCondition, observed string, secret bool) bool {
	if secret && cond.Kind == browser.ConditionFieldValue {
		return observed == "true"
	}
	return conditionSatisfied(cond, observed)
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
