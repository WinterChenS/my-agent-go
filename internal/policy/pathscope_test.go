package policy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// scopeTree 是一个根目录 + 一个根外目录 + 若干链接的测试地形。
type scopeTree struct {
	base    string
	root    string
	outside string
}

func newScopeTree(t *testing.T) scopeTree {
	t.Helper()
	base := t.TempDir()
	tr := scopeTree{
		base:    base,
		root:    filepath.Join(base, "root"),
		outside: filepath.Join(base, "outside"),
	}
	for _, d := range []string{tr.root, tr.outside, filepath.Join(tr.root, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("建目录 %s: %v", d, err)
		}
	}
	mustWrite(t, filepath.Join(tr.root, "inside.txt"), "inside")
	mustWrite(t, filepath.Join(tr.outside, "secret.txt"), "secret")
	return tr
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

func mustGuard(t *testing.T, roots ...string) *PathGuard {
	t.Helper()
	g, err := NewPathGuard(roots...)
	if err != nil {
		t.Fatalf("构造 PathGuard: %v", err)
	}
	return g
}

// TestPathGuardAcceptsInsideRoot 覆盖"根内的正常路径与不存在的新文件都应通过"。
// 不存在的新文件必须可用：写新文件是最常见的写操作，不能因为目标还没建就拒绝。
//
// 断言用 os.SameFile（OS 层面的身份判定）而不是字符串比较：
// t.TempDir() 在 Windows 上常给出 8.3 短名（ADMINI~1），与解析后的长名文本不同
// 但指向同一文件，字符串比较会给出假失败。
func TestPathGuardAcceptsInsideRoot(t *testing.T) {
	tr := newScopeTree(t)
	g := mustGuard(t, tr.root)

	existing := []struct {
		name   string
		target string
		want   string
	}{
		{"已存在文件", filepath.Join(tr.root, "inside.txt"), filepath.Join(tr.root, "inside.txt")},
		{"含 .. 但仍在根内", filepath.Join(tr.root, "sub", "..", "inside.txt"), filepath.Join(tr.root, "inside.txt")},
		{"根目录自身", tr.root, tr.root},
	}
	for _, tc := range existing {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := g.Check(tc.target)
			if err != nil {
				t.Fatalf("应通过，实际拒绝: %v", err)
			}
			got, err1 := os.Stat(dec.Real)
			want, err2 := os.Stat(tc.want)
			if err1 != nil || err2 != nil {
				t.Fatalf("stat: %v / %v", err1, err2)
			}
			if !os.SameFile(got, want) {
				t.Fatalf("真实路径 %q 与期望 %q 不是同一文件", dec.Real, tc.want)
			}
			if dec.Root != tr.root {
				t.Fatalf("命中根 = %q，期望 %q", dec.Root, tr.root)
			}
		})
	}

	// 尚不存在的新文件：断言父目录是同一目录，且文件名原样保留。
	missing := []struct {
		name   string
		target string
		parent string
		base   string
	}{
		{"新文件", filepath.Join(tr.root, "new.txt"), tr.root, "new.txt"},
		{"深层新文件", filepath.Join(tr.root, "sub", "deep", "new.txt"), filepath.Join(tr.root, "sub", "deep"), "new.txt"},
	}
	for _, tc := range missing {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := g.Check(tc.target)
			if err != nil {
				t.Fatalf("尚不存在的新文件应通过: %v", err)
			}
			if filepath.Base(dec.Real) != tc.base {
				t.Fatalf("文件名 = %q，期望 %q", filepath.Base(dec.Real), tc.base)
			}
			if foldKey(filepath.Dir(dec.Real)) != foldKey(tr.root) && foldKey(filepath.Dir(dec.Real)) != foldKey(tc.parent) {
				t.Fatalf("父目录 = %q，期望 %q（或根的等价长名）", filepath.Dir(dec.Real), tc.parent)
			}
			if dec.Root != tr.root {
				t.Fatalf("命中根 = %q，期望 %q", dec.Root, tr.root)
			}
		})
	}
}

