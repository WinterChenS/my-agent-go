package app

// F06b 绑定层测试：应用必须真的用上托管的 active Pi。
//
// 这组用例守的是 F06 复核发现的三个真实缺口（不是可选优化）：
//  1. 托管安装落在 <managed>/versions/<version>-<target>/，而 F08 的发现只扫
//     托管根**一层**，所以 piDiscoveryRequest 必须把 versions 目录交给发现层；
//  2. resolveInstallation 以前只读 settings.piPath，active.json 从没被消费——
//     装好了也永远不会被用上；
//  3. F08 的托管候选按**目录名**排序（1.5.0 排在 1.6.0 前面），所以"哪个版本是
//     当前版本"只能由 active 指针回答，不能由排序回答。
//
// 解析顺序（本文件逐条覆盖）：settings.piPath（用户显式）→ <managed>/active.json
// （用前验证哈希）→ CONFIG_MISSING。任何一步失败都不许换一个托管版本凑数。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/runtime/pi"
)

// managedFixture 是一条造出来的托管安装（<managed>/versions/<version>-<target>/）。
type managedFixture struct {
	root    string
	version string
	target  string
	exe     string
	sha256  string
}

// writeManagedInstall 在托管根里造一条版本安装（不写指针）。
func writeManagedInstall(t *testing.T, root, version string, payload []byte) managedFixture {
	t.Helper()
	target := pi.HostTarget()
	name, err := piinstall.ExpectedExecutable(target)
	if err != nil {
		t.Fatalf("宿主目标 %q 不在六目标内: %v", target, err)
	}
	dir := filepath.Join(root, "versions", version+"-"+target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建版本目录: %v", err)
	}
	exe := filepath.Join(dir, name)
	if err := os.WriteFile(exe, payload, 0o755); err != nil {
		t.Fatalf("写托管可执行体: %v", err)
	}
	sum, err := piinstall.HashFileContent(exe)
	if err != nil {
		t.Fatalf("计算可执行体哈希: %v", err)
	}
	return managedFixture{root: root, version: version, target: target, exe: exe, sha256: sum}
}

// activate 用 F06 的同一实现写出 active.json。
func (f managedFixture) activate(t *testing.T, installationID string) {
	t.Helper()
	err := piinstall.WriteActive(f.root, piinstall.ActivePointer{
		Version:          f.version,
		TargetID:         f.target,
		VersionDir:       filepath.ToSlash(filepath.Join("versions", f.version+"-"+f.target)),
		Executable:       filepath.Base(f.exe),
		ExecutableSHA256: f.sha256,
		InstalledAt:      time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
		InstallationID:   installationID,
	})
	if err != nil {
		t.Fatalf("写 active 指针: %v", err)
	}
}

