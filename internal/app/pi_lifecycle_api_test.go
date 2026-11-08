package app

// F08-A 绑定层测试：DiscoverPi / SelectPi。
//
// 这里的重点是"绑定层不是转发器"：
//   - 发现请求真的用到 settings.piPath（用户指定）与真实 PATH 的**副本**；
//   - 发现过程不改 PATH、不改设置文件；
//   - 选一个不存在的 id 必须报错，不许静默回退到第一个可用候选。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/runtime/pi"
)

func appTestPiName() string {
	if runtime.GOOS == "windows" {
		return "pi.exe"
	}
	return "pi"
}

func writeAppPiStub(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录 %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
		t.Fatalf("写 %s: %v", p, err)
	}
	return p
}

func TestDiscoverPiListsCandidatesInPriorityOrder(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()

	pref := writeAppPiStub(t, filepath.Join(root, "preferred"), name)
	managedRoot := filepath.Join(root, "managed")
	managed := writeAppPiStub(t, filepath.Join(managedRoot, "1.5.0"), name)
	pathDir := filepath.Join(root, "pathdir")
	pathPi := writeAppPiStub(t, pathDir, name)
	commonDir := filepath.Join(root, "common")
	common := writeAppPiStub(t, commonDir, name)

	// 只覆盖"发现环境"（PATH/常见位置/探针），不覆盖绑定层要验证的字段来源。
	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = pref
		req.ManagedRoot = managedRoot
		req.PathEnv = []string{pathDir}
		req.CommonLocations = []string{commonDir}
		req.VersionProbe = func(context.Context, pi.PiInstallation) (string, []string) {
			return "1.5.0", nil
		}
		return req
	}

	pathBefore := os.Getenv("PATH")
	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if os.Getenv("PATH") != pathBefore {
		t.Fatal("DiscoverPi 不得修改进程 PATH")
	}
	if len(res.Candidates) != 4 {
		t.Fatalf("期望 4 个候选（preferred/managed/path/common），实际 %+v", res.Candidates)
	}
	wantOrigins := []string{pi.CandidateOriginPreferred, pi.CandidateOriginManaged, pi.CandidateOriginPATH, pi.CandidateOriginCommon}
	for i, want := range wantOrigins {
		if res.Candidates[i].Origin != want {
			t.Fatalf("候选 %d origin = %q，期望 %q", i, res.Candidates[i].Origin, want)
		}
	}
	wantPaths := []string{pref, managed, pathPi, common}
	for i, want := range wantPaths {
		if !strings.EqualFold(filepath.Clean(res.Candidates[i].LauncherPath), filepath.Clean(want)) {
			t.Fatalf("候选 %d 路径 = %q，期望 %q", i, res.Candidates[i].LauncherPath, want)
		}
	}
	if res.Selected == nil || res.Selected.Origin != pi.CandidateOriginPreferred {
		t.Fatalf("应选中用户指定候选，实际 %+v", res.Selected)
	}
}

// stubPiDiscovery 把发现环境固定成"只有这些 PATH 目录、没有用户指定路径"。
//
// 关键：它只改"发现环境"，**不碰 req.Saved**——saved 来源必须由设置文件经
// piDiscoveryRequest 填进来，测试不能自己塞。
func stubPiDiscovery(d *Desktop, pathDirs ...string) {
	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = ""
		req.ManagedRoot = ""
		req.PathEnv = pathDirs
		req.CommonLocations = nil
		req.VersionProbe = func(context.Context, pi.PiInstallation) (string, []string) {
			return "1.5.0", nil
		}
		return req
	}
}

// samePath 比较两个路径是否指向同一处（Windows 大小写不敏感）。
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// settingsOnDisk 读**文件**里的设置。持久化是否真的发生，只能由磁盘回答。
func settingsOnDisk(t *testing.T, root string) Settings {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatalf("读 settings.json: %v", err)
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("解析 settings.json: %v", err)
	}
	return s
}

