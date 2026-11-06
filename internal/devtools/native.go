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
		},
		{
			ID:      "BR01",
			Target:  "win-arm64",
			Display: DisplayDesktop,
			Steps: []nativeStep{{
				Package: "tests/native",
				Run:     "TestNativeRightPaneWindows",
			}},
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
func RunNative(target, display string) int {
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
	reqs, problems := ResolveRequiredEvidence(idx, "G1")
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
		fmt.Fprintf(os.Stderr, "target=%s display=%s 没有任何 G1 必需 case（索引缺失）\n", target, display)
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
		fmt.Fprintf(os.Stderr, "以下 G1 必需 case 在 target=%s display=%s 上没有 native runner：%s\n",
			target, display, strings.Join(dedupe(missing), ", "))
		fmt.Fprintln(os.Stderr, "缺失 runner 属于配置缺失（退出 2），不是通过。")
		return 2
	}

	rev, dirty := SourceIdentity()
	root := evidenceRoot(rev, "G1", target, display)
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
		res := runNativeCase(nc, target, display, root, env)
		if res.blocked {
			blocks++
		} else if !res.passed {
			fails++
		}
	}

	fmt.Printf("汇总: 可跑 %d / 必需 %d 条 G1 必需 case，失败 %d，阻塞 %d\n", len(runnable), len(selected), fails, blocks)
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "以下 G1 必需 case 在 target=%s display=%s 上没有 native runner：%s\n",
			target, display, strings.Join(dedupe(missing), ", "))
		fmt.Fprintln(os.Stderr, "已跑的部分不等于 G1 通过；缺 runner 属于配置缺失。")
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
func runNativeCase(c nativeCase, target, display, root string, environment contracts.Environment) caseOutcome {
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
		GateID:           "G1",
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