// appSamePath 比较两个路径是否指向同一文件（Windows 大小写不敏感 + 软链接）。
func appSamePath(t *testing.T, a, b string) bool {
	t.Helper()
	if a == "" || b == "" {
		return false
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		a = r
	}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		b = r
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// appManagedVersionProbe 从托管版本目录名（<version>-<target>）解析版本。
// 载荷是假可执行体，不跑 --version（那会真的起进程）。
func appManagedVersionProbe(_ context.Context, inst pi.PiInstallation) (string, []string) {
	if v, ok := appVersionFromDir(filepath.Base(filepath.Dir(inst.Executable))); ok {
		return v, nil
	}
	return "", []string{"kind:\"version-unreadable\" 托管目录名不含版本"}
}

// TestResolveInstallationUsesActiveManagedPiWhenNoUserPath：没有用户路径时，
// active 指针指向的托管安装必须被真正用上（Origin=managed，Version 来自指针）。
func TestResolveInstallationUsesActiveManagedPiWhenNoUserPath(t *testing.T) {
	d, _ := newTestDesktop(t)
	root := pi.ManagedRoot()
	if root == "" {
		t.Fatal("测试环境必须有隔离的托管根（MYAGENT_PI_HOME）")
	}
	f := writeManagedInstall(t, root, "1.6.0", []byte("managed-pi-1.6.0"))
	f.activate(t, "pi-active-160")

	d.mu.Lock()
	d.settings.PiPath = ""
	d.mu.Unlock()

	inst, err := d.resolveInstallation(d.Settings())
	if err != nil {
		t.Fatalf("active 指针有效时解析失败: %v", err)
	}
	if !appSamePath(t, inst.Executable, f.exe) {
		t.Fatalf("解析到的可执行体 = %q，期望 active 指针指向的 %q", inst.Executable, f.exe)
	}
	if inst.Origin != pi.OriginManaged {
		t.Fatalf("Origin = %q，期望 %q（托管安装不能被当成外部安装）", inst.Origin, pi.OriginManaged)
	}
	if inst.Version != "1.6.0" {
		t.Fatalf("Version = %q，期望来自 active 指针的 1.6.0", inst.Version)
	}
	if !inst.Runnable {
		t.Fatal("验证通过的托管安装必须是可运行的")
	}
	if inst.ID != "pi-active-160" {
		t.Fatalf("ID = %q，期望沿用指针里的 installationID", inst.ID)
	}
}

// TestResolveInstallationRejectsTamperedActivePointer：指针记录的可执行体哈希不符时，
// 必须结构化失败（带期望/实际哈希），且 run 路径不能启动。
func TestResolveInstallationRejectsTamperedActivePointer(t *testing.T) {
	d, _ := newTestDesktop(t)
	root := pi.ManagedRoot()
	f := writeManagedInstall(t, root, "1.6.0", []byte("managed-pi-1.6.0"))
	f.activate(t, "pi-active-160")

	// 篡改一个字节：文件还在，但已不是激活时那个二进制。
	raw, err := os.ReadFile(f.exe)
	if err != nil {
		t.Fatalf("读可执行体: %v", err)
	}
	raw[0] ^= 0xFF
	if err := os.WriteFile(f.exe, raw, 0o755); err != nil {
		t.Fatalf("篡改可执行体: %v", err)
	}
	actual, err := piinstall.HashFileContent(f.exe)
	if err != nil {
		t.Fatalf("计算篡改后的哈希: %v", err)
	}

	_, err = d.resolveInstallation(d.Settings())
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望契约错误，实际 %T: %v", err, err)
	}
	if appErr.Code != contracts.CodeVerificationFailed {
		t.Fatalf("错误码 = %q，期望 %q（指针损坏，不是\"没配置\"）", appErr.Code, contracts.CodeVerificationFailed)
	}
	if !strings.Contains(appErr.Message, f.sha256) {
		t.Fatalf("错误信息必须带期望哈希 %s，实际 %q", f.sha256, appErr.Message)
	}
	if !strings.Contains(appErr.Message, actual) {
		t.Fatalf("错误信息必须带实际哈希 %s，实际 %q", actual, appErr.Message)
	}
	if !strings.Contains(appErr.Message, piinstall.ActiveFileName) {
		t.Fatalf("错误信息必须指明是哪个指针坏了，实际 %q", appErr.Message)
	}

	// run 路径：校验失败时不得构建出运行时（不能拿一个被换掉的二进制去跑）。
	cur := d.CurrentWorkspace()
	if cur.Error != nil {
		t.Fatalf("登记工作区: %s", cur.Error.Message)
	}
	if tr := d.SetWorkspaceTrust(string(cur.Item.ID), true); tr.Error != nil {
		t.Fatalf("信任工作区: %s", tr.Error.Message)
	}
	if _, startErr := d.Start("你好"); startErr == nil {
		t.Fatal("指针校验失败时 Start 必须拒绝启动")
	} else {
		var startAppErr *contracts.AppError
		if !errors.As(startErr, &startAppErr) || startAppErr.Code != contracts.CodeVerificationFailed {
			t.Fatalf("Start 的错误码 = %v，期望 %q", startErr, contracts.CodeVerificationFailed)
		}
	}
	d.mu.Lock()
	svc := d.svc
	d.mu.Unlock()
	if svc != nil {
		t.Fatal("指针校验失败时不得构建出运行时")
	}
}

