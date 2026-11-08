package app

// 产品自带扩展必须跟着二进制走，而不是跟着仓库布局走。
//
// 打包后的 Anvil.exe 旁边没有 pi-extensions/，旧定位逻辑（exe 旁 → 上级 →
// cwd → 上级）于是让每一次 run 都停在
// CONFIG_MISSING: 找不到策略扩展 pi-extensions/harness-policy.ts。
// 这里的测试都在"没有 pi-extensions 的空目录"里跑，复现打包现场：
// 只有把嵌入内容物化到 <appRoot>/extensions 才能解析出扩展。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	anvil "my-agent-go"
	"my-agent-go/internal/contracts"
)

// isolateToEmptyCwd 把工作目录换成没有 pi-extensions 的空目录。
//
// 不换目录的话，测试进程的 cwd 可能正好是仓库，旧定位逻辑仍能找到开发态副本，
// 于是"打包后能不能跑"这条断言就变得没意义。
func isolateToEmptyCwd(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

// materializedPath 是产品扩展在一个应用数据根下的物化位置。
//
// 子目录 product 不是随便起的：<appRoot>/extensions 是用户可以安装/启用/删除的
// 第三方扩展目录，产品自带扩展不能混进去（否则会以"可关闭"的形式出现在扩展页，
// 澄清/附件扩展还会被当成托管扩展再 -e 一次）。
func materializedPath(root, name string) string {
	return filepath.Join(root, "extensions", "product", name)
}

// withAssetAbsent 把某条产品扩展从"嵌入"里摘掉，用来覆盖"嵌入缺条目"的分支。
//
// 用钩子而不是真删嵌入：删了就没法验证"缺条目"这条路径本身的行为。
func withAssetAbsent(t *testing.T, names ...string) {
	t.Helper()
	orig := productExtensionAsset
	productExtensionAsset = func(name string) ([]byte, bool) {
		for _, n := range names {
			if n == name {
				return nil, false
			}
		}
		return orig(name)
	}
	t.Cleanup(func() { productExtensionAsset = orig })
}

func embeddedExtension(t *testing.T, name string) []byte {
	t.Helper()
	body, ok := anvil.ProductExtension(name)
	if !ok || len(body) == 0 {
		t.Fatalf("嵌入里必须有产品扩展 %s", name)
	}
	return body
}

// TestProductExtensionsMaterializeIntoAppRoot 覆盖打包现场：没有 pi-extensions
// 目录时，三条产品扩展都能从 <appRoot>/extensions 解析出来，且字节等于嵌入内容。
func TestProductExtensionsMaterializeIntoAppRoot(t *testing.T) {
	isolateToEmptyCwd(t)
	_, root := newTestDesktop(t)

	names := anvil.ProductExtensionNames()
	if len(names) != 3 {
		t.Fatalf("应嵌入三条产品扩展（策略/澄清/附件），实际 %v", names)
	}
	for _, name := range names {
		target := materializedPath(root, name)
		body, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("启动后应已物化 %s: %v", name, err)
		}
		if !bytes.Equal(body, embeddedExtension(t, name)) {
			t.Fatalf("%s 的物化字节必须等于嵌入内容（否则加载的不是这份二进制自带的扩展）", name)
		}
	}

	// 策略扩展的解析结果必须指向物化副本，而不是别处的同名文件。
	got, err := findPolicyExtension()
	if err != nil {
		t.Fatalf("没有 pi-extensions 目录时也必须解析到策略扩展: %v", err)
	}
	want := materializedPath(root, "harness-policy.ts")
	if got != want {
		t.Fatalf("策略扩展应解析到物化副本 %s，实际 %s", want, got)
	}
}

