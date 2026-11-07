package browserfixture

// ScriptedBackend 是 B05 控制权/串行队列/授权平面的 browser.BrowserBackend 替身。
//
// 为什么需要它、以及它到底证明了什么，必须写清楚：
//   - 它**不是**原生宿主，也不冒充原生输入：B05 的 BR07.native/B01 证据由
//     tests/native 在真实 WebView2 上产出，这里不替代。
//   - 它证明的是"该不该派发、派发了几次、派发之后服务端有没有看到副作用"这一层：
//     Act 真的走一次 HTTP 写请求打到 fixture 服务端，所以 counter 是**服务端**的
//     真实计数，而不是替身里的一个自增变量。
//   - 它可以在任意平台跑，所以 BR11/BR12/BR13/BR14 的控制平面语义不必等
//     Windows/macOS/Linux 原生宿主到齐就能被门禁取证。
//
// 队列/授权用例关心的量全部暴露成可观测量（CreateCalls/Instances/Dispatched/
// ActLog），因此"第 5 个 tab 没创建实例""被拒绝的调用 counter 为 0"这类断言
// 落在计数上，而不是落在错误文案上。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

// ActRecord 是一次真正到达 backend 的动作（= 输入被交给"引擎"）。
type ActRecord struct {
	Seq          int     `json:"seq"`
	TabID        string  `json:"tabId"`
	Kind         string  `json:"kind"`
	Ref          string  `json:"ref"`
	DispatchedAt string  `json:"dispatchedAt"`
	DurationMS   float64 `json:"durationMs"`
	Outcome      string  `json:"outcome"`
	Error        string  `json:"error,omitempty"`
}

// ScriptedBackend 是可控的 backend 替身。
type ScriptedBackend struct {
	Fixture *Fixture

	// Latency 让某个 (kind, ref) 的动作变慢/阻塞；nil 表示立即返回。
	// 用例用它在"一个慢动作 + 两个排队动作"里制造真实的重叠。
	Latency func(kind browser.ActionKind, ref string) time.Duration

	mu          sync.Mutex
	createCalls int
	instances   int
	dispatched  int
	navSeq      int
	tabSeq      int
	actLog      []ActRecord
	tabs        map[browser.TabID]*scriptedTab
}

type scriptedTab struct {
	id      browser.TabID
	url     string
	nav     string
	values  map[string]string
	nodes   []scriptedNode
	nextUID int
}

type scriptedNode struct {
	ref       string
	role      string
	name      string
	tag       string
	inputType string
	field     string // 非空表示文本输入框（fill 的目标）
	submit    bool
}

// NewScriptedBackend 创建一个基于 fixture 服务端的替身。
func NewScriptedBackend(f *Fixture) *ScriptedBackend {
	return &ScriptedBackend{Fixture: f, tabs: map[browser.TabID]*scriptedTab{}}
}

// Capabilities 报告"这个替身能做什么"，与真实宿主同一套字段。
func (b *ScriptedBackend) Capabilities() browser.CapabilitySet {
	return browser.CapabilitySet{
		Platform: "scripted", Engine: "fixture-http",
		Navigate: true, Observe: true, Act: true, Screenshot: false,
		TrustedInput: false,
	}
}

// CreateCalls 返回 CreateTab 被调用过多少次（限额判定的核心断言）。
func (b *ScriptedBackend) CreateCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.createCalls
}

// Instances 返回当前存活的 tab 实例数。
func (b *ScriptedBackend) Instances() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.instances
}

// Dispatched 返回真正被派发到 backend 的动作数。
//
// 它与 counter 的区别很关键：Dispatched 证明"输入是否被派发"，counter 证明
// "服务端是否真的收到了副作用"。两者都为 0 才是"既没派发、也没生效"。
func (b *ScriptedBackend) Dispatched() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dispatched
}

// ActLog 返回动作日志副本。
func (b *ScriptedBackend) ActLog() []ActRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ActRecord, len(b.actLog))
	copy(out, b.actLog)
	return out
}

