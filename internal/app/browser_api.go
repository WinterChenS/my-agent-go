package app

// B07：右栏原生 slot、遮挡协调与接管的**应用层**。
//
// 分层（这一层为什么存在）：
//
//	React（frontend/src/features/browser）
//	  │  只上报事件与几何：UpdateBrowserSlot(SlotGeometry)、SelectBrowserTab、
//	  │  TakeOver/Resume、SetBrowserOverlay。**拿不到任何原生句柄**。
//	  ▼
//	BrowserBridge（本文件）
//	  │  几何 revision/去重、工具条保留区、遮挡集合 → 可见性、地址历史、
//	  │  面板状态 DTO（可 JSON、无句柄）。
//	  ▼
//	browser.BrowserSurface / browser.Service（B00–B06）
//	  原生视图位置与控制权；句柄只活在这一层以下。
//
// 四条不变量（每条都有 browser_api_test.go 的用例）：
//
//  1. 旧 revision 迟到一律丢弃；相同几何不重复 Present（拖动分栏不抖）。
//  2. slot.Top 不得进入工具条保留区：停止按钮永远在原生网页区域之外。
//     违反时**拒绝这次几何**（不是夹一下就算了），宁可不显示也不覆盖停止按钮。
//  3. 遮挡（设置 dialog / 命令面板 / 审批框）只调 surface.SetVisible：
//     不接管、不关 tab、不改控制代数。**隐藏面板 ≠ 暂停 agent**：
//     agent 的写动作在面板隐藏期间照常派发（用例真的派发了一次 fill）。
//  4. BrowserState 是可序列化的面板事实，绝不含 hwnd/handle。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

const (
	// BrowserSlotID 是 React 上报的槽位 ID（与 ContextPanel 的浏览器分区一致）。
	BrowserSlotID = "browser-slot"
	// BrowserChromeReservedTop 是工具条（地址栏 + tab 条 + 停止按钮）占用的
	// 顶部高度，单位是应用 WebView 内的 CSS px。
	//
	// 前端从 BrowserState.chromeReservedTop 读同一个值来计算 slot.top，两侧不会各写一份。
	BrowserChromeReservedTop = 96.0
	// BrowserMaxHistoryEntries 是每个 tab 的地址栏历史保留条数。
	BrowserMaxHistoryEntries = 32
	// browserOverlayLogLimit 是遮挡转换日志的保留条数（给证据/诊断用，不进 UI）。
	browserOverlayLogLimit = 32
	// browserActionTimeout 是绑定层转发的控制类动作的上限。
	browserActionTimeout = 20 * time.Second
)

// BrowserTabView 是面板里的一个 tab。字段与 browser.TabInfo 对齐，面板与
// 工具输出（MCP）看到的是同一组事实。
type BrowserTabView struct {
	TabID             string `json:"tabId"`
	URL               string `json:"url"`
	Active            bool   `json:"active"`
	NavigationID      string `json:"navigationId"`
	ControlGeneration uint64 `json:"controlGeneration"`
	Mode              string `json:"mode"`
	Queued            int    `json:"queued"`
	InFlight          int    `json:"inFlight"`
}

// BrowserControlView 是当前 tab 的控制状态（面板只按 mode 分支）。
type BrowserControlView struct {
	TabID        string `json:"tabId"`
	Mode         string `json:"mode"`
	Generation   uint64 `json:"generation"`
	NavigationID string `json:"navigationId"`
	Queued       int    `json:"queued"`
	InFlight     int    `json:"inFlight"`
	// Detail 是给人看的具体原因（例如"还有 1 个已派发动作未静止"）。
	Detail string `json:"detail,omitempty"`
	// CanTakeOver / CanResume 只是**按钮可用性**，不是授权：真正的判定仍在
	// browser.Service（Resume 还要求新快照）。
	CanTakeOver bool `json:"canTakeOver"`
	CanResume   bool `json:"canResume"`
}

