package devtools

// native runner：把「必须真实启动 GUI」的 case 跑成可被门禁评估的证据树。
//
// 与 integration 的区别：
//   - integration 跑的是纯逻辑/进程级测试，单目标机器上跑一次就够；
//   - native 必须启动真实 Wails 窗口与原生视图，Playwright 渲染 React 不能代替
//     （master plan 明确：前端组件通过不等于 native child view 工作）。
//
// 所以每条 native case 都带 target + display：
//   - Linux 的 X11 与 Wayland 是两份独立证据，缺一份 G1 就不闭合；
//   - Windows/macOS 归一化为 desktop。
//
// 纪律同 integration：case 表里没有的必需 case 直接判配置缺失（2），不静默跳过；
// 环境不具备（没有目标机器）记 BLOCKED；任何时候都不写「全绿」的假报告。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// nativeCase 是一条原生 case 的真实执行方式。
//
// 一条 case 可以由多个步骤组成（例如 BR00：契约自检 + 双 origin fixture 各一个
// go test）。所有步骤都必须退出 0 才算该 case PASS——把两步合成一个正则跨包跑
// 做不到，而只跑其中一步会让另一半的失败没有证据。
type nativeCase struct {
	// ID 必须与 tests/acceptance/cases.json 完全一致。
	ID string
	// Target 为空表示适用于任意目标（对应 scope=HOST 的 case）。
	Target string
	// Display 为空表示不区分显示；Linux 原生用例必须显式给 x11 或 wayland。
	Display string
	// Steps 至少一个；全部退出 0 才算 PASS。
	Steps []nativeStep
	// Blocked 非空表示当前实现还跑不了，值是原因（不许写成「跳过」当通过）。
	Blocked string
	// Limitation 会写进证据的 Limitations 字段：说明这条 case 只覆盖了哪个子集，
	// 防止「跑了子集 = 整条通过」的误读。
	Limitation string
	// Observation 非空表示该 case 必须产出 external-observation oracle。
	//
	// 与 integration 的 observationSpec 是同一个类型与同一套规则：声明之后没有退路，
	// case 不产出 observation.json 就是 FAIL，runner 不会替它编观察值，也不会
	// 退回 test-process 自证。BR00 是纯进程级的 case（不需要 GUI），但它在用例
	// 索引里属于 G1，所以只能从 native 套件出证据。
	Observation *observationSpec
}

// nativeStep 是一次 go test 调用。
type nativeStep struct {
	// Package 是 go test 的包路径（相对仓库根）。
	Package string
	// Run 是 go test -run 的正则。
	Run string
}

