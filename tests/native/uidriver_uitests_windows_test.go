//go:build windows

package nativetest

// U07 用例用到的"应用层观测"小工具：壳层状态、页面身份、主题事实、导出结果、
// 以及按 locale 计算期望格式化的入口。
//
// 放在这个文件里的都是**读取**，不带断言：断言留在用例里，读法只有一份。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// startU07AppWithHome 用同一份 ANVIL_HOME 再启动一次（重启持久化证据）。
func startU07AppWithHome(t *testing.T, home string) *u07App {
	t.Helper()
	exe, _ := buildU07App(t)
	port, err := freePort()
	if err != nil {
		t.Fatalf("分配 CDP 端口失败: %v", err)
	}
	return launchU07App(t, exe, home, port)
}

// shellState 读壳层里"用户可能丢掉的东西"：搜索草稿与右侧详情栏状态。
func (a *u07App) shellState(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	expr := `(()=>{
		const input=document.querySelector('header input');
		const toggle=document.querySelector('header button');
		const right=document.querySelectorAll('aside').length;
		return JSON.stringify({
			searchDraft: input?input.value:'',
			detailOpen: toggle?toggle.getAttribute('aria-pressed')==='true':false,
			asideCount: right,
			activeRunId: (document.querySelector('[data-run-id]')||{}).getAttribute?document.querySelector('[data-run-id]').getAttribute('data-run-id'):''
		});
	})()`
	if err := a.cdp.EvalJSON(expr, &out); err != nil {
		t.Fatalf("读取壳层状态失败: %v", err)
	}
	return out
}

// apiStatus 返回 Status 绑定的原始 JSON（canonical，用于前后比对）。
func (a *u07App) apiStatus(t *testing.T) string {
	t.Helper()
	raw, err := a.APICall(t, "Status")
	if err != nil {
		t.Fatalf("调用 Status 失败: %v", err)
	}
	return canonicalJSON(t, raw)
}

// browserState 返回 BrowserState 绑定的原始 JSON（浏览器面板的原生状态）。
func (a *u07App) browserState(t *testing.T) string {
	t.Helper()
	raw, err := a.APICall(t, "BrowserState")
	if err != nil {
		// 浏览器面板未装配时这里会失败（本机没有主窗口句柄前的正常状态）。
		// 这不是"切换主题改变了浏览器状态"的证据，如实返回错误文本。
		return "unavailable: " + head(err.Error(), 200)
	}
	return canonicalJSON(t, raw)
}

// ownershipNote 说明接管归属在本次运行里是否可观测。
func (a *u07App) ownershipNote(state string) string {
	if strings.HasPrefix(state, "unavailable") {
		return "takeover ownership 未装配浏览器面板，不可观测（deferred）"
	}
	return state
}

// canonicalJSON 把 JSON 规范化（键序稳定），用于"前后是否相同"的比对。
func canonicalJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("规范化 JSON 失败: %v（%s）", err, head(string(raw), 200))
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("重新编码 JSON 失败: %v", err)
	}
	return string(out)
}

// pageIdentity 返回"页面是否被重建"的判据：timeOrigin + 一次性标记。
func (a *u07App) pageIdentity(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	expr := `(()=>{
		if (!window.__u07marker) window.__u07marker = Math.random().toString(36).slice(2);
		return JSON.stringify({timeOrigin: performance.timeOrigin, marker: window.__u07marker});
	})()`
	if err := a.cdp.EvalJSON(expr, &out); err != nil {
		t.Fatalf("读取页面身份失败: %v", err)
	}
	return out
}

// ThemeFacts 返回主题/语言/dpr/语义 token 的当前值。
func (a *u07App) ThemeFacts(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	expr := `(()=>{
		const cs=getComputedStyle(document.documentElement);
		const tokens={};
		for (const n of ['background','surface','surface-raised','foreground','muted','border','accent','success','warning','danger','focus']) {
			tokens[n]=(cs.getPropertyValue('--anvil-'+n)||'').trim();
		}
		return JSON.stringify({
			theme: document.documentElement.dataset.theme||'',
			locale: document.documentElement.lang||'',
			dpr: window.devicePixelRatio,
			tokens: tokens
		});
	})()`
	if err := a.cdp.EvalJSON(expr, &out); err != nil {
		t.Fatalf("读取主题事实失败: %v", err)
	}
	return out
}