// BrowserCounters 是绑定层自己的计数，用于证明"没有重建/没有重摆"。
//
// 原生侧的挂载计数（子窗口句柄、controller）由 tests/native 的 UI04.native
// 从真实进程里读，不在这里冒充。
type BrowserCounters struct {
	SlotPresented      int `json:"slotPresented"`
	SlotDropped        int `json:"slotDropped"`
	SlotDeduped        int `json:"slotDeduped"`
	VisibilityChanges  int `json:"visibilityChanges"`
	OverlayTransitions int `json:"overlayTransitions"`
	TabSelectDeduped   int `json:"tabSelectDeduped"`
	TabsClosed         int `json:"tabsClosed"`
}

// BrowserOcclusionStep 是一次遮挡转换（诊断/证据用）。
type BrowserOcclusionStep struct {
	Seq     int    `json:"seq"`
	Source  string `json:"source"`
	Open    bool   `json:"open"`
	Visible bool   `json:"visible"`
	At      string `json:"at"`
}

// BrowserStateResult 是右栏面板需要的全部事实。
//
// 刻意**不含**原生句柄、surface 实例或任何指针：它会被序列化进前端，
// 而前端只该拿到事件与状态（用例断言序列化结果里没有 hwnd/handle）。
type BrowserStateResult struct {
	// Available 表示挂了原生宿主；false 时面板显示不可用原因而不是空白。
	Available bool `json:"available"`
	// Slot / HasSlot 是最近一次被接受的几何。
	Slot    browser.SlotGeometry `json:"slot"`
	HasSlot bool                 `json:"hasSlot"`
	// ChromeReservedTop 是工具条保留区高度（前端据此算 slot.top）。
	ChromeReservedTop float64            `json:"chromeReservedTop"`
	ActiveTab         string             `json:"activeTab"`
	Tabs              []BrowserTabView   `json:"tabs"`
	Control           BrowserControlView `json:"control"`
	// Visible 是原生网页**实际**的可见性（= 面板可见 且 没有浮层）。
	Visible bool `json:"visible"`
	// RequestedVisible 是面板自己想要的可见性（不受浮层影响）。
	RequestedVisible bool     `json:"requestedVisible"`
	Overlays         []string `json:"overlays"`
	// History 是当前 tab 的地址栏历史（应用层，不是引擎 BFCache）。
	History      []string                        `json:"history"`
	Capabilities browser.CapabilitySet           `json:"capabilities"`
	Unsupported  []browser.UnsupportedCapability `json:"unsupported"`
	Counters     BrowserCounters                 `json:"counters"`
	Occlusion    []BrowserOcclusionStep          `json:"occlusion"`
	Error        *CallError                      `json:"error,omitempty"`
}

// BrowserBridgeOptions 构造桥接层。
//
// Surface/Service 都可以为 nil：那时面板如实报告不可用（Unsupported 带具体原因），
// 而不是假装成功。生产环境由启动装配注入真实宿主；测试用替身。
type BrowserBridgeOptions struct {
	// Surface 是原生视图的位置/可见性（B01–B03）。
	Surface browser.BrowserSurface
	// Service 是控制权/tab/队列（B05）。
	Service *browser.Service
	// Exec 是服务端绑定的执行身份（面板不允许自报）。
	//
	// ExecProvider 不为 nil 时它只作为退避值；面板身份按 ExecProvider 实时取。
	Exec contracts.ExecutionContext
	// ExecProvider 让面板跟随**当前 run** 的执行身份。
	//
	// 为什么不能只用构造时的固定 Exec：tab 的归属是**会话**
	//（browser.Service.Tabs 按 exec.SessionID 过滤），而产品只有一个 Desktop。
	// 身份固定在启动那一刻，agent 后续在它自己的会话里开的 tab 就永远不会出现在
	// 面板上（面板看起来“没 tab”，实际有人开着）。所以身份要在**每次调用**时取。
	//
	// 它必须是快的、无阻塞的，且不能反过来调本桥接层的方法（会自锁）。
	ExecProvider func() contracts.ExecutionContext
	// ChromeReservedTop 覆盖工具条保留区高度（<=0 用 BrowserChromeReservedTop）。
	ChromeReservedTop float64
	// Now 便于测试注入时钟（遮挡日志的时间戳）。
	Now func() time.Time
}

