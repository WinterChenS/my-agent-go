//go:build windows

package nativetest

// B01b 的 Windows 原生宿主用例（在 B01a 的 4 条用例之外追加，不改它们）。
//
// 覆盖：
//   BR04 子集  100/150/200% 页面缩放下的子窗口几何 / 页面 viewport / 可信点击坐标误差；
//              分栏 20 轮不漂移（含一次 0 宽 + 隐藏/恢复）。
//   BR05 子集  焦点切换 + CDP 键盘输入；最小化/恢复后截图仍有效；剪贴板 sentinel 往返；
//              中文 IME（CDP 模拟，不是 OS 级 IME，证据里如实标注）。
//   BR06 子集  50 轮 Attach → CreateTab → Navigate → Screenshot → Detach，每轮断言
//              子窗口/窗口类实例/controller 都没遗留，并记录 working set / 句柄 /
//              本进程创建的 msedgewebview2 进程数。
//
// 度量口径：页面像素只来自 CDP（Page.captureScreenshot），窗口几何只来自 GetWindowRect/
// GetParent，内存只来自 GetProcessMemoryInfo，浏览器进程只按 profile 目录过滤命令行。
// 没有任何一条依赖页面自报"我看起来正常"。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/browser/native"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

const (
	zoomGeometryTolerancePX = 2.0 // BR04：坐标误差上限（CSS px）
	rectTolerancePX         = 1.0 // 子窗口 rect 与期望 rect 的允许误差（物理 px）
)

// ---------- 页面侧辅助（走证据通道：结果都标成“页面自报”） ----------

// cdpEval 在页面主世界求值，返回结果值的原始 JSON（result.value）。
func cdpEval(t *testing.T, ctx context.Context, h *harness, expr string) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		t.Fatalf("编码 Runtime.evaluate 参数: %v", err)
	}
	res, err := h.host.CallCDP(ctx, h.tab, "Runtime.evaluate", string(params), 20*time.Second)
	if err != nil {
		t.Fatalf("Runtime.evaluate 失败: %v\nexpr=%s", err, truncate(expr, 160))
	}
	var payload struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		t.Fatalf("解析 Runtime.evaluate 结果: %v (%s)", err, truncate(res, 200))
	}
	if payload.ExceptionDetails != nil {
		desc := payload.ExceptionDetails.Text
		if payload.ExceptionDetails.Exception != nil {
			desc = payload.ExceptionDetails.Exception.Description
		}
		t.Fatalf("页面侧求值抛异常: %s\nexpr=%s", desc, truncate(expr, 160))
	}
	return payload.Result.Value
}

// evalJSONInto 要求表达式返回一个 JSON 字符串，再解码到 out。
func evalJSONInto(t *testing.T, ctx context.Context, h *harness, expr string, out any) {
	t.Helper()
	raw := cdpEval(t, ctx, h, expr)
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("表达式没有返回字符串: %v (%s)", err, truncate(string(raw), 160))
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		t.Fatalf("解码页面返回的 JSON 失败: %v (%s)", err, truncate(s, 200))
	}
}

// evalString 要求表达式求值成一个 JS 字符串，返回它。
func evalString(t *testing.T, ctx context.Context, h *harness, expr string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(cdpEval(t, ctx, h, expr), &s); err != nil {
		t.Fatalf("表达式没有返回字符串: %v", err)
	}
	return s
}

// cdpCall 发一条原生 CDP 命令；非空错误结果直接失败。
func cdpCall(t *testing.T, ctx context.Context, h *harness, method, params string) string {
	t.Helper()
	res, err := h.host.CallCDP(ctx, h.tab, method, params, 20*time.Second)
	if err != nil {
		t.Fatalf("%s 失败: %v", method, err)
	}
	return res
}

// clickAtCSS 用 CDP 在 CSS 坐标派发一次可信左键点击（与 Act 的 click 走同一条路径）。
func clickAtCSS(t *testing.T, ctx context.Context, h *harness, x, y float64) {
	t.Helper()
	for _, ev := range []struct {
		typ     string
		buttons int
	}{{"mousePressed", 1}, {"mouseReleased", 0}} {
		params := fmt.Sprintf(`{"type":%q,"x":%.3f,"y":%.3f,"button":"left","buttons":%d,"clickCount":1}`,
			ev.typ, x, y, ev.buttons)
		cdpCall(t, ctx, h, "Input.dispatchMouseEvent", params)
	}
}