// TestResolveInstallationPrefersUserPathOverActiveManaged：用户显式路径优先，
// 且它失效时报 CONFIG_MISSING，绝不静默回退到托管版本。
func TestResolveInstallationPrefersUserPathOverActiveManaged(t *testing.T) {
	d, root := newTestDesktop(t)
	managedRoot := pi.ManagedRoot()
	f := writeManagedInstall(t, managedRoot, "1.6.0", []byte("managed-pi-1.6.0"))
	f.activate(t, "pi-active-160")

	user := writeAppPiStub(t, filepath.Join(root, "user-pi"), appTestPiName())
	d.mu.Lock()
	d.settings.PiPath = user
	d.mu.Unlock()

	inst, err := d.resolveInstallation(d.Settings())
	if err != nil {
		t.Fatalf("用户路径有效时解析失败: %v", err)
	}
	if !appSamePath(t, inst.Executable, user) {
		t.Fatalf("解析到的可执行体 = %q，期望用户显式指定的 %q", inst.Executable, user)
	}
	if inst.Origin != pi.OriginExternal {
		t.Fatalf("Origin = %q，期望 %q（用户指定的是外部安装）", inst.Origin, pi.OriginExternal)
	}

	// 用户路径失效：必须报 CONFIG_MISSING，而不是悄悄用托管版本。
	gone := filepath.Join(root, "gone", "pi.exe")
	d.mu.Lock()
	d.settings.PiPath = gone
	d.mu.Unlock()

	_, err = d.resolveInstallation(d.Settings())
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望契约错误，实际 %T: %v", err, err)
	}
	if appErr.Code != contracts.CodeConfigMissing {
		t.Fatalf("错误码 = %q，期望 %q", appErr.Code, contracts.CodeConfigMissing)
	}
	if !strings.Contains(appErr.Message, "不存在") {
		t.Fatalf("错误信息要说清用户路径不存在，实际 %q", appErr.Message)
	}
}