func TestSelectPiPersistsSavedPathAndSurvivesRestart(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	dir := filepath.Join(root, "cand")
	writeAppPiStub(t, dir, name)
	stubPiDiscovery(d, dir)

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("期望 1 个候选，实际 %+v", res.Candidates)
	}
	sel, err := d.SelectPi(res.Candidates[0].Installation.ID)
	if err != nil {
		t.Fatalf("SelectPi: %v", err)
	}
	if sel.Selected == nil {
		t.Fatal("SelectPi 没有给出选中项")
	}
	want := sel.Selected.Installation.Executable

	// 持久化必须真的落到文件，而不是只改内存。
	onDisk := settingsOnDisk(t, root)
	if !samePath(onDisk.PiSavedPath, want) {
		t.Fatalf("settings.json 的 piSavedPath = %q，期望 %q", onDisk.PiSavedPath, want)
	}

	// 重启：同一个 root 上新建 Desktop，发现请求只能来自磁盘。
	d2, err := New(root)
	if err != nil {
		t.Fatalf("重启后构造 Desktop: %v", err)
	}
	t.Cleanup(d2.Shutdown)
	if !samePath(d2.Settings().PiSavedPath, want) {
		t.Fatalf("重启后 piSavedPath = %q，期望 %q", d2.Settings().PiSavedPath, want)
	}

	// 无用户指定路径、无 PATH、无托管根：唯一可能的来源就是 saved。
	stubPiDiscovery(d2)
	got, err := d2.DiscoverPi()
	if err != nil {
		t.Fatalf("重启后 DiscoverPi: %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Origin != pi.CandidateOriginSaved {
		t.Fatalf("重启后应只有 saved 来源的候选，实际 %+v", got.Candidates)
	}
	if got.Selected == nil || got.Selected.Origin != pi.CandidateOriginSaved {
		t.Fatalf("重启后应选中 saved 候选，实际 %+v", got.Selected)
	}
	if !samePath(got.Selected.LauncherPath, want) {
		t.Fatalf("重启后选中路径 = %q，期望 %q", got.Selected.LauncherPath, want)
	}
}

func TestSelectPiUnknownIDLeavesSettingsByteIdentical(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	dir := filepath.Join(root, "cand")
	writeAppPiStub(t, dir, name)
	stubPiDiscovery(d, dir)

	// 先让 settings.json 真实存在："失败没写盘"必须先有一份盘上内容可比。
	if err := d.persistSettings(d.Settings()); err != nil {
		t.Fatalf("落初始设置: %v", err)
	}
	settingsPath := filepath.Join(root, "settings.json")
	before := sha256Hex([]byte(mustRead(t, settingsPath)))

	if _, err := d.SelectPi("no-such-installation"); err == nil {
		t.Fatal("不认识的 id 必须报错")
	}

	after := sha256Hex([]byte(mustRead(t, settingsPath)))
	if before != after {
		t.Fatalf("失败的选择改写了 settings.json: before=%s after=%s", before, after)
	}
	if d.Settings().PiSavedPath != "" {
		t.Fatalf("失败的选择不得改内存设置，实际 piSavedPath=%q", d.Settings().PiSavedPath)
	}
}

func TestSelectPiPersistFailureIsReported(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	dir := filepath.Join(root, "cand")
	writeAppPiStub(t, dir, name)
	stubPiDiscovery(d, dir)

	// 可移植的"写不进去"：把 settings.json 占位成目录，
	// 任何平台的 os.WriteFile 都会失败（比只读位可靠：Windows 的只读位
	// 在部分容器/网络盘上并不生效）。
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.Mkdir(settingsPath, 0o700); err != nil {
		t.Fatalf("占位 settings.json 目录: %v", err)
	}

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("期望 1 个候选，实际 %+v", res.Candidates)
	}
	if _, err := d.SelectPi(res.Candidates[0].Installation.ID); err == nil {
		t.Fatal("持久化失败必须报错，不能声称选择生效")
	}

	// 内存设置不得静默偏离磁盘：磁盘上没有这条，内存里也不能有。
	if d.Settings().PiSavedPath != "" {
		t.Fatalf("持久化失败后内存设置偏离磁盘: piSavedPath=%q", d.Settings().PiSavedPath)
	}
	info, statErr := os.Stat(settingsPath)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("写失败的 settings.json 不该变成文件（statErr=%v）", statErr)
	}
}

