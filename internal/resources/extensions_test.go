package resources

// C03 扩展部分：RES04 的"系统 policy 扩展不可关闭"与"启停真的影响执行参数"。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/runtime/pi"
)

// newExtService 造一个带系统扩展与一个第三方扩展的服务。
func newExtService(t *testing.T) (*Service, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	system := filepath.Join(root, "pi-extensions", "harness-policy.ts")
	if err := os.MkdirAll(filepath.Dir(system), 0o755); err != nil {
		t.Fatalf("建系统扩展目录: %v", err)
	}
	if err := os.WriteFile(system, []byte("export default {}"), 0o644); err != nil {
		t.Fatalf("写系统扩展: %v", err)
	}
	third := filepath.Join(root, "extensions", "hello.ts")
	if err := os.MkdirAll(filepath.Dir(third), 0o755); err != nil {
		t.Fatalf("建扩展目录: %v", err)
	}
	if err := os.WriteFile(third, []byte("export const hi = 1"), 0o644); err != nil {
		t.Fatalf("写第三方扩展: %v", err)
	}
	svc := New(store, credentials.NewMemStore(),
		WithLayout(NewLayout(root)), WithSystemExtension(system))
	return svc, root, system
}

// TestSystemExtensionCannotBeDisabled 守 RES04 的第一句。
func TestSystemExtensionCannotBeDisabled(t *testing.T) {
	svc, _, _ := newExtService(t)
	ctx := context.Background()

	set, err := svc.ListExtensions(ctx)
	if err != nil {
		t.Fatalf("列举失败: %v", messageOf(err))
	}
	var sys *Extension
	for i := range set.Items {
		if set.Items[i].System {
			sys = &set.Items[i]
		}
	}
	if sys == nil {
		t.Fatal("列表里必须包含系统策略扩展")
	}
	if !sys.Enabled || sys.CanDisable {
		t.Fatalf("系统扩展应恒为启用且不可关闭，实际 enabled=%v canDisable=%v", sys.Enabled, sys.CanDisable)
	}

	_, err = svc.SetExtensionEnabled(ctx, SystemExtensionID, set.Revision, false)
	if err == nil {
		t.Fatal("关闭系统策略扩展必须被拒绝")
	}
	if !strings.Contains(messageOf(err), "不可关闭") {
		t.Fatalf("错误应说明不可关闭，实际 %q", messageOf(err))
	}

	// 拒绝之后它仍然在运行集合里，且排在最前。
	plan, err := svc.CompileExtensions(ctx)
	if err != nil {
		t.Fatalf("编译失败: %v", messageOf(err))
	}
	if len(plan.Paths) == 0 || plan.IDs[0] != SystemExtensionID {
		t.Fatalf("系统扩展必须始终出现在 -e 参数里，实际 %v", plan.IDs)
	}
}

// TestThirdPartyExtensionDefaultsDisabled 守"第三方默认不自动启用"。
func TestThirdPartyExtensionDefaultsDisabled(t *testing.T) {
	svc, _, _ := newExtService(t)
	ctx := context.Background()

	plan, err := svc.CompileExtensions(ctx)
	if err != nil {
		t.Fatalf("编译失败: %v", messageOf(err))
	}
	if len(plan.IDs) != 1 {
		t.Fatalf("未显式启用时只应有系统扩展，实际 %v", plan.IDs)
	}

	set, _ := svc.ListExtensions(ctx)
	var third *Extension
	for i := range set.Items {
		if set.Items[i].Origin == OriginLocal {
			third = &set.Items[i]
		}
	}
	if third == nil {
		t.Fatal("托管目录里的扩展应被列出")
	}
	if third.Enabled {
		t.Fatal("第三方扩展默认不应启用")
	}
	if !third.Executable || !strings.Contains(third.Note, "审批") {
		t.Fatalf("要说明它是可执行代码且启用不等于免审批，实际 executable=%v note=%q", third.Executable, third.Note)
	}
}