// CreateTab 新建一个脚本化 tab（真的分配一个 id，并记一次创建调用）。
func (b *ScriptedBackend) CreateTab(ctx context.Context, opts browser.TabOptions) (browser.TabID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := opts.Validate(); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.createCalls++
	b.tabSeq++
	id := browser.TabID(fmt.Sprintf("scripted-tab-%d", b.tabSeq))
	b.navSeq++
	tab := &scriptedTab{
		id:     id,
		url:    opts.URL,
		nav:    fmt.Sprintf("nav-%d", b.navSeq),
		values: map[string]string{},
	}
	tab.buildNodes()
	b.tabs[id] = tab
	b.instances++
	return id, nil
}

// Navigate 换文档身份（旧 ref 因此失效，与真实引擎一致）。
func (b *ScriptedBackend) Navigate(ctx context.Context, id browser.TabID, url string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[id]
	if !ok {
		return fmt.Errorf("scripted: 未知 tab %s", id)
	}
	b.navSeq++
	tab.url = url
	tab.nav = fmt.Sprintf("nav-%d", b.navSeq)
	tab.values = map[string]string{}
	tab.nodes = nil
	tab.nextUID = 0
	tab.buildNodes()
	return nil
}

// Observe 返回脚本化页面的快照。同一文档内 ref 稳定（与 B04 口径一致）。
func (b *ScriptedBackend) Observe(ctx context.Context, id browser.TabID, opts browser.ObserveOptions) (browser.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return browser.Snapshot{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[id]
	if !ok {
		return browser.Snapshot{}, fmt.Errorf("scripted: 未知 tab %s", id)
	}
	els := make([]browser.Element, 0, len(tab.nodes))
	for i, n := range tab.nodes {
		els = append(els, browser.Element{
			Ref: n.ref, Role: n.role, Name: n.name, Tag: n.tag, InputType: n.inputType,
			Bounds: browser.Rect{X: 10, Y: float64(20 + i*40), Width: 200, Height: 32},
		})
	}
	snap := browser.Snapshot{
		ID:           fmt.Sprintf("%s-snap-%d", tab.id, len(tab.nodes)),
		TabID:        tab.id,
		NavigationID: tab.nav,
		Viewport:     browser.Viewport{Width: 800, Height: 600, Scale: 1},
		Elements:     els,
		Text:         "scripted fixture page",
	}
	if maxEls := opts.EffectiveMaxElements(); len(els) > maxEls {
		snap.Elements = els[:maxEls]
		snap.Truncated = true
	}
	return snap, nil
}

// Act 派发一次动作：fill 记下字段值，click 真的向 fixture 服务端提交表单。
func (b *ScriptedBackend) Act(ctx context.Context, id browser.TabID, action browser.Action) (browser.ActionResult, error) {
	b.mu.Lock()
	b.dispatched++
	seq := b.dispatched
	tab, ok := b.tabs[id]
	latency := time.Duration(0)
	if b.Latency != nil {
		latency = b.Latency(action.Kind, action.Ref)
	}
	b.mu.Unlock()

	start := time.Now()
	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			b.record(seq, id, action, start, "canceled", ctx.Err())
			return browser.ActionResult{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		b.record(seq, id, action, start, "canceled", err)
		return browser.ActionResult{}, err
	}

	b.mu.Lock()
	tab, ok = b.tabs[id]
	if !ok {
		b.mu.Unlock()
		err := fmt.Errorf("scripted: 未知 tab %s", id)
		b.record(seq, id, action, start, "error", err)
		return browser.ActionResult{}, err
	}
	node, found := tab.node(action.Ref)
	if !found {
		b.mu.Unlock()
		err := contracts.NewAppError(contracts.CodeStaleSnapshot,
			"ref "+action.Ref+" 不在当前文档里：必须先 observe", false)
		b.record(seq, id, action, start, "stale", err)
		return browser.ActionResult{}, err
	}
	values := map[string]string{}
	for k, v := range tab.values {
		values[k] = v
	}
	res := browser.DeliveredResult(tab.nav)
	b.mu.Unlock()

	switch {
	case action.Kind == browser.ActionFill && node.field != "":
		b.mu.Lock()
		tab, ok = b.tabs[id]
		if ok {
			tab.values[node.field] = action.Text
		}
		b.mu.Unlock()
		b.record(seq, id, action, start, "delivered", nil)
		return res, nil
	case action.Kind == browser.ActionClick && node.submit:
		// 真的提交一次：服务端 counter 就是这个动作的副作用 oracle。
		if err := b.submit(ctx, id, values); err != nil {
			b.record(seq, id, action, start, "error", err)
			return browser.ActionResult{}, err
		}
		b.record(seq, id, action, start, "delivered", nil)
		return res, nil
	default:
		b.record(seq, id, action, start, "delivered", nil)
		return res, nil
	}
}

// Screenshot 未实现：替身不产生页面像素，所以明确报未实现而不是返回空白图。
func (b *ScriptedBackend) Screenshot(context.Context, browser.TabID) (browser.Image, error) {
	return browser.Image{}, fmt.Errorf("scripted backend 没有页面像素：截图由真实原生宿主提供")
}

// CloseTab 释放 tab 实例。
func (b *ScriptedBackend) CloseTab(ctx context.Context, id browser.TabID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.tabs[id]; !ok {
		return fmt.Errorf("scripted: 未知 tab %s", id)
	}
	delete(b.tabs, id)
	if b.instances > 0 {
		b.instances--
	}
	return nil
}

// URL 返回 tab 当前 URL（供用例核对工具输出与"面板"一致）。
func (b *ScriptedBackend) URL(id browser.TabID) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if tab, ok := b.tabs[id]; ok {
		return tab.url
	}
	return ""
}