// BrowserBridge 是右栏面板的状态与动作。
//
// 并发：一把锁保护全部字段。持锁期间会调用 surface，因为 surface 的调用本身
// 就是"这一帧的结论"，放出去再做会让两次几何上报互相穿插（实测拖分栏时更难查）；
// surface 的调用都以 ctx 设限，不会无限阻塞。
type BrowserBridge struct {
	mu sync.Mutex

	surface browser.BrowserSurface
	svc     *browser.Service
	exec    contracts.ExecutionContext
	// execProvider 让面板跟随当前 run（见 BrowserBridgeOptions.ExecProvider）。
	// 构造后不再变，读它不需要锁。
	execProvider func() contracts.ExecutionContext
	now          func() time.Time

	chromeReservedTop float64

	slot             browser.SlotGeometry
	hasSlot          bool
	acceptedRevision uint64
	activeTab        browser.TabID

	overlays map[string]bool
	// appliedVisible 是 surface 上**已确认**的可见性；nil 表示还没有确立过。
	appliedVisible *bool

	history map[browser.TabID][]string

	counters BrowserCounters
	occlog   []BrowserOcclusionStep
	lastErr  *CallError
}

// NewBrowserBridge 构造桥接层。
func NewBrowserBridge(opts BrowserBridgeOptions) *BrowserBridge {
	b := &BrowserBridge{
		surface:           opts.Surface,
		svc:               opts.Service,
		exec:              opts.Exec,
		execProvider:      opts.ExecProvider,
		now:               opts.Now,
		chromeReservedTop: opts.ChromeReservedTop,
		overlays:          map[string]bool{},
		history:           map[browser.TabID][]string{},
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.chromeReservedTop <= 0 {
		b.chromeReservedTop = BrowserChromeReservedTop
	}
	return b
}

// ---------- 几何 ----------

// UpdateSlot 接受 React 上报的槽位几何。
//
// 结局有三种，只有第一种真的会动原生视图：
//   - 接受：revision 前进且几何/可见性变了 → Present（必要时 SetVisible）；
//   - 去重：几何完全相同 → 只前进 revision，不重摆；
//   - 丢弃：revision 比已接受的旧 → 计数后忽略。
//
// 几何落进工具条保留区是**错误**（返回 UNSUPPORTED 且不 Present）：
// 停止按钮必须永远位于原生网页区域之外。
func (b *BrowserBridge) UpdateSlot(slot browser.SlotGeometry) error {
	if err := slot.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(slot.SlotID) == "" {
		slot.SlotID = BrowserSlotID
	}
	if slot.Zoom <= 0 {
		slot.Zoom = 1
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if slot.Top < b.chromeReservedTop {
		err := contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"槽位 top=%.1f 落进工具条保留区（%.1f）：拒绝这次几何，停止按钮不能被原生网页覆盖",
			slot.Top, b.chromeReservedTop), false)
		b.lastErr = toCallError(err)
		return err
	}
	if b.hasSlot && b.acceptedRevision != 0 && slot.Revision != 0 && slot.Revision < b.acceptedRevision {
		// 迟到的旧 revision：不把视图拽回旧位置。
		b.counters.SlotDropped++
		return nil
	}

	needPresent := !b.hasSlot || !sameSlotGeometry(b.slot, slot)
	b.slot = slot
	b.hasSlot = true
	if slot.Revision > b.acceptedRevision {
		b.acceptedRevision = slot.Revision
	}
	if needPresent {
		if err := b.presentLocked(b.activeTab); err != nil {
			return err
		}
	} else {
		b.counters.SlotDeduped++
	}
	return b.applyVisibilityLocked()
}

