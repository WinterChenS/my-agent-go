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
	"testing"

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