// TestPathGuardRejectsEscapes 覆盖 SEC05 点名的越界形态。
//
// 每一行都必须被拒，且拒绝码是 UNAUTHORIZED（不是"参数错误"）——
// 越界是权限结论，调用方据此拒绝执行，不能当成可重试的参数问题。
func TestPathGuardRejectsEscapes(t *testing.T) {
	tr := newScopeTree(t)
	g := mustGuard(t, tr.root)

	// 根内指向根外的符号链接：目录链接与文件链接各一。
	dirLink := filepath.Join(tr.root, "out-dir")
	if err := os.Symlink(tr.outside, dirLink); err != nil {
		t.Fatalf("创建目录符号链接: %v", err)
	}
	fileLink := filepath.Join(tr.root, "out-file.txt")
	if err := os.Symlink(filepath.Join(tr.outside, "secret.txt"), fileLink); err != nil {
		t.Fatalf("创建文件符号链接: %v", err)
	}

	cases := []struct {
		name   string
		target string
	}{
		{"父目录越界", filepath.Join(tr.root, "..", "outside", "secret.txt")},
		{"多级父目录越界", filepath.Join(tr.root, "sub", "..", "..", "outside", "secret.txt")},
		{"绝对路径越界", filepath.Join(tr.outside, "secret.txt")},
		{"前缀相同的兄弟目录", filepath.Join(tr.base, "root-sibling", "x.txt")},
		{"目录符号链接指向根外", filepath.Join(dirLink, "secret.txt")},
		{"目录符号链接 + 不存在的新文件名", filepath.Join(dirLink, "created.txt")},
		{"文件符号链接指向根外", fileLink},
		{"空路径", ""},
		{"仅空白", "   "},
		{"含 NUL", tr.root + "\x00.txt"},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases,
			struct {
				name   string
				target string
			}{"UNC 共享", `\\localhost\C$\Windows\win.ini`},
			struct {
				name   string
				target string
			}{"设备命名空间前缀", `\\?\` + strings.ReplaceAll(tr.root, `/`, `\`) + `\..\..\outside\secret.txt`},
			struct {
				name   string
				target string
			}{"设备路径", `\\.\PhysicalDrive0`},
			struct {
				name   string
				target string
			}{"驱动器相对路径", `C:` + `inside`},
			struct {
				name   string
				target string
			}{"备用数据流", filepath.Join(tr.root, "inside.txt") + ":hidden"},
		)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := g.Check(tc.target); err == nil {
				t.Fatalf("必须拒绝，实际通过: %q", tc.target)
			} else {
				requireCode(t, err, contracts.CodeUnauthorized)
			}
			if g.Trusted(tc.target) {
				t.Fatalf("Trusted 必须与 Check 结论一致（应拒绝）: %q", tc.target)
			}
		})
	}
}

// TestPathGuardResolvesSymlinkInsideRoot 验证：根内的链接若仍指向根内，
// 必须通过，且真实路径是链接目标——许可绑定的是目标文件，不是链接名。
func TestPathGuardResolvesSymlinkInsideRoot(t *testing.T) {
	tr := newScopeTree(t)
	g := mustGuard(t, tr.root)

	alias := filepath.Join(tr.root, "alias")
	if err := os.Symlink(filepath.Join(tr.root, "sub"), alias); err != nil {
		t.Fatalf("创建符号链接: %v", err)
	}
	viaAlias := filepath.Join(alias, "payload.txt")
	direct := filepath.Join(tr.root, "sub", "payload.txt")

	decAlias, err := g.Check(viaAlias)
	if err != nil {
		t.Fatalf("根内链接应通过: %v", err)
	}
	decDirect, err := g.Check(direct)
	if err != nil {
		t.Fatalf("直连路径应通过: %v", err)
	}
	if decAlias.Real != decDirect.Real {
		t.Fatalf("同一真实文件的不同拼写应解析到同一路径: %q vs %q", decAlias.Real, decDirect.Real)
	}
}

// TestPathGuardRejectsDanglingSymlinkOutside 覆盖"链接目标不存在"的越界：
// resolveReal 在目标不存在时会回退到最近存在的祖先，这个回退不能把根外路径洗白。
func TestPathGuardRejectsDanglingSymlinkOutside(t *testing.T) {
	tr := newScopeTree(t)
	g := mustGuard(t, tr.root)

	dangling := filepath.Join(tr.root, "dangling")
	if err := os.Symlink(filepath.Join(tr.outside, "never-exists"), dangling); err != nil {
		t.Fatalf("创建悬空链接: %v", err)
	}
	if _, err := g.Check(dangling); err == nil {
		t.Fatal("指向根外（目标不存在）的链接必须被拒")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}
}

// TestPathGuardRejectsJunctionOutside 覆盖 Windows 目录联接（junction）。
//
// 联接与符号链接在文件系统里是两种 reparse point，解析实现可能只处理其中一种；
// 因此必须单独测，而不是"测过 symlink 就算覆盖"。
func TestPathGuardRejectsJunctionOutside(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("目录联接是 Windows 特有机制")
	}
	tr := newScopeTree(t)
	g := mustGuard(t, tr.root)

	junc := filepath.Join(tr.root, "junc")
	if err := makeJunction(junc, tr.outside); err != nil {
		t.Fatalf("创建目录联接失败（环境不支持则该用例不能算通过）: %v", err)
	}
	for _, target := range []string{
		junc,
		filepath.Join(junc, "secret.txt"),
		filepath.Join(junc, "created.txt"), // 目标不存在，仍必须拒
	} {
		if _, err := g.Check(target); err == nil {
			t.Fatalf("经目录联接越界必须被拒: %q", target)
		} else {
			requireCode(t, err, contracts.CodeUnauthorized)
		}
	}
}

// TestPathGuardCaseFoldingMatchesFilesystem Windows/macOS 默认大小写不敏感：
// 大小写不同的同一路径必须被视为同一资源，否则可以用大小写变体绕过"已批准"结论。
func TestPathGuardCaseFoldingMatchesFilesystem(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("仅大小写不敏感文件系统适用")
	}
	tr := newScopeTree(t)
	g := mustGuard(t, tr.root)

	loud := filepath.Join(strings.ToUpper(tr.root), "INSIDE.TXT")
	dec, err := g.Check(loud)
	if err != nil {
		t.Fatalf("大小写变体应命中同一根: %v", err)
	}
	got, err1 := os.Stat(dec.Real)
	want, err2 := os.Stat(filepath.Join(tr.root, "inside.txt"))
	if err1 != nil || err2 != nil {
		t.Fatalf("stat: %v / %v", err1, err2)
	}
	if !os.SameFile(got, want) {
		t.Fatalf("大小写变体解析到了不同文件: %q", dec.Real)
	}
}

// TestNewPathGuardRejectsBadRoots 防止"守卫构造失败但被当成没限制"。
func TestNewPathGuardRejectsBadRoots(t *testing.T) {
	if _, err := NewPathGuard(); err == nil {
		t.Fatal("无根时必须报错")
	}
	if _, err := NewPathGuard("", "  "); err == nil {
		t.Fatal("全空根时必须报错")
	}
	tr := newScopeTree(t)
	if _, err := NewPathGuard(filepath.Join(tr.base, "nope")); err == nil {
		t.Fatal("不存在的根必须报错")
	}
	if _, err := NewPathGuard(filepath.Join(tr.root, "inside.txt")); err == nil {
		t.Fatal("根是文件而非目录时必须报错")
	}
}

// TestAuthorizeRejectsFilesystemToolWithoutScope 覆盖"许可只针对实际资源范围"的前提：
// 给不出范围就不该签发许可。
func TestAuthorizeRejectsFilesystemToolWithoutScope(t *testing.T) {
	tr := newScopeTree(t)
	clock := newClock()
	svc := New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: true},
		PathGuard:   mustGuard(t, tr.root),
		Now:         clock.Now,
		ApprovalTTL: 60 * time.Second,
	})
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		call ToolCall
	}{
		{"write 无范围", ToolCall{ID: "c1", Name: "write", Source: SourceBuiltin, Arguments: jsonArgs(`{"path":"x","content":"y"}`)}},
		{"write 越界范围", ToolCall{ID: "c1", Name: "write", Source: SourceBuiltin, Arguments: jsonArgs(`{"path":"x","content":"y"}`), ResourceScope: filepath.Join(tr.outside, "secret.txt")}},
		{"read 经链接越界", ToolCall{ID: "c1", Name: "read", Source: SourceBuiltin, Arguments: jsonArgs(`{"path":"x"}`), ResourceScope: filepath.Join(tr.root, "..", "outside", "secret.txt")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := svc.Authorize(ctx, testExec("s1"), tc.call)
			if err != nil {
				t.Fatalf("越界/缺范围应以 deny 返回结论，而不是错误: %v", err)
			}
			if res.Decision != DecisionDeny {
				t.Fatalf("必须 deny，实际 %s", res.Decision)
			}
			if res.ApprovalID != "" {
				t.Fatalf("被拒的调用不得签发审批: %s", res.ApprovalID)
			}
		})
	}
}

// TestApprovalBindsRealPathNotSpelling 是 SEC05 的核心断言：
// 审批绑定的是"实际文件"，不是调用方写的字符串。
//
// 同一真实文件的两种拼写（链接名 vs 真实路径）必须共用同一条审批；
// 不同文件必须不能复用。
func TestApprovalBindsRealPathNotSpelling(t *testing.T) {
	tr := newScopeTree(t)
	clock := newClock()
	svc := New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: true},
		PathGuard:   mustGuard(t, tr.root),
		Now:         clock.Now,
		ApprovalTTL: 60 * time.Second,
	})
	ctx := context.Background()
	exec := testExec("s1")

	alias := filepath.Join(tr.root, "alias")
	if err := os.Symlink(filepath.Join(tr.root, "sub"), alias); err != nil {
		t.Fatalf("创建符号链接: %v", err)
	}

	call := ToolCall{
		ID: "c1", Name: "write", Source: SourceBuiltin,
		Arguments:     jsonArgs(`{"content":"hi"}`),
		ResourceScope: filepath.Join(alias, "f.txt"),
	}
	res, err := svc.Authorize(ctx, exec, call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("根内写应进入审批，实际 %s（原因: %s）", res.Decision, res.Reason)
	}

	sameReal := call
	sameReal.ResourceScope = filepath.Join(tr.root, "sub", "f.txt")
	if err := svc.ValidateBinding(ctx, exec, res.ApprovalID, sameReal); err != nil {
		t.Fatalf("同一真实文件的不同拼写必须视为同一资源: %v", err)
	}

	other := call
	other.ResourceScope = filepath.Join(tr.root, "sub", "other.txt")
	if err := svc.ValidateBinding(ctx, exec, res.ApprovalID, other); err == nil {
		t.Fatal("不同文件不得复用同一审批")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// 批准后：执行前复查只放行一次，第二次必须失败（否则等于可重放）。
	if err := svc.ResolveApproval(ctx, res.ApprovalID, ApprovalOnce, call); err != nil {
		t.Fatalf("批准一次: %v", err)
	}
	if err := svc.ValidateBinding(ctx, exec, res.ApprovalID, call); err != nil {
		t.Fatalf("批准后的第一次执行前复查应通过: %v", err)
	}
	if err := svc.ValidateBinding(ctx, exec, res.ApprovalID, call); err == nil {
		t.Fatal("同一条一次性许可不得被领取两次")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}
}

// TestTrustOriginResolvesSymlink 验证信任登记按真实路径生效，
// 但同时不能因为"根内新建了同名链接"就把根外目录当成受信任项目。
func TestTrustOriginResolvesSymlink(t *testing.T) {
	tr := newScopeTree(t)
	clock := newClock()
	svc := New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: true},
		PathGuard:   mustGuard(t, tr.root),
		Now:         clock.Now,
	})
	ctx := context.Background()

	if err := svc.TrustOrigin(tr.root); err != nil {
		t.Fatalf("TrustOrigin: %v", err)
	}
	call := ToolCall{ID: "c1", Name: "mcp_x", Source: SourceExtension, Arguments: jsonArgs(`{}`), Origin: "project:" + tr.root}
	res, err := svc.Authorize(ctx, testExec("s1"), call)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision == DecisionDeny {
		t.Fatalf("已信任项目不应被拒: %s", res.Reason)
	}

	// 根外目录冒充项目来源 → 必须拒。
	outsider := call
	outsider.Origin = "project:" + tr.outside
	res, err = svc.Authorize(ctx, testExec("s1"), outsider)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("未信任项目必须拒，实际 %s", res.Decision)
	}

	// 用链接伪装成已信任项目路径 → 解析后是根外目录，仍必须拒。
	spoof := filepath.Join(tr.base, "spoof")
	if err := os.Symlink(tr.outside, spoof); err != nil {
		t.Fatalf("创建链接: %v", err)
	}
	spoofCall := call
	spoofCall.Origin = "project:" + spoof
	res, err = svc.Authorize(ctx, testExec("s1"), spoofCall)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("链接伪装的未信任项目必须拒，实际 %s", res.Decision)
	}
}