// nativeCases 是 G1 原生 case 到真实命令的映射。
//
// 现在只接入了 BR00（契约 + 双 origin fixture，纯进程级但归 G1）。
// BR01–BR07 需要 internal/browser/native 的真实宿主，B01–B03 落地时在这里加条目。
// 没有条目 = 判配置缺失，而不是「没有发现失败所以通过」。
func nativeCases() []nativeCase {
	return []nativeCase{
		{
			// BR01：同一主窗口右侧真实 WebView2（不是第二个顶层窗口）。
			// 测试断言 GetParent(child)==main、GetAncestor(child,GA_ROOT)==main、
			// 导航完成、CDP 页面像素非纯色；不靠"截图里看起来像网页"。
			ID:      "BR01",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestNativeRightPaneWindows",
			}},
			// 父子/几何事实来自 Win32 GetParent/GetAncestor/GetWindowRect/
			// GetDpiForWindow/WindowFromPoint，落盘后读回；页面像素来自 CDP 截图 PNG。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"window-facts.json", "screenshots/right-pane.png"}},
		},
		{
			ID:      "BR01",
			Target:  "win-arm64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestNativeRightPaneWindows",
			}},
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"window-facts.json", "screenshots/right-pane.png"}},
		},
		{
			// BR04：缩放 100/150/200%（页面缩放 = 平台等价分辨率下的 CSS/physical 比例）
			// + 分栏 20 轮不漂移。系统 DPI 150/200% 本机无法改（需注销），写在 Limitation。
			ID:      "BR04",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{
				{Package: "tests/native", Run: "TestNativeZoomFactor|TestNativeSplitter20Rounds"},
			},
			Limitation: "页面缩放 1.00/1.50/2.00 已实测（devicePixelRatio 1/1.5/2，误差 0.000px，OS 物理点击命中）；" +
				"Windows 系统显示缩放 150%/200%（GetDpiForWindow=144/192）未验证——本机 96 DPI，改显示缩放需注销/重启。",
			// 每档缩放一行（CDP dpr、GetWindowRect、WindowFromPoint、服务端计数器）、
			// 20 轮分栏每轮一行（GetWindowRect 误差、GetParent/GetAncestor），截图落盘。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"zoom-steps.jsonl", "splitter-rounds.jsonl",
				"screenshots/zoom-100.png", "screenshots/zoom-150.png", "screenshots/zoom-200.png",
			}},
		},
		{
			// BR05：中文 IME（CDP 模拟组合/提交）、focus、clipboard、最小化恢复。
			// dialog 层级遮挡属于 UI04.native/B07，不在这里冒充。
			ID:      "BR05",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{
				{Package: "tests/native", Run: "TestNativeFocusAndKeyboard|TestNativeMinimizeRestore|TestNativeClipboardSentinel|TestNativeIMEComposition"},
			},
			Limitation: "IME 用 CDP Input.imeSetComposition/insertText 模拟组合与提交，不是 OS 级真机手输；" +
				"dialog 层级遮挡未覆盖（B07 的 BrowserOcclusionCoordinator / UI04.native）。",
			// 前台窗口/最小化状态来自 GetForegroundWindow/IsIconic/GetWindowRect；
			// 剪贴板由**独立的 PowerShell 进程** Get-Clipboard 写出；IME 的调用结果与
			// 页面读回值逐条落盘。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"window-facts.json", "clipboard.txt", "clipboard.raw", "clipboard-sentinel.txt",
				"ime.jsonl", "screenshots/after-restore.png",
			}},
		},
		{
			// BR06：50 轮创建销毁 + 句柄/进程/working set 记录。
			ID:      "BR06",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{
				{Package: "tests/native", Run: "TestNativeCreateDestroy50Rounds|TestNativeCreateDestroyRepeated"},
			},
			Limitation: "50 轮子集；未做 >50 轮 soak 与分配级泄漏追踪（只记录 working set/句柄趋势）。",
			// 每轮句柄数/working set 来自 GetProcessHandleCount / GetProcessMemoryInfo；
			// 浏览器子进程由**独立的 PowerShell 进程** Get-CimInstance Win32_Process 写成
			// process-table-before/during/after.json。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"handles.jsonl", "process-table-before.json", "process-table-during.json", "process-table-after.json",
			}},
		},
		{
			// BR07.native：标准表单的观察/填写/点击（B04 的工具语义）。
			// 复合 case BR07 由 BR07.native（G1）与 BR07.tool（G3，经 MCP 工具，B05）组成，
			// 这里只能覆盖前者。
			// 稳定引用那条测试同时用于 BR09 的限额证据：两条 case 各自在自己的
			// 证据目录里跑一遍，谁也不借谁的产物。
			ID:      "BR07.native",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestObserveReturnsStableRefsAndHonoursLimits|TestFillAndClickReachTheFixtureServer",
			}},
			Limitation: "只覆盖 win-x64/desktop 的真实 WebView2；macOS/Linux 宿主（B02/B03）未实现，" +
				"BR07.tool（G3，经 MCP 工具）属于 B05。",
			// 快照原始 JSON、每次动作一行、服务端自己记录的表单载荷、两个 origin 的 counter 快照。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"snapshots/br07-form.json", "snapshots/br07-form-stable-refs.json",
				"actions.jsonl", "form-last.json", "counters.json",
			}},
		},
		{
			// UI04.native：右侧真实原生视图的 slot 几何、遮挡协调（设置 dialog → 命令面板
			// → 审批框）、停止按钮位置，以及 agent 填字 → 用户手改 → 恢复后 agent 观察到
			// 人工修改的接管交接（B07）。
			//
			// 两条用例都走**应用层**（internal/app 的 Desktop 绑定 → BrowserBridge），
			// 不是直接调 native 包：证据里这条路径就是 React 调用的那条。
			// 验收矩阵把 UI04.native 记为 G1 的最小 dialog 验证；UI04.workbench/E2E03
			// （成品 UI + 真 Pi）属 G3，不能用这里的通过顶替。
			ID:      "UI04.native",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestUI04NativeSlotOcclusionAndStopButton|TestUI04NativeHandOver",
			}},
			Limitation: "只覆盖 win-x64/desktop 的真实 WebView2；macOS/Linux 宿主（B02/B03）未实现。" +
				"人工手改由 OS 物理点击 + OS 键盘（keybd_event）注入小写 ASCII，中文仍需真机人工步骤；" +
				"停止按钮本身由 React 工具条渲染（BrowserPanel 用例覆盖），这里用 WindowFromPoint 证明" +
				"工具条保留区里没有任何原生视图。隐藏态没有页内截图：controller 隐藏后 CDP 截图会超时，" +
				"隐藏/恢复改用 IsWindowVisible + SetVisible 计数证明。React 面板通过浏览器路径浏览的成品级验收属 " +
				"UI04.workbench/E2E03（G3）。",
			// 窗口事实（GetParent/GetWindowRect/IsWindowVisible/WindowFromPoint）、每次 slot 几何与
			// 拒绝记录、每次遮挡转换、停止按钮命中测试、接管交接每一步、两张 CDP 页面截图。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"window-facts.json", "slot-geometry.jsonl", "occlusion-steps.jsonl",
				"stop-button.json", "handover.jsonl", "screenshots/ui04-restored.png",
			}},
		},
		{
			// BR08：同 URL 导航 / SPA 断开节点后旧 ref 必须 STALE_SNAPSHOT，且 counter 保持 0。
			//
			// 注意：BR08 在索引里是 G2 阶段（v1-gates.md），而 RunNative 目前只选 G1
			// 必需位。条目先落地，等 G2 的 native 选择接上就直接生效；这不是"跑过了"。
			ID:      "BR08",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestStaleRefAfterNavigationIsRejected",
			}},
			Limitation: "G2 阶段的 native 选择器尚未接入（RunNative 只取 G1）；本条目只定义执行方式。" +
				"覆盖 win-x64/desktop；跨平台矩阵待 B02/B03。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"stale-rejections.jsonl", "snapshots/br08-spa-after-replace.json", "counters.json",
			}},
		},
		{
			// BR09：密码不泄漏 + 三条限额 + 截图完整且 ≤4MiB（含受控缩小的真实证据）。
			ID:      "BR09",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestObserveReturnsStableRefsAndHonoursLimits|TestPasswordNeverLeaks|TestScreenshotIsCompleteAndBounded",
			}},
			Limitation: "截图的 4MiB 分支用 1600×2400 随机像素页面在真实 WebView2 上触发（受控缩小到 scale=0.5）；" +
				"更大的视口/多显示器场景未测。G2 阶段选择器同上未接入。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"limits.json", "snapshots/br09-login.json", "password-scan.json",
				"screenshots/br09-screen.png", "screenshot-sha256.json", "screenshot-outcome.json",
			}},
		},
		{
			// BR10：cross-origin / closed shadow 明确 Unsupported；trusted-only 后置条件失败。
			ID:      "BR10",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestClosedShadowAndCrossOriginAreUnsupported|TestTrustedOnlyPostConditionFailure",
			}},
			Limitation: "closed shadow root 的探测走 CDP DOM.getDocument(pierce)，不覆盖 UA shadow root；" +
				"trusted-only 只证明「后置条件不成立时不返回 Verified」，不声称能预判任意站点的事件策略。" +
				"G2 阶段选择器同上未接入。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"snapshots/br10-cross-origin.json", "snapshots/br10-closed-shadow.json",
				"unsupported-surfaces.json", "trusted-only.json", "counters.json",
			}},
		},
		// ── U07：可访问性、视觉回归与真机 UI 验收（G3） ──
		//
		// 与 BR01–BR10 的分工：那些是浏览器/原生视图的**能力**（父子窗口、缩放、
		// 句柄趋势…），这里是**产品界面**的验收：九页在五档窗口里的布局、可访问性、
		// 两语言两主题、以及切换时不丢状态。
		//
		// 所有 U07 用例都跑构建后的 Wails 应用（npm build → go build → 启动真进程），
		// 页面事实来自它的 WebView2（DevTools 协议），窗口事实来自 Win32，
		// 状态事实来自 Go API 与磁盘。Playwright 预检不产出门禁证据。
		{
			// UI12：880×600 至 1920×1080 五档窗口 × 九页无裁剪/重叠/不可达，
			// 客户区尺寸与 GetDpiForWindow/devicePixelRatio 三方一致，
			// WindowFromPoint 证明没有原生视图遮挡客户区。
			ID:      "UI12",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07UI12WindowMatrix$"}},
			Limitation: "本机为 96 DPI（系统缩放 100%）：Windows 150%/200% 缩放、macOS Retina、" +
				"Linux X11/Wayland 均未验证（见 ADR 0005 与 U07 报告）。五档窗口为真实 SetWindowPos + GetClientRect 收敛。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"window-facts.jsonl",
				"screenshots/ui12-workbench-880x600.png", "screenshots/ui12-settings-1920x1080.png",
			}},
		},
		{
			// UI13：可访问名称、WCAG AA 对比度、键盘可见焦点、真实渲染器的可访问性树。
			ID:      "UI13",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07UI13Accessibility$"}},
			Limitation: "键盘焦点用 CDP Input.dispatchKeyEvent 走真实 Tab 序列（渲染器进入键盘模态）；" +
				"读屏软件本体未接入。审批风险态（waiting_approval 卡片）需要一次真实 run，" +
				"本机缺 Pi 1.5.0 无法产生，改用 warning/danger 语义 token 的对比度作为该状态色彩基线。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"focus-order.jsonl", "token-contrast.jsonl",
			}},
		},
		{
			// I18N02：走设置页切语言 → 即时生效、持久化（API + 磁盘）、不重载、不丢状态。
			ID:      "I18N02",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07I18N02LocaleSwitch$"}},
			Limitation: "用户/模型/工具原文不被翻译这一条，用全局搜索草稿（含中文与西里尔字母）验证；" +
				"一次真实 Pi 会话里的用户消息原文未覆盖（缺 Pi 1.5.0）。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"preferences-after-locale.json",
			}},
		},
		{
			// I18N03：错误 code+参数走 catalog、日期/数字/体积按 locale 格式化。
			ID:      "I18N03",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07I18N03Formatting$"}},
			Limitation: "费用（formatCost）需要 provider 的真实 usage/计价，本机缺 Pi 与模型凭证 → 未验证；" +
				"日期/数字/体积来自真实动作（设置页导出诊断包）的输出。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"error-codes.jsonl", "formatting.jsonl",
			}},
		},
		{
			// I18N04：九页 × 两语言 × 880/1920 无截断/重叠/不可达，且 en-US 下无中文硬编码。
			ID:      "I18N04",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07I18N04BilingualLayout$"}},
			Limitation: "只覆盖 880×600 与 1920×1080 两个极端宽度（UI12 覆盖五档窗口）；" +
				"中文泄漏检查排除 option（语言名）与带 lang 的元素。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"bilingual-layout.jsonl",
			}},
		},
		{
			// THEME02：light/dark/跟随系统选择正确、持久化、重启首屏不闪。
			ID:      "THEME02",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07THEME02ThemeSwitch$"}},
			Limitation: "\"不闪\"的判据是：重启后第一个出现内容的采样点上 data-theme 已经是最终值；" +
				"这是进程内的观测，不是屏幕录制。系统配色变化（跟随系统）在运行中变化的路径由 theme.ts 的" +
				"媒体查询订阅实现，本机无法在系统设置里切换配色（需交互式桌面操作）。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"preferences-theme.json",
			}},
		},
		{
			// THEME03：两主题下 default/hover/active/disabled 与状态色 token 的对比度/可见性。
			ID:      "THEME03",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07THEME03StateContrast$"}},
			Limitation: "hover/active 用真实 CDP 输入事件触发（mouseMoved/mousePressed）；" +
				"disabled 态只对页面上真实存在的禁用控件取样；审批风险卡片状态同 UI13 一样缺 Pi 无法产生。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"state-contrast.jsonl",
			}},
		},
		{
			// THEME04：切主题不重载、不丢草稿、不重建原生子窗口、不改变 run/Pi/浏览器状态。
			ID:      "THEME04",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07THEME04StatePreservation$"}},
			Limitation: "下载不被取消、人工接管归属不变这两条需要一次真实 run + 浏览器标签页，" +
				"本机缺 Pi 1.5.0 无法产生 → 未验证（deferred）；本次证明的是页面不重载、" +
				"草稿/详情栏保持、原生子窗口集合不变、run/Pi/BrowserState 绑定值不变。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"preservation.jsonl",
			}},
		},
		{
			// THEME05：九页 × 两主题的真实截图与 hash + 偏好 revision + 窗口事实。
			ID:      "THEME05",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07THEME05ThemeScreenshots$"}},
			Limitation: "截图为 CDP Page.captureScreenshot（应用自己渲染的像素），与窗口几何、DPI、偏好 revision 一并落盘；" +
				"跨平台像素对比（macOS/Linux）未做——ADR 0005 本轮只验收 win-x64。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"window-facts.jsonl", "screenshots/light-workbench.png", "screenshots/dark-settings.png",
			}},
		},
		{
			// 检查器自证：往真实页面注入违规结构，要求每条检查变红（否则它在生产页面的绿无意义）。
			ID:      "UI12.detectors",
			Target:  "win-x64",
			Display: DisplayDesktop,
			Steps:   []nativeStep{{Package: "tests/native", Run: "^TestU07DetectorsAreNotVacuous$"}},
			Limitation: "只证明检查器在 win-x64/WebView2 引擎上会红；Playwright 侧的夹具红证在 " +
				"frontend/e2e/fixtures/violations/（预检，不产出门禁证据）。",
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{
				"detector-selfcheck.json",
			}},
		},
		{
			// BR00：两个 origin 独立、server counter 可核对、trusted-only 负例。
			// 它不需要 GUI，但 G1 把它算作浏览器基线的一部分，所以从 native 套件出证据。
			ID: "BR00",
			Steps: []nativeStep{
				{
					// 契约层：DTO/schema/限额/不可序列化的 NativeParent。
					Package: "internal/browser",
					Run:     "TestBaselineLimits|TestSchemaMatchesGoTypes|TestSchemaLimitsMatchConstants|TestSchemaCoversEveryExportedType|TestNativeParentNeverSerializes|TestExpectedConditionRejectsArbitraryJS|TestActionValidate|TestActionResultDeliveredIsNotVerified|TestSnapshotLimits|TestSnapshotValidateRefs|TestImageValidate|TestSlotGeometryValidate|TestTabLimit|TestObserveOptionsClamp|TestUnimplementedReturnsNotImplemented|TestWireFormatGolden",
				},
				{
					// fixture：origin 独立 / counter / trusted-only / SPA / 慢加载 /
					// cookie / 下载弹窗 / closed shadow 与 frame 的明确声明。
					// TestBR00FixtureRequestLogAndCounters 是外部观察装置：服务端 handler
					// 追加 fixture-requests.jsonl，计数器快照由 HTTP /__counters 取回。
					Package: "tests/fixtures/browser",
					Run:     "TestFixtureTwoDistinctOrigins|TestCounterSemantics|TestTrustedOnlyRejectsUntrustedInput|TestSPACountersDistinguishTargetAndReplacement|TestSlowLoadIsWaitable|TestLoginCookieIsServerSideOracle|TestDownloadAndPopupHaveServerSideEvidence|TestClosedShadowAndFramePagesDeclareTheHardParts|TestFixtureCloseReleasesPorts|TestBR00FixtureRequestLogAndCounters",
				},
			},
			// 两个 origin 独立、counter 可核对、trusted-only 负例确实拒非 trusted 输入，
			// 三条结论都由服务端写的日志与 /__counters 快照证明（不是页面文案、不是断言变量）。
			Observation: &observationSpec{Source: "filesystem", Artifacts: []string{"fixture-requests.jsonl", "counters.json"}},
		},
	}
}