// sameSlotGeometry 只比较"要不要重摆"需要的字段。
//
// Revision 刻意不参与：拖动分栏时每帧 revision 都会变，但位置没变就不该重摆。
func sameSlotGeometry(a, b browser.SlotGeometry) bool {
	return a.SlotID == b.SlotID &&
		a.Left == b.Left && a.Top == b.Top &&
		a.Width == b.Width && a.Height == b.Height &&
		a.Zoom == b.Zoom && a.Visible == b.Visible
}

// presentLocked 把当前几何交给 surface；调用方必须持锁。
func (b *BrowserBridge) presentLocked(tab browser.TabID) error {
	if b.surface == nil {
		// 没有原生宿主：几何仍然记下来（面板位置是真的），但没有东西可摆。
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	if err := b.surface.Present(ctx, tab, b.slot); err != nil {
		b.lastErr = toCallError(err)
		return err
	}
	b.counters.SlotPresented++
	return nil
}

// effectiveVisibleLocked 是原生网页**应当**的可见性。
//
// 隐藏的三个来源都只是"看不见"：面板自己关了、有浮层、还没有几何。
// 任何一条都不改变控制权（隐藏面板 ≠ 暂停 agent）。
func (b *BrowserBridge) effectiveVisibleLocked() bool {
	if !b.hasSlot || !b.slot.Visible {
		return false
	}
	return len(b.overlays) == 0
}

// applyVisibilityLocked 在可见性真的变化时才调 surface；调用方必须持锁。
func (b *BrowserBridge) applyVisibilityLocked() error {
	if b.surface == nil || !b.hasSlot {
		return nil
	}
	want := b.effectiveVisibleLocked()
	if b.appliedVisible != nil && *b.appliedVisible == want {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	if err := b.surface.SetVisible(ctx, want); err != nil {
		b.lastErr = toCallError(err)
		return err
	}
	applied := want
	b.appliedVisible = &applied
	b.counters.VisibilityChanges++
	return nil
}

// ---------- 遮挡 ----------

// SetOverlay 报告一个浮层的开关（设置 dialog / 命令面板 / 审批框…）。
//
// 这是**唯一**的遮挡入口：任何 app overlay 都必须经过它，否则原生网页会盖住浮层。
// source 必须有名字：空名字会让"哪一层还开着"无从判断，最后退化成"一律隐藏"。
func (b *BrowserBridge) SetOverlay(source string, open bool) error {
	name := strings.TrimSpace(source)
	if name == "" {
		err := contracts.NewAppError(contracts.CodeUnsupported,
			"遮挡来源必须有名字（例如 settings-dialog/command-palette/approval-dialog）", false)
		b.mu.Lock()
		b.lastErr = toCallError(err)
		b.mu.Unlock()
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.counters.OverlayTransitions++
	if open {
		b.overlays[name] = true
	} else {
		delete(b.overlays, name)
	}
	if err := b.applyVisibilityLocked(); err != nil {
		return err
	}
	step := BrowserOcclusionStep{
		Seq:     len(b.occlog) + 1,
		Source:  name,
		Open:    open,
		Visible: b.effectiveVisibleLocked(),
		At:      b.now().UTC().Format(time.RFC3339Nano),
	}
	b.occlog = append(b.occlog, step)
	if len(b.occlog) > browserOverlayLogLimit {
		b.occlog = b.occlog[len(b.occlog)-browserOverlayLogLimit:]
	}
	return nil
}

// ---------- tab ----------

// SelectTab 选中一个 tab（不创建、不关闭任何原生实例）。
//
// 同一个 tab 重复选中不会重摆视图；换 tab 会用**当前几何**重摆，
// 不改 revision，因此不会触发"slot 被重建"。
func (b *BrowserBridge) SelectTab(tabID string) error {
	id := browser.TabID(strings.TrimSpace(tabID))
	if id == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "选择 tab 需要 tabId", false)
	}
	// 身份在持锁**之前**取：ExecProvider 会拿宿主侧的锁（Desktop），
	// 而持有本锁再取它会把锁序复杂化。
	exec := b.execContext()

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.svc == nil {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"没有浏览器服务：当前无法选择/创建 tab", false)
	}
	info, err := b.svc.TabInfo(exec, id)
	if err != nil {
		b.lastErr = toCallError(err)
		return err
	}
	if b.activeTab == id {
		b.counters.TabSelectDeduped++
		return nil
	}
	b.activeTab = id
	b.seedHistoryLocked(id, info.URL)
	if b.hasSlot {
		return b.presentLocked(id)
	}
	return nil
}

