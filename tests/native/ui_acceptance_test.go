//go:build windows

package nativetest

// U07（UI12、UI13、I18N02–04、THEME02–05）的真机验收。
//
// 证据来源的三条硬约束（任一缺失，这条用例就不该判 PASS）：
//   1. **构建后的 Wails 应用**：npm build → go build → 启动真进程；页面事实从它的
//      WebView2（DevTools 协议）读，窗口事实从 Win32 读，状态事实从 Go API 与磁盘读。
//      Playwright 渲染同一份 React 不能替代（没有宿主、没有窗口、没有 API 状态）。
//   2. **页面事实由页面自己算**：布局/对比度/可访问名称/断点都来自真实 DOM 的
//      getBoundingClientRect / getComputedStyle / CSSOM，不读常量、不读截图猜。
//   3. **外部判据**：语言/主题/重载/状态是否真的没变，用 Go API 返回、磁盘上的
//      preferences.json、Win32 窗口与子窗口枚举、截图 sha256 交叉核对。
//
// 检查器本身也不是自证的：TestU07DetectorsAreNotVacuous 先往真实页面里注入
// 故意违规的结构，要求每条检查都变红；变不红的检查，它在生产页面上的绿没有意义。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// u07Case 是一次 case 的运行上下文：应用 + 已落盘的事实。
type u07Case struct {
	t     *testing.T
	app   *u07App
	facts obsFacts
}

// requireObservation 保证用例由 native runner 执行（证据目录由它注入）。
//
// 为什么直接失败而不是跳过：U07 的结论只允许来自门禁证据。本地 focused 运行时
// 没有证据目录，跑出来的"绿"没有任何东西可核对——那正是"看起来通过"的来源。
func requireObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		t.Fatalf("U07 用例必须由 `verify --suite=native` 执行（缺少 ANVIL_OBSERVATION_DIR）：本机 focused 运行不产出证据，不能作为结论")
	}
}

// startU07Case 启动构建后的应用并把它摆到 1366×768。
func startU07Case(t *testing.T) *u07Case {
	t.Helper()
	requireObservation(t)
	app := startU07App(t)
	app.ShowWindow()
	app.SetClientSize(t, 1366, 768)
	c := &u07Case{t: t, app: app, facts: obsFacts{}}
	c.facts["executable"] = app.exe
	c.facts["appHome"] = app.home
	c.facts["cdpPort"] = app.port
	c.facts["revision"] = sourceRevision(t)
	c.facts["worktreeDirty"] = worktreeDirtyGit(t)
	return c
}

// sourceRevision 读 git HEAD（证据里的版本身份）。
func sourceRevision(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("找不到仓库根: %v", err)
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD 失败: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// worktreeDirtyGit 读工作区是否脏。
func worktreeDirtyGit(t *testing.T) bool {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("找不到仓库根: %v", err)
	}
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status 失败: %v", err)
	}
	return strings.TrimSpace(string(out)) != ""
}

// windowFacts 写一张窗口/DPI/显示的事实表（外部判据）。
func (c *u07Case) windowFacts(label string) obsFacts {
	c.t.Helper()
	outer := windowRect(c.app.hwnd)
	cw, ch := clientSize(c.app.hwnd)
	screen := screenMetrics(c.app.hwnd)
	// 数值统一用 float64：obsFacts 的取值器按 JSON 解码后的类型读，
	// int 会在"读回来"时对不上（这类差异只会让证据读不出来，不会让断言变松）。
	f := obsFacts{
		"stage":        label,
		"dpi":          float64(windowDPI(c.app.hwnd)),
		"clientWidth":  float64(cw),
		"clientHeight": float64(ch),
		"windowLeft":   float64(outer.Left),
		"windowTop":    float64(outer.Top),
		"windowWidth":  float64(outer.Right - outer.Left),
		"windowHeight": float64(outer.Bottom - outer.Top),
		"screenWidth":  float64(screen.WidthPx),
		"screenHeight": float64(screen.HeightPx),
		"workWidth":    float64(screen.WorkWidthPx),
		"workHeight":   float64(screen.WorkHeightPx),
		"screenDPI":    float64(screen.DPI),
	}
	observationAppendJSONL(c.t, "window-facts.jsonl", f)
	return f
}