// normalizeNativeDisplay 把非 Linux 目标的空 display 归一化为 desktop。
func normalizeNativeDisplay(target, display string) string {
	d := strings.TrimSpace(display)
	if strings.HasPrefix(target, "linux-") {
		return d
	}
	if d == "" {
		return DisplayDesktop
	}
	return d
}

// validNativeDisplay 校验 display 取值。Linux 必须显式给会话，别的平台只认 desktop。
func validNativeDisplay(target, display string) bool {
	if strings.HasPrefix(target, "linux-") {
		return display == DisplayX11 || display == DisplayWayland
	}
	return display == DisplayDesktop
}

// RunNative 是 `verify --suite=native` 的入口。
// RunNative 跑 G1 的原生必需位（兼容旧调用）。
func RunNative(target, display string) int {
	return RunNativeForGate(target, display, "G1")
}

// RunNativeForGate 跑指定 gate 的原生必需位。
//
// 为什么需要 gate 参数：原生 case 不只属于 G1（BR08–BR10 属 G2），而证据目录按
// <rev>/<gate>/<target>/<display>/<case> 分开。写错 gate 的证据会被门禁判死
// （gateId 与请求不一致），所以选位与落盘必须用同一个 gate。
func RunNativeForGate(target, display, gate string) int {
	if !contains(KnownGates, gate) {
		fmt.Fprintf(os.Stderr, "未知 gate: %s\n", gate)
		return 2
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "native 必须指定 --target")
		return 2
	}
	want, ok := targetPlatforms[target]
	if !ok {
		fmt.Fprintf(os.Stderr, "未知 target: %s\n", target)
		return 2
	}
	if got := runtime.GOOS + "/" + runtime.GOARCH; got != want[0]+"/"+want[1] {
		fmt.Fprintf(os.Stderr, "当前宿主 %s 与目标 %s(%s) 不一致；原生用例必须在目标本机执行\n",
			got, target, want[0]+"/"+want[1])
		return 2
	}
	display = normalizeNativeDisplay(target, display)
	if !validNativeDisplay(target, display) {
		fmt.Fprintf(os.Stderr, "display 非法: %q（Linux 必须是 x11 或 wayland；其他目标用 desktop）\n", display)
		return 2
	}

	// GOV05：原生证据同样必须带真实环境身份；display 用已归一化的当前会话。
	env, envErr := HostEnvironment(display)
	if envErr != nil {
		fmt.Fprintf(os.Stderr, "宿主环境探测失败，不能写证据: %v\n", envErr)
		return 2
	}

	idx, err := LoadCasesIndex("tests/acceptance/cases.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	reqs, problems := ResolveRequiredEvidence(idx, gate)
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "索引问题: %s\n", p)
		return 2
	}

	// 只跑本 target × display 的必需位；HOST case（Target 为空）在每个目标上都跑，
	// 门禁要求「出现多份则每份都必须通过」。
	var selected []Requirement
	for _, r := range reqs {
		if r.Target != "" && (r.Target != target || r.Display != display) {
			continue
		}
		selected = append(selected, r)
	}
	if len(selected) == 0 {
		fmt.Fprintf(os.Stderr, "target=%s display=%s 没有任何 %s 必需 case（索引缺失）", target, display, gate)
		fmt.Fprintln(os.Stderr)
		return 2
	}

	table := nativeCases()
	var runnable []Requirement
	var missing []string
	for _, r := range selected {
		if _, ok := lookupNativeCase(table, r, target, display); ok {
			runnable = append(runnable, r)
			continue
		}
		missing = append(missing, r.CaseID)
	}
	if len(runnable) == 0 {
		fmt.Fprintf(os.Stderr, "以下 %s 必需 case 在 target=%s display=%s 上没有 native runner：%s", gate, target, display, strings.Join(dedupe(missing), ", "))
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "缺失 runner 属于配置缺失（退出 2），不是通过。")
		return 2
	}

	rev, dirty := SourceIdentity()
	root := evidenceRoot(rev, gate, target, display)
	fmt.Printf("== verify --suite=native --target=%s --display=%s ==\n", target, display)
	fmt.Printf("证据根: %s\n", root)
	fmt.Printf("宿主环境: os=%s version=%s build=%s arch=%s display=%s\n",
		env.OS, env.OSVersion, orNone(env.OSBuild), env.Arch, orNone(env.Display))
	warnIfBelowMinimum(env, target)
	if dirty {
		fmt.Println("[警告] 工作区不干净：本次证据带 worktreeDirty=true，gate 会判死，不能用作正式结论。")
	}

	blocks, fails := 0, 0
	for _, r := range runnable {
		nc, _ := lookupNativeCase(table, r, target, display)
		res := runNativeCase(nc, target, display, root, gate, env)
		if res.blocked {
			blocks++
		} else if !res.passed {
			fails++
		}
	}

	fmt.Printf("汇总: 可跑 %d / 必需 %d 条 %s 必需 case，失败 %d，阻塞 %d", len(runnable), len(selected), gate, fails, blocks)
	fmt.Println()
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "以下 %s 必需 case 在 target=%s display=%s 上没有 native runner：%s", gate, target, display, strings.Join(dedupe(missing), ", "))
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "已跑的部分不等于 %s 通过；缺 runner 属于配置缺失。", gate)
		fmt.Fprintln(os.Stderr)
	}

	if !dirty {
		if _, nowDirty := SourceIdentity(); nowDirty {
			fmt.Println("[FAIL] 测试运行期间工作区变脏：有测试在写仓库文件。")
			fmt.Println("       历史证据必须不可变；需要归档新证据请显式指定独立输出目录。")
			if out, err := exec.Command("git", "status", "--porcelain").Output(); err == nil {
				fmt.Printf("       git status --porcelain:\n%s", out)
			}
			return 2
		}
	}
	if fails > 0 {
		// 断言失败优先于「配置缺失」：这是真的回归，不能被缺失清单盖过去。
		return 1
	}
	if blocks > 0 || len(missing) > 0 {
		return 2
	}
	return 0
}