// FirstPaintTheme 轮询"内容出现的第一刻"的主题。
//
// 这是"启动不闪"的判据：如果主题是在 React 挂载后才应用，那么第一个有内容的
// 采样点会看到 data-theme 为空或与最终值不同（hadContentBeforeTheme=true）。
func (a *u07App) FirstPaintTheme(t *testing.T) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := a.cdp.EvalString(`(()=>{
			const root=document.getElementById('root');
			return JSON.stringify({
				theme: document.documentElement.dataset.theme||'',
				hasContent: !!root && root.childElementCount>0,
				ready: document.readyState
			});
		})()`)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var sample struct {
			Theme      string `json:"theme"`
			HasContent bool   `json:"hasContent"`
		}
		if err := json.Unmarshal([]byte(raw), &sample); err != nil {
			continue
		}
		if sample.HasContent {
			return map[string]any{
				"theme":                 sample.Theme,
				"hadContentBeforeTheme": sample.Theme == "",
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("60s 内首屏内容没有出现")
	return nil
}

// exportRows 读回设置页"导出诊断包"的结果行（按 dt 文案归位）。
func (a *u07App) exportRows(t *testing.T) map[string]string {
	t.Helper()
	var rows map[string]string
	expr := `(()=>{
		const dl=document.querySelector('dl[data-testid="export-result"]');
		if (!dl) return JSON.stringify({});
		const out={};
		for (const row of dl.children) {
			const dt=row.querySelector('dt');
			const dd=row.querySelector('dd');
			if (dt && dd) out[(dt.textContent||'').trim()]=(dd.textContent||'').trim();
		}
		return JSON.stringify(out);
	})()`
	if err := a.cdp.EvalJSON(expr, &rows); err != nil {
		t.Fatalf("读取导出结果失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("页面上没有导出结果（dl[data-testid=export-result]）")
	}
	// 归位成稳定键：顺序与 DiagnosticsExport.tsx 一致。
	locale := a.currentLocale(t)
	keys := []string{
		"observability.export.path",
		"observability.export.bytes",
		"observability.export.sha256",
		"observability.export.redactions",
		"observability.export.runs",
		"observability.export.events",
		"observability.export.artifacts",
		"observability.export.schema",
		"observability.export.generatedAt",
	}
	fields := []string{"path", "bytes", "sha256", "redactions", "runs", "events", "artifacts", "schema", "generatedAt"}
	out := map[string]string{}
	for i, key := range keys {
		label := catalogString(t, locale, key)
		if v, ok := rows[label]; ok {
			out[fields[i]] = v
		}
	}
	if out["generatedAt"] == "" || out["bytes"] == "" {
		t.Fatalf("导出结果缺少关键字段: %v", rows)
	}
	return out
}

// exportDiagnostics 在设置页点一次"导出诊断包"并读回结果行。
func (a *u07App) exportDiagnostics(t *testing.T) map[string]string {
	t.Helper()
	a.OpenPage(t, navSettingsIndex)
	label := catalogString(t, a.currentLocale(t), "observability.export.action")
	if err := a.ClickByText("button", label); err != nil {
		t.Fatalf("点击导出诊断包失败: %v", err)
	}
	shaLabel := catalogString(t, a.currentLocale(t), "observability.export.sha256")
	if err := a.WaitText(shaLabel, 40*time.Second); err != nil {
		t.Fatalf("导出结果没有出现: %v", err)
	}
	return a.exportRows(t)
}

// formatNumberInPage 用默认的 Intl.NumberFormat 在页面里算期望值。
func (a *u07App) formatNumberInPage(t *testing.T, value float64, locale string) (string, error) {
	t.Helper()
	expr := fmt.Sprintf(`new Intl.NumberFormat(%q).format(%v)`, locale, value)
	return a.cdp.EvalString(expr)
}

// formatDateInPage 用 format.ts 的选项在页面里算出期望的日期文本。
func (a *u07App) formatDateInPage(t *testing.T, iso, locale string) (string, error) {
	t.Helper()
	expr := fmt.Sprintf(`new Intl.DateTimeFormat(%q,{dateStyle:'medium',timeStyle:'short'}).format(new Date(%q))`, locale, iso)
	return a.cdp.EvalString(expr)
}

// bytesUnitInPage 返回该体积数值在当前语言下应使用的单位词。
//
// 单位选择规则与 frontend/src/i18n/format.ts 的 formatBytes 一致（1024 进制），
// 单位词来自 catalog 的 units.*——这样断的是"单位真的按语言走"，不是抄字符串。
func (a *u07App) bytesUnitInPage(t *testing.T, bytes int, locale string) string {
	t.Helper()
	units := []string{"byte", "kilobyte", "megabyte", "gigabyte", "terabyte"}
	value := float64(bytes)
	index := 0
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	return catalogString(t, locale, "units."+units[index])
}

// SnapshotAt 读回屏幕坐标处元素的样式快照（hover/active 探测用）。
func (a *u07App) SnapshotAt(t *testing.T, x, y float64) *ControlSnapshot {
	t.Helper()
	raw, err := a.cdp.EvalString(fmt.Sprintf(`(()=>{
		const el=document.elementFromPoint(%f,%f);
		if(!el) return "null";
		return JSON.stringify(window.__u07audit.controlSnapshotAt(el));
	})()`, x, y))
	if err != nil {
		t.Fatalf("读取坐标处样式失败: %v", err)
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

// focusProbe 读回当前焦点的可见性证据（不依赖 Tab：用于注入式红证）。
func (a *u07App) focusProbe(t *testing.T) *TabFocusStep {
	t.Helper()
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

// errorCodes 从错误的渲染块里取错误码（AsyncState 会把 code 渲染成等宽标签）。
func errorCodes(t *testing.T, a *u07App) []string {
	t.Helper()
	var codes []string
	expr := `(()=>{
		const out=[];
		for (const block of document.querySelectorAll('[data-phase="error"]')) {
			for (const s of block.querySelectorAll('span')) {
				const t=(s.textContent||'').trim();
				if (/^[A-Z][A-Z0-9_]{2,}$/.test(t)) out.push(t);
			}
		}
		return JSON.stringify([...new Set(out)]);
	})()`
	if err := a.cdp.EvalJSON(expr, &codes); err != nil {
		t.Fatalf("读取错误码失败: %v", err)
	}
	return codes
}

// screenshotSize 返回已落盘截图的字节数。
func (a *u07App) screenshotSize(t *testing.T, name string) int {
	t.Helper()
	fi, err := os.Stat(observationFile(t, name))
	if err != nil {
		t.Fatalf("读取截图 %s 失败: %v", name, err)
	}
	return int(fi.Size())
}