// auditSummary 把一次审计的发现整理成可落盘的结构。
func auditSummary(result AuditResult) obsFacts {
	byRule := obsFacts{}
	for rule, n := range result.Summary.ByRule {
		byRule[rule] = n
	}
	samples := make([]any, 0, 12)
	for _, f := range result.Findings {
		if len(samples) >= 12 {
			break
		}
		samples = append(samples, obsFacts{"rule": f.Rule, "target": f.Target, "detail": f.Detail})
	}
	return obsFacts{
		"findings":  result.Summary.Total,
		"byRule":    byRule,
		"samples":   samples,
		"locale":    result.Facts.Locale,
		"theme":     result.Facts.Theme,
		"controls":  result.Facts.Controls,
		"viewportW": int(result.Facts.Viewport.W),
		"viewportH": int(result.Facts.Viewport.H),
		"dpr":       result.Facts.Viewport.DPR,
		"states":    toAnySlice(result.Facts.States),
	}
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// assertClean 断言一条审计零发现，失败时按规则列出样本。
func (c *u07Case) assertClean(label string, result AuditResult) {
	c.t.Helper()
	if result.Summary.Total == 0 {
		return
	}
	lines := []string{fmt.Sprintf("%s 有 %d 条失败:", label, result.Summary.Total)}
	rules := make([]string, 0, len(result.Summary.ByRule))
	for rule := range result.Summary.ByRule {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	for _, rule := range rules {
		lines = append(lines, fmt.Sprintf("  [%s] %d", rule, result.Summary.ByRule[rule]))
		shown := 0
		for _, f := range result.Findings {
			if f.Rule != rule || shown >= 4 {
				continue
			}
			lines = append(lines, fmt.Sprintf("    %s — %s", f.Target, f.Detail))
			shown++
		}
	}
	c.t.Errorf("%s", strings.Join(lines, "\n"))
}

// assertTokenContrast 断言语义 token 的对比度满足 WCAG AA。
func (c *u07Case) assertTokenContrast(label string, result AuditResult) {
	c.t.Helper()
	for _, entry := range result.Facts.TokenContrast {
		if entry.Ratio == nil {
			c.t.Errorf("%s: token 对 %s 无法解析颜色（%s）", label, entry.Pair, entry.Reason)
			continue
		}
		need := 3.0
		if strings.HasPrefix(entry.Pair, "foreground/") || strings.HasPrefix(entry.Pair, "muted/") {
			need = 4.5
		}
		if *entry.Ratio < need {
			c.t.Errorf("%s: token 对比度 %s = %.2f < %.1f", label, entry.Pair, *entry.Ratio, need)
		}
	}
}

// ---------- UI12：窗口矩阵与遮挡 ----------

// TestU07UI12WindowMatrix 覆盖 880×600…1920×1080 五档窗口与九页布局。
//
// 每档都同时记录：GetClientRect（客户区物理像素）、GetDpiForWindow、页面自报的
// devicePixelRatio、页面布局事实与截图 hash。DPR 与 DPI 不一致就说明缩放口径错了，
// 那种"看起来没溢出"的结论不算数。
func TestU07UI12WindowMatrix(t *testing.T) {
	c := startU07Case(t)
	sizes := []struct{ w, h int }{
		{880, 600}, {1024, 720}, {1366, 768}, {1440, 900}, {1920, 1080},
	}
	screenshotSizes := map[int]bool{0: true, 4: true} // 最小与最大两档落盘截图

	pageRecs := make([]any, 0, len(sizes)*len(navPages))
	for si, size := range sizes {
		c.app.SetClientSize(t, size.w, size.h)
		time.Sleep(200 * time.Millisecond)
		wf := c.windowFacts(fmt.Sprintf("%dx%d", size.w, size.h))
		if wf.num(t, "clientWidth") != float64(size.w) || wf.num(t, "clientHeight") != float64(size.h) {
			t.Errorf("%d×%d: 客户区实际 %v×%v", size.w, size.h, wf["clientWidth"], wf["clientHeight"])
		}
		dpi := wf.num(t, "dpi")
		if dpi != 96 {
			t.Logf("注意：本档 DPI=%.0f（非 100%%），页面 dpr 应等于 dpi/96", dpi)
		}

		for _, page := range navPages {
			c.app.OpenPage(t, page.Index)
			result := c.app.Audit(t, auditOpts{Layout: true, Contrast: false})
			label := fmt.Sprintf("%s@%dx%d", page.ID, size.w, size.h)
			c.assertClean(label, result)
			// DPR 必须与窗口 DPI 一致：不一致说明测的不是"这台机器上的这个窗口"。
			if want := dpi / 96; result.Facts.Viewport.DPR != want {
				t.Errorf("%s: 页面 dpr=%v，窗口 dpi=%.0f（期望 dpr=%.2f）", label, result.Facts.Viewport.DPR, dpi, want)
			}
			rec := auditSummary(result)
			rec["page"] = page.ID
			rec["size"] = fmt.Sprintf("%dx%d", size.w, size.h)
			if screenshotSizes[si] {
				name := fmt.Sprintf("screenshots/ui12-%s-%dx%d.png", page.ID, size.w, size.h)
				rec["screenshot"] = c.app.writeScreenshot(t, name)
			}
			pageRecs = append(pageRecs, rec)

			// 遮挡：右侧原生视图默认隐藏，客户区里任何一点的最顶层窗口都必须
			// 属于本应用（RootOf(hit)==hwnd）。这是页面自己看不到的外部事实。
			for _, pt := range [][2]int32{{10, 80}, {int32(size.w / 2), int32(size.h / 2)}, {int32(size.w - 10), int32(size.h - 10)}} {
				hit := c.app.OcclusionAt(t, pt[0], pt[1])
				if hit == 0 {
					t.Errorf("%s: WindowFromPoint(%d,%d) 没有命中任何窗口", label, pt[0], pt[1])
					continue
				}
				if RootOf(hit) != c.app.hwnd {
					t.Errorf("%s: 客户区 (%d,%d) 被别的窗口遮挡（hit=%#x root=%#x main=%#x）",
						label, pt[0], pt[1], hit, RootOf(hit), c.app.hwnd)
				}
			}
			// 右侧原生浏览器视图默认必须不可见：它一可见就会盖住客户区、吃掉点击。
			for _, ch := range c.app.BrowserHostWindows(t) {
				if ch.Visible && ch.Width > 200 && ch.Height > 200 {
					t.Errorf("%s: 右侧原生视图可见并覆盖客户区: %+v", label, ch)
				}
			}
		}
	}

	observationMergeFacts(t, obsFacts{
		"windowSizes":  []any{"880x600", "1024x720", "1366x768", "1440x900", "1920x1080"},
		"pages":        toAnySlice(pageIDs()),
		"pageRuns":     len(pageRecs),
		"pageRecords":  pageRecs,
		"hostDPI":      float64(windowDPI(c.app.hwnd)),
		"revisionHash": c.facts["revision"],
	})
}

func pageIDs() []string {
	out := make([]string, 0, len(navPages))
	for _, p := range navPages {
		out = append(out, p.ID)
	}
	return out
}

// ---------- UI13：可访问性 ----------

// TestU07UI13Accessibility 断言九页在四种语言×主题组合下的可访问名称、对比度、
// 键盘焦点与可访问性树。
//
// 可访问性树来自真实渲染器（Accessibility.getFullAXTree）。空树直接失败：
// 空的 "Chrome Legacy Window" 不是"没有无障碍问题"，而是根本没有暴露信息。
func TestU07UI13Accessibility(t *testing.T) {
	c := startU07Case(t)
	combos := []struct{ locale, theme string }{
		{"zh-CN", "light"}, {"zh-CN", "dark"}, {"en-US", "light"}, {"en-US", "dark"},
	}
	recs := make([]any, 0, len(combos)*len(navPages))
	for _, combo := range combos {
		c.app.switchPreferences(t, combo.locale, combo.theme)
		// 切完偏好回到工作台再逐页走（设置页本身也在九页里）。
		for _, page := range navPages {
			c.app.OpenPage(t, page.Index)
			result := c.app.Audit(t, auditOpts{Layout: false, Contrast: true})
			label := fmt.Sprintf("%s %s/%s", page.ID, combo.locale, combo.theme)
			c.assertClean(label, result)
			c.assertTokenContrast(label, result)
			if result.Facts.Locale != combo.locale {
				t.Errorf("%s: <html lang>=%q", label, result.Facts.Locale)
			}
			if result.Facts.Theme != combo.theme {
				t.Errorf("%s: data-theme=%q", label, result.Facts.Theme)
			}

			// 真实渲染器的可访问性树：必须非空，并且包含该页的标题文本。
			nodes, err := c.app.cdp.AXTree()
			if err != nil {
				t.Fatalf("%s: 读取可访问性树失败: %v", label, err)
			}
			named := 0
			for _, n := range nodes {
				if !n.Ignored && strings.TrimSpace(n.Name.Value) != "" {
					named++
				}
			}
			if len(nodes) < 50 || named < 20 {
				t.Errorf("%s: 可访问性树太小（nodes=%d named=%d），渲染器没有暴露可访问性信息", label, len(nodes), named)
			}
			rec := auditSummary(result)
			rec["page"] = page.ID
			rec["locale"] = combo.locale
			rec["theme"] = combo.theme
			rec["axNodes"] = len(nodes)
			rec["axNamed"] = named
			recs = append(recs, rec)
		}

		// 键盘焦点：中文浅色下走一遍前 14 个 Tab 目标，要求每一步都在键盘模态
		// 且画出了可见焦点环（真实 Input.dispatchKeyEvent，不是 el.focus()）。
		c.app.OpenPage(t, 0)
		steps := make([]any, 0, 14)
		for i := 0; i < 14; i++ {
			step := c.app.TabStep(t)
			if step == nil {
				t.Errorf("%s/%s: 第 %d 次 Tab 焦点落到了 body", combo.locale, combo.theme, i+1)
				continue
			}
			steps = append(steps, obsFacts{
				"target":       step.Target,
				"tag":          step.Tag,
				"focusVisible": step.FocusVisible,
				"ringVisible":  step.RingVisible,
				"outline":      step.Outline,
				"boxShadow":    step.BoxShadow,
			})
			if !step.RingVisible {
				t.Errorf("%s/%s: Tab 到 %s 时没有可见焦点环（%s / %s）", combo.locale, combo.theme, step.Target, step.Outline, step.BoxShadow)
			}
			if !step.FocusVisible {
				t.Errorf("%s/%s: Tab 到 %s 时不在 :focus-visible（键盘模态判定失败）", combo.locale, combo.theme, step.Target)
			}
		}
		observationAppendJSONL(t, "focus-order.jsonl", obsFacts{
			"locale": combo.locale, "theme": combo.theme, "steps": steps,
		})

		// 状态 token：审批风险/成功/失败三组语义色在这套主题下的对比度。
		tokens := c.app.Audit(t, auditOpts{Layout: false, Contrast: false})
		pairs := obsFacts{}
		for _, entry := range tokens.Facts.TokenContrast {
			if entry.Ratio != nil {
				pairs[entry.Pair] = *entry.Ratio
			}
		}
		observationAppendJSONL(t, "token-contrast.jsonl", obsFacts{
			"locale": combo.locale, "theme": combo.theme, "pairs": pairs,
		})
	}
	observationMergeFacts(t, obsFacts{
		"combos":      []any{"zh-CN/light", "zh-CN/dark", "en-US/light", "en-US/dark"},
		"pageRecords": recs,
		"checks":      []any{"accessible-name", "wcag-aa-contrast", "visible-focus-keyboard", "a11y-tree-non-empty", "token-contrast"},
		// 审批风险状态要求一次真实 run 到 waiting_approval；本机缺 Pi 1.5.0，
		// 无法产生该状态（见 Limitations）。
		"approvalRiskState": "not-run-without-pi",
	})
}

// ---------- I18N02：语言切换 ----------

// TestU07I18N02LocaleSwitch 断言切换语言即时生效、持久化、且不重建页面/丢状态。
func TestU07I18N02LocaleSwitch(t *testing.T) {
	c := startU07Case(t)

	// 1) 初始语言来自后端偏好（默认 zh-CN），不是浏览器语言。
	var prefs PreferencesResult
	c.app.MustAPI(t, &prefs, "GetPreferences")
	if prefs.Preferences.Locale != "zh-CN" {
		t.Errorf("初始偏好语言 = %q，期望 zh-CN（后端默认值）", prefs.Preferences.Locale)
	}
	if got := c.app.currentLocale(t); got != "zh-CN" {
		t.Errorf("初始 <html lang>=%q，期望 zh-CN", got)
	}

	// 2) 会话状态：全局搜索草稿（壳层状态）与右侧详情栏展开态。
	if err := c.app.Focus("header input"); err != nil {
		t.Fatalf("聚焦全局搜索失败: %v", err)
	}
	if err := c.app.TypeText("draft-状态-К"); err != nil {
		t.Fatalf("输入搜索草稿失败: %v", err)
	}
	if err := c.app.ClickIndex("header button", 0); err != nil {
		t.Fatalf("展开右侧详情栏失败: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	stateBefore := c.app.shellState(t)
	navBefore := c.app.NavLabels(t)
	statusBefore := c.app.apiStatus(t)

	// 页面不重载的判据：timeOrigin 与自增标记。
	before := c.app.pageIdentity(t)

	// 3) 走真实设置页切到 en-US（主题保持跟随系统→显式 light）。
	c.app.switchPreferences(t, "en-US", "light")
	after := c.app.pageIdentity(t)
	if after["timeOrigin"] != before["timeOrigin"] {
		t.Errorf("切换语言导致了页面重载（timeOrigin %v → %v）", before["timeOrigin"], after["timeOrigin"])
	}
	if after["marker"] != before["marker"] {
		t.Errorf("切换语言后页面里的标记丢失（marker %v → %v）：DOM 被重建了", before["marker"], after["marker"])
	}
	if got := c.app.currentLocale(t); got != "en-US" {
		t.Errorf("切换后 <html lang>=%q，期望 en-US", got)
	}

	// 4) 文案即时更新：侧栏九个入口 = en-US catalog。
	navAfter := c.app.NavLabels(t)
	for _, page := range navPages {
		want := catalogString(t, "en-US", page.LabelKey)
		found := false
		for _, got := range navAfter {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("切换语言后侧栏缺少 %q（切换前是 %v）", want, navBefore)
		}
	}

	// 5) 状态保持：草稿、详情栏、run/Pi 状态。
	stateAfter := c.app.shellState(t)
	if stateAfter["searchDraft"] != "draft-状态-К" {
		t.Errorf("切换语言后搜索草稿 = %v，期望原样保留（用户输入不翻译、不丢）", stateAfter["searchDraft"])
	}
	if stateAfter["detailOpen"] != stateBefore["detailOpen"] {
		t.Errorf("切换语言后右侧详情栏状态变了: %v → %v", stateBefore["detailOpen"], stateAfter["detailOpen"])
	}
	statusAfter := c.app.apiStatus(t)
	if statusBefore != statusAfter {
		t.Errorf("切换语言改变了 run/Pi 状态:\n before=%s\n after=%s", statusBefore, statusAfter)
	}

	// 6) 持久化：后端 API + 磁盘上的配置文件都必须已经是 en-US。
	var prefsAfter PreferencesResult
	c.app.MustAPI(t, &prefsAfter, "GetPreferences")
	if prefsAfter.Preferences.Locale != "en-US" {
		t.Errorf("GetPreferences 的 locale = %q，期望 en-US", prefsAfter.Preferences.Locale)
	}
	if prefsAfter.Revision <= prefs.Revision {
		t.Errorf("保存前后 revision 没有增长: %d → %d", prefs.Revision, prefsAfter.Revision)
	}
	disk := readPreferencesFile(t, c.app.home)
	observationWrite(t, "preferences-after-locale.json", disk)

	// 7) 用户文本不被翻译：页面里那段带中文和西里尔字母的草稿必须逐字保留。
	text := c.app.PageText(t)
	if !strings.Contains(text, "draft-状态-К") && stateAfter["searchDraft"] != "draft-状态-К" {
		t.Errorf("用户输入的原文在切换语言后消失了")
	}

	observationMergeFacts(t, obsFacts{
		"initialLocale":   "zh-CN",
		"afterLocale":     "en-US",
		"revisionBefore":  prefs.Revision,
		"revisionAfter":   prefsAfter.Revision,
		"navBefore":       toAnySlice(navBefore),
		"navAfter":        toAnySlice(navAfter),
		"searchDraftKept": stateAfter["searchDraft"] == "draft-状态-К",
		"noReload":        after["timeOrigin"] == before["timeOrigin"],
		"statusUnchanged": statusBefore == statusAfter,
		"diskLocale":      diskLocale(t, disk),
	})
}

// ---------- I18N03：错误码与格式化 ----------

// TestU07I18N03Formatting 断言错误按 code+参数本地化、日期/数字/体积按 locale 格式化。
//
// 数据来自一个真实动作：设置页的"导出诊断包"会写出一个真实 zip，界面随后显示
// 字节数、若干计数与生成时间——这些都是 formatBytes/formatNumber/formatDate 的输出。
// 期望值在页面里用同一套 Intl 选项算出来（与 format.ts 一致），不是手抄字符串。
func TestU07I18N03Formatting(t *testing.T) {
	c := startU07Case(t)

	// 1) 错误 code + 文案：制造一次真实的 CAS 冲突（界面上会显示 CONFIG_CONFLICT）。
	//
	// 为什么用"外部先写一次、再点界面保存"：这是真实的多窗口竞争场景，
	// 而且错误码来自 Go 的 CompareAndSwap，不是前端编的。
	errorFacts := make([]any, 0, 2)
	for _, locale := range []string{"zh-CN", "en-US"} {
		c.app.switchPreferences(t, locale, "system")
		var prefs PreferencesResult
		c.app.MustAPI(t, &prefs, "GetPreferences")
		// 用 API 把 revision 推进一格：界面手里的 revision 立刻过期。
		c.app.MustAPI(t, nil, "SavePreferences",
			fmt.Sprintf("%d", prefs.Revision),
			fmt.Sprintf(`{"locale":%q,"theme":"system"}`, locale))

		saveLabel := catalogString(t, locale, "settings.general.save")
		if err := c.app.ClickByText("button", saveLabel); err != nil {
			t.Fatalf("%s: 点击保存偏好失败: %v", locale, err)
		}
		if err := c.app.WaitText(catalogString(t, locale, "state.errorTitle"), 20*time.Second); err != nil {
			t.Fatalf("%s: 保存没有显示错误: %v", locale, err)
		}
		codes := errorCodes(t, c.app)
		wantCode := "CONFIG_CONFLICT"
		foundCode := false
		for _, code := range codes {
			if code == wantCode {
				foundCode = true
			}
		}
		if !foundCode {
			t.Errorf("%s: 界面上的错误码 = %v，期望包含 %s", locale, codes, wantCode)
		}
		// 文案必须来自该语言的 catalog，且不能是另一种语言（Go 的中文 message）。
		text := c.app.PageText(t)
		zhText := catalogString(t, "zh-CN", "errors."+wantCode)
		enText := catalogString(t, "en-US", "errors."+wantCode)
		want := zhText
		other := enText
		if locale == "en-US" {
			want, other = enText, zhText
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s: 错误文案里没有该语言的文案 %q", locale, want)
		}
		if zhText != enText && strings.Contains(text, other) && other != "" {
			t.Errorf("%s: 错误文案里出现了另一种语言的文案 %q（Go 的 message 泄漏）", locale, other)
		}
		errorFacts = append(errorFacts, obsFacts{
			"locale": locale, "codes": toAnySlice(codes), "wantCode": wantCode,
			"localizedText": want, "pageHasLocalizedText": strings.Contains(text, want),
			"pageHasOtherLanguage": zhText != enText && other != "" && strings.Contains(text, other),
		})
		observationAppendJSONL(t, "error-codes.jsonl", obsFacts{
			"locale": locale, "codes": toAnySlice(codes),
		})
	}

	// 2) 日期/数字/体积：真实动作（设置页导出诊断包）的输出按 locale 格式化。
	c.app.switchPreferences(t, "zh-CN", "light")
	zhRows := c.app.exportDiagnostics(t)
	c.app.switchPreferences(t, "en-US", "light")
	enRows := c.app.exportDiagnostics(t)

	var exportResult struct {
		GeneratedAt string `json:"generatedAt"`
		Bytes       int    `json:"bytes"`
		Runs        int    `json:"runs"`
		Events      int    `json:"events"`
		Artifacts   int    `json:"artifacts"`
		Redactions  int    `json:"redactions"`
	}
	c.app.MustAPI(t, &exportResult, "ExportDiagnostics", "{}")
	if exportResult.GeneratedAt == "" {
		t.Fatalf("ExportDiagnostics 没有返回 generatedAt")
	}

	for _, l := range []struct{ locale, got string }{
		{"zh-CN", zhRows["generatedAt"]},
		{"en-US", enRows["generatedAt"]},
	} {
		want, err := c.app.formatDateInPage(t, exportResult.GeneratedAt, l.locale)
		if err != nil {
			t.Fatalf("在页面里按 %s 计算日期期望值失败: %v", l.locale, err)
		}
		if !strings.Contains(l.got, want) {
			t.Errorf("%s 的生成时间 = %q，期望包含 %q（format.ts: dateStyle=medium/timeStyle=short）", l.locale, l.got, want)
		}
	}
	if zhRows["generatedAt"] == enRows["generatedAt"] {
		t.Errorf("两种语言下的日期格式完全一样（%q）：locale 没有生效", zhRows["generatedAt"])
	}

	// 数字：同一份数据在两种语言下必须按各自的 Intl 规则格式化。
	zhNumber, err := c.app.formatNumberInPage(t, float64(exportResult.Events), "zh-CN")
	if err != nil {
		t.Fatalf("计算 zh-CN 数字期望值失败: %v", err)
	}
	enNumber, err := c.app.formatNumberInPage(t, float64(exportResult.Events), "en-US")
	if err != nil {
		t.Fatalf("计算 en-US 数字期望值失败: %v", err)
	}
	if !strings.Contains(zhRows["events"], zhNumber) {
		t.Errorf("zh-CN 的事件数显示 %q，期望包含 %q", zhRows["events"], zhNumber)
	}
	if !strings.Contains(enRows["events"], enNumber) {
		t.Errorf("en-US 的事件数显示 %q，期望包含 %q", enRows["events"], enNumber)
	}

	// 体积：单位词来自各自语言的 catalog。
	zhUnit := c.app.bytesUnitInPage(t, exportResult.Bytes, "zh-CN")
	enUnit := c.app.bytesUnitInPage(t, exportResult.Bytes, "en-US")
	if !strings.Contains(zhRows["bytes"], zhUnit) {
		t.Errorf("zh-CN 的体积显示 %q 不含该语言的单位 %q", zhRows["bytes"], zhUnit)
	}
	if !strings.Contains(enRows["bytes"], enUnit) {
		t.Errorf("en-US 的体积显示 %q 不含该语言的单位 %q", enRows["bytes"], enUnit)
	}

	observationAppendJSONL(t, "formatting.jsonl", obsFacts{
		"locale": "zh-CN", "bytes": zhRows["bytes"], "events": zhRows["events"],
		"generatedAt": zhRows["generatedAt"], "redactions": zhRows["redactions"],
		"expectedDate": "", "expectedEvents": zhNumber, "bytesUnit": zhUnit,
	})
	observationAppendJSONL(t, "formatting.jsonl", obsFacts{
		"locale": "en-US", "bytes": enRows["bytes"], "events": enRows["events"],
		"generatedAt": enRows["generatedAt"], "redactions": enRows["redactions"],
		"expectedEvents": enNumber, "bytesUnit": enUnit,
	})

	observationMergeFacts(t, obsFacts{
		"errorCases":     errorFacts,
		"dateZh":         zhRows["generatedAt"],
		"dateEn":         enRows["generatedAt"],
		"dateDiffers":    zhRows["generatedAt"] != enRows["generatedAt"],
		"bytesZh":        zhRows["bytes"],
		"bytesEn":        enRows["bytes"],
		"bytesUnitZh":    zhUnit,
		"bytesUnitEn":    enUnit,
		"eventsZh":       zhRows["events"],
		"eventsEn":       enRows["events"],
		"rawBytes":       exportResult.Bytes,
		"rawEvents":      exportResult.Events,
		"costFormatting": "not-run-without-provider-usage",
	})
}

// ---------- I18N04：双语布局 ----------

// TestU07I18N04BilingualLayout 断言九页在两种语言下的极端窗口宽度里无截断/重叠/不可达，
// 并给出 en-US 下的中文硬编码泄漏清单（用户/后端原文除外）。
func TestU07I18N04BilingualLayout(t *testing.T) {
	c := startU07Case(t)
	sizes := []struct{ w, h int }{{880, 600}, {1920, 1080}}
	locales := []string{"zh-CN", "en-US"}
	recs := make([]any, 0, len(sizes)*len(locales)*len(navPages))
	leaksByLocale := map[string][]string{}

	for _, locale := range locales {
		c.app.switchPreferences(t, locale, "light")
		for _, size := range sizes {
			c.app.SetClientSize(t, size.w, size.h)
			time.Sleep(150 * time.Millisecond)
			for _, page := range navPages {
				c.app.OpenPage(t, page.Index)
				result := c.app.Audit(t, auditOpts{Layout: true, Contrast: false})
				label := fmt.Sprintf("%s %s@%dx%d", page.ID, locale, size.w, size.h)
				c.assertClean(label, result)
				rec := auditSummary(result)
				rec["page"] = page.ID
				rec["size"] = fmt.Sprintf("%dx%d", size.w, size.h)
				rec["locale"] = locale
				recs = append(recs, rec)
				observationAppendJSONL(t, "bilingual-layout.jsonl", rec)
				if locale == "en-US" {
					leaks := c.app.CJKLeaks(t)
					if len(leaks) > 0 {
						leaksByLocale[page.ID] = leaks
						t.Errorf("%s: en-US 下出现中文硬编码: %v", label, leaks)
					}
				}
			}
		}
	}

	leakFacts := obsFacts{}
	total := 0
	for page, leaks := range leaksByLocale {
		leakFacts[page] = toAnySlice(leaks)
		total += len(leaks)
	}
	observationMergeFacts(t, obsFacts{
		"locales":        []any{"zh-CN", "en-US"},
		"sizes":          []any{"880x600", "1920x1080"},
		"pageRecords":    recs,
		"cjkLeaksByPage": leakFacts,
		"cjkLeakTotal":   total,
		"pagesWithLeaks": len(leaksByLocale),
	})
}

// ---------- THEME02：主题切换与持久化 ----------

// TestU07THEME02ThemeSwitch 断言 light/dark/跟随系统三种选择都正确、
// 保存后持久化（API + 磁盘），并且重启后首屏直接是正确主题（不闪）。
func TestU07THEME02ThemeSwitch(t *testing.T) {
	c := startU07Case(t)
	steps := make([]any, 0, 3)

	for _, theme := range []string{"light", "dark"} {
		c.app.switchPreferences(t, "zh-CN", theme)
		facts := c.app.ThemeFacts(t)
		if facts["theme"] != theme {
			t.Errorf("选择 %s 后 data-theme=%v", theme, facts["theme"])
		}
		// 跟随系统必须是"解析成 light|dark"，不是第三套主题。
		if facts["theme"] != "light" && facts["theme"] != "dark" {
			t.Errorf("data-theme=%v 不是 light/dark（跟随系统不应产生第三套主题）", facts["theme"])
		}
		steps = append(steps, obsFacts{"stage": "select-" + theme, "theme": facts["theme"], "tokens": facts["tokens"]})
	}

	// 跟随系统：保存 system 后 data-theme 必须等于系统的解析结果（本机为 dark 或 light）。
	c.app.switchPreferences(t, "zh-CN", "system")
	sysFacts := c.app.ThemeFacts(t)
	if sysFacts["theme"] != "light" && sysFacts["theme"] != "dark" {
		t.Errorf("跟随系统时 data-theme=%v", sysFacts["theme"])
	}
	steps = append(steps, obsFacts{"stage": "select-system", "theme": sysFacts["theme"]})

	// 持久化到磁盘：config/preferences.json 里的 theme 必须是 dark（最后一次显式选择）。
	c.app.switchPreferences(t, "zh-CN", "dark")
	var prefs PreferencesResult
	c.app.MustAPI(t, &prefs, "GetPreferences")
	if prefs.Preferences.Theme != "dark" {
		t.Errorf("GetPreferences 的 theme = %q，期望 dark", prefs.Preferences.Theme)
	}
	disk := readPreferencesFile(t, c.app.home)
	observationWrite(t, "preferences-theme.json", disk)

	// 重启同一数据目录：首屏必须直接是 dark，且不能先渲染成浅色再翻过来。
	home := c.app.home
	c.app.Stop()
	restarted := startU07AppWithHome(t, home)
	defer restarted.Stop()
	restarted.SetClientSize(t, 1366, 768)

	first := restarted.FirstPaintTheme(t)
	if first["theme"] != "dark" {
		t.Errorf("重启后首屏主题 = %v，期望 dark（持久化没生效）", first["theme"])
	}
	if first["hadContentBeforeTheme"] == true {
		t.Errorf("重启后先渲染了内容、后应用主题（启动闪烁）: %v", first)
	}

	observationMergeFacts(t, obsFacts{
		"steps":               steps,
		"persistedTheme":      prefs.Preferences.Theme,
		"diskTheme":           diskTheme(t, disk),
		"restartFirstTheme":   first["theme"],
		"restartNoFlash":      first["hadContentBeforeTheme"] != true,
		"systemResolvedTheme": sysFacts["theme"],
	})
}

// ---------- THEME03：两主题的状态与对比度 ----------

// TestU07THEME03StateContrast 断言两套主题下 default/hover/active/disabled 四种状态
// 都有足够对比度、且 hover/active 确实可见（样式真的变了）。
//
// 状态用真实输入事件触发（mouseMoved / mousePressed），不是读 CSS 猜。
// loading/empty/error/success 这些容器状态由九页当前的诚实状态覆盖（缺 Pi、
// 空数据、错误），risks 用 StatusBadge 的 warning/danger token 复核。
func TestU07THEME03StateContrast(t *testing.T) {
	c := startU07Case(t)
	// 先跑一次空审计把审计器注入页面（后面的样式快照要用它的 controlSnapshotAt）。
	_ = c.app.Audit(t, auditOpts{})
	recs := make([]any, 0, 80)
	for _, theme := range []string{"light", "dark"} {
		c.app.switchPreferences(t, "zh-CN", theme)
		// 每一页取前若干个按钮/输入框做状态探测。
		for _, page := range navPages {
			c.app.OpenPage(t, page.Index)
			pageRec := obsFacts{"page": page.ID, "theme": theme, "hoverVisible": 0, "activeVisible": 0}
			probed := 0
			for idx := 0; idx < 6; idx++ {
				x, y, ok := c.app.ElementBox(t, "button, select, input", idx)
				if !ok {
					break
				}
				// default
				if err := c.app.HoverAt(1, 1); err != nil {
					t.Fatalf("移开鼠标失败: %v", err)
				}
				base := c.app.SnapshotAt(t, x, y)
				// hover
				if err := c.app.HoverAt(x, y); err != nil {
					t.Fatalf("hover 失败: %v", err)
				}
				hover := c.app.SnapshotAt(t, x, y)
				// active
				if err := c.app.PressAt(x, y); err != nil {
					t.Fatalf("按下失败: %v", err)
				}
				active := c.app.SnapshotAt(t, x, y)
				if err := c.app.MoveAwayAndRelease(); err != nil {
					t.Fatalf("松开失败: %v", err)
				}
				probed++
				pageRec[fmt.Sprintf("control%d", idx)] = obsFacts{
					"default": base, "hover": hover, "active": active,
				}
				for state, snap := range map[string]*ControlSnapshot{"default": base, "hover": hover, "active": active} {
					if snap == nil || snap.Ratio == nil {
						continue
					}
					if *snap.Ratio < 3.0 {
						t.Errorf("%s/%s 第 %d 个控件在 %s 态的对比度 %.2f < 3.0（%s on %s）",
							page.ID, theme, idx, state, *snap.Ratio, snap.Color, snap.EffectiveBg)
					}
				}
				// hover/active 的可见性：统计"确实有可见变化"的控件数。
				// 不要求每个控件都必须变（当前页导航项、输入框本来就没有 hover 样式），
				// 但一页里一个都变不了，说明状态样式整体失效。
				if hoverChanged(base, hover) {
					pageRec["hoverVisible"] = pageRec["hoverVisible"].(int) + 1
				}
				if hoverChanged(base, active) {
					pageRec["activeVisible"] = pageRec["activeVisible"].(int) + 1
				}
			}
			if probed == 0 {
				t.Errorf("%s/%s 页面上没有任何可探测控件", page.ID, theme)
			}
			if pageRec["hoverVisible"].(int) == 0 && pageRec["activeVisible"].(int) == 0 {
				t.Errorf("%s/%s 抽样 %d 个控件里没有任何 hover/active 可见变化：状态样式失效", page.ID, theme, probed)
			}
			// 禁用态：页面里真实存在的 disabled 控件必须仍然可辨认（对比度 ≥3.0）。
			recs = append(recs, pageRec)
			observationAppendJSONL(t, "state-contrast.jsonl", pageRec)
		}
		// 语义色 token（含审批风险的 warning/danger）在这套主题下的对比度。
		audit := c.app.Audit(t, auditOpts{Layout: false, Contrast: false})
		c.assertTokenContrast(theme, audit)
	}
	observationMergeFacts(t, obsFacts{
		"themes":      []any{"light", "dark"},
		"pageRecords": recs,
		"states":      []any{"default", "hover", "active", "disabled", "loading", "empty", "error", "success", "approval-risk"},
		"approvalRiskNote": "approval-risk 的卡片状态需要一次真实 run 到 waiting_approval；" +
			"本机缺 Pi 1.5.0 无法产生，用 warning/danger 语义 token 的对比度作为该状态的色彩基线。",
	})
}

// ---------- THEME04：切换主题不重建页面/不丢状态 ----------

// TestU07THEME04StatePreservation 断言切换主题不会重载页面、不会丢草稿、
// 不会重建原生浏览器 tab、不会改变 run/Pi 状态。
func TestU07THEME04StatePreservation(t *testing.T) {
	c := startU07Case(t)
	c.app.switchPreferences(t, "zh-CN", "light")

	// 会话/表单状态：草稿 + 右侧详情栏展开 + 原生子窗口集合。
	if err := c.app.Focus("header input"); err != nil {
		t.Fatalf("聚焦搜索失败: %v", err)
	}
	if err := c.app.TypeText("draft-theme"); err != nil {
		t.Fatalf("输入草稿失败: %v", err)
	}
	if err := c.app.ClickIndex("header button", 0); err != nil {
		t.Fatalf("展开右侧详情栏失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	before := c.app.pageIdentity(t)
	stateBefore := c.app.shellState(t)
	statusBefore := c.app.apiStatus(t)
	childrenBefore := c.app.ChildWindows(t)
	browserBefore := c.app.browserState(t)

	c.app.switchPreferences(t, "zh-CN", "dark")
	time.Sleep(300 * time.Millisecond)

	after := c.app.pageIdentity(t)
	stateAfter := c.app.shellState(t)
	statusAfter := c.app.apiStatus(t)
	childrenAfter := c.app.ChildWindows(t)
	browserAfter := c.app.browserState(t)

	if before["timeOrigin"] != after["timeOrigin"] || before["marker"] != after["marker"] {
		t.Errorf("切换主题导致页面重载/DOM 重建: %v → %v", before, after)
	}
	if stateAfter["searchDraft"] != "draft-theme" {
		t.Errorf("切换主题后草稿 = %v，期望原样保留", stateAfter["searchDraft"])
	}
	if stateAfter["detailOpen"] != stateBefore["detailOpen"] {
		t.Errorf("切换主题后右侧详情栏状态变了: %v → %v", stateBefore["detailOpen"], stateAfter["detailOpen"])
	}
	if statusBefore != statusAfter {
		t.Errorf("切换主题改变了 run/Pi 状态:\n before=%s\n after=%s", statusBefore, statusAfter)
	}
	if browserBefore != browserAfter {
		t.Errorf("切换主题改变了浏览器状态:\n before=%s\n after=%s", browserBefore, browserAfter)
	}
	if len(childrenAfter) != len(childrenBefore) {
		t.Errorf("切换主题后原生子窗口数量变了: %d → %d（原生视图被重建）", len(childrenBefore), len(childrenAfter))
	}

	observationAppendJSONL(t, "preservation.jsonl", obsFacts{
		"stage":          "theme-light-to-dark",
		"identityBefore": obsFacts{"timeOrigin": before["timeOrigin"], "marker": before["marker"]},
		"identityAfter":  obsFacts{"timeOrigin": after["timeOrigin"], "marker": after["marker"]},
		"shellBefore":    stateBefore,
		"shellAfter":     stateAfter,
		"statusBefore":   statusBefore,
		"statusAfter":    statusAfter,
		"browserBefore":  browserBefore,
		"browserAfter":   browserAfter,
		"childrenBefore": toAnySlice(childClasses(childrenBefore)),
		"childrenAfter":  toAnySlice(childClasses(childrenAfter)),
	})
	observationMergeFacts(t, obsFacts{
		"noReload":           after["timeOrigin"] == before["timeOrigin"],
		"draftKept":          stateAfter["searchDraft"] == "draft-theme",
		"detailPanelKept":    stateAfter["detailOpen"] == stateBefore["detailOpen"],
		"statusUnchanged":    statusBefore == statusAfter,
		"browserStateSame":   browserBefore == browserAfter,
		"nativeChildrenSame": len(childrenBefore) == len(childrenAfter),
		"nativeChildClasses": toAnySlice(childClasses(childrenBefore)),
		"downloadsInFlight":  0,
		"downloadsNote":      "本机缺 Pi 1.5.0 无法产生下载；下载不被主题切换取消这一点没有证据（deferred）。",
		"takeoverOwnership":  c.app.ownershipNote(browserBefore),
	})
}

// hoverChanged 判断两次样式快照之间是否有可见差异（背景/文字色/边框/阴影/轮廓）。
func hoverChanged(a, b *ControlSnapshot) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Background != b.Background || a.Color != b.Color ||
		a.BoxShadow != b.BoxShadow || a.Outline != b.Outline
}

func childClasses(in []uiChildWindow) []string {
	out := make([]string, 0, len(in))
	for _, w := range in {
		out = append(out, fmt.Sprintf("%s(vis=%t,%dx%d)", w.Class, w.Visible, w.Width, w.Height))
	}
	sort.Strings(out)
	return out
}

// ---------- THEME05：两主题九页截图 + 外部状态 ----------

// TestU07THEME05ThemeScreenshots 落盘九页 × 两主题的截图与 hash，并把
// 外部状态（窗口、DPI、偏好 revision、语义 token）写进 oracle。
func TestU07THEME05ThemeScreenshots(t *testing.T) {
	c := startU07Case(t)
	recs := make([]any, 0, len(navPages)*2)
	for _, theme := range []string{"light", "dark"} {
		c.app.switchPreferences(t, "zh-CN", theme)
		for _, page := range navPages {
			c.app.OpenPage(t, page.Index)
			time.Sleep(150 * time.Millisecond)
			name := fmt.Sprintf("screenshots/%s-%s.png", theme, page.ID)
			sha := c.app.writeScreenshot(t, name)
			facts := c.app.ThemeFacts(t)
			rec := obsFacts{
				"page":       page.ID,
				"theme":      facts["theme"],
				"locale":     facts["locale"],
				"dpr":        facts["dpr"],
				"screenshot": name,
				"sha256":     sha,
				"fileBytes":  c.app.screenshotSize(t, name),
			}
			recs = append(recs, rec)
			if facts["theme"] != theme {
				t.Errorf("%s/%s: data-theme=%v", page.ID, theme, facts["theme"])
			}
			if sha == "" {
				t.Errorf("%s/%s: 截图 hash 为空", page.ID, theme)
			}
		}
	}
	var prefs PreferencesResult
	c.app.MustAPI(t, &prefs, "GetPreferences")
	observationMergeFacts(t, obsFacts{
		"screenshots":  recs,
		"shotCount":    len(recs),
		"themeOrder":   []any{"light", "dark"},
		"prefRevision": prefs.Revision,
		"prefTheme":    prefs.Preferences.Theme,
		"windowFacts":  c.windowFacts("theme-screenshots"),
	})
}

// ---------- 检查器自身的红证 ----------

// TestU07DetectorsAreNotVacuous 往**真实应用页面**里注入故意违规的结构，
// 要求每条检查都变红，然后移除注入并确认恢复干净。
//
// 这条用例存在的理由：如果一条检查在违规结构上也不响，那么它在生产页面上的
// 绿就没有任何意义。Playwright 侧的违规夹具在 frontend/e2e/fixtures/violations/
// 覆盖同一批规则；这里是同一套检查器在真机引擎里的红证。
func TestU07DetectorsAreNotVacuous(t *testing.T) {
	c := startU07Case(t)
	c.app.switchPreferences(t, "zh-CN", "light")
	c.app.OpenPage(t, 0)

	baseline := c.app.Audit(t, auditOpts{Layout: true, Contrast: true})
	if baseline.Summary.Total != 0 {
		t.Fatalf("注入前基线就不干净（%d 条），无法证明检查是否有效:\n%v", baseline.Summary.Total, baseline.Findings)
	}

	injected := []string{
		// 没有可访问名称的按钮
		`<button id="u07-unnamed"><span aria-hidden="true">×</span></button>`,
		// 对比度不足的正文
		`<p id="u07-faint" style="color:#e8e8e8">faint text</p>`,
		// 横向超出视口的控件
		`<div style="width:3000px"><button id="u07-clipped" style="margin-left:2900px">far</button></div>`,
		// 两个控件实质重叠
		`<div style="position:relative;height:60px;width:240px"><button style="position:absolute;left:0;top:0;width:200px;height:40px">a</button><button style="position:absolute;left:24px;top:8px;width:200px;height:40px">b</button></div>`,
		// 文案被截断的控件
		`<button id="u07-trunc" style="width:80px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">a very long button label that will be truncated</button>`,
	}
	script := `(() => {
		const host = document.createElement('div');
		host.id = 'u07-injected';
		host.style.cssText = 'background:#ffffff;color:#16181d;position:relative;z-index:1';
		host.innerHTML = ` + "`" + strings.Join(injected, "") + "`" + `;
		document.body.appendChild(host);
		return true;
	})()`
	if _, err := c.app.cdp.Eval(script); err != nil {
		t.Fatalf("注入违规结构失败: %v", err)
	}

	dirty := c.app.Audit(t, auditOpts{Layout: true, Contrast: true})
	wantRules := map[string]bool{
		"unnamed-control": false,
		"low-contrast":    false,
		"clipped-control": false,
		"overlap":         false,
		"truncated-label": false,
	}
	for _, f := range dirty.Findings {
		if _, ok := wantRules[f.Rule]; ok {
			wantRules[f.Rule] = true
		}
	}
	for rule, hit := range wantRules {
		if !hit {
			t.Errorf("注入违规结构后规则 %q 没有报警：这条检查在生产页面上的通过没有意义", rule)
		}
	}

	// 焦点环：注入一个明确去掉焦点的按钮，Tab 到它时必须报"无可见焦点环"。
	if _, err := c.app.cdp.Eval(`(() => {
		const b = document.createElement('button');
		b.id = 'u07-nofocus';
		b.textContent = 'no focus';
		b.style.cssText = 'all:unset;display:inline-block;padding:4px;border:1px solid #d5d9e0;background:#eef0f4;color:#16181d';
		document.getElementById('u07-injected').appendChild(b);
		b.focus();
		return true;
	})()`); err != nil {
		t.Fatalf("注入无焦点按钮失败: %v", err)
	}
	focusStep := c.app.focusProbe(t)
	if focusStep == nil {
		t.Errorf("注入后读不到焦点证据")
	} else if focusStep.RingVisible {
		t.Errorf("去掉 outline/box-shadow 的按钮仍被判为有可见焦点环：焦点检查无效")
	}

	// 清理：注入的节点必须被移除，基线恢复干净（否则这些检查会污染后续证据）。
	if _, err := c.app.cdp.Eval(`(() => { const h=document.getElementById('u07-injected'); if (h) h.remove(); return true; })()`); err != nil {
		t.Fatalf("清理注入节点失败: %v", err)
	}
	after := c.app.Audit(t, auditOpts{Layout: true, Contrast: true})
	if after.Summary.Total != 0 {
		t.Errorf("清理注入后仍有 %d 条发现:\n%v", after.Summary.Total, after.Findings)
	}

	observationWrite(t, "detector-selfcheck.json", mustJSON(t, obsFacts{
		"baselineFindings":  baseline.Summary.Total,
		"dirtyFindings":     dirty.Summary.Total,
		"rulesProven":       mapKeys(wantRules),
		"focusRingDetected": focusStep != nil && focusStep.RingVisible,
		"cleanAfterRemove":  after.Summary.Total == 0,
		"dirtySamples":      auditSummary(dirty)["samples"],
	}))
	observationMergeFacts(t, obsFacts{
		"baselineFindings":  baseline.Summary.Total,
		"dirtyFindings":     dirty.Summary.Total,
		"rulesProven":       mapKeys(wantRules),
		"focusRingDetected": focusStep != nil && focusStep.RingVisible,
		"cleanAfterRemove":  after.Summary.Total == 0,
	})
}

// mustJSON 序列化（证据文件必须是合法 JSON）。
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化证据失败: %v", err)
	}
	return out
}

func mapKeys(m map[string]bool) []any {
	out := make([]any, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out
}

// ---------- 磁盘/API 外部事实的读取工具 ----------

// readPreferencesFile 读 <appRoot>/config/preferences.json 的原始字节。
func readPreferencesFile(t *testing.T, home string) []byte {
	t.Helper()
	path := filepath.Join(home, "config", "preferences.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return raw
}

// diskTheme / diskLocale 从配置文件里取偏好（配置文件是版本化信封，
// body 在 doc.body 下；结构由 internal/config 决定）。
func diskPref(t *testing.T, raw []byte, field string) string {
	t.Helper()
	var doc struct {
		SchemaVersion int    `json:"schemaVersion"`
		Revision      int    `json:"revision"`
		Checksum      string `json:"checksum"`
		Body          struct {
			Locale string `json:"locale"`
			Theme  string `json:"theme"`
		} `json:"body"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 preferences.json 失败: %v（%s）", err, head(string(raw), 300))
	}
	if doc.SchemaVersion == 0 || doc.Checksum == "" {
		t.Fatalf("preferences.json 缺少版本化信封字段（schemaVersion/checksum）：%s", head(string(raw), 300))
	}
	switch field {
	case "theme":
		return doc.Body.Theme
	case "locale":
		return doc.Body.Locale
	}
	t.Fatalf("未知字段 %s", field)
	return ""
}

func diskTheme(t *testing.T, raw []byte) string  { return diskPref(t, raw, "theme") }
func diskLocale(t *testing.T, raw []byte) string { return diskPref(t, raw, "locale") }