// lookupNativeCase 按 (case, target, display) 找执行方式。
// Target 为空的条目适用于任意目标；Display 必须精确匹配（Linux 的 X11/Wayland
// 不能互相顶替，HOST case 跟随本次运行的 display）。
func lookupNativeCase(table []nativeCase, r Requirement, target, display string) (nativeCase, bool) {
	for _, nc := range table {
		if nc.ID != r.CaseID {
			continue
		}
		if nc.Target != "" && nc.Target != target {
			continue
		}
		if nc.Display != "" && nc.Display != display {
			continue
		}
		return nc, true
	}
	return nativeCase{}, false
}

// runNativeCase 执行一条 native case 并写证据。
func runNativeCase(c nativeCase, target, display, root, gate string, environment contracts.Environment) caseOutcome {
	dir := filepath.Join(root, c.ID)
	_ = os.MkdirAll(dir, 0o755)

	// external-observation 声明（BR00）：观察文件必须由本次执行重新产出，
	// 先清掉上一轮的残留，否则「这次什么都没写」会被旧文件掩盖成 PASS。
	// 观察目录必须是绝对路径：go test 的 cwd 是包目录，给相对路径会写进仓库。
	absDir, absErr := filepath.Abs(dir)
	if absErr != nil {
		absDir = dir
	}
	if c.Observation != nil {
		clearObservationArtifacts(dir, c.Observation)
	}
	obsEnv := observationEnv(c.Observation, absDir)

	started := time.Now().UTC()
	ev := contracts.GateEvidence{
		SchemaVersion:    1,
		GateID:           gate,
		TaskID:           taskForNativeCase(c.ID),
		SourceRevision:   GetSourceRevision(),
		WorktreeDirty:    worktreeDirty(),
		VersionsLockHash: versionsHash(),
		TargetID:         target,
		Display:          display,
		Environment:      environment,
		StartedAt:        started.Format(time.RFC3339),
		Reviewer: contracts.Reviewer{
			Name:       "devtools-native-runner",
			Machine:    hostname(),
			ReviewedAt: started.Format(time.RFC3339),
			Note:       "机器执行证据；结构完整不等于人工审查通过",
		},
	}

	cr := contracts.CaseResult{
		ID:         c.ID,
		Expected:   "每个步骤的 go test 都退出 0",
		OraclePath: "oracle.json",
		StartedAt:  started.Format(time.RFC3339),
	}

	var cmdlines, stdouts, stderrs []string
	exit := 0
	switch {
	case c.Blocked != "":
		cr.Status = contracts.StatusBlocked
		cr.Actual = c.Blocked
		ev.Limitations = c.Blocked
		cmdlines = append(cmdlines, "(未执行：环境或实现缺失)")
	case len(c.Steps) == 0:
		cr.Status = contracts.StatusFail
		cr.Actual = "runner 定义错误：没有任何步骤"
		exit = 1
	default:
		for _, step := range c.Steps {
			args := []string{"test", "./" + step.Package, "-run", step.Run, "-count=1", "-v"}
			env := append([]string{"MYAGENT_TARGET=" + target, "MYAGENT_DISPLAY=" + display}, obsEnv...)
			cmdlines = append(cmdlines, "go "+strings.Join(args, " "))
			code, out, errOut, _ := runWithEnv(env, "go", args...)
			stdouts = append(stdouts, "### $ go "+strings.Join(args, " ")+"\n"+out)
			stderrs = append(stderrs, "### $ go "+strings.Join(args, " ")+"\n"+errOut)
			if code != 0 {
				exit = code
				break
			}
		}
	}

	if cr.Status == "" {
		if exit == 0 {
			cr.Status = contracts.StatusPass
			cr.Actual = "全部步骤退出 0"
		} else {
			cr.Status = contracts.StatusFail
			cr.Actual = fmt.Sprintf("命令退出 %d：%s", exit, tail(strings.Join(stderrs, "\n")+"\n"+strings.Join(stdouts, "\n"), 600))
		}
	}
	cr.EndedAt = time.Now().UTC().Format(time.RFC3339)

	if cr.Status == contracts.StatusPass && c.Observation != nil {
		// 观察值必须由 case 自己写出来，runner 只负责封装。
		// 失败时不写任何 oracle —— 绝不能退回 test-process 自证。
		if err := writeExternalObservationOracle(dir, c.Observation); err != nil {
			cr.Status = contracts.StatusFail
			cr.Actual = err.Error()
		}
	}

	cmdline := strings.Join(cmdlines, "\n")
	ev.Command = cmdline
	ev.ExitCode = exit
	ev.Limitations = c.Limitation
	ev.CaseResults = []contracts.CaseResult{cr}
	ev.EndedAt = cr.EndedAt

	// oracle 已由 external 路径写好时不能再覆盖成 test-process。
	var oracleFn func() error
	if c.Observation == nil {
		oracleFn = func() error {
			return writeProcessOracle(dir, cmdline, ev.ExitCode, []byte(strings.Join(stdouts, "\n")), []byte(strings.Join(stderrs, "\n")))
		}
	}
	writeEvidenceDirWithOracle(dir, &ev, cmdline, strings.Join(stdouts, "\n"), strings.Join(stderrs, "\n"), oracleFn)

	fmt.Printf("  [%s] %s — %s\n", cr.Status, c.ID, firstLine(cr.Actual))
	return caseOutcome{passed: cr.Status == contracts.StatusPass, blocked: cr.Status == contracts.StatusBlocked}
}

// taskForNativeCase 把原生 case 归属到模块计划。
func taskForNativeCase(id string) string {
	switch {
	case strings.HasPrefix(id, "BR"):
		return "B00-B07"
	case strings.HasPrefix(id, "UI"):
		return "B07/C06"
	}
	return "unknown"
}