func TestDiscoverPiReportsBrokenSavedPath(t *testing.T) {
	d, root := newTestDesktop(t)
	gone := filepath.Join(root, "gone", appTestPiName())

	// 走既有配置 API 落盘：saved 来源只能来自设置，不能由测试直接塞进请求。
	s := d.Settings()
	s.PiSavedPath = gone
	if err := d.persistSettings(s); err != nil {
		t.Fatalf("落 saved 路径: %v", err)
	}
	stubPiDiscovery(d)

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("失效的 saved 路径不能让发现崩溃: %v", err)
	}

	// F08 的 Discover 把失效的 saved 路径留成一条 missing-path 诊断候选，
	// 但**永远不可选**。所以这里要求的是"没有可用的 saved 候选"。
	for _, c := range res.Candidates {
		if c.Origin != pi.CandidateOriginSaved {
			continue
		}
		if c.Compatibility != pi.StateMissing || c.Installation.Runnable {
			t.Fatalf("失效的 saved 候选必须是 missing 且不可运行，实际 %+v", c)
		}
	}
	if res.Selected != nil {
		t.Fatalf("失效的 saved 路径不能被选中，实际 %+v", res.Selected)
	}

	named := strings.Contains(strings.Join(res.Diagnostics, "\n"), gone)
	for _, c := range res.Candidates {
		if strings.Contains(strings.Join(c.Diagnostics, "\n"), gone) {
			named = true
		}
	}
	if !named {
		t.Fatalf("诊断必须点名失效的 saved 路径 %q；result=%+v", gone, res)
	}
}

func TestSavedPiIsNotOverwrittenByPATHDiscovery(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	savedDir := filepath.Join(root, "saved")
	savedPi := writeAppPiStub(t, savedDir, name)
	pathDir := filepath.Join(root, "pathdir")
	writeAppPiStub(t, pathDir, name)

	s := d.Settings()
	s.PiSavedPath = savedPi
	if err := d.persistSettings(s); err != nil {
		t.Fatalf("落 saved 路径: %v", err)
	}
	stubPiDiscovery(d, pathDir)

	settingsPath := filepath.Join(root, "settings.json")
	before := sha256Hex([]byte(mustRead(t, settingsPath)))

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("期望 2 个候选（saved + PATH），实际 %+v", res.Candidates)
	}
	if res.Candidates[0].Origin != pi.CandidateOriginSaved {
		t.Fatalf("saved 必须排在 PATH 前面，实际 %+v", res.Candidates)
	}
	if res.Selected == nil || res.Selected.Origin != pi.CandidateOriginSaved {
		t.Fatalf("应选中 saved 候选，实际 %+v", res.Selected)
	}
	if !samePath(res.Selected.LauncherPath, savedPi) {
		t.Fatalf("选中路径 = %q，期望 %q", res.Selected.LauncherPath, savedPi)
	}

	// 发现是只读的：PATH 命中不得回写 saved。
	after := sha256Hex([]byte(mustRead(t, settingsPath)))
	if before != after {
		t.Fatalf("发现改写了 settings.json: before=%s after=%s", before, after)
	}
}

// TestSelectPiDoesNotFreezeEnvOverrides 守一条容易被"顺手复用 persistSettings"破坏的性质：
// 选择 Pi 只应写 piSavedPath，不能把本次进程的环境覆盖（MYAGENT_PI_PATH 等）固化进用户配置。
//
// 为什么重要：loadSettings 会把 MYAGENT_* 覆盖应用到内存 Settings 上。若 SelectPi 直接
// persistSettings(d.Settings())，一次点击就把自动化/调试用的环境值永久钉进 settings.json；
// 之后环境变量消失，resolveInstallation 对失效 piPath **不静默回退**，用户会突然启动不了任何 Pi。
func TestSelectPiDoesNotFreezeEnvOverrides(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	pathDir := filepath.Join(root, "pathdir")
	piOnPath := writeAppPiStub(t, pathDir, name)

	// 模拟"本次进程带着环境覆盖启动"：内存设置里 piPath 是 env 值，磁盘上并没有。
	envPi := filepath.Join(root, "from-env", name)
	d.settings.PiPath = envPi
	if err := d.persistSettings(Settings{Workspace: d.settings.Workspace}); err != nil {
		t.Fatalf("先写一份不含 piPath 的配置: %v", err)
	}
	// persistSettings 会用入参整体替换内存设置，这里把 env 覆盖重新放回去。
	d.mu.Lock()
	d.settings.PiPath = envPi
	d.mu.Unlock()

	stubPiDiscovery(d, pathDir)
	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if res.Selected == nil {
		t.Fatalf("应选中 PATH 上的 Pi: %+v", res.Candidates)
	}
	if _, err := d.SelectPi(res.Selected.Installation.ID); err != nil {
		t.Fatalf("SelectPi: %v", err)
	}

	onDisk := settingsOnDisk(t, root)
	if onDisk.PiSavedPath == "" {
		t.Fatal("选择后 piSavedPath 必须落盘")
	}
	// 比较"是否同一个文件"而不是字符串：临时目录可能以 8.3 短名出现在环境里，
	// 发现层给出的是解析后的长名，两者指同一个文件。
	eitherFile, err := os.Stat(piOnPath)
	if err != nil {
		t.Fatalf("stat %s: %v", piOnPath, err)
	}
	savedFile, err := os.Stat(onDisk.PiSavedPath)
	if err != nil {
		t.Fatalf("stat piSavedPath %s: %v", onDisk.PiSavedPath, err)
	}
	if !os.SameFile(eitherFile, savedFile) {
		t.Fatalf("piSavedPath = %q 与期望 %q 不是同一个文件", onDisk.PiSavedPath, piOnPath)
	}
	if onDisk.PiPath != "" {
		t.Fatalf("选择 Pi 不得把环境覆盖的 piPath 固化进配置，实际 %q", onDisk.PiPath)
	}
	// 内存里这次进程仍然用 env 值，不影响下一个 run 的行为。
	if got := d.Settings().PiPath; !samePath(got, envPi) {
		t.Fatalf("内存设置里的 piPath = %q，期望仍是本次进程的覆盖值 %q", got, envPi)
	}
}