// TestResolveInstallationDoesNotSilentlyDowngrade：两个托管版本都在、active 指向
// 较新那个，而旧版目录名排在前面 → 解析必须仍选 active 指向的版本。
func TestResolveInstallationDoesNotSilentlyDowngrade(t *testing.T) {
	d, _ := newTestDesktop(t)
	root := pi.ManagedRoot()
	older := writeManagedInstall(t, root, "1.5.0", []byte("managed-pi-1.5.0"))
	newer := writeManagedInstall(t, root, "1.6.0", []byte("managed-pi-1.6.0"))
	newer.activate(t, "pi-active-160")

	inst, err := d.resolveInstallation(d.Settings())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !appSamePath(t, inst.Executable, newer.exe) {
		t.Fatalf("解析到 %q，期望 active 指针指向的 %q（不得按目录名排序降级）", inst.Executable, newer.exe)
	}
	if inst.Version != "1.6.0" {
		t.Fatalf("Version = %q，期望 1.6.0", inst.Version)
	}

	// 对照：F08 的托管候选按目录名排序，firstUsable 取第一个可用——
	// 只按排序选会选到旧版。这正是"由指针而不是排序决定当前版本"的理由。
	disc, err := pi.Discover(context.Background(), pi.DiscoveryRequest{
		ManagedRoot:  filepath.Join(root, "versions"),
		Policy:       pi.DefaultPolicy,
		VersionProbe: appManagedVersionProbe,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if disc.Selected == nil {
		t.Fatalf("对照断言：托管目录里应有可选中候选: %+v", disc.Candidates)
	}
	if !appSamePath(t, disc.Selected.Installation.Executable, older.exe) {
		t.Fatalf("对照断言：按目录名排序应选到旧版 %q，实际 %q",
			older.exe, disc.Selected.Installation.Executable)
	}
}

// TestResolveInstallationWithoutAnyPiReportsConfigMissing：既没有用户路径也没有
// active 指针时，报"未配置"（CONFIG_MISSING），与"指针损坏"（VERIFICATION_FAILED）可区分。
func TestResolveInstallationWithoutAnyPiReportsConfigMissing(t *testing.T) {
	d, _ := newTestDesktop(t)
	if p, err := piinstall.ReadActive(pi.ManagedRoot()); err != nil || p != nil {
		t.Fatalf("隔离的托管根不该有 active 指针: p=%v err=%v", p, err)
	}

	_, err := d.resolveInstallation(d.Settings())
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望契约错误，实际 %T: %v", err, err)
	}
	if appErr.Code != contracts.CodeConfigMissing {
		t.Fatalf("错误码 = %q，期望 %q", appErr.Code, contracts.CodeConfigMissing)
	}
	if !strings.Contains(appErr.Message, "settings.piPath") {
		t.Fatalf("错误信息要指明缺哪个配置，实际 %q", appErr.Message)
	}
}

// TestDiscoverPiSeesManagedVersionAfterInstall：跑真实安装服务之后，DiscoverPi 必须
// 看得到那条托管安装（Origin=managed），并在没有用户路径时选中它。
//
// 这就是 F06 复核发现的缺口：安装落在 <managed>/versions/<version>-<target>/，
// 而发现只扫托管根一层，于是"装好了但应用看不见"。
func TestDiscoverPiSeesManagedVersionAfterInstall(t *testing.T) {
	h := newPiInstallHarness(t)
	h.addPackage(t, "1.5.0")

	installed := h.d.InstallPi("1.5.0")
	if installed.Error != "" {
		t.Fatalf("安装失败: %s（阶段=%s）", installed.Error, installed.FailedStage)
	}
	if installed.Executable == "" {
		t.Fatal("安装结果必须带可执行体路径")
	}

	// 只覆盖版本探针（测试载荷不是 Pi，不能真的执行）；ManagedRoot 仍由生产代码给出，
	// 否则测不到"托管根该指哪里"这件事。
	h.d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = ""
		req.Saved = ""
		req.PathEnv = nil
		req.CommonLocations = nil
		req.VersionProbe = appManagedVersionProbe
		return req
	}

	disc, err := h.d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	var found *pi.PiCandidate
	for i := range disc.Candidates {
		if appSamePath(t, disc.Candidates[i].Installation.Executable, installed.Executable) {
			found = &disc.Candidates[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("托管安装对应用不可见（候选 %+v）", disc.Candidates)
	}
	if found.Origin != pi.CandidateOriginManaged {
		t.Fatalf("候选来源 = %q，期望 %q", found.Origin, pi.CandidateOriginManaged)
	}
	if found.Installation.Origin != pi.OriginManaged {
		t.Fatalf("安装件 Origin = %q，期望 %q", found.Installation.Origin, pi.OriginManaged)
	}
	if found.Installation.Version != "1.5.0" {
		t.Fatalf("候选版本 = %q，期望 1.5.0", found.Installation.Version)
	}
	if disc.Selected == nil || !appSamePath(t, disc.Selected.Installation.Executable, installed.Executable) {
		t.Fatalf("没有用户路径时应选中托管安装，实际 %+v", disc.Selected)
	}
}

// TestResolveInstallationEnvPathBetweenSettingsAndManaged 守新增的第三层优先级。
//
// 环境变量 ANVIL_PI_PATH（旧名 MYAGENT_PI_PATH）是"设置里没选时的默认"：
// 它必须赢过托管副本，但**不能**盖过设置里显式选的路径——否则用户选了 A 却被
// 环境变量改回 B，是那种"重启后又变回去"的隐蔽故障。
func TestResolveInstallationEnvPathBetweenSettingsAndManaged(t *testing.T) {
	d, root := newTestDesktop(t)
	managedRoot := pi.ManagedRoot()
	f := writeManagedInstall(t, managedRoot, "1.6.0", []byte("managed-pi-env"))
	f.activate(t, "pi-active-env")

	envPi := writeAppPiStub(t, filepath.Join(root, "env-pi"), appTestPiName())
	t.Setenv("ANVIL_PI_PATH", envPi)

	inst, err := d.resolveInstallation(d.Settings())
	if err != nil {
		t.Fatalf("环境变量路径有效时解析失败: %v", err)
	}
	if !appSamePath(t, inst.Executable, envPi) {
		t.Fatalf("解析到的可执行体 = %q，期望环境变量指定的 %q", inst.Executable, envPi)
	}

	// 设置里显式选了一条 → 环境变量不得覆盖。
	settingsPi := writeAppPiStub(t, filepath.Join(root, "settings-pi"), appTestPiName())
	d.mu.Lock()
	d.settings.PiPath = settingsPi
	d.mu.Unlock()
	inst, err = d.resolveInstallation(d.Settings())
	if err != nil {
		t.Fatalf("设置路径有效时解析失败: %v", err)
	}
	if !appSamePath(t, inst.Executable, settingsPi) {
		t.Fatalf("设置里显式选择必须优先，实际 %q", inst.Executable)
	}
}