// TestTamperedProductExtensionIsRewritten 覆盖"被改过的副本不能被继续使用"。
//
// 策略扩展是执行前授权的唯一来源：盘上那份被换成"永远 allow"的版本，
// 比找不到它更危险，因为 run 会照常跑起来。
func TestTamperedProductExtensionIsRewritten(t *testing.T) {
	isolateToEmptyCwd(t)
	_, root := newTestDesktop(t)

	for _, name := range []string{"harness-policy.ts", "anvil-clarification.ts"} {
		target := materializedPath(root, name)
		if err := os.WriteFile(target, []byte("// 篡改：删掉执行前授权\n"), 0o644); err != nil {
			t.Fatalf("写篡改副本 %s: %v", name, err)
		}
	}

	got, err := findPolicyExtension()
	if err != nil {
		t.Fatalf("篡改的策略扩展应被重写而不是让 run 拒绝启动: %v", err)
	}
	if got != materializedPath(root, "harness-policy.ts") {
		t.Fatalf("应解析到物化副本，实际 %s", got)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("读回策略扩展: %v", err)
	}
	if !bytes.Equal(body, embeddedExtension(t, "harness-policy.ts")) {
		t.Fatal("篡改过的策略扩展必须先被重写回嵌入内容，再交给 Pi")
	}

	// 可选扩展同理：解析时也要先纠正盘上那份。
	paths := productExtensionPaths()
	if len(paths) != 2 {
		t.Fatalf("两条可选产品扩展都应被解析出来，实际 %v", paths)
	}
	clarification := materializedPath(root, "anvil-clarification.ts")
	body, err = os.ReadFile(clarification)
	if err != nil {
		t.Fatalf("读回澄清扩展: %v", err)
	}
	if !bytes.Equal(body, embeddedExtension(t, "anvil-clarification.ts")) {
		t.Fatal("篡改过的澄清扩展必须先被重写回嵌入内容")
	}
}

// TestProductExtensionEnvOverrideWins env 覆盖是发布/排障入口，优先级最高。
func TestProductExtensionEnvOverrideWins(t *testing.T) {
	isolateToEmptyCwd(t)
	_, root := newTestDesktop(t)

	custom := filepath.Join(t.TempDir(), "custom-policy.ts")
	if err := os.WriteFile(custom, []byte("// 外部策略扩展\n"), 0o600); err != nil {
		t.Fatalf("写外部策略扩展: %v", err)
	}
	t.Setenv("ANVIL_POLICY_EXT", custom)

	got, err := findPolicyExtension()
	if err != nil {
		t.Fatalf("env 覆盖存在时必须用它: %v", err)
	}
	if got != custom {
		t.Fatalf("env 覆盖应压过物化副本 %s，实际 %s", materializedPath(root, "harness-policy.ts"), got)
	}
}

// TestMaterializedCopyBeatsDevLayout 固定解析顺序：<appRoot>/extensions 先于
// 开发态的 pi-extensions/（cwd 及其上级）。
func TestMaterializedCopyBeatsDevLayout(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	_, root := newTestDesktop(t)

	devDir := filepath.Join(cwd, "pi-extensions")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatalf("建开发态扩展目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "harness-policy.ts"), []byte("// 开发态副本\n"), 0o644); err != nil {
		t.Fatalf("写开发态副本: %v", err)
	}

	got, err := findPolicyExtension()
	if err != nil {
		t.Fatalf("解析策略扩展: %v", err)
	}
	want := materializedPath(root, "harness-policy.ts")
	if got != want {
		t.Fatalf("物化副本优先于开发态布局：期望 %s，实际 %s", want, got)
	}
}

// TestMaterializedExtensionsAreNotUserExtensions 钉住"产品目录与用户目录分开"。
//
// <appRoot>/extensions 是第三方扩展目录：把自带扩展直接放进去，扩展页就会出现
// 一条"harness-policy"的"可关闭"条目（点下去什么也不会变），而澄清/附件扩展会被
// 当成托管扩展再 -e 一次——同一份扩展加载两遍。这里从绑定层看结果，而不是看常量。
func TestMaterializedExtensionsAreNotUserExtensions(t *testing.T) {
	isolateToEmptyCwd(t)
	d, root := newTestDesktop(t)

	for _, name := range anvil.ProductExtensionNames() {
		if _, err := os.Stat(materializedPath(root, name)); err != nil {
			t.Fatalf("前置条件：%s 应已物化: %v", name, err)
		}
	}

	listed := d.ListExtensions()
	if listed.Error != nil {
		t.Fatalf("列扩展失败: %s: %s", listed.Error.Code, listed.Error.Message)
	}
	for _, item := range listed.Items {
		if item.System {
			continue
		}
		t.Fatalf("物化的产品扩展不能以用户可关闭的第三方扩展出现: id=%s path=%s", item.ID, item.Path)
	}
}

