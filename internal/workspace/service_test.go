package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	svc := New(store)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	var tick int
	svc.SetClock(func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Minute)
	})
	return svc, root
}

func mustDir(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("创建目录 %s: %v", p, err)
	}
	return p
}

// TestWorkspaceCanonicalizationAndTrust 覆盖 C01 的四条硬要求：
// 同一路径重复添加返回已有 ID、链接路径与真实路径归一、未信任不可运行、
// 不可访问目录给出明确错误。
func TestWorkspaceCanonicalizationAndTrust(t *testing.T) {
	ctx := context.Background()
	svc, root := newTestService(t)
	dir := mustDir(t, root, "proj")

	first, err := svc.CreateWorkspace(ctx, dir)
	if err != nil {
		t.Fatalf("首次添加: %v", err)
	}
	if first.Trusted {
		t.Fatal("新工作区默认必须是未信任：选中目录不等于允许执行代码")
	}
	if first.Name != "proj" {
		t.Fatalf("展示名应取目录名 proj，实际 %q", first.Name)
	}

	// 同一个路径换个写法（尾部分隔符 + . 段）不能造出第二个工作区。
	variants := []string{
		dir,
		dir + string(filepath.Separator),
		filepath.Join(dir, "."),
		filepath.Join(dir, "..", "proj"),
	}
	for _, v := range variants {
		got, err := svc.CreateWorkspace(ctx, v)
		if err != nil {
			t.Fatalf("变体 %q 添加失败: %v", v, err)
		}
		if got.ID != first.ID {
			t.Fatalf("变体 %q 生成了新 ID %s（期望复用 %s）", v, got.ID, first.ID)
		}
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列举: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("同一目录的 4 种写法应只有 1 个工作区，实际 %d 个", len(list))
	}

	// 未信任时不能启动可执行资源。
	err = svc.AssertRunnable(ctx, first.ID)
	if err == nil {
		t.Fatal("未信任的工作区必须拒绝运行")
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeUnauthorized {
		t.Fatalf("期望 UNAUTHORIZED，实际 %v", err)
	}

	if err := svc.SetTrust(ctx, first.ID, true); err != nil {
		t.Fatalf("设置信任: %v", err)
	}
	if err := svc.AssertRunnable(ctx, first.ID); err != nil {
		t.Fatalf("信任后应可运行: %v", err)
	}

	// 信任状态要持久化，而不是只活在内存里。
	reloaded, err := svc.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("重新读取: %v", err)
	}
	if !reloaded.Trusted {
		t.Fatal("信任状态没有落盘")
	}
	if reloaded.CanonicalPath != first.CanonicalPath {
		t.Fatalf("路径被改写: %q → %q", first.CanonicalPath, reloaded.CanonicalPath)
	}

	// 关闭信任后立刻回到不可运行。
	if err := svc.SetTrust(ctx, first.ID, false); err != nil {
		t.Fatalf("取消信任: %v", err)
	}
	if err := svc.AssertRunnable(ctx, first.ID); err == nil {
		t.Fatal("取消信任后必须重新拒绝运行")
	}
}

// TestWorkspaceLinkResolvesToSameEntry 验证经符号链接/junction 进入同一目录
// 不会裂成两个工作区。
//
// 这条不是洁癖：两个工作区意味着两份信任状态和两份配置层，
// 用户以为只信任了一个，实际上另一个仍是未信任或反之，行为会变得不可预测。
func TestWorkspaceLinkResolvesToSameEntry(t *testing.T) {
	ctx := context.Background()
	svc, root := newTestService(t)
	target := mustDir(t, root, "real-proj")
	link := filepath.Join(root, "link-proj")

	if err := os.Symlink(target, link); err != nil {
		// 本机未开启符号链接权限时如实跳过，并说明这条断言未被执行，
		// 不让"跳过"看起来像"通过"。
		t.Skipf("本机无法创建目录符号链接，链接归一未验证: %v", err)
	}

	viaReal, err := svc.CreateWorkspace(ctx, target)
	if err != nil {
		t.Fatalf("添加真实路径: %v", err)
	}
	viaLink, err := svc.CreateWorkspace(ctx, link)
	if err != nil {
		t.Fatalf("添加链接路径: %v", err)
	}
	if viaLink.ID != viaReal.ID {
		t.Fatalf("链接与真实路径应是同一工作区：%s ≠ %s", viaLink.ID, viaReal.ID)
	}
	if viaLink.CanonicalPath != viaReal.CanonicalPath {
		t.Fatalf("链接路径必须归一成真实路径：%q vs %q", viaLink.CanonicalPath, viaReal.CanonicalPath)
	}
}

// TestWorkspaceCaseInsensitiveOnWindows 守住"用户输入不同大小写不会裂成两个工作区"。
//
// 需要说明它的实际覆盖：做对抗性验证时把 dedupeKey 的 ToLower 去掉，本条**依然通过**——
// 因为 Windows 上 EvalSymlinks 已经返回磁盘上的真实大小写，把 dedupeKey 的作用掩盖了。
// 所以它验证的是端到端行为，不能当作 dedupeKey 的证明；dedupeKey 由下一条直接覆盖。
func TestWorkspaceCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 文件系统默认大小写不敏感")
	}
	ctx := context.Background()
	svc, root := newTestService(t)
	dir := mustDir(t, root, "MixedCase")

	a, err := svc.CreateWorkspace(ctx, dir)
	if err != nil {
		t.Fatalf("添加: %v", err)
	}
	b, err := svc.CreateWorkspace(ctx, strings.ToLower(dir))
	if err != nil {
		t.Fatalf("小写路径添加: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("同一目录不同大小写应复用同一工作区：%s ≠ %s", a.ID, b.ID)
	}
}