// CloseTab 关闭一个 tab（走服务，队列会先撤销）。
func (b *BrowserBridge) CloseTab(ctx context.Context, tabID string) error {
	id := browser.TabID(strings.TrimSpace(tabID))
	if id == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "关闭 tab 需要 tabId", false)
	}

	b.mu.Lock()
	svc := b.svc
	b.mu.Unlock()
	if svc == nil {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"没有浏览器服务：当前无法关闭 tab", false)
	}
	if err := svc.CloseTab(ctx, b.execContext(), id); err != nil {
		b.mu.Lock()
		b.lastErr = toCallError(err)
		b.mu.Unlock()
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.history, id)
	b.counters.TabsClosed++
	if b.activeTab == id {
		b.activeTab = ""
	}
	return nil
}

// seedHistoryLocked 在第一次选中某个 tab 时把它的当前 URL 记为历史起点。
func (b *BrowserBridge) seedHistoryLocked(tab browser.TabID, url string) {
	if _, ok := b.history[tab]; ok || strings.TrimSpace(url) == "" {
		return
	}
	b.history[tab] = []string{url}
}

// ---------- 地址栏 ----------

// Navigate 在指定 tab 里导航，并把这条 URL 记进地址栏历史。
func (b *BrowserBridge) Navigate(ctx context.Context, tabID, url string) error {
	id := browser.TabID(strings.TrimSpace(tabID))
	if id == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "导航需要 tabId", false)
	}
	return b.navigate(ctx, id, url, true)
}

func (b *BrowserBridge) navigate(ctx context.Context, tab browser.TabID, url string, record bool) error {
	b.mu.Lock()
	svc := b.svc
	b.mu.Unlock()
	if svc == nil {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"没有浏览器服务：当前无法导航", false)
	}
	if err := svc.Navigate(ctx, b.execContext(), tab, url); err != nil {
		b.mu.Lock()
		b.lastErr = toCallError(err)
		b.mu.Unlock()
		return err
	}
	if record {
		b.mu.Lock()
		b.seedHistoryLocked(tab, url)
		h := b.history[tab]
		// 当前 URL 已经是历史末尾时不重复追加。
		if len(h) == 0 || h[len(h)-1] != url {
			h = append(h, url)
		}
		if len(h) > BrowserMaxHistoryEntries {
			h = h[len(h)-BrowserMaxHistoryEntries:]
		}
		b.history[tab] = h
		b.mu.Unlock()
	}
	return nil
}

// Back 回到地址栏历史里的上一条。
//
// 这是**应用层地址历史**（用户在这个面板里导航过的 URL），不是引擎的 BFCache：
// 页面内的 SPA 历史不参与，B04 的契约里也没有 history API。因此这里的回退是
// 一次真实导航，文档身份会变（旧 ref 照常失效）。
func (b *BrowserBridge) Back(ctx context.Context, tabID string) error {
	id := browser.TabID(strings.TrimSpace(tabID))
	if id == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "回退需要 tabId", false)
	}

	b.mu.Lock()
	h := b.history[id]
	if len(h) < 2 {
		b.mu.Unlock()
		return contracts.NewAppError(contracts.CodeUnsupported,
			"地址历史里没有可回退的上一条（页面内的 SPA 历史不参与回退）", false)
	}
	prev := h[len(h)-2]
	b.history[id] = h[:len(h)-1]
	b.mu.Unlock()

	return b.navigate(ctx, id, prev, false)
}