func TestSelectPiRejectsBogusInstallationID(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	pref := writeAppPiStub(t, filepath.Join(root, "preferred"), name)

	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = pref
		req.ManagedRoot = ""
		req.PathEnv = nil
		req.CommonLocations = nil
		req.VersionProbe = func(context.Context, pi.PiInstallation) (string, []string) {
			return "1.5.0", nil
		}
		return req
	}

	if _, err := d.SelectPi("bogus"); err == nil {
		t.Fatal("不在候选里的 id 必须报错，不许静默回退")
	}

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("期望 1 个候选，实际 %+v", res.Candidates)
	}
	sel, err := d.SelectPi(res.Candidates[0].Installation.ID)
	if err != nil {
		t.Fatalf("选中已发现候选不应报错: %v", err)
	}
	if sel.Selected == nil || sel.Selected.Installation.ID != res.Candidates[0].Installation.ID {
		t.Fatalf("Selected = %+v", sel.Selected)
	}
}

// TestDiscoverPiAcceptsRealPiThroughProductionProbe 是 F08-E 的端到端证据。
//
// 背景（2026-11-06 实测）：argv 修好之后真实 Pi 的 mcp/extension 已经能过，但生产默认
// 探测不传 provider/model，隔离 agentDir 里也没有 models.json，于是真实 Pi 回
// "No API key found for the selected model."，rpc/settled/abort 全失败——
// 一个完全可用的 Pi 被判 incompatible。这条测试走**绑定层的真实路径**
// （DiscoverPi → defaultContractProbe），不注入任何探针。
func TestDiscoverPiAcceptsRealPiThroughProductionProbe(t *testing.T) {
	// 目标目录按宿主命名，和 internal/runtime/pi 的真实 Pi 用例同一口径
	//（win-x64 / mac-arm64 / linux-arm64 …）：缺包就是失败（带修复步骤），
	// 不是 skip——否则"没装包"会被读成"探测通过"。
	target := appStandaloneTarget()
	name := "pi"
	if runtime.GOOS == "windows" {
		name = "pi.exe"
	}
	rootDir := repoRootForAppTest(t)
	dir := "v1.5.0-" + target
	exe := filepath.Join(rootDir, ".tools", "pi", "standalone", dir, name)
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("本机没有官方独立包（%s）。修复：把官方 Pi 1.5.0 独立包解到 "+
			".tools/pi/standalone/%s/（只读引用，不覆盖任何外部 Pi）后重跑 "+
			"scripts/devtools.ps1 verify --suite=integration --target=%s --gate=G0 --case=PI_COMP01。", exe, dir, target)
	}

	d, _ := newTestDesktop(t)
	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = exe
		req.ManagedRoot = ""
		req.PathEnv = nil
		req.CommonLocations = nil
		// 不注入 VersionProbe / ProbeFunc：走生产默认的 ContractProbe。
		return req
	}

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if res.Selected == nil {
		t.Fatalf("真实 Pi 没有被选中: state=%s diagnostics=%v", res.State, res.Diagnostics)
	}
	if res.State == pi.StateIncompatible {
		t.Fatalf("真实 Pi 被判 incompatible：%v", res.Diagnostics)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	if strings.Contains(joined, "No API key") {
		t.Fatalf("探测仍然借用了用户的模型配置（没有自带模型）：%s", joined)
	}
	if res.State != pi.StateReadyCompatible && res.State != pi.StateReadyVerified {
		t.Fatalf("真实 Pi 状态 = %s，期望 ready_compatible/ready_verified；诊断：%v", res.State, res.Diagnostics)
	}
}