// Field 返回脚本化表单里某个字段的当前值。
func (b *ScriptedBackend) Field(id browser.TabID, field string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if tab, ok := b.tabs[id]; ok {
		return tab.values[field]
	}
	return ""
}

// RefOf 返回当前文档里某个脚本化节点的 ref（按 field 或 "submit" 找）。
func (b *ScriptedBackend) RefOf(id browser.TabID, field string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[id]
	if !ok {
		return ""
	}
	for _, n := range tab.nodes {
		if n.field == field && field != "" {
			return n.ref
		}
		if field == "submit" && n.submit {
			return n.ref
		}
	}
	return ""
}

func (b *ScriptedBackend) record(seq int, id browser.TabID, action browser.Action, start time.Time, outcome string, err error) {
	rec := ActRecord{
		Seq: seq, TabID: string(id), Kind: string(action.Kind), Ref: action.Ref,
		DispatchedAt: start.UTC().Format(time.RFC3339Nano),
		DurationMS:   float64(time.Since(start).Microseconds()) / 1000,
		Outcome:      outcome,
	}
	if err != nil {
		rec.Error = err.Error()
	}
	b.mu.Lock()
	b.actLog = append(b.actLog, rec)
	b.mu.Unlock()
}

// submit 向 fixture primary origin 的表单端点提交一次（真实 HTTP）。
func (b *ScriptedBackend) submit(ctx context.Context, id browser.TabID, values map[string]string) error {
	b.mu.Lock()
	tab, ok := b.tabs[id]
	base := ""
	if ok {
		base = tab.url
		b.mu.Unlock()
	} else {
		b.mu.Unlock()
		return fmt.Errorf("scripted: 未知 tab %s", id)
	}
	if i := strings.Index(base, "/form"); i > 0 {
		base = base[:i]
	}
	payload, _ := json.Marshal(map[string]any{"name": values["name"], "plan": "pro", "confirm": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/__form/submit", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fixture 服务端拒绝了提交：HTTP %d", resp.StatusCode)
	}
	return nil
}

// buildNodes 为当前文档分配节点与 uid（同一文档内稳定）。
func (t *scriptedTab) buildNodes() {
	if t.nodes != nil {
		return
	}
	t.nodes = make([]scriptedNode, 0, 2)
	for _, spec := range []struct {
		role, name, tag, inputType, field string
		submit                            bool
	}{
		{role: "textbox", name: "名字", tag: "input", inputType: "text", field: "name"},
		{role: "button", name: "提交", tag: "button", submit: true},
	} {
		t.nextUID++
		t.nodes = append(t.nodes, scriptedNode{
			ref:       fmt.Sprintf("%s-e%d", t.nav, t.nextUID),
			role:      spec.role,
			name:      spec.name,
			tag:       spec.tag,
			inputType: spec.inputType,
			field:     spec.field,
			submit:    spec.submit,
		})
	}
}

func (t *scriptedTab) node(ref string) (scriptedNode, bool) {
	for _, n := range t.nodes {
		if n.ref == ref {
			return n, true
		}
	}
	return scriptedNode{}, false
}