// TestMaterializationKeepsIdenticalFilesUntouched 内容相同就不写：
// 每次启动都重写会让 mtime 抖动，测试与安装包校验都会被噪音干扰。
func TestMaterializationKeepsIdenticalFilesUntouched(t *testing.T) {
	isolateToEmptyCwd(t)
	_, root := newTestDesktop(t)

	target := materializedPath(root, "harness-policy.ts")
	old := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("调旧 mtime: %v", err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat 物化副本: %v", err)
	}

	if _, err := findPolicyExtension(); err != nil {
		t.Fatalf("解析策略扩展: %v", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatalf("再次 stat 物化副本: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("内容一致时不该重写（mtime 从 %s 变成 %s）", before.ModTime(), after.ModTime())
	}
}

// TestMissingEmbeddedPolicyExtensionIsConfigMissing 嵌入里没有策略扩展 =
// 没有执行前授权 = 拒绝启动，错误码与文案形状保持和旧逻辑一致。
func TestMissingEmbeddedPolicyExtensionIsConfigMissing(t *testing.T) {
	isolateToEmptyCwd(t)
	newTestDesktop(t)
	withAssetAbsent(t, "harness-policy.ts")

	_, err := findPolicyExtension()
	if err == nil {
		t.Fatal("嵌入里没有策略扩展时必须失败，而不是让 Pi 在没有执行前授权的情况下跑工具")
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("应是带错误码的 AppError，实际 %T: %v", err, err)
	}
	if appErr.Code != contracts.CodeConfigMissing {
		t.Fatalf("期望 CONFIG_MISSING，实际 %s", appErr.Code)
	}
	if !strings.Contains(appErr.Message, "策略扩展") ||
		!strings.Contains(appErr.Message, "harness-policy.ts") {
		t.Fatalf("错误文案要指明缺的是哪个扩展，实际 %q", appErr.Message)
	}
}

// TestMissingOptionalExtensionIsReportedNotPassed 可选扩展缺失时既不传给 Pi，
// 也必须让 Status/诊断（同一份隔离参数）看出来它没在里面。
func TestMissingOptionalExtensionIsReportedNotPassed(t *testing.T) {
	isolateToEmptyCwd(t)
	d, root := newTestDesktop(t)
	withAssetAbsent(t, "anvil-clarification.ts")

	// 启动时物化过的那份还在盘上，但嵌入里没有它就不该再被当成产品扩展使用。
	if _, err := os.Stat(materializedPath(root, "anvil-clarification.ts")); err != nil {
		t.Fatalf("前置条件：启动时应已物化过澄清扩展: %v", err)
	}

	paths := productExtensionPaths()
	if len(paths) != 1 || filepath.Base(paths[0]) != "anvil-attachment.ts" {
		t.Fatalf("缺失的澄清扩展不能被传给 Pi，实际 %v", paths)
	}
	if got := d.ProductExtensions(); len(got) != 1 {
		t.Fatalf("诊断口径必须与实际加载一致，实际 %v", got)
	}
	if opts := d.clarificationOptions(); opts.Enabled {
		t.Fatalf("没有澄清扩展就不能建立澄清桥，实际 %+v", opts)
	}
	if iso := d.Status().Isolation; strings.Contains(iso, "anvil-clarification.ts") {
		t.Fatalf("Status 的隔离参数不能宣称加载了不存在的扩展: %s", iso)
	}
}