// repoRootForAppTest 从当前包目录向上找 go.mod，返回仓库根。
func repoRootForAppTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前目录: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("向上找不到 go.mod，无法定位仓库根")
		}
		dir = parent
	}
}

// appStandaloneTarget 把 GOOS/GOARCH 映射成官方独立包的目录名（win-x64 等）。
func appStandaloneTarget() string {
	goos := runtime.GOOS
	switch goos {
	case "windows":
		goos = "win"
	case "darwin":
		goos = "mac"
	}
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x64"
	}
	return goos + "-" + arch
}

// FIX1-B：装配层的快速发现。
//
// 用户实测的启动等待来自 DiscoverPi：它会对每个可用候选跑六项契约探测（本机单个
// 候选 10.7s）。挂载/列表走 DiscoverPiFast；契约探测只在用户显式动作里跑。
// 这里用注入的 ProbeFunc 计数证明"快速路径一次也没 spawn 契约探测"，而不是靠
// 耗时去猜；同时证明 SelectPi 也不再顺带跑完整探测。
func TestDiscoverPiFastSkipsContractProbeButFullDiscoveryRunsIt(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	dir := filepath.Join(root, "cand")
	writeAppPiStub(t, dir, name)

	var probeCalls int32
	const probeDelay = 2 * time.Second
	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = dir
		req.ManagedRoot = ""
		req.PathEnv = nil
		req.CommonLocations = nil
		req.VersionProbe = func(context.Context, pi.PiInstallation) (string, []string) {
			return "1.5.0", nil
		}
		req.ProbeFunc = func(context.Context, pi.PiCandidate) (bool, []string) {
			atomic.AddInt32(&probeCalls, 1)
			time.Sleep(probeDelay)
			return true, nil
		}
		return req
	}

	start := time.Now()
	fast, err := d.DiscoverPiFast()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DiscoverPiFast: %v", err)
	}
	if got := atomic.LoadInt32(&probeCalls); got != 0 {
		t.Fatalf("DiscoverPiFast 执行了 %d 次契约探测，期望 0 次", got)
	}
	if elapsed >= probeDelay {
		t.Fatalf("DiscoverPiFast 耗时 %s ≥ 探针耗时 %s：探针真的被跑了", elapsed, probeDelay)
	}
	if fast.Verification != pi.VerificationVersionOnly {
		t.Fatalf("快速发现的 verification = %q，期望 %q", fast.Verification, pi.VerificationVersionOnly)
	}
	if fast.Selected == nil {
		t.Fatalf("快速发现应选中可启动的候选: state=%s diagnostics=%v", fast.State, fast.Diagnostics)
	}
	if fast.Selected.ContractVerified {
		t.Fatal("快速发现不得声称契约已验证")
	}
	if fast.Selected.Compatibility != pi.StateReadyCompatible {
		t.Fatalf("未跑契约探测时最高只能 ready_compatible，实际 %q", fast.Selected.Compatibility)
	}

	// 选一个 Pi 也不该再等完整探测：它走的是快速发现。
	selectedID := fast.Candidates[0].Installation.ID
	sel, err := d.SelectPi(selectedID)
	if err != nil {
		t.Fatalf("SelectPi: %v", err)
	}
	if got := atomic.LoadInt32(&probeCalls); got != 0 {
		t.Fatalf("SelectPi 执行了 %d 次契约探测，期望 0 次（选择不是验证动作）", got)
	}
	if sel.Selected == nil || sel.Selected.Installation.ID != selectedID {
		t.Fatalf("SelectPi 没有选中同一条候选: %+v", sel.Selected)
	}

	// 显式完整发现仍然真的跑契约探测，并把结论升级成 contract_probed。
	full, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	// 同一真实路径可能从两个来源（preferred/saved）各解析一次再去重，所以只断言
	// "确实跑了"，不把次数钉成 1（那是去重顺序的实现细节，不是本任务的承诺）。
	if got := atomic.LoadInt32(&probeCalls); got < 1 {
		t.Fatalf("完整发现执行了 %d 次契约探测，期望至少 1 次", got)
	}
	if full.Verification != pi.VerificationContractProbed {
		t.Fatalf("完整发现的 verification = %q，期望 %q", full.Verification, pi.VerificationContractProbed)
	}
	if full.Selected == nil || !full.Selected.ContractVerified {
		t.Fatalf("完整发现应标记契约已验证: %+v", full.Selected)
	}
}