// TestEnableThirdPartyExtensionAffectsArgs 守"启停影响真实执行，不只是菜单"。
func TestEnableThirdPartyExtensionAffectsArgs(t *testing.T) {
	svc, root, _ := newExtService(t)
	third := filepath.Join(root, "extensions", "hello.ts")
	ctx := context.Background()

	set, _ := svc.ListExtensions(ctx)
	if _, err := svc.SetExtensionEnabled(ctx, "hello", set.Revision, true); err != nil {
		t.Fatalf("启用失败: %v", messageOf(err))
	}
	plan, err := svc.CompileExtensions(ctx)
	if err != nil {
		t.Fatalf("编译失败: %v", messageOf(err))
	}
	if len(plan.IDs) != 2 || plan.IDs[1] != "hello" {
		t.Fatalf("启用后应同时包含系统与第三方，实际 %v", plan.IDs)
	}
	if plan.Paths[1] != third {
		t.Fatalf("第三方扩展路径不对: %s", plan.Paths[1])
	}

	set, _ = svc.ListExtensions(ctx)
	if _, err := svc.SetExtensionEnabled(ctx, "hello", set.Revision, false); err != nil {
		t.Fatalf("禁用失败: %v", messageOf(err))
	}
	plan, _ = svc.CompileExtensions(ctx)
	if len(plan.IDs) != 1 {
		t.Fatalf("禁用后应只剩系统扩展，实际 %v", plan.IDs)
	}

	// RES04 的外部观察（filesystem）：启用第三方扩展后，编译出的隔离参数交给
	// fakeworker，argv 由**子进程自己**写下来，父进程只读文件。
	set, _ = svc.ListExtensions(ctx)
	if _, err := svc.SetExtensionEnabled(ctx, "hello", set.Revision, true); err != nil {
		t.Fatalf("重新启用失败: %v", messageOf(err))
	}
	enabled, err := svc.CompileExtensions(ctx)
	if err != nil {
		t.Fatalf("重新编译失败: %v", messageOf(err))
	}
	isoArgs := pi.IsolationSpec{ExtraExtensions: enabled.Paths}.Args()
	rec := runRecordingChild(t, isoArgs)
	if resourcesObservationDir() == "" {
		return
	}
	argvPath := filepath.Join(resourcesObservationDir(), "worker-argv.json")
	if err := os.WriteFile(argvPath, mustJSON(t, rec.Argv), 0o644); err != nil {
		t.Fatalf("写 worker-argv.json 失败: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("读回 worker-argv.json 失败: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("磁盘上的 worker-argv.json 不是合法 JSON: %v", err)
	}
	extFlags := make([]string, 0, 2)
	isolationFlags := make([]string, 0, 8)
	for i, a := range argv {
		// Pi 的显式扩展参数是 -e <path>（策略扩展与第三方扩展都走它）。
		if (a == "-e" || a == "--extension") && i+1 < len(argv) {
			extFlags = append(extFlags, argv[i+1])
		}
		if strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "--no-") {
			isolationFlags = append(isolationFlags, a)
		}
	}
	writeResourcesObservation(t, map[string]any{
		"argv":               argv,
		"argvLen":            len(argv),
		"extensionFlags":     extFlags,
		"extensionFlagCount": len(extFlags),
		"isolationFlags":     isolationFlags,
		"extensionCount":     len(enabled.IDs),
		"thirdPartyPath":     third,
		"argvFile":           filepath.ToSlash(argvPath),
	}, nil)
}

// mustJSON 序列化观察值（序列化失败直接失败，不写半截文件）。
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

// TestUnknownExtensionRejected 防"启用了不存在的扩展却显示成功"。
func TestUnknownExtensionRejected(t *testing.T) {
	svc, _, _ := newExtService(t)
	set, _ := svc.ListExtensions(context.Background())
	if _, err := svc.SetExtensionEnabled(context.Background(), "ghost", set.Revision, true); err == nil {
		t.Fatal("不存在的扩展应被拒绝")
	}
}

// TestExtensionDigestChangesWithState 守运行时签名：启用状态变了，签名必须变，
// 否则换扩展之后仍然复用旧运行时，界面显示已启用而实际没加载。
func TestExtensionDigestChangesWithState(t *testing.T) {
	svc, _, _ := newExtService(t)
	ctx := context.Background()
	before, _ := svc.CompileExtensions(ctx)
	set, _ := svc.ListExtensions(ctx)
	if _, err := svc.SetExtensionEnabled(ctx, "hello", set.Revision, true); err != nil {
		t.Fatalf("启用失败: %v", messageOf(err))
	}
	after, _ := svc.CompileExtensions(ctx)
	if before.Digest == after.Digest {
		t.Fatal("启用扩展后摘要必须变化，否则运行时不会重建")
	}
}