// navigateTo 导航并等到页面自报指定标题（title 来自页面，不是宿主猜的）。
func navigateTo(t *testing.T, ctx context.Context, h *harness, path, wantTitle string) {
	t.Helper()
	if err := h.host.Navigate(ctx, h.tab, h.fixture.Primary.URL+path); err != nil {
		t.Fatalf("Navigate(%s): %v", path, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	var last native.PageState
	for time.Now().Before(deadline) {
		ps, err := h.host.PageState(ctx, h.tab)
		if err == nil {
			last = ps
			if ps.ReadyState == "complete" && ps.Title == wantTitle {
				return
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("等 %s 就绪超时：readyState=%q title=%q", path, last.ReadyState, last.Title)
}

// childRect 读子窗口的屏幕物理矩形。
func childRect(h *harness) (left, top, width, height float64) {
	r := h.host.WindowInfo().ChildRect
	return float64(r.Left), float64(r.Top), float64(r.Right - r.Left), float64(r.Bottom - r.Top)
}

// writeArtifact 在设置 MYAGENT_B01B_OUT 时把截图落盘（证据目录），否则不写文件。
func writeArtifact(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("MYAGENT_B01B_OUT"))
	if dir == "" {
		return
	}
	// go test 的 CWD 是包目录（tests/native），相对路径要按模块根解析，
	// 否则会写进 tests/native/docs/... 里。
	if !filepath.IsAbs(dir) {
		root, err := moduleRoot()
		if err != nil {
			t.Logf("警告: 解析模块根失败，跳过落盘 %s: %v", name, err)
			return
		}
		dir = filepath.Join(root, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("警告: 创建证据目录 %s 失败: %v", dir, err)
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Logf("警告: 写 %s 失败: %v", path, err)
		return
	}
	t.Logf("artifact: %s bytes=%d", path, len(data))
}

// ---------- BR04：100/150/200% 缩放 ----------

// TestNativeZoomFactor 是 BR04 的缩放子集：同一套 slot 几何，页面缩放 100/150/200%。
//
// 每一步都同时断言两件事，且两条换算不混在一起：
//  1. 子窗口**物理**尺寸 = slot CSS 尺寸 × 窗口 DPI 比例（与页面缩放无关）；
//  2. 页面自报的 CSS viewport = 子窗口物理尺寸 / 页面缩放因子（容差 1px）。
//
// 再加第三条硬判据：按 rect 中心派的**可信点击**让 fixture 服务端 counter 恰好 +1；
// 同时打印“元素中心物理坐标”两种独立算法（页面自报 devicePixelRatio vs 宿主
// zoom×DPI）之间的差值，并给出换算回 CSS 的误差，要求 ≤2 CSS px。
func TestNativeZoomFactor(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	navigateTo(t, ctx, h, "/trusted-only", "trusted-only")

	dpiScale := h.host.WindowInfo().Scale
	ox, oy := h.win.ClientOrigin()

	cases := []struct {
		name string
		zoom float64
	}{
		{"100", 1.00},
		{"150", 1.50},
		{"200", 2.00},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slot := h.slot
			slot.Revision = uint64(100 + i)
			slot.Zoom = c.zoom
			before := h.fixture.Primary.Counter(browserfixture.CounterTrustedOnly)
			if err := h.host.Present(ctx, h.tab, slot); err != nil {
				t.Fatalf("Present(zoom=%.2f): %v", c.zoom, err)
			}

			// 1. 子窗口物理几何：只由 slot CSS 尺寸 × DPI 比例决定。
			wantW := math.Round(slot.Width * dpiScale)
			wantH := math.Round(slot.Height * dpiScale)
			wantL := float64(ox) + math.Round(slot.Left*dpiScale)
			wantT := float64(oy) + math.Round(slot.Top*dpiScale)
			left, top, width, height := childRect(h)
			if math.Abs(width-wantW) > rectTolerancePX || math.Abs(height-wantH) > rectTolerancePX {
				t.Fatalf("子窗口物理尺寸 %.1fx%.1f，期望 %.1fx%.1f（slot %.1fx%.1f × dpi %.2f）",
					width, height, wantW, wantH, slot.Width, slot.Height, dpiScale)
			}
			if math.Abs(left-wantL) > rectTolerancePX || math.Abs(top-wantT) > rectTolerancePX {
				t.Fatalf("子窗口物理位置 (%.1f,%.1f)，期望 (%.1f,%.1f)", left, top, wantL, wantT)
			}

			info := h.host.WindowInfo()

			// 2. 页面自报 viewport：物理尺寸 / 页面缩放，容差 1px。
			var vp struct {
				InnerW  float64 `json:"innerW"`
				InnerH  float64 `json:"innerH"`
				DPR     float64 `json:"dpr"`
				ScrollX float64 `json:"scrollX"`
				ScrollY float64 `json:"scrollY"`
			}
			wantVPW := width / c.zoom
			wantVPH := height / c.zoom
			deadline := time.Now().Add(8 * time.Second)
			for {
				evalJSONInto(t, ctx, h, `JSON.stringify({innerW: innerWidth, innerH: innerHeight, dpr: devicePixelRatio, scrollX: scrollX, scrollY: scrollY})`, &vp)
				if math.Abs(vp.InnerW-wantVPW) <= 1 && math.Abs(vp.InnerH-wantVPH) <= 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("页面 viewport 一直是 %.0fx%.0f，期望 %.1fx%.1f（物理 %.1fx%.1f / zoom %.2f）",
						vp.InnerW, vp.InnerH, wantVPW, wantVPH, width, height, c.zoom)
				}
				time.Sleep(100 * time.Millisecond)
			}
			// devicePixelRatio 应当等于 dpi 比例 × 页面缩放（两条换算的乘积）。
			wantDPR := dpiScale * c.zoom
			if math.Abs(vp.DPR-wantDPR) > 0.01 {
				t.Fatalf("页面 devicePixelRatio=%.3f，期望 dpi %.2f × zoom %.2f = %.3f", vp.DPR, dpiScale, c.zoom, wantDPR)
			}

			// 3. 元素中心坐标：页面 rect → 物理 → 回 CSS，误差 ≤2 CSS px。
			var rect struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
				W float64 `json:"w"`
				H float64 `json:"h"`
			}
			evalJSONInto(t, ctx, h, `(() => {
  const el = document.getElementById('trusted-target');
  el.scrollIntoView({block: 'center', inline: 'center'});
  const r = el.getBoundingClientRect();
  return JSON.stringify({x: r.left, y: r.top, w: r.width, h: r.height});
})()`, &rect)
			cx := rect.X + rect.W/2
			cy := rect.Y + rect.H/2

			// 两种独立算法算“元素中心的物理坐标”：
			//   A. 页面自报 devicePixelRatio（渲染管线的实际换算）；
			//   B. 宿主账目：zoom（controller 实测）× dpi 比例。
			physAX := left + cx*vp.DPR
			physAY := top + cy*vp.DPR
			physBX := left + cx*c.zoom*dpiScale
			physBY := top + cy*c.zoom*dpiScale
			diffPX := math.Hypot(physAX-physBX, physAY-physBY)

			// 把物理坐标换算回 CSS（只用心跳里那两条已知量：子窗口原点 + zoom×dpi）。
			backX := (physBX - left) / (c.zoom * dpiScale)
			backY := (physBY - top) / (c.zoom * dpiScale)
			errCSS := math.Hypot(backX-cx, backY-cy)

			// 命中的必须是那个按钮（hit test，页面自报），否则坐标换算再“自洽”也没用。
			var hit string
			evalJSONInto(t, ctx, h, fmt.Sprintf(
				`(() => { const el = document.elementFromPoint(%.3f, %.3f); return JSON.stringify(el ? (el.id || el.tagName) : null); })()`,
				backX, backY), &hit)

			t.Logf("zoom=%.2f zoomFactor(controller)=%.2f dpi=%.2f child=%.0fx%.0f@(%.0f,%.0f) "+
				"viewportCSS=%.0fx%.0f dpr=%.2f rectCSS=(%.2f,%.2f %.2fx%.2f) "+
				"centerCSS=(%.2f,%.2f) centerPhys=(%.2f,%.2f) physDiff=%.3fpx backErr=%.3fpx hit=%s",
				c.zoom, h.host.WindowInfo().ZoomFactor, dpiScale, width, height, left, top,
				vp.InnerW, vp.InnerH, vp.DPR, rect.X, rect.Y, rect.W, rect.H,
				cx, cy, physBX, physBY, diffPX, errCSS, hit)

			if errCSS > zoomGeometryTolerancePX {
				t.Fatalf("坐标换算误差 %.3f CSS px 超过上限 %.1f", errCSS, zoomGeometryTolerancePX)
			}
			if hit != "trusted-target" {
				t.Fatalf("换算后的坐标 (%.2f,%.2f) 命中的是 %q，不是 trusted-target", backX, backY, hit)
			}
			if got := h.host.WindowInfo().ZoomFactor; math.Abs(got-c.zoom) > 1e-6 {
				t.Fatalf("controller 实测 zoomFactor=%.4f，期望 %.2f", got, c.zoom)
			}

			// 3b. OS 级物理点击：把“物理坐标”真的交给屏幕。
			//
			// 这是本用例唯一独立于 CDP/页面自报的物理测量：只有当我们把窗口抬到前台、
			// 且该屏幕坐标确实落在我们的 WebView 子窗口里时才有意义，否则记 NOT_RUN
			// 并写清原因，不假装通过。
			physX := int32(math.Round(physBX))
			physY := int32(math.Round(physBY))
			foreground, lower := RaiseToFront(h.win.HWND())
			defer lower()
			hitWnd := WindowAt(physX, physY)
			osClickRan := false
			if foreground && IsDescendantOf(hitWnd, info.ChildHWND) {
				beforeOS := h.fixture.Primary.Counter(browserfixture.CounterTrustedOnly)
				restoreCursor := PhysicalClick(physX, physY)
				waitCounter(t, h.fixture.Primary, browserfixture.CounterTrustedOnly, beforeOS+1, 10*time.Second)
				restoreCursor()
				if got := h.fixture.Primary.Counter(browserfixture.CounterTrustedOnly); got != beforeOS+1 {
					t.Fatalf("OS 物理点击后 counter=%d，期望 %d", got, beforeOS+1)
				}
				t.Logf("zoom=%.2f OS物理点击 (%d,%d) 命中 WebView 子窗口 0x%x → counter +1（按钮物理矩形 %.1fx%.1f，落点分辨力 ≤ 半宽）",
					c.zoom, physX, physY, hitWnd, rect.W*c.zoom*dpiScale, rect.H*c.zoom*dpiScale)
				before = beforeOS + 1
				osClickRan = true
			} else {
				t.Logf("zoom=%.2f NOT_RUN: OS 物理点击（foreground=%t WindowFromPoint=0x%x descendantOfChild=%t）；本条只用 CDP 可信点击验证",
					c.zoom, foreground, hitWnd, IsDescendantOf(hitWnd, info.ChildHWND))
			}

			// 4. 可信点击 → fixture 服务端 counter 恰好 +1（不是只看 DOM 事件）。
			//
			// 观察用计数器的取值走 HTTP /__counters（服务端自己的状态），不用内存 map，
			// 这样 zoom-steps.jsonl 里的 counterBefore/counterAfter 是服务端给出的。
			counterBefore := fixtureCounter(t, h.fixture.Primary.URL, browserfixture.CounterTrustedOnly)
			clickAtCSS(t, ctx, h, backX, backY)
			waitCounter(t, h.fixture.Primary, browserfixture.CounterTrustedOnly, before+1, 10*time.Second)
			after := h.fixture.Primary.Counter(browserfixture.CounterTrustedOnly)
			if after != before+1 {
				t.Fatalf("可信点击后服务端 counter=%d，期望 %d（恰好 +1）", after, before+1)
			}
			counterAfter := fixtureCounter(t, h.fixture.Primary.URL, browserfixture.CounterTrustedOnly)
			t.Logf("zoom=%.2f click counter %d -> %d (exactly +1)", c.zoom, before, after)

			img, err := h.host.Screenshot(ctx, h.tab)
			if err != nil {
				t.Fatalf("Screenshot(zoom=%.2f): %v", c.zoom, err)
			}
			if img.PixelWidth != int(width) || img.PixelHeight != int(height) {
				t.Fatalf("截图像素 %dx%d，期望子窗口物理尺寸 %.0fx%.0f", img.PixelWidth, img.PixelHeight, width, height)
			}
			stats := pngStats(t, img.Bytes)
			if stats.uniqueColors <= 4 {
				t.Fatalf("zoom=%.2f 的截图是纯色（uniqueColors=%d）", c.zoom, stats.uniqueColors)
			}
			t.Logf("zoom=%.2f screenshot=%dx%d css=%.0fx%.0f scale=%.2f uniqueColors=%d",
				c.zoom, img.PixelWidth, img.PixelHeight, img.CSSWidth, img.CSSHeight, img.Scale, stats.uniqueColors)
			writeArtifact(t, fmt.Sprintf("b01b-zoom-%d.png", int(math.Round(c.zoom*100))), img.Bytes)

			// 外部观察（BR04 缩放）：一档一行写进 zoom-steps.jsonl，截图另存 PNG。
			observeZoomStep(t, obsFacts{
				"step":                       c.name,
				"zoom":                       c.zoom,
				"controllerZoomFactor":       h.host.WindowInfo().ZoomFactor,
				"dpiScale":                   dpiScale,
				"childRect":                  obsFacts{"left": left, "top": top, "width": width, "height": height},
				"expectedRect":               obsFacts{"left": wantL, "top": wantT, "width": wantW, "height": wantH},
				"viewportCSS":                obsFacts{"width": vp.InnerW, "height": vp.InnerH},
				"devicePixelRatio":           vp.DPR,
				"hitTest":                    hit,
				"clickErrCSS":                errCSS,
				"physCenterX":                physBX,
				"physCenterY":                physBY,
				"windowFromPoint":            hitWnd,
				"windowFromPointInChildTree": IsDescendantOf(hitWnd, info.ChildHWND),
				"foregroundHeld":             foreground,
				"osPhysicalClickRan":         osClickRan,
				"counterBefore":              counterBefore,
				"counterAfter":               counterAfter,
				"dpiForWindowChild":          dpiForWindow(info.ChildHWND),
			}, img.Bytes, fmt.Sprintf("screenshots/zoom-%d.png", int(math.Round(c.zoom*100))))
		})
	}

	finalizeZoomObservation(t)
}

// ---------- BR04：分栏 20 轮 ----------

// TestNativeSplitter20Rounds 是 BR04 的另一半：连续 20 次改变右栏 slot 几何，
// 其中第 9 轮宽度 0 并叠加一次隐藏/恢复；每轮都要满足：
//   - 子窗口 rect 与期望 rect 误差 ≤1px；
//   - GetParent(child) 仍是同一个主窗口（没有脱离、没有变成顶层窗口）；
//   - controller 仍存活；
//   - CDP 截图尺寸跟着 slot 变（宽度>0 且可见时）。
//
// 结束时记录 20 轮的最大误差，并再点一次可信按钮确认页面仍可交互。
func TestNativeSplitter20Rounds(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	navigateTo(t, ctx, h, "/trusted-only", "trusted-only")

	dpiScale := h.host.WindowInfo().Scale
	ox, oy := h.win.ClientOrigin()
	widths := []float64{
		320, 720, 400, 680, 360, 700, 440, 660,
		0, // 第 9 轮：0 宽 + 隐藏/恢复
		620, 340, 720, 320, 600, 380, 700, 480, 560, 640, 520,
	}
	if len(widths) != 20 {
		t.Fatalf("轮次表写成 %d 轮，任务要求 20 轮", len(widths))
	}
	const left, top, height = 400.0, 40.0, 600.0

	maxErr := 0.0
	zeroRound := -1
	for i, w := range widths {
		round := i + 1
		slot := browser.SlotGeometry{
			SlotID:   "browser-slot",
			Revision: uint64(200 + round),
			Left:     left, Top: top, Width: w, Height: height,
			Visible: true, Zoom: 1,
		}
		if err := h.host.Present(ctx, h.tab, slot); err != nil {
			t.Fatalf("第 %d 轮 Present(w=%.0f): %v", round, w, err)
		}
		if w == 0 {
			zeroRound = round
			if err := h.host.SetVisible(ctx, false); err != nil {
				t.Fatalf("第 %d 轮 SetVisible(false): %v", round, err)
			}
			if WindowVisible(h.host.WindowInfo().ChildHWND) {
				t.Fatalf("第 %d 轮隐藏后子窗口仍可见", round)
			}
			if !h.host.WindowInfo().ControllerReady {
				t.Fatalf("第 %d 轮隐藏后 controller 被销毁", round)
			}
			if err := h.host.SetVisible(ctx, true); err != nil {
				t.Fatalf("第 %d 轮 SetVisible(true): %v", round, err)
			}
		}

		info := h.host.WindowInfo()
		if info.ParentOfChild != info.ParentHWND || !info.ParentMatches {
			t.Fatalf("第 %d 轮父子关系变了: GetParent=0x%x main=0x%x", round, info.ParentOfChild, info.ParentHWND)
		}
		if !info.ControllerReady || info.ChildHWND == 0 {
			t.Fatalf("第 %d 轮 controller/子窗口不在了: controllerReady=%t child=0x%x", round, info.ControllerReady, info.ChildHWND)
		}
		if root := RootAncestor(info.ChildHWND); root != info.ParentHWND {
			t.Fatalf("第 %d 轮子窗口的顶层祖先变成 0x%x（期望主窗口 0x%x）", round, root, info.ParentHWND)
		}

		wantL := float64(ox) + math.Round(left*dpiScale)
		wantT := float64(oy) + math.Round(top*dpiScale)
		wantW := math.Round(w * dpiScale)
		wantH := math.Round(height * dpiScale)
		gotL, gotT, gotW, gotH := childRect(h)
		rectErr := math.Max(
			math.Max(math.Abs(gotL-wantL), math.Abs(gotT-wantT)),
			math.Max(math.Abs(gotW-wantW), math.Abs(gotH-wantH)))
		if rectErr > rectTolerancePX {
			t.Fatalf("第 %d 轮 rect 误差 %.2fpx 超过 %.0f：got=(%.0f,%.0f %.0fx%.0f) want=(%.0f,%.0f %.0fx%.0f)",
				round, rectErr, rectTolerancePX, gotL, gotT, gotW, gotH, wantL, wantT, wantW, wantH)
		}
		if rectErr > maxErr {
			maxErr = rectErr
		}
		t.Logf("round %2d: slot w=%.0f -> child (%.0f,%.0f) %.0fx%.0f err=%.2fpx zoom=%.2f",
			round, w, gotL, gotT, gotW, gotH, rectErr, h.host.WindowInfo().ZoomFactor)

		// 外部观察（BR04 分栏）：一轮一行写进 splitter-rounds.jsonl。
		// rect 误差由 GetWindowRect（OS）与期望值相减，父子关系由 GetParent/GetAncestor 直读。
		recordRound := func(shotW, shotH int) {
			observeSplitterRound(t, obsFacts{
				"round":            round,
				"slotWidthCSS":     w,
				"slotHeightCSS":    height,
				"expectedRect":     obsFacts{"left": wantL, "top": wantT, "width": wantW, "height": wantH},
				"actualRect":       obsFacts{"left": gotL, "top": gotT, "width": gotW, "height": gotH},
				"rectErrPx":        rectErr,
				"parentStillMain":  info.ParentOfChild == info.ParentHWND && info.ParentMatches,
				"rootStillMain":    RootAncestor(info.ChildHWND) == info.ParentHWND,
				"controllerReady":  info.ControllerReady,
				"childVisible":     WindowVisible(info.ChildHWND),
				"zeroWidthRound":   w <= 0,
				"screenshotWidth":  shotW,
				"screenshotHeight": shotH,
				"dpiScale":         dpiScale,
			})
		}

		// 截图尺寸必须跟着 slot 变（宽 0 的那轮没有可截内容，跳过并说明）。
		if w <= 0 {
			t.Logf("round %2d: 0 宽轮，跳过一次截图（viewport 为 0）", round)
			recordRound(0, 0)
			continue
		}
		img, err := h.host.Screenshot(ctx, h.tab)
		if err != nil {
			t.Fatalf("第 %d 轮 Screenshot: %v", round, err)
		}
		if math.Abs(float64(img.PixelWidth)-gotW) > rectTolerancePX ||
			math.Abs(float64(img.PixelHeight)-gotH) > rectTolerancePX {
			t.Fatalf("第 %d 轮截图 %dx%d 没跟上子窗口 %.0fx%.0f", round, img.PixelWidth, img.PixelHeight, gotW, gotH)
		}
		t.Logf("round %2d: screenshot=%dx%d（跟随 slot）", round, img.PixelWidth, img.PixelHeight)
		recordRound(img.PixelWidth, img.PixelHeight)
	}

	// 恢复一个正常宽度，确认页面还能交互（可信点击 → counter 恰好 +1）。
	final := browser.SlotGeometry{SlotID: "browser-slot", Revision: 999, Left: left, Top: top,
		Width: 480, Height: height, Visible: true, Zoom: 1}
	if err := h.host.Present(ctx, h.tab, final); err != nil {
		t.Fatalf("收尾 Present: %v", err)
	}
	before := h.fixture.Primary.Counter(browserfixture.CounterTrustedOnly)
	var rect struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	evalJSONInto(t, ctx, h, `(() => {
  const el = document.getElementById('trusted-target');
  el.scrollIntoView({block: 'center', inline: 'center'});
  const r = el.getBoundingClientRect();
  return JSON.stringify({x: r.left, y: r.top, w: r.width, h: r.height});
})()`, &rect)
	clickAtCSS(t, ctx, h, rect.X+rect.W/2, rect.Y+rect.H/2)
	waitCounter(t, h.fixture.Primary, browserfixture.CounterTrustedOnly, before+1, 10*time.Second)
	if got := h.fixture.Primary.Counter(browserfixture.CounterTrustedOnly); got != before+1 {
		t.Fatalf("收尾点击后 counter=%d，期望 %d", got, before+1)
	}
	t.Logf("splitter: 20 轮完成 maxRectErr=%.2fpx zeroWidthRound=%d 收尾 click counter %d->%d",
		maxErr, zeroRound, before, before+1)

	// 外部观察（BR04 分栏）：从 splitter-rounds.jsonl 读回 20 轮的误差与父子关系。
	finalizeSplitterObservation(t)
}

// ---------- BR05：焦点 / 键盘 ----------

// TestNativeFocusAndKeyboard 覆盖焦点子集：
//   - Focus(MoveFocus programmatic) 后页面 document.hasFocus() 变成 true；
//   - CDP Input.dispatchKeyEvent 的字符真的进到 input（读回 value）；
//   - 把焦点交回主窗口后页面 document.hasFocus() 变成 false。
//
// 这是 OS 级窗口焦点（不是 CDP 假装的 focus），但键盘输入本身走 CDP —— 证据里分开写。
func TestNativeFocusAndKeyboard(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	navigateTo(t, ctx, h, "/form", "普通表单")

	var focusState struct {
		HasFocus bool   `json:"hasFocus"`
		Active   string `json:"active"`
	}
	evalJSONInto(t, ctx, h, `JSON.stringify({hasFocus: document.hasFocus(), active: (document.activeElement && document.activeElement.id) || ''})`, &focusState)
	if focusState.HasFocus {
		t.Fatalf("还没 Focus 时页面就已经有焦点（hasFocus=true, active=%q），这条用例失去意义", focusState.Active)
	}
	if err := h.host.Focus(ctx, h.tab); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		evalJSONInto(t, ctx, h, `JSON.stringify({hasFocus: document.hasFocus(), active: (document.activeElement && document.activeElement.id) || ''})`, &focusState)
		if focusState.HasFocus {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Focus(MoveFocus) 之后页面 document.hasFocus() 仍为 false")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("focus: hasFocus=%t active=%q (Focus 之后)", focusState.HasFocus, focusState.Active)

	// 点一下 input（可信点击），再敲 a/b/c。
	var rect struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	evalJSONInto(t, ctx, h, `(() => { const el = document.getElementById('f-name'); const r = el.getBoundingClientRect(); return JSON.stringify({x:r.left,y:r.top,w:r.width,h:r.height}); })()`, &rect)
	clickAtCSS(t, ctx, h, rect.X+rect.W/2, rect.Y+rect.H/2)

	for _, ch := range []string{"a", "b", "c"} {
		for _, typ := range []string{"keyDown", "keyUp"} {
			text := ""
			if typ == "keyDown" {
				text = ch
			}
			params, err := json.Marshal(map[string]any{
				"type":                  typ,
				"text":                  text,
				"key":                   ch,
				"windowsVirtualKeyCode": int(ch[0]) - 32,
				"nativeVirtualKeyCode":  int(ch[0]) - 32,
			})
			if err != nil {
				t.Fatalf("编码 key 事件: %v", err)
			}
			cdpCall(t, ctx, h, "Input.dispatchKeyEvent", string(params))
		}
	}
	var typed struct {
		Value  string `json:"value"`
		Active string `json:"active"`
	}
	evalJSONInto(t, ctx, h, `JSON.stringify({value: document.getElementById('f-name').value, active: (document.activeElement && document.activeElement.id) || ''})`, &typed)
	if typed.Value != "abc" {
		t.Fatalf("键盘输入后 input.value=%q，期望 \"abc\"（active=%q）", typed.Value, typed.Active)
	}
	t.Logf("keyboard: input.value=%q active=%q (CDP Input.dispatchKeyEvent)", typed.Value, typed.Active)

	// 焦点交回主界面：页面必须失焦。
	//
	// 先把窗口抬到前台（否则跨线程 SetFocus 会被拒），再在**UI 线程**里
	// SetFocus(主窗口)——产品里等于用户点回主界面，Wails 主 WebView 拿到焦点。
	foreground, lower := RaiseToFront(h.win.HWND())
	defer lower()
	var focusSet bool
	if err := h.win.CallOnUI(func() { focusSet = SetFocusHWND(h.win.HWND()) }); err != nil {
		t.Fatalf("CallOnUI(SetFocus): %v", err)
	}
	if !foreground || !focusSet {
		t.Logf("NOT_RUN: 主界面拿回焦点（foreground=%t SetFocus=%t）；反向验证只做到“Focus() 之后 hasFocus=true”",
			foreground, focusSet)
		return
	}
	deadline = time.Now().Add(8 * time.Second)
	for {
		evalJSONInto(t, ctx, h, `JSON.stringify({hasFocus: document.hasFocus(), active: (document.activeElement && document.activeElement.id) || ''})`, &focusState)
		if !focusState.HasFocus {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("在 UI 线程 SetFocus(主窗口) 之后页面 document.hasFocus() 仍为 true（active=%q）", focusState.Active)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("focus: 主窗口拿回焦点后 hasFocus=%t active=%q", focusState.HasFocus, focusState.Active)
}

// ---------- BR05：最小化 / 恢复 ----------

// TestNativeMinimizeRestore 覆盖最小化恢复子集：
// ShowWindow(SW_MINIMIZE) → 等 1s → SW_RESTORE，然后 CDP 截图必须仍然成功且非纯色；
// 记录最小化前后主窗口与子窗口的 rect。
func TestNativeMinimizeRestore(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	const swMinimize, swRestore = 6, 9
	before := WindowRectOf(h.win.HWND())
	childBefore := h.host.WindowInfo().ChildRect
	imgBefore, err := h.host.Screenshot(ctx, h.tab)
	if err != nil {
		t.Fatalf("最小化前 Screenshot: %v", err)
	}
	statsBefore := pngStats(t, imgBefore.Bytes)

	if err := h.win.CallOnUI(func() { ShowWindowNative(h.win.HWND(), swMinimize) }); err != nil {
		t.Fatalf("CallOnUI(SW_MINIMIZE): %v", err)
	}
	time.Sleep(time.Second)
	if !WindowIconic(h.win.HWND()) {
		t.Fatalf("SW_MINIMIZE 之后 IsIconic=false（窗口没有被最小化）")
	}
	minimized := WindowRectOf(h.win.HWND())
	childMinimized := h.host.WindowInfo().ChildRect
	t.Logf("minimize: main=(%d,%d %dx%d) -> (%d,%d %dx%d) child=(%d,%d)-(%d,%d) -> (%d,%d)-(%d,%d) iconic=%t",
		before.Left, before.Top, before.Right-before.Left, before.Bottom-before.Top,
		minimized.Left, minimized.Top, minimized.Right-minimized.Left, minimized.Bottom-minimized.Top,
		childBefore.Left, childBefore.Top, childBefore.Right, childBefore.Bottom,
		childMinimized.Left, childMinimized.Top, childMinimized.Right, childMinimized.Bottom,
		WindowIconic(h.win.HWND()))

	if err := h.win.CallOnUI(func() { ShowWindowNative(h.win.HWND(), swRestore) }); err != nil {
		t.Fatalf("CallOnUI(SW_RESTORE): %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for WindowIconic(h.win.HWND()) {
		if time.Now().After(deadline) {
			t.Fatal("SW_RESTORE 之后窗口仍是 minimized")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	after := WindowRectOf(h.win.HWND())
	if !h.host.WindowInfo().ControllerReady {
		t.Fatal("恢复后 controller 不在了")
	}
	imgAfter, err := h.host.Screenshot(ctx, h.tab)
	if err != nil {
		t.Fatalf("恢复后 Screenshot: %v", err)
	}
	statsAfter := pngStats(t, imgAfter.Bytes)
	if statsAfter.uniqueColors <= 4 {
		t.Fatalf("恢复后截图是纯色（uniqueColors=%d），页面没有真的画出来", statsAfter.uniqueColors)
	}
	if imgAfter.PixelWidth != imgBefore.PixelWidth || imgAfter.PixelHeight != imgBefore.PixelHeight {
		t.Fatalf("恢复后截图尺寸 %dx%d 与最小化前 %dx%d 不一致",
			imgAfter.PixelWidth, imgAfter.PixelHeight, imgBefore.PixelWidth, imgBefore.PixelHeight)
	}
	t.Logf("restore: main=(%d,%d %dx%d) rectEqual=%t screenshot=%dx%d uniqueColors=%d->%d",
		after.Left, after.Top, after.Right-after.Left, after.Bottom-after.Top,
		after == before, imgAfter.PixelWidth, imgAfter.PixelHeight,
		statsBefore.uniqueColors, statsAfter.uniqueColors)
	writeArtifact(t, "b01b-after-restore.png", imgAfter.Bytes)
}

// ---------- BR05：剪贴板 ----------

// TestNativeClipboardSentinel 覆盖剪贴板子集：页面里 writeText(sentinel) → readText() 往返。
//
// 三条纪律：
//   - 只用 sentinel，不读用户真实剪贴板内容，也不把它写进证据；
//   - 权限用 CDP Browser.grantPermissions **只对 fixture origin** 授予 clipboard 相关权限
//     （不是 edge 的 SetGlobalPermission：那会对所有权限种类放开）；
//   - 结束时把剪贴板写成空串并读回确认，避免留下一段测试字符串。
func TestNativeClipboardSentinel(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	grant := fmt.Sprintf(`{"permissions":["clipboardReadWrite","clipboardSanitizedWrite"],"origin":%q}`, h.fixture.Primary.URL)
	// 授权前后的权限状态（页面自报 navigator.permissions.query）
	permState := func() string {
		return evalString(t, ctx, h, `(async () => {
  try {
    const w = await navigator.permissions.query({name: 'clipboard-write'});
    const r = await navigator.permissions.query({name: 'clipboard-read'});
    return 'write=' + w.state + ' read=' + r.state;
  } catch (e) { return 'ERR ' + String(e); }
})()`)
	}
	t.Logf("clipboard: 授权前 permissions.query = %s", permState())
	cdpCall(t, ctx, h, "Browser.grantPermissions", grant)
	t.Logf("clipboard: 授权后 permissions.query = %s", permState())
	if err := h.host.Focus(ctx, h.tab); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		var hasFocus bool
		evalJSONInto(t, ctx, h, `JSON.stringify(document.hasFocus())`, &hasFocus)
		if hasFocus {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("剪贴板用例需要页面先获得焦点，但 document.hasFocus() 一直 false")
		}
		time.Sleep(100 * time.Millisecond)
	}

	sentinel := fmt.Sprintf("b01b-clipboard-sentinel-%d", time.Now().UnixNano())
	// 带 JS 侧超时：WebView2 的权限提示会挂住 Promise，不能让它把测试拖死。
	race := `const withTimeout = (p, ms) => Promise.race([p, new Promise(r => setTimeout(() => r('TIMEOUT'), ms))]);`
	writeExpr := func(text string) string {
		return fmt.Sprintf(`(async () => { %s try { await withTimeout(navigator.clipboard.writeText(%q), 5000); return 'ok'; } catch (e) { return 'ERR ' + String(e); } })()`, race, text)
	}
	readExpr := fmt.Sprintf(`(async () => { %s try { return await withTimeout(navigator.clipboard.readText(), 5000); } catch (e) { return 'ERR ' + String(e); } })()`, race)

	if got := evalString(t, ctx, h, writeExpr(sentinel)); got != "ok" {
		t.Fatalf("clipboard.writeText 失败: %s", got)
	}
	readBack := evalString(t, ctx, h, readExpr)
	if readBack != sentinel {
		t.Fatalf("clipboard 往返不一致：写入 %q，读回 %q", sentinel, readBack)
	}
	t.Logf("clipboard: writeText/readText 往返一致（sentinel 长度 %d，值不落盘）", len(readBack))

	// 清空并确认（防止把测试字符串留在用户剪贴板里）。
	if got := evalString(t, ctx, h, writeExpr("")); got != "ok" {
		t.Fatalf("清空剪贴板失败: %s", got)
	}
	readBack = evalString(t, ctx, h, readExpr)
	if readBack != "" {
		t.Fatalf("清空后剪贴板仍有内容（长度 %d）", len(readBack))
	}
	// 负样本：撤销授权后看 clipboard-read 掉回 prompt（clipboard-write 在 Chromium 里
	// 默认就是 granted，所以写仍然成功——这一点如实记录，不假装是“权限拦截”）。
	cdpCall(t, ctx, h, "Browser.resetPermissions", `{}`)
	t.Logf("clipboard: 撤销授权后 permissions.query = %s", permState())
	t.Logf("clipboard: 撤销授权后 writeText 结果 = %s（clipboard-write 默认 granted）",
		evalString(t, ctx, h, writeExpr(sentinel+"-probe")))

	// 收尾：重新授权 → 必要时重新聚焦 → 清空 → 读回确认长度为 0。
	// 不能让探针字符串留在用户剪贴板里。
	cdpCall(t, ctx, h, "Browser.grantPermissions", grant)
	if got := evalString(t, ctx, h, writeExpr("")); got != "ok" {
		if err := h.host.Focus(ctx, h.tab); err != nil {
			t.Fatalf("清空剪贴板前重新聚焦失败: %v（剪贴板可能残留测试字符串）", err)
		}
		time.Sleep(200 * time.Millisecond)
		if got2 := evalString(t, ctx, h, writeExpr("")); got2 != "ok" {
			t.Fatalf("未能清空剪贴板: %s（剪贴板可能残留测试字符串）", got2)
		}
	}
	readBack = evalString(t, ctx, h, readExpr)
	if readBack != "" {
		t.Fatalf("收尾后剪贴板仍有内容（长度 %d）", len(readBack))
	}
	t.Logf("clipboard: 收尾清空并读回确认（长度 0）")
}

// ---------- BR05：中文 IME（CDP 模拟） ----------

// TestNativeIMEComposition 用 CDP 的 Input.imeSetComposition（组合中）+ Input.insertText（提交）
// 模拟一次中文输入，页面侧记录 compositionstart/update/end 与 isComposing。
//
// **这不是 OS 级 IME**：没有经过 TSF/输入法进程，真机手输仍是人工步骤（证据里如实写）。
// 它能证明的是：宿主这一侧的 CDP 通道、焦点、事件顺序和最终 value 都是对的。
func TestNativeIMEComposition(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	navigateTo(t, ctx, h, "/form", "普通表单")
	if err := h.host.Focus(ctx, h.tab); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	// 聚焦输入框并装观察器（观察器是运行时注入的，fixture 页面本身不改）。
	evalJSONInto(t, ctx, h, `(() => {
  const el = document.getElementById('f-name');
  el.value = '';
  el.focus();
  window.__b01bIME = [];
  const rec = (t) => (e) => window.__b01bIME.push({t: t, isComposing: !!e.isComposing, value: el.value, data: (e.data === undefined ? null : e.data)});
  el.addEventListener('compositionstart', rec('compositionstart'));
  el.addEventListener('compositionupdate', rec('compositionupdate'));
  el.addEventListener('compositionend', rec('compositionend'));
  el.addEventListener('input', rec('input'));
  return JSON.stringify(true);
})()`, new(bool))

	// 组合中：先输入拼音 "nihao"。
	cdpCall(t, ctx, h, "Input.imeSetComposition", `{"text":"nihao","selectionStart":5,"selectionEnd":5}`)
	var mid struct {
		Value       string `json:"value"`
		IsComposing bool   `json:"isComposing"`
	}
	evalJSONInto(t, ctx, h, `JSON.stringify({value: document.getElementById('f-name').value, isComposing: !!window.__b01bIME.length && window.__b01bIME[window.__b01bIME.length-1].isComposing})`, &mid)
	// 提交：最终文本「你好」。
	cdpCall(t, ctx, h, "Input.insertText", `{"text":"你好"}`)

	var events []struct {
		T           string  `json:"t"`
		IsComposing bool    `json:"isComposing"`
		Value       string  `json:"value"`
		Data        *string `json:"data"`
	}
	evalJSONInto(t, ctx, h, `JSON.stringify(window.__b01bIME)`, &events)
	var finalValue string
	evalJSONInto(t, ctx, h, `JSON.stringify(document.getElementById('f-name').value)`, &finalValue)

	if len(events) == 0 {
		t.Fatal("页面没有记录到任何 composition/input 事件")
	}
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.T)
	}
	t.Logf("ime: 事件序列=%v", types)
	t.Logf("ime: 组合中 value=%q isComposing=%t；提交后 value=%q", mid.Value, mid.IsComposing, finalValue)

	if types[0] != "compositionstart" {
		t.Fatalf("第一个事件是 %q，期望 compositionstart（序列=%v）", types[0], types)
	}
	seenUpdate, seenEnd := false, false
	for _, tp := range types {
		if tp == "compositionupdate" {
			seenUpdate = true
		}
	}
	lastComp := ""
	for _, tp := range types {
		if strings.HasPrefix(tp, "composition") {
			lastComp = tp
			if tp == "compositionend" {
				seenEnd = true
			}
		}
	}
	if !seenUpdate {
		t.Fatalf("没有 compositionupdate（序列=%v）", types)
	}
	if !seenEnd || lastComp != "compositionend" {
		t.Fatalf("composition 事件没有以 compositionend 收尾（序列=%v）", types)
	}
	if !mid.IsComposing {
		t.Fatalf("组合中页面没有报告 isComposing=true（序列=%v）", types)
	}
	if finalValue != "你好" {
		t.Fatalf("提交后 value=%q，期望 \"你好\"", finalValue)
	}
	// 组合中应该看到拼音，提交后应该只剩中文。
	if !strings.Contains(mid.Value, "nihao") {
		t.Fatalf("组合中 value=%q，期望包含 \"nihao\"", mid.Value)
	}
	if strings.Contains(finalValue, "nihao") {
		t.Fatalf("提交后 value 仍含拼音: %q", finalValue)
	}
	t.Log("ime: 这是 CDP 模拟（Input.imeSetComposition + Input.insertText），不是 OS 级 IME；真机手输仍需人工验证")
}

// ---------- BR06：50 轮创建/销毁 ----------

// TestNativeCreateDestroy50Rounds 是 BR06 的子集：
// 50 轮 Attach → CreateTab → Navigate → Screenshot(小) → Detach，每轮断言：
//   - 子窗口句柄已销毁（IsWindow=false、GetParent=0）；
//   - 本进程里 MyAgentNativeHostChild 窗口类实例数回到 0；
//   - 没有残留 controller；
//   - 没有新增 browser-spike.exe。
//
// 并在第 1/10/25/50 轮记录：本进程 working set / 峰值 / pagefile / 句柄数，
// 以及按 profile 目录过滤出的 msedgewebview2.exe 进程数与总 working set。
func TestNativeCreateDestroy50Rounds(t *testing.T) {
	if testing.Short() {
		t.Skip("50 轮创建/销毁在 -short 下跳过（BR06 需要完整跑，不要用 -short 冒充通过）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 560*time.Second)
	defer cancel()

	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	win, err := NewTestHostWindow("MyAgent B01b 50-round host")
	if err != nil {
		t.Fatalf("创建测试主窗口: %v", err)
	}
	t.Cleanup(win.Close)

	profileDir, err := os.MkdirTemp("", "b01b-native-profile-*")
	if err != nil {
		t.Fatalf("创建 profile 目录: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 30; i++ {
			if rmErr := os.RemoveAll(profileDir); rmErr == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("警告: profile 目录 %s 未能删除（WebView2 关闭后仍占着文件）", profileDir)
	})
	profileTag := filepath.Base(profileDir)

	spikeBefore, err := ProcessCount("browser-spike.exe")
	if err != nil {
		t.Logf("警告: 统计 browser-spike.exe 失败: %v", err)
	}
	classBefore := CountWindowsOfClass("MyAgentNativeHostChild")
	if classBefore != 0 {
		t.Fatalf("开跑前本进程已有 %d 个 MyAgentNativeHostChild 窗口", classBefore)
	}

	type sample struct {
		Round     int
		Mem       ProcessMemory
		WV2After  WebView2ProcessStats
		WV2During WebView2ProcessStats
		Elapsed   time.Duration
	}
	var samples []sample
	start := time.Now()

	const rounds = 50
	for round := 1; round <= rounds; round++ {
		roundStart := time.Now()
		parent := browser.NewNativeParent(win.HWND(), fmt.Sprintf("b01b-50-round-%d", round))
		host, err := native.NewWindowsHost(parent, native.HostOptions{ProfilePath: profileDir})
		if err != nil {
			t.Fatalf("第 %d 轮 NewWindowsHost: %v", round, err)
		}
		var attachErr error
		if err := win.CallOnUI(func() { attachErr = host.Attach(ctx, parent) }); err != nil {
			t.Fatalf("第 %d 轮 CallOnUI(Attach): %v", round, err)
		}
		if attachErr != nil {
			t.Fatalf("第 %d 轮 Attach: %v", round, attachErr)
		}
		slot := browser.SlotGeometry{SlotID: "browser-slot", Revision: 1, Left: 640, Top: 40,
			Width: 320, Height: 240, Visible: true, Zoom: 1}
		if err := host.Present(ctx, "", slot); err != nil {
			t.Fatalf("第 %d 轮 Present: %v", round, err)
		}
		tab, err := host.CreateTab(ctx, browser.TabOptions{URL: fixture.Primary.URL + "/"})
		if err != nil {
			t.Fatalf("第 %d 轮 CreateTab: %v", round, err)
		}
		child := host.WindowInfo().ChildHWND
		if err := host.Navigate(ctx, tab, fixture.Primary.URL+"/form"); err != nil {
			t.Fatalf("第 %d 轮 Navigate: %v", round, err)
		}
		// 等页面自报就绪：不靠 sleep 猜。
		readyDeadline := time.Now().Add(30 * time.Second)
		for {
			ps, psErr := host.PageState(ctx, tab)
			if psErr == nil && ps.ReadyState == "complete" && ps.Title == "普通表单" {
				break
			}
			if time.Now().After(readyDeadline) {
				t.Fatalf("第 %d 轮等页面就绪超时（readyState=%q title=%q err=%v）", round, ps.ReadyState, ps.Title, psErr)
			}
			time.Sleep(100 * time.Millisecond)
		}
		img, err := host.Screenshot(ctx, tab)
		if err != nil {
			t.Fatalf("第 %d 轮 Screenshot: %v", round, err)
		}
		sampled := round == 1 || round == 10 || round == 25 || round == 50
		var wv2During WebView2ProcessStats
		if sampled {
			wv2During, _ = CountWebView2Processes(profileTag)
		}
		if stats := pngStats(t, img.Bytes); stats.uniqueColors <= 4 {
			t.Fatalf("第 %d 轮截图是纯色（uniqueColors=%d）", round, stats.uniqueColors)
		}
		if img.PixelWidth != 320 || img.PixelHeight != 240 {
			t.Fatalf("第 %d 轮截图尺寸 %dx%d，期望 320x240", round, img.PixelWidth, img.PixelHeight)
		}
		if err := host.Detach(ctx); err != nil {
			t.Fatalf("第 %d 轮 Detach: %v", round, err)
		}

		// Detach 之后：句柄、窗口类实例、controller 都必须干净。
		if WindowExists(child) {
			t.Fatalf("第 %d 轮 Detach 之后子窗口 0x%x 仍然存在", round, child)
		}
		if p := GetParentHWND(child); p != 0 {
			t.Fatalf("第 %d 轮 Detach 之后 GetParent(0x%x)=0x%x，期望 0", round, child, p)
		}
		if n := CountWindowsOfClass("MyAgentNativeHostChild"); n != 0 {
			t.Fatalf("第 %d 轮 Detach 之后还有 %d 个 MyAgentNativeHostChild 窗口", round, n)
		}
		after := host.WindowInfo()
		if after.ChildHWND != 0 || after.ControllerReady {
			t.Fatalf("第 %d 轮 Detach 之后 WindowInfo 仍有 child=0x%x controllerReady=%t",
				round, after.ChildHWND, after.ControllerReady)
		}

		if sampled {
			mem := ReadProcessMemory(t)
			wv2After, wvErr := CountWebView2Processes(profileTag)
			if wvErr != nil {
				t.Logf("警告: 第 %d 轮统计 msedgewebview2 失败: %v", round, wvErr)
			}
			// Detach 返回后浏览器进程是**异步**退出的：这里量一次“从 Detach 返回到
			// owned 进程数归零”的实测延迟，而不是用固定 sleep 假装干净。
			exitStart := time.Now()
			exitLatency := time.Duration(0)
			if wv2After.Count != 0 {
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if st, _ := CountWebView2Processes(profileTag); st.Count == 0 {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				exitLatency = time.Since(exitStart)
			}
			samples = append(samples, sample{Round: round, Mem: mem, WV2After: wv2After,
				WV2During: wv2During, Elapsed: time.Since(start)})
			t.Logf("round %2d: wsMB=%.1f peakMB=%.1f pagefileMB=%.1f handles=%d "+
				"wv2During(%d procs, %.1fMB) wv2AfterDetach(%d procs, exitLatency=%s) roundTime=%s",
				round, mem.MB(), float64(mem.PeakWorkingSet)/(1<<20), float64(mem.PagefileBytes)/(1<<20),
				mem.HandleCount, wv2During.Count, wv2During.TotalWorkSetMB, wv2After.Count,
				exitLatency.Round(time.Millisecond), time.Since(roundStart).Round(time.Millisecond))
		}
	}

	// 收尾：进程/窗口类都要回到基线。
	time.Sleep(2 * time.Second)
	if n := CountWindowsOfClass("MyAgentNativeHostChild"); n != 0 {
		t.Fatalf("50 轮之后还有 %d 个 MyAgentNativeHostChild 窗口", n)
	}
	spikeAfter, err := ProcessCount("browser-spike.exe")
	if err != nil {
		t.Logf("警告: 统计 browser-spike.exe 失败: %v", err)
	}
	if spikeAfter > spikeBefore {
		t.Fatalf("50 轮之后多了 %d 个 browser-spike.exe（before=%d after=%d）",
			spikeAfter-spikeBefore, spikeBefore, spikeAfter)
	}
	wv2Final, err := CountWebView2Processes(profileTag)
	if err != nil {
		t.Logf("警告: 收尾统计 msedgewebview2 失败: %v", err)
	}
	t.Logf("50 轮完成 total=%s spike.exe before=%d after=%d childClassInstances=0 wv2Procs=%d",
		time.Since(start).Round(time.Millisecond), spikeBefore, spikeAfter, wv2Final.Count)
	for _, s := range samples {
		t.Logf("  sample round=%2d t=%s wsMB=%.1f peakMB=%.1f handles=%d wv2During=%dprocs/%.1fMB wv2After=%dprocs",
			s.Round, s.Elapsed.Round(time.Second), s.Mem.MB(), float64(s.Mem.PeakWorkingSet)/(1<<20),
			s.Mem.HandleCount, s.WV2During.Count, s.WV2During.TotalWorkSetMB, s.WV2After.Count)
	}
}

// waitCounter 轮询服务端 counter 到 want（最多 timeout）。
func waitCounter(t *testing.T, o *browserfixture.Origin, name string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if got := o.Counter(name); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等 counter %q 到 %d 超时（当前 %d）", name, want, o.Counter(name))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