// Reload 重新加载当前 URL。
//
// 走 Navigate 而不是引擎级 reload：B00 的 BrowserBackend 契约里没有 reload，
// 而 native 的 vtable 封装（edgewrap_windows.go）改动必须同时更新 ADR（不在 B07
// 的改动范围内）。因此这里如实标注：**整页重新导航**，会换文档身份。
func (b *BrowserBridge) Reload(ctx context.Context, tabID string) error {
	id := browser.TabID(strings.TrimSpace(tabID))
	if id == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "刷新需要 tabId", false)
	}
	b.mu.Lock()
	svc := b.svc
	b.mu.Unlock()
	if svc == nil {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"没有浏览器服务：当前无法刷新", false)
	}
	info, err := svc.TabInfo(b.execContext(), id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(info.URL) == "" {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"服务端还没有记录该 tab 的 URL：不能凭空刷新", false)
	}
	return b.navigate(ctx, id, info.URL, false)
}

// ---------- 接管 / 恢复 ----------

// TakeOver 把控制权交给人工（封装 browser.Service.TakeOver）。
func (b *BrowserBridge) TakeOver(ctx context.Context, tabID string) (browser.ControlState, error) {
	b.mu.Lock()
	svc := b.svc
	b.mu.Unlock()
	if svc == nil {
		return browser.ControlState{}, contracts.NewAppError(contracts.CodeUnsupported,
			"没有浏览器服务：当前无法接管", false)
	}
	return svc.TakeOver(ctx, b.execContext(), browser.TabID(strings.TrimSpace(tabID)))
}

// Resume 把控制权交还 agent（封装 browser.Service.Resume）。
//
// 前置条件（新快照/静止）由服务判定，这里不重写一遍：否则两处口径迟早分叉。
func (b *BrowserBridge) Resume(ctx context.Context, tabID string) (browser.ControlState, error) {
	b.mu.Lock()
	svc := b.svc
	b.mu.Unlock()
	if svc == nil {
		return browser.ControlState{}, contracts.NewAppError(contracts.CodeUnsupported,
			"没有浏览器服务：当前无法恢复", false)
	}
	return svc.Resume(ctx, b.execContext(), browser.TabID(strings.TrimSpace(tabID)))
}

// execContext 返回这次调用应当使用的执行身份。
//
// 面板必须跟随**当前 run**：tab 的归属是会话（browser.Service 按 SessionID 过滤），
// 而产品只有一个 Desktop。没有 ExecProvider 时用构造时的固定 Exec（替身/单会话）。
func (b *BrowserBridge) execContext() contracts.ExecutionContext {
	if b.execProvider == nil {
		return b.exec
	}
	return b.execProvider()
}

// ---------- 状态 ----------

// State 汇总面板需要的全部事实。
//
// 只读：它会做默认 activeTab 的选择（第一个 tab），但不改桥接层状态。
func (b *BrowserBridge) State() BrowserStateResult {
	// 同 SelectTab：身份在持锁之前取。
	exec := b.execContext()

	b.mu.Lock()
	defer b.mu.Unlock()

	out := BrowserStateResult{
		Available:         b.surface != nil,
		Slot:              b.slot,
		HasSlot:           b.hasSlot,
		ChromeReservedTop: b.chromeReservedTop,
		Visible:           b.effectiveVisibleLocked(),
		RequestedVisible:  b.hasSlot && b.slot.Visible,
		Overlays:          b.overlayNamesLocked(),
		Counters:          b.counters,
		Occlusion:         append([]BrowserOcclusionStep(nil), b.occlog...),
		Error:             b.lastErr,
	}

	if b.surface == nil {
		out.Unsupported = append(out.Unsupported, browser.UnsupportedCapability{
			Capability: "surface",
			Reason:     "没有挂载原生宿主：右侧不显示网页（不是加载失败，也不是空页面）",
		})
	}
	if b.svc == nil {
		out.Unsupported = append(out.Unsupported, browser.UnsupportedCapability{
			Capability: "control",
			Reason:     "没有浏览器服务：tab/控制权/接管暂不可用",
		})
		out.Control = BrowserControlView{Mode: string(browser.ControlAgent)}
		return out
	}

	out.Capabilities = b.svc.Capabilities()
	out.Unsupported = append(out.Unsupported, out.Capabilities.Unsupported...)
	ids := b.svc.Tabs(exec)
	active := b.activeTab
	if active == "" && len(ids) > 0 {
		active = ids[0]
	}
	for _, id := range ids {
		info, err := b.svc.TabInfo(exec, id)
		if err != nil {
			continue
		}
		view := BrowserTabView{
			TabID:             string(info.TabID),
			URL:               info.URL,
			Active:            info.TabID == active,
			NavigationID:      info.NavigationID,
			ControlGeneration: info.ControlGeneration,
			Mode:              string(info.Mode),
			Queued:            info.Queued,
			InFlight:          info.InFlight,
		}
		out.Tabs = append(out.Tabs, view)
		if info.TabID == active {
			out.Control = BrowserControlView{
				TabID:        string(info.TabID),
				Mode:         string(info.Mode),
				Generation:   info.ControlGeneration,
				NavigationID: info.NavigationID,
				Queued:       info.Queued,
				InFlight:     info.InFlight,
				CanTakeOver:  info.Mode != browser.ControlHuman,
				CanResume:    info.Mode == browser.ControlHuman || info.Mode == browser.ControlPausing,
			}
		}
	}
	out.ActiveTab = string(active)
	out.History = append([]string(nil), b.history[active]...)
	return out
}

