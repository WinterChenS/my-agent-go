//go:build windows

package nativetest

// U07 驱动层的第二块：页面审计器、Go API 外部判据、导航、语言/主题切换、
// 原生子窗口与遮挡探测。
//
// 与 uidriver_windows.go 的分工：那边是"进程/窗口/CDP 通道"的地基，这里是
// "页面事实与产品动作"的操作层。拆成两个文件只是因为两边都在长，不是两个概念。

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// ---------- 页面审计器（与 Playwright 预检共用同一份实现） ----------
//
// go:embed 只认包目录内的文件，所以审计器放在测试包里；Playwright 侧读的是
// 同一份源码（frontend/e2e/lib/audit.ts）。两份实现必然分叉，而分叉之后
// "预检绿、门禁红"会变成常态，最后没人再相信检查。

//go:embed uiaudit.js
var uiAuditSource string

// AuditFinding 是一条审计发现。
type AuditFinding struct {
	Rule   string `json:"rule"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

// AuditFacts 是页面自报的事实（语言/主题/token/视口/控件数/状态词）。
type AuditFacts struct {
	Locale        string            `json:"locale"`
	Theme         string            `json:"theme"`
	ThemeTokens   map[string]string `json:"themeTokens"`
	TokenContrast []struct {
		Pair   string   `json:"pair"`
		Ratio  *float64 `json:"ratio"`
		Reason string   `json:"reason"`
	} `json:"tokenContrast"`
	Viewport struct {
		W   float64 `json:"w"`
		H   float64 `json:"h"`
		DPR float64 `json:"dpr"`
	} `json:"viewport"`
	Document struct {
		ScrollWidth  float64 `json:"scrollWidth"`
		ClientWidth  float64 `json:"clientWidth"`
		ScrollHeight float64 `json:"scrollHeight"`
	} `json:"document"`
	Controls int      `json:"controls"`
	States   []string `json:"states"`
	Headings []string `json:"headings"`
}

// AuditResult 是 audit() 的完整返回。
type AuditResult struct {
	Findings []AuditFinding `json:"findings"`
	Facts    AuditFacts     `json:"facts"`
	Summary  struct {
		Total  int            `json:"total"`
		ByRule map[string]int `json:"byRule"`
	} `json:"summary"`
}

// auditOpts 控制审计范围（转成 JS 的 opts）。
type auditOpts struct {
	Contrast bool
	Layout   bool
}

// Audit 在真实应用页面里跑一次审计。
func (a *u07App) Audit(t *testing.T, opts auditOpts) AuditResult {
	t.Helper()
	if _, err := a.cdp.Eval(`(() => { if (!window.__u07audit) { ` + uiAuditSource + ` } return true; })()`); err != nil {
		t.Fatalf("注入审计器失败: %v", err)
	}
	jsOpts := fmt.Sprintf(`{contrast:%t, layout:%t}`, opts.Contrast, opts.Layout)
	var out AuditResult
	if err := a.cdp.EvalJSON(`JSON.stringify(window.__u07audit.audit(`+jsOpts+`))`, &out); err != nil {
		t.Fatalf("运行页面审计失败: %v", err)
	}
	return out
}

// TabFocusStep 是一次键盘 Tab 之后的焦点证据。
type TabFocusStep struct {
	Target       string `json:"target"`
	Tag          string `json:"tag"`
	FocusVisible bool   `json:"focusVisible"`
	Outline      string `json:"outline"`
	BoxShadow    string `json:"boxShadow"`
	RingVisible  bool   `json:"ringVisible"`
}

// TabStep 按一次 Tab 键并读回焦点证据（真实键盘事件，走 CDP 输入通道）。
func (a *u07App) TabStep(t *testing.T) *TabFocusStep {
	t.Helper()
	down := `{"type":"keyDown","windowsVirtualKeyCode":9,"key":"Tab","code":"Tab"}`
	if _, err := a.cdp.Call("Input.dispatchKeyEvent", down, 10*time.Second); err != nil {
		t.Fatalf("发送 Tab 失败: %v", err)
	}
	up := `{"type":"keyUp","windowsVirtualKeyCode":9,"key":"Tab","code":"Tab"}`
	if _, err := a.cdp.Call("Input.dispatchKeyEvent", up, 10*time.Second); err != nil {
		t.Fatalf("发送 Tab(keyUp) 失败: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	raw, err := a.cdp.EvalString(`(()=>JSON.stringify(window.__u07audit.tabFocusStep()))()`)
	if err != nil {
		t.Fatalf("读取焦点证据失败: %v", err)
	}
	if raw == "null" {
		return nil
	}
	var step TabFocusStep
	if err := json.Unmarshal([]byte(raw), &step); err != nil {
		t.Fatalf("解析焦点证据失败: %v (%s)", err, raw)
	}
	return &step
}

// ---------- Go API（外部判据：值来自应用进程，不是页面文本） ----------

// APICall 通过应用自己的 Wails 传输层调用一个绑定方法。
//
// 为什么这算外部判据：绑定请求是页面发往 /wails/runtime 的 HTTP POST，返回值
// 由 **Go 进程**生成。这里在页面里发同一条请求、把响应体读回测试进程，于是
// 能比对"界面显示的值 vs Go 给出的值"，而不是拿页面自己证明页面。
func (a *u07App) APICall(t *testing.T, method string, args ...string) (json.RawMessage, error) {
	t.Helper()
	argsJSON := "[]"
	if len(args) > 0 {
		argsJSON = "[" + strings.Join(args, ",") + "]"
	}
	expr := fmt.Sprintf(`(async () => {
		const res = await fetch('/wails/runtime', {
			method: 'POST',
			headers: {'Content-Type': 'application/json', 'x-wails-client-id': 'u07-evidence'},
			body: JSON.stringify({object: 0, method: 0, args: {'call-id': 'u07-' + Math.random().toString(36).slice(2), methodName: %q, args: %s}})
		});
		const text = await res.text();
		return JSON.stringify({status: res.status, body: text});
	})()`, "my-agent-go/internal/app.Desktop."+method, argsJSON)

	raw, err := a.cdp.EvalString(expr)
	if err != nil {
		return nil, err
	}
	var res struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return json.RawMessage(res.Body), fmt.Errorf("%s 返回 %d: %s", method, res.Status, tail(res.Body, 300))
	}
	return json.RawMessage(res.Body), nil
}

// MustAPI 调用并解码；失败时用 t.Errorf 记录（调用方继续跑，收集更多证据）。
func (a *u07App) MustAPI(t *testing.T, out any, method string, args ...string) {
	t.Helper()
	raw, err := a.APICall(t, method, args...)
	if err != nil {
		t.Errorf("调用 %s 失败: %v\n响应: %s", method, err, head(string(raw), 300))
		return
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Errorf("解析 %s 的返回失败: %v（%s）", method, err, head(string(raw), 300))
	}
}

// PreferencesResult 是 GetPreferences 的返回（只取证据用到的字段）。
type PreferencesResult struct {
	Preferences struct {
		Locale string `json:"locale"`
		Theme  string `json:"theme"`
	} `json:"preferences"`
	Revision int `json:"revision"`
}

// ---------- 导航（九页） ----------

// navPage 是侧栏里的一个入口：索引 + 该入口的文案 key。
//
// 点击用**索引**而不是文案：同一次运行里会切换语言，索引稳定、文案不稳定。
// 文案 key 用来把点击结果与 catalog 对齐（证明"当前页高亮 + <main aria-label>"
// 都对）。
type navPage struct {
	ID       string
	Index    int
	LabelKey string
}

// navPages 与 navigation.ts 的 navGroups 顺序一致：
// primary: workbench, chat, agents；tools: library, memory, skills, mcp,
// connectors, schedules, settings, observability。
// U07 的九页跳过 memory(4) 与 observability(10)。
var navPages = []navPage{
	{ID: "workbench", Index: 0, LabelKey: "nav.workbench"},
	{ID: "chat", Index: 1, LabelKey: "nav.chat"},
	{ID: "agents", Index: 2, LabelKey: "nav.agents"},
	{ID: "library", Index: 3, LabelKey: "nav.library"},
	{ID: "skills", Index: 5, LabelKey: "nav.skills"},
	{ID: "mcp", Index: 6, LabelKey: "nav.mcp"},
	{ID: "connectors", Index: 7, LabelKey: "nav.connectors"},
	{ID: "schedules", Index: 8, LabelKey: "nav.schedules"},
	{ID: "settings", Index: 9, LabelKey: "nav.settings"},
}

const navSettingsIndex = 9

// NavLabels 读回侧栏当前所有入口的文案（证明语言真的切换了）。
func (a *u07App) NavLabels(t *testing.T) []string {
	t.Helper()
	var labels []string
	expr := `JSON.stringify([...document.querySelectorAll('nav li button, aside li button')].map(b => (b.textContent||'').replace(/\s+/g,' ').trim()))`
	if err := a.cdp.EvalJSON(expr, &labels); err != nil {
		t.Fatalf("读取侧栏文案失败: %v", err)
	}
	return labels
}

// OpenPage 点第 index 个侧栏入口，并等它成为当前页。
func (a *u07App) OpenPage(t *testing.T, index int) {
	t.Helper()
	if err := a.ClickIndex("nav li button, aside li button", index); err != nil {
		t.Fatalf("打开侧栏第 %d 个入口失败: %v", index, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := a.cdp.EvalString(`(()=>{const b=[...document.querySelectorAll('nav li button, aside li button')].filter(x=>x.getAttribute('aria-current')==='page'); return b.length===1?(b[0].textContent||'').trim():''})()`)
		if err == nil && cur != "" {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("侧栏第 %d 个入口没有变成当前页高亮", index)
}

// MainLabel 返回 <main> 的 aria-label（当前页朗读名）。
func (a *u07App) MainLabel(t *testing.T) string {
	t.Helper()
	s, err := a.cdp.EvalString(`(()=>{const m=document.querySelector('main'); return m?(m.getAttribute('aria-label')||''):''})()`)
	if err != nil {
		t.Fatalf("读取 <main aria-label> 失败: %v", err)
	}
	return s
}

// ClickIndex 真实点击选择器匹配的第 index 个可见元素（CDP 输入事件）。
func (a *u07App) ClickIndex(selector string, index int) error {
	box, err := a.cdp.EvalString(fmt.Sprintf(`(()=>{
		const els=[...document.querySelectorAll(%q)].filter(e=>{const r=e.getBoundingClientRect(); return r.width>0&&r.height>0;});
		const el=els[%d];
		if(!el) return JSON.stringify({found:false, count:els.length});
		el.scrollIntoView({block:'center'});
		const r=el.getBoundingClientRect();
		return JSON.stringify({found:true, x:r.left+r.width/2, y:r.top+r.height/2, count:els.length});
	})()`, selector, index))
	if err != nil {
		return err
	}
	var b struct {
		Found bool    `json:"found"`
		X     float64 `json:"x"`
		Y     float64 `json:"y"`
		Count int     `json:"count"`
	}
	if err := json.Unmarshal([]byte(box), &b); err != nil {
		return err
	}
	if !b.Found {
		return fmt.Errorf("%s 只有 %d 个可见元素，没有第 %d 个", selector, b.Count, index)
	}
	return a.clickAt(b.X, b.Y)
}

func (a *u07App) clickAt(x, y float64) error {
	for _, ev := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := fmt.Sprintf(`{"type":%q,"x":%f,"y":%f,"button":"left","clickCount":1}`, ev, x, y)
		if _, err := a.cdp.Call("Input.dispatchMouseEvent", params, 10*time.Second); err != nil {
			return err
		}
	}
	time.Sleep(120 * time.Millisecond)
	return nil
}

// HoverAt 把真实鼠标移到坐标上（触发 :hover）。
func (a *u07App) HoverAt(x, y float64) error {
	params := fmt.Sprintf(`{"type":"mouseMoved","x":%f,"y":%f}`, x, y)
	_, err := a.cdp.Call("Input.dispatchMouseEvent", params, 10*time.Second)
	time.Sleep(80 * time.Millisecond)
	return err
}

// PressAt 按下左键但不松开（触发 :active）。松开由 MoveAwayAndRelease 完成，
// 这样不会真的触发一次点击（点击要求按下与松开落在同一个元素上）。
func (a *u07App) PressAt(x, y float64) error {
	params := fmt.Sprintf(`{"type":"mousePressed","x":%f,"y":%f,"button":"left","clickCount":1}`, x, y)
	_, err := a.cdp.Call("Input.dispatchMouseEvent", params, 10*time.Second)
	time.Sleep(80 * time.Millisecond)
	return err
}

// MoveAwayAndRelease 把指针移开再松开：取消这次点击。
func (a *u07App) MoveAwayAndRelease() error {
	if _, err := a.cdp.Call("Input.dispatchMouseEvent", `{"type":"mouseMoved","x":1,"y":1}`, 10*time.Second); err != nil {
		return err
	}
	_, err := a.cdp.Call("Input.dispatchMouseEvent", `{"type":"mouseReleased","x":1,"y":1,"button":"left","clickCount":1}`, 10*time.Second)
	return err
}

// ElementBox 返回第 index 个可见匹配元素的中心点。
func (a *u07App) ElementBox(t *testing.T, selector string, index int) (x, y float64, ok bool) {
	t.Helper()
	raw, err := a.cdp.EvalString(fmt.Sprintf(`(()=>{
		const els=[...document.querySelectorAll(%q)].filter(e=>{const r=e.getBoundingClientRect(); return r.width>0&&r.height>0;});
		const el=els[%d];
		if(!el) return JSON.stringify({found:false});
		el.scrollIntoView({block:'center'});
		const r=el.getBoundingClientRect();
		return JSON.stringify({found:true, x:r.left+r.width/2, y:r.top+r.height/2});
	})()`, selector, index))
	if err != nil {
		t.Fatalf("读取元素位置失败: %v", err)
	}
	var b struct {
		Found bool    `json:"found"`
		X     float64 `json:"x"`
		Y     float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("解析元素位置失败: %v", err)
	}
	return b.X, b.Y, b.Found
}

// ControlSnapshot 是某个控件此刻的样式快照（hover/active/disabled 证据）。
type ControlSnapshot struct {
	Target      string   `json:"target"`
	Color       string   `json:"color"`
	Background  string   `json:"background"`
	EffectiveBg string   `json:"effectiveBg"`
	Ratio       *float64 `json:"ratio"`
	Outline     string   `json:"outline"`
	BoxShadow   string   `json:"boxShadow"`
	Opacity     string   `json:"opacity"`
	Text        string   `json:"text"`
}

// SnapshotSelector 读回指定元素的样式。
func (a *u07App) SnapshotSelector(t *testing.T, selector string) *ControlSnapshot {
	t.Helper()
	raw, err := a.cdp.EvalString(fmt.Sprintf(`(()=>JSON.stringify(window.__u07audit.controlSnapshot(%q)))()`, selector))
	if err != nil {
		t.Fatalf("读取控件样式失败: %v", err)
	}
	if raw == "null" {
		return nil
	}
	var snap ControlSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatalf("解析控件样式失败: %v (%s)", err, raw)
	}
	return &snap
}

// ---------- 语言/主题切换（走真实设置页） ----------

// switchPreferences 在设置页里改语言与主题并保存（真实 select + 真实点击）。
//
// 这是 I18N02/THEME02/THEME04 的关键路径：不直接改 localStorage、不直接改 DOM，
// 而是用户在界面上会做的两步。保存后由后端 CAS 写盘，证据再从磁盘读。
func (a *u07App) switchPreferences(t *testing.T, locale, theme string) {
	t.Helper()
	a.OpenPage(t, navSettingsIndex)
	// 设置页可能停在 CONFIG_CONFLICT 的错误态（例如上一个用例故意制造过冲突）：
	// 先用界面自己的"重试"把 revision 读回来，再继续操作。这不是绕过检查，
	// 而是走用户会走的那一步（冲突 → 重新读取 → 再保存）。
	if a.hasPhase(t, "error") {
		retry := catalogString(t, a.currentLocale(t), "common.retry")
		if err := a.ClickByText("button", retry); err != nil {
			t.Fatalf("设置页处于错误态，点击重试失败: %v", err)
		}
		// 重试会先把该分区切回 loading（此时表单不在 DOM 里），必须等表单回来。
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if a.hasElement(t, fmt.Sprintf("select[aria-label=%q]", catalogString(t, a.currentLocale(t), "locale.label"))) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	from := a.currentLocale(t)
	localeLabel := catalogString(t, from, "locale.label")
	themeLabel := catalogString(t, from, "theme.label")
	if err := a.SetSelect(fmt.Sprintf("select[aria-label=%q]", localeLabel), locale); err != nil {
		t.Fatalf("选择语言失败: %v", err)
	}
	if err := a.SetSelect(fmt.Sprintf("select[aria-label=%q]", themeLabel), theme); err != nil {
		t.Fatalf("选择主题失败: %v", err)
	}
	saveLabel := catalogString(t, from, "settings.general.save")
	if err := a.ClickByText("button", saveLabel); err != nil {
		t.Fatalf("点击保存偏好失败: %v", err)
	}
	// 保存成功的信号是设置页那块 `data-testid=settings-general-saved` 的已保存标记。
	//
	// 为什么不用文案匹配：页面上别处也可能出现「已保存」三个字，匹配文案会在保存
	// 真正完成之前就返回——主题还没应用就被读走，证据里就会看到"选了浅色还是深色"
	// 这种假失败（U07 实测踩过一次）。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := a.cdp.EvalString(`(()=>document.querySelector('[data-testid="settings-general-saved"]')?'yes':'no')()`)
		if err == nil && raw == "yes" {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("保存偏好后 20s 内没有出现已保存标记（data-testid=settings-general-saved）")
}

// evalRaw 求值一个返回字符串的表达式（调试/证据用）。
func (a *u07App) evalRaw(t *testing.T, expr string) string {
	t.Helper()
	out, err := a.cdp.EvalString(expr)
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	return out
}

// hasElement 报告选择器是否有匹配元素。
func (a *u07App) hasElement(t *testing.T, selector string) bool {
	t.Helper()
	raw, err := a.cdp.EvalString(fmt.Sprintf(`(()=>document.querySelector(%q)?'yes':'no')()`, selector))
	if err != nil {
		t.Fatalf("查询 %s 失败: %v", selector, err)
	}
	return raw == "yes"
}

// hasPhase 报告页面上是否存在某个 AsyncState 阶段标记（loading/error/empty/blocked）。
func (a *u07App) hasPhase(t *testing.T, phase string) bool {
	t.Helper()
	raw, err := a.cdp.EvalString(fmt.Sprintf(`(()=>document.querySelectorAll('[data-phase=%q]').length>0?'yes':'no')()`, phase))
	if err != nil {
		t.Fatalf("读取页面阶段失败: %v", err)
	}
	return raw == "yes"
}

// currentLocale 读 <html lang>。
func (a *u07App) currentLocale(t *testing.T) string {
	t.Helper()
	s, err := a.cdp.EvalString(`(()=>document.documentElement.lang||'')()`)
	if err != nil {
		t.Fatalf("读取 <html lang> 失败: %v", err)
	}
	return s
}

// PageText 返回 body 的可读文本（去重空白）。
func (a *u07App) PageText(t *testing.T) string {
	t.Helper()
	s, err := a.cdp.EvalString(`(() => (document.body ? document.body.innerText : '').replace(/\s+/g, ' '))()`)
	if err != nil {
		t.Fatalf("读取页面文本失败: %v", err)
	}
	return s
}

// ---------- catalog（Go 侧读同一份 locale JSON） ----------

var catalogMu sync.Mutex
var catalogCache = map[string]map[string]any{}

func loadCatalog(t *testing.T, locale string) map[string]any {
	t.Helper()
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if c, ok := catalogCache[locale]; ok {
		return c
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("找不到仓库根: %v", err)
	}
	path := filepath.Join(root, "frontend", "src", "i18n", "locales", locale+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 的 catalog 失败: %v", locale, err)
	}
	var cat map[string]any
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("解析 %s 的 catalog 失败: %v", locale, err)
	}
	catalogCache[locale] = cat
	return cat
}

// catalogString 按点分 key 取文案（取不到直接失败：缺 key 本身就是缺陷）。
func catalogString(t *testing.T, locale, key string) string {
	t.Helper()
	var node any = loadCatalog(t, locale)
	for _, part := range strings.Split(key, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%s 里 %s 不是对象", locale, key)
		}
		node, ok = m[part]
		if !ok {
			t.Fatalf("%s 的 catalog 里没有 %s", locale, key)
		}
	}
	s, ok := node.(string)
	if !ok {
		t.Fatalf("%s 的 %s 不是字符串", locale, key)
	}
	return s
}

// ---------- 中文泄漏（en-US 下的硬编码） ----------

// CJKLeaks 返回页面上出现的、不属于当前语言的中文文案。
//
// 排除 option（语言名「简体中文」本来就该用中文写）、带 lang 的元素与
// title/script/style。这条检查是"某个分区还在写死中文"的直接证据。
func (a *u07App) CJKLeaks(t *testing.T) []string {
	t.Helper()
	var leaks []string
	expr := `(()=>{
		const out=[];
		const walker=document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
		while (walker.nextNode()) {
			const n=walker.currentNode;
			const text=(n.nodeValue||'').trim();
			if (!text) continue;
			if (!/[\u3400-\u9fff]/.test(text)) continue;
			const p=n.parentElement;
			if (!p) continue;
			if (p.closest('option, title, script, style')) continue;
			// 显式标了**别的**语言的元素不算泄漏；但 <html lang> 是所有节点的祖先，
			// 不能把它算进来——否则整页文本都会被排除，检查永远是绿的（实测踩过）。
			const langEl = p.closest('[lang]');
			if (langEl && langEl !== document.documentElement) continue;
			out.push(text.slice(0,80));
		}
		return JSON.stringify([...new Set(out)].slice(0,20));
	})()`
	if err := a.cdp.EvalJSON(expr, &leaks); err != nil {
		t.Fatalf("读取中文泄漏失败: %v", err)
	}
	return leaks
}

// ---------- 原生子窗口（遮挡与"没有重建原生视图"的证据） ----------

// childWindow 是一个子窗口的外部事实。
type uiChildWindow struct {
	Class   string `json:"class"`
	Visible bool   `json:"visible"`
	Left    int32  `json:"left"`
	Top     int32  `json:"top"`
	Width   int32  `json:"width"`
	Height  int32  `json:"height"`
}

// ChildWindows 枚举主窗口的**直系**子窗口。
//
// 为什么不用 EnumChildWindows：它会把 WebView2 自己的内部窗口
// （Chrome_WidgetWin_0/1、Chrome_RenderWidgetHostHWND、Intermediate D3D Window）
// 全带出来，那些是主页面渲染链的一部分，不是"多出来的原生视图"，把它们当成
// 遮挡会让这条检查永远红。这里只取一层，并用 nativeBrowserChildClass 区分
// 右侧浏览器面板。
func (a *u07App) ChildWindows(t *testing.T) []uiChildWindow {
	t.Helper()
	const gwChild = 5
	const gwHwndNext = 2
	var out []uiChildWindow
	child, _, _ := uUser32.NewProc("GetWindow").Call(a.hwnd, gwChild)
	for child != 0 {
		r := windowRect(child)
		out = append(out, uiChildWindow{
			Class:   windowClass(child),
			Visible: windowVisible(child),
			Left:    r.Left, Top: r.Top,
			Width: r.Right - r.Left, Height: r.Bottom - r.Top,
		})
		next, _, _ := uUser32.NewProc("GetWindow").Call(child, gwHwndNext)
		child = next
	}
	return out
}

// nativeBrowserChildClass 是 internal/browser/native 的子窗口类名。
// 它是"右侧原生网页视图"，与主页面渲染链的 WebView2 内部窗口不是一回事。
const nativeBrowserChildClass = "MyAgentNativeHostChild"

// BrowserHostWindows 只返回右侧原生浏览器视图（面板没接管时为空）。
func (a *u07App) BrowserHostWindows(t *testing.T) []uiChildWindow {
	t.Helper()
	var out []uiChildWindow
	for _, w := range a.ChildWindows(t) {
		if w.Class == nativeBrowserChildClass {
			out = append(out, w)
		}
	}
	return out
}

// OcclusionAt 用 WindowFromPoint 检查客户区坐标 (x,y) 上最顶层的窗口。
//
// 用途：UI12 的"关键操作不被原生视图遮挡"。右侧原生网页视图是**独立子窗口**，
// 它盖上来时 WindowFromPoint 直接返回它——这是页面自己看不到的外部事实。
func (a *u07App) OcclusionAt(t *testing.T, clientX, clientY int32) uintptr {
	t.Helper()
	var pt struct{ X, Y int32 }
	ok, _, _ := uUser32.NewProc("ClientToScreen").Call(a.hwnd, uintptr(unsafe.Pointer(&pt)))
	if ok == 0 {
		t.Fatalf("ClientToScreen 失败")
	}
	// WindowFromPoint 的 POINT 是**按值**传的两个 int32（不是指针）。
	// 传指针会拿到 0，看起来像"没有窗口命中"，那是测量错误而不是遮挡。
	sx := uint32(pt.X + clientX)
	sy := uint32(pt.Y + clientY)
	packed := uintptr(sx) | uintptr(sy)<<32
	hit, _, _ := uUser32.NewProc("WindowFromPoint").Call(packed)
	return hit
}

// RootOf 返回窗口的顶层祖先（GA_ROOT）。
func RootOf(hwnd uintptr) uintptr {
	r, _, _ := uUser32.NewProc("GetAncestor").Call(hwnd, 2)
	return r
}