// TestDedupeKeyNormalizesCase 直接覆盖第二道防线。
//
// 不能只靠 EvalSymlinks：它依赖文件系统实际返回的形态，遇到网络盘、
// 大小写敏感目录或平台行为差异时未必归一，而那正是裂成两个工作区的时刻。
func TestDedupeKeyNormalizesCase(t *testing.T) {
	got := dedupeKey(`C:\Work\Proj`)
	want := dedupeKey(`c:\work\proj`)
	if runtime.GOOS == "windows" {
		if got != want {
			t.Fatalf("Windows 下 dedupeKey 必须大小写不敏感：%q vs %q", got, want)
		}
		if idFor(`C:\Work\Proj`) != idFor(`c:\work\proj`) {
			t.Fatal("同一路径不同大小写必须得到同一 ID")
		}
		return
	}
	// 非 Windows 保持原样：大小写敏感文件系统上这是两个真实不同的目录。
	if got == want {
		t.Fatalf("非 Windows 不应折叠大小写：%q", got)
	}
}

// TestWorkspaceRejectsUnusablePaths 覆盖空路径、不存在、非目录三类输入。
// 每类都要给出可分辨的错误码，而不是统一"失败"。
func TestWorkspaceRejectsUnusablePaths(t *testing.T) {
	ctx := context.Background()
	svc, root := newTestService(t)

	cases := []struct {
		name string
		path string
		code string
	}{
		{"空路径", "   ", contracts.CodeConfigMissing},
		{"不存在", filepath.Join(root, "nope"), contracts.CodeNotFound},
		{"是文件不是目录", writeFile(t, root, "a.txt"), contracts.CodeUnsupported},
	}
	for _, c := range cases {
		_, err := svc.CreateWorkspace(ctx, c.path)
		if err == nil {
			t.Fatalf("%s：应当失败", c.name)
		}
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) {
			t.Fatalf("%s：应返回 AppError，实际 %T %v", c.name, err, err)
		}
		if appErr.Code != c.code {
			t.Fatalf("%s：期望 %s，实际 %s（%s）", c.name, c.code, appErr.Code, appErr.Message)
		}
	}
}

// TestSetTrustPreservesOtherConfigFields 确认改信任状态不会顺手删掉同层配置。
func TestSetTrustPreservesOtherConfigFields(t *testing.T) {
	ctx := context.Background()
	svc, root := newTestService(t)
	dir := mustDir(t, root, "proj")

	ws, err := svc.CreateWorkspace(ctx, dir)
	if err != nil {
		t.Fatalf("添加: %v", err)
	}
	key := config.WorkspaceKey(ws.ID)

	// 模拟用户在这一层写了权限配置。
	cur, err := svc.store.Load(ctx, key)
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	if _, err := svc.store.CompareAndSwap(ctx, key, cur.Revision,
		[]byte(`{"schemaVersion":1,"workspace":{"id":"`+string(ws.ID)+`","name":"proj","canonicalPath":"`+
			strings.ReplaceAll(ws.CanonicalPath, `\`, `\\`)+`","trusted":false},`+
			`"permissions":{"shell":true,"network":true}}`)); err != nil {
		t.Fatalf("写入同层配置: %v", err)
	}

	if err := svc.SetTrust(ctx, ws.ID, true); err != nil {
		t.Fatalf("设置信任: %v", err)
	}
	after, err := svc.store.Load(ctx, key)
	if err != nil {
		t.Fatalf("再次读取: %v", err)
	}
	if !strings.Contains(string(after.Body), `"shell":true`) {
		t.Fatalf("改信任时丢了同层的 permissions：%s", string(after.Body))
	}
}

// TestSetTrustRejectsUnknownWorkspace 不能给不存在的 ID 凭空造一条记录。
func TestSetTrustRejectsUnknownWorkspace(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	err := svc.SetTrust(ctx, "ws-doesnotexist", true)
	if err == nil {
		t.Fatal("不存在的工作区应拒绝")
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeNotFound {
		t.Fatalf("期望 NOT_FOUND，实际 %v", err)
	}
}

// TestListSurfacesCorruptDocument 坏掉的工作区文档必须报错，不能静默消失。
func TestListSurfacesCorruptDocument(t *testing.T) {
	ctx := context.Background()
	svc, root := newTestService(t)
	dir := mustDir(t, root, "proj")
	if _, err := svc.CreateWorkspace(ctx, dir); err != nil {
		t.Fatalf("添加: %v", err)
	}

	// 直接改磁盘文件：checksum 不再匹配，Load 会判为外部编辑。
	path := filepath.Join(svc.store.Root(), config.WorkspaceKey(idFor(dir))+".json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"revision":1,"body":{},"checksum":"deadbeef"}`), 0o600); err != nil {
		t.Fatalf("篡改文件: %v", err)
	}

	if _, err := svc.List(ctx); err == nil {
		t.Fatal("配置被外部改动后列举必须报错，而不是当作没有这个工作区")
	} else {
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) || appErr.Code != contracts.CodeConfigConflict {
			t.Fatalf("期望 CONFIG_CONFLICT，实际 %v", err)
		}
	}
}

func writeFile(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("创建文件: %v", err)
	}
	return p
}