func (b *BrowserBridge) overlayNamesLocked() []string {
	names := make([]string, 0, len(b.overlays))
	for name := range b.overlays {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---------- Desktop 绑定 ----------

// browserBridges 把桥接层挂在 Desktop 上。
//
// 为什么不加字段：Desktop 的结构体字段在 desktop.go（F05/C01 的所有权），
// 不在 B07 的改动范围内。Desktop 是进程级单例，生产环境这里只有一条记录；
// 测试里每个 Desktop 各一份，互不干扰。
var browserBridges sync.Map // map[*Desktop]*BrowserBridge

// SetBrowserBridge 注入桥接层（启动装配用它接上真实宿主与服务）。
func (d *Desktop) SetBrowserBridge(b *BrowserBridge) {
	if b == nil {
		browserBridges.Delete(d)
		return
	}
	browserBridges.Store(d, b)
}

// browserBridge 返回本 Desktop 的桥接层，没有就建一个"如实不可用"的。
func (d *Desktop) browserBridge() *BrowserBridge {
	if v, ok := browserBridges.Load(d); ok {
		return v.(*BrowserBridge)
	}
	b := NewBrowserBridge(BrowserBridgeOptions{})
	actual, _ := browserBridges.LoadOrStore(d, b)
	return actual.(*BrowserBridge)
}

// BrowserExecutionContext 返回右侧浏览器面板此刻应当使用的执行身份。
//
// 为什么是"此刻"：tab 的归属是**会话**（browser.Service.Tabs 按 exec.SessionID 过滤），
// 而产品只有一个 Desktop、一个浏览器服务。启动时把身份焊死，agent 后续在它自己的
// 会话里开的 tab 就永远不会出现在面板上——面板看起来"没 tab"，实际有人开着。
//
// 会话取 `runtimeSessionID`（最近一次构建的 run 会话，与 RunService.Start 拿到的
// 是同一个值）。还没跑过任何一轮时它是空的，面板就如实显示"没有 tab"：那是真的，
// 不是读不到。
//
// 不传 ConfigRevision/ControlGeneration：本版没有任何东西按它们授权面板动作；
// 写动作的授权点在 browser.Authorizer（BR07.tool 接线时在那里取真值）。
func (d *Desktop) BrowserExecutionContext() contracts.ExecutionContext {
	d.mu.Lock()
	defer d.mu.Unlock()
	return contracts.ExecutionContext{
		WorkspaceID: defaultWorkspaceID,
		SessionID:   contracts.SessionID(d.runtimeSessionID),
	}
}

// BrowserState 是面板的读入口。永远返回结构化结果，不抛异常。
func (d *Desktop) BrowserState() BrowserStateResult {
	return d.browserBridge().State()
}

// UpdateBrowserSlot 接受 React 上报的几何。
func (d *Desktop) UpdateBrowserSlot(slot browser.SlotGeometry) *CallError {
	if err := d.browserBridge().UpdateSlot(slot); err != nil {
		return toCallError(err)
	}
	return nil
}

// SetBrowserOverlay 报告一个 app 浮层的开关（遮挡协调器唯一入口）。
func (d *Desktop) SetBrowserOverlay(source string, open bool) *CallError {
	if err := d.browserBridge().SetOverlay(source, open); err != nil {
		return toCallError(err)
	}
	return nil
}

// SelectBrowserTab 选中一个 tab。
func (d *Desktop) SelectBrowserTab(tabID string) *CallError {
	if err := d.browserBridge().SelectTab(tabID); err != nil {
		return toCallError(err)
	}
	return nil
}

// CloseBrowserTab 关闭一个 tab。
func (d *Desktop) CloseBrowserTab(tabID string) *CallError {
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	if err := d.browserBridge().CloseTab(ctx, tabID); err != nil {
		return toCallError(err)
	}
	return nil
}

// NavigateBrowserTab 是地址栏的导航入口。
func (d *Desktop) NavigateBrowserTab(tabID, url string) *CallError {
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	if err := d.browserBridge().Navigate(ctx, tabID, url); err != nil {
		return toCallError(err)
	}
	return nil
}

// BackBrowserTab 是地址栏的回退入口（应用层地址历史）。
func (d *Desktop) BackBrowserTab(tabID string) *CallError {
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	if err := d.browserBridge().Back(ctx, tabID); err != nil {
		return toCallError(err)
	}
	return nil
}

// ReloadBrowserTab 是地址栏的刷新入口（整页重新导航，见 BrowserBridge.Reload）。
func (d *Desktop) ReloadBrowserTab(tabID string) *CallError {
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	if err := d.browserBridge().Reload(ctx, tabID); err != nil {
		return toCallError(err)
	}
	return nil
}

// BrowserControlResult 是接管/恢复的绑定层结果。
type BrowserControlResult struct {
	TabID   string             `json:"tabId"`
	Control BrowserControlView `json:"control"`
	Error   *CallError         `json:"error,omitempty"`
}

// TakeOverBrowserTab 把控制权交给人工。
func (d *Desktop) TakeOverBrowserTab(tabID string) BrowserControlResult {
	b := d.browserBridge()
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	cs, err := b.TakeOver(ctx, tabID)
	if err != nil {
		return BrowserControlResult{TabID: tabID, Error: toCallError(err)}
	}
	return BrowserControlResult{
		TabID: string(cs.TabID),
		Control: BrowserControlView{
			TabID:      string(cs.TabID),
			Mode:       string(cs.Mode),
			Generation: cs.Generation,
			Queued:     cs.Queued,
			InFlight:   cs.InFlight,
			Detail:     cs.Detail,
			CanResume:  cs.Mode == browser.ControlHuman || cs.Mode == browser.ControlPausing,
		},
	}
}

// ResumeBrowserTab 把控制权交还 agent。
func (d *Desktop) ResumeBrowserTab(tabID string) BrowserControlResult {
	b := d.browserBridge()
	ctx, cancel := context.WithTimeout(context.Background(), browserActionTimeout)
	defer cancel()
	cs, err := b.Resume(ctx, tabID)
	if err != nil {
		return BrowserControlResult{TabID: tabID, Error: toCallError(err)}
	}
	return BrowserControlResult{
		TabID: string(cs.TabID),
		Control: BrowserControlView{
			TabID:        string(cs.TabID),
			Mode:         string(cs.Mode),
			Generation:   cs.Generation,
			NavigationID: cs.NavigationID,
			Queued:       cs.Queued,
			InFlight:     cs.InFlight,
			Detail:       cs.Detail,
		},
	}
}
