package session

// C06c 附件测试：普通会话的附件接收（app-owned content-addressed blob store）
// 与只读 broker。
//
// 这里守的是 5 条不变量：
//   - 路径必须重新解析：`..`、符号链接、Windows 目录联接、越界绝对路径、目录
//     逐项结构化拒绝，且拒绝不产生 blob、不影响同一批里的其它文件；
//   - 内容寻址：同内容只落一个 blob，且复用前必须先校验既有字节；
//   - broker 只读且先验后发：size/sha256 不符、blob 被截断/替换时，
//     一个字节都不交出去；
//   - 能力（capability）会过期，过期即 fail closed；
//   - 源文件只读：接收前后 hash 与 mtime 完全一致。
//
// 全部在 t.TempDir() + 临时库里跑：不碰真实 appData、~/.pi 或用户的文件。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// newAttachmentFixture 构造一个隔离的目录 + 临时库 + 一个普通会话。
func newAttachmentFixture(t *testing.T) (*AttachmentStore, string, string) {
	t.Helper()
	cat, _ := openTestCatalog(t)
	sess, err := cat.CreateSession(context.Background(), contracts.WorkspaceID("ws-attachment-test"))
	if err != nil {
		t.Fatalf("创建普通会话: %v", err)
	}
	blobRoot := t.TempDir()
	store, err := NewAttachmentStore(cat, blobRoot)
	if err != nil {
		t.Fatalf("构造附件存储: %v", err)
	}
	return store, blobRoot, string(sess.ID)
}

func writeAttachmentFile(t *testing.T, path string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("写文件 %s: %v", path, err)
	}
	return path
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// countBlobs 数出 blob store 里的实际内容文件（不含 tmp 目录）。
func countBlobs(t *testing.T, blobRoot string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(blobRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.Base(path) == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 blob store: %v", err)
	}
	return n
}

// TestAttachmentEscapesRejected 覆盖所有"路径不是它看起来那样"的形态。
//
// 一个坏文件不能静默丢掉整批：同一批里放一个合法文件，合法项必须落地，
// 非法项逐条给结构化原因，且不产生 blob。
func TestAttachmentEscapesRejected(t *testing.T) {
	store, blobRoot, sessionID := newAttachmentFixture(t)

	allowed := t.TempDir()
	outside := t.TempDir()
	if err := store.SetSourceRoots([]string{allowed}); err != nil {
		t.Fatalf("设置附件来源根: %v", err)
	}

	secret := writeAttachmentFile(t, filepath.Join(outside, "secret.txt"), []byte("top secret"))
	good := writeAttachmentFile(t, filepath.Join(allowed, "note.txt"), []byte("hello"))
	folder := filepath.Join(allowed, "folder")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}

	// 符号链接指向允许根之外的文件。
	fileLink := filepath.Join(allowed, "link-outside.txt")
	if err := os.Symlink(secret, fileLink); err != nil {
		t.Fatalf("创建文件符号链接: %v", err)
	}
	// 目录链接/联接指向允许根之外的目录。Windows 上单独造目录联接
	// （junction 与 symlink 是两种 reparse tag，只测一种不算覆盖）。
	dirEscape := filepath.Join(allowed, "escape-dir")
	makeAttachmentDirLink(t, dirEscape, outside)

	// 允许根的父目录里放一个文件，用来构造 `..` 回溯目标。
	writeAttachmentFile(t, filepath.Join(filepath.Dir(allowed), "parent-secret.txt"), []byte("parent"))

	cases := []struct {
		name string
		path string
	}{
		{"父目录回溯", filepath.Join(allowed, "..", filepath.Base(filepath.Dir(allowed)), "parent-secret.txt")},
		{"符号链接越界", fileLink},
		{"目录链接越界", filepath.Join(dirEscape, "secret.txt")},
		{"绝对路径越界", secret},
		{"目录而非文件", folder},
	}
	reqs := []AttachmentRequest{{Path: good, DisplayName: "合法附件"}}
	for _, c := range cases {
		reqs = append(reqs, AttachmentRequest{Path: c.path, DisplayName: c.name})
	}

	res, err := store.ResolveAttachments(sessionID, reqs)
	if err != nil {
		t.Fatalf("逐个文件的拒绝不能让整批失败，实际: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("合法附件应恰好 1 个，实际 %d（拒绝项 %+v）", len(res.Items), res.Rejected)
	}
	if res.Items[0].DisplayName != "合法附件" || res.Items[0].SHA256 != sha256Hex([]byte("hello")) {
		t.Fatalf("合法附件字段不对: %+v", res.Items[0])
	}
	if len(res.Rejected) != len(cases) {
		t.Fatalf("每个非法文件都要有自己的结构化原因：期望 %d 条，实际 %+v", len(cases), res.Rejected)
	}
	for i, c := range cases {
		got := res.Rejected[i]
		if got.Path != c.path {
			t.Fatalf("拒绝项顺序/路径错位：#%d 期望 %q，实际 %q", i, c.path, got.Path)
		}
		if got.DisplayName != c.name {
			t.Fatalf("拒绝项要带回展示名：#%d 期望 %q，实际 %q", i, c.name, got.DisplayName)
		}
		if got.Code == "" || got.Message == "" {
			t.Fatalf("拒绝项必须带 Code 与原因：%+v", got)
		}
	}
	// 非法项不能留下 blob：只有合法附件那一份内容。
	if n := countBlobs(t, blobRoot); n != 1 {
		t.Fatalf("被拒文件不能写 blob：期望 1 个 blob，实际 %d", n)
	}

	// 明确逐条断言错误码：越界一律 UNAUTHORIZED，目录是 UNSUPPORTED。
	wantCode := []string{
		contracts.CodeUnauthorized, // ..
		contracts.CodeUnauthorized, // symlink
		contracts.CodeUnauthorized, // junction / dir link
		contracts.CodeUnauthorized, // absolute outside
		contracts.CodeUnsupported,  // directory
	}
	for i, want := range wantCode {
		if res.Rejected[i].Code != want {
			t.Fatalf("%s 的拒绝码期望 %s，实际 %s（%s）",
				cases[i].name, want, res.Rejected[i].Code, res.Rejected[i].Message)
		}
	}
}

// TestAttachmentBlobIsContentAddressedAndVerified 覆盖内容寻址与复用前校验。
func TestAttachmentBlobIsContentAddressedAndVerified(t *testing.T) {
	store, blobRoot, sessionID := newAttachmentFixture(t)
	body := []byte("same bytes, same blob\n")
	a := writeAttachmentFile(t, filepath.Join(t.TempDir(), "a.txt"), body)
	b := writeAttachmentFile(t, filepath.Join(t.TempDir(), "b.txt"), body)

	res, err := store.ResolveAttachments(sessionID, []AttachmentRequest{
		{Path: a, DisplayName: "a.txt"},
		{Path: b, DisplayName: "b.txt"},
	})
	if err != nil {
		t.Fatalf("ResolveAttachments: %v", err)
	}
	if len(res.Items) != 2 || len(res.Rejected) != 0 {
		t.Fatalf("同内容两个文件都应落地：%+v", res)
	}
	if res.Items[0].SHA256 != res.Items[1].SHA256 || res.Items[0].ID == res.Items[1].ID {
		t.Fatalf("同内容应共享 sha256、各自持有独立能力：%+v", res.Items)
	}
	if n := countBlobs(t, blobRoot); n != 1 {
		t.Fatalf("同内容只允许一个 blob，实际 %d", n)
	}
	// blob 布局：<root>/<sha[:2]>/<sha>。
	sha := res.Items[0].SHA256
	blobPath := filepath.Join(blobRoot, sha[:2], sha)
	if fi, err := os.Stat(blobPath); err != nil || fi.Size() != int64(len(body)) {
		t.Fatalf("blob 未按内容寻址布局落地: %s (%v)", blobPath, err)
	}

	// 先证明正常路径可读、字节一致。
	rc, meta, err := store.OpenAttachment(res.Items[0].ID)
	if err != nil {
		t.Fatalf("OpenAttachment: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(got) != string(body) {
		t.Fatalf("broker 字节不一致: err=%v got=%q", err, got)
	}
	if meta.SHA256 != sha || meta.Size != int64(len(body)) || meta.Status != AttachmentReady {
		t.Fatalf("broker 元数据不对: %+v", meta)
	}

	// 源文件删掉不影响已落地快照：附件是内容快照，不是指向用户文件的引用。
	if err := os.Remove(a); err != nil {
		t.Fatalf("删除源文件: %v", err)
	}
	rc, _, err = store.OpenAttachment(res.Items[1].ID)
	if err != nil {
		t.Fatalf("源文件消失不应影响已落地快照: %v", err)
	}
	if got, _ := io.ReadAll(rc); string(got) != string(body) {
		t.Fatalf("快照字节不一致: %q", got)
	}
	_ = rc.Close()

	// 篡改 blob（等长不同内容）：必须在返回任何字节之前失败。
	if err := os.WriteFile(blobPath, []byte("SAME BYTES, SAME BLOB\n"), 0o600); err != nil {
		t.Fatalf("篡改 blob: %v", err)
	}
	rc, _, err = store.OpenAttachment(res.Items[0].ID)
	requireAppCode(t, err, contracts.CodeVerificationFailed)
	if rc != nil {
		t.Fatalf("校验失败时不能返回可读句柄")
	}
}

// TestAttachmentBrokerRefusesExpiredCapability 覆盖能力过期。
func TestAttachmentBrokerRefusesExpiredCapability(t *testing.T) {
	store, _, sessionID := newAttachmentFixture(t)
	base := time.Now()
	store.SetClock(func() time.Time { return base })
	store.SetCapabilityTTL(2 * time.Minute)

	src := writeAttachmentFile(t, filepath.Join(t.TempDir(), "expiring.txt"), []byte("expires"))
	res, err := store.ResolveAttachments(sessionID, []AttachmentRequest{{Path: src}})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ResolveAttachments: err=%v res=%+v", err, res)
	}
	id := res.Items[0].ID

	// 过期前可读。
	store.SetClock(func() time.Time { return base.Add(time.Minute) })
	rc, _, err := store.OpenAttachment(id)
	if err != nil {
		t.Fatalf("过期前应可读: %v", err)
	}
	_ = rc.Close()

	// 过期后 fail closed：连句柄都不给。
	store.SetClock(func() time.Time { return base.Add(3 * time.Minute) })
	rc, _, err = store.OpenAttachment(id)
	requireAppCode(t, err, contracts.CodeUnauthorized)
	if rc != nil {
		t.Fatalf("能力过期后不能返回可读句柄")
	}
}

// TestAttachmentSourceIsNotWrittenBack 覆盖"源文件只读"。
func TestAttachmentSourceIsNotWrittenBack(t *testing.T) {
	store, _, sessionID := newAttachmentFixture(t)
	body := []byte("do not touch the source\n")
	src := writeAttachmentFile(t, filepath.Join(t.TempDir(), "readonly.txt"), body)

	before, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat 源文件: %v", err)
	}
	beforeHash := sha256Hex(body)
	beforeMode := before.Mode()

	res, err := store.ResolveAttachments(sessionID, []AttachmentRequest{{Path: src, DisplayName: "readonly.txt"}})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ResolveAttachments: err=%v res=%+v", err, res)
	}

	after, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat 源文件（接收后）: %v", err)
	}
	afterBody, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读源文件（接收后）: %v", err)
	}
	if sha256Hex(afterBody) != beforeHash {
		t.Fatalf("源文件内容被改写了")
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() || after.Mode() != beforeMode {
		t.Fatalf("源文件元数据被改写：before=%v/%d/%v after=%v/%d/%v",
			before.ModTime(), before.Size(), beforeMode,
			after.ModTime(), after.Size(), after.Mode())
	}
}

// TestAttachmentBlobIsOutsideTheWorkspace 覆盖 policy 边界：
// blob store 不在 run 被授予的 workspace 里，写它必须被策略拒绝。
func TestAttachmentBlobIsOutsideTheWorkspace(t *testing.T) {
	cat, _ := openTestCatalog(t)
	sess, err := cat.CreateSession(context.Background(), contracts.WorkspaceID("ws-policy-test"))
	if err != nil {
		t.Fatalf("创建会话: %v", err)
	}
	workspaceDir := t.TempDir()
	blobRoot := t.TempDir()
	store, err := NewAttachmentStore(cat, blobRoot)
	if err != nil {
		t.Fatalf("构造附件存储: %v", err)
	}

	src := writeAttachmentFile(t, filepath.Join(t.TempDir(), "outside.txt"), []byte("payload"))
	res, err := store.ResolveAttachments(string(sess.ID), []AttachmentRequest{{Path: src}})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ResolveAttachments: err=%v res=%+v", err, res)
	}
	sha := res.Items[0].SHA256
	blobPath := filepath.Join(blobRoot, sha[:2], sha)

	// 1. 结构上不在 workspace 目录内。
	rel, err := filepath.Rel(workspaceDir, blobPath)
	if err != nil || rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
		t.Fatalf("blob 必须落在 workspace 之外：workspace=%s blob=%s rel=%s", workspaceDir, blobPath, rel)
	}

	// 2. run 的路径守卫（以 workspace 为唯一根）读不到它。
	guard, err := policy.NewPathGuard(workspaceDir)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	if _, err := guard.Check(blobPath); err == nil {
		t.Fatalf("以 workspace 为根的守卫不能放行 blob 路径: %s", blobPath)
	} else {
		requireAppCode(t, err, contracts.CodeUnauthorized)
	}

	// 3. 策略层对 blob 的写调用是 deny，而不是 ask/allow。
	svc := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace"},
		PathGuard:   guard,
	})
	exec := contracts.ExecutionContext{
		WorkspaceID: "ws-policy-test", SessionID: sess.ID, RunID: "run-policy-test", ConfigRevision: 1,
	}
	writeCall := func(scope string) policy.ToolCall {
		return policy.ToolCall{
			ID: "c1", Name: "write", Source: policy.SourceBuiltin,
			Arguments:     json.RawMessage(`{"content":"x"}`),
			ResourceScope: scope,
		}
	}
	denied, err := svc.Authorize(context.Background(), exec, writeCall(blobPath))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if denied.Decision != policy.DecisionDeny {
		t.Fatalf("对 blob store 的写必须 deny，实际 %s（%s）", denied.Decision, denied.Reason)
	}
	// 对照组：workspace 内的写是 ask（证明确实是"边界"在拒，而不是一律拒）。
	inside := writeAttachmentFile(t, filepath.Join(workspaceDir, "note.txt"), []byte("x"))
	allowed, err := svc.Authorize(context.Background(), exec, writeCall(inside))
	if err != nil {
		t.Fatalf("Authorize(inside): %v", err)
	}
	if allowed.Decision == policy.DecisionDeny {
		t.Fatalf("workspace 内的写不应被边界拒绝：%s", allowed.Reason)
	}
}

// TestConcurrentReplacementFailsClosed 覆盖"打开前后 blob 被替换/截断"。
func TestConcurrentReplacementFailsClosed(t *testing.T) {
	store, blobRoot, sessionID := newAttachmentFixture(t)
	body := []byte("0123456789abcdef")
	src := writeAttachmentFile(t, filepath.Join(t.TempDir(), "tamper.txt"), body)
	res, err := store.ResolveAttachments(sessionID, []AttachmentRequest{{Path: src}})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ResolveAttachments: err=%v res=%+v", err, res)
	}
	sha := res.Items[0].SHA256
	blobPath := filepath.Join(blobRoot, sha[:2], sha)

	// A. 等长替换：内容不同但大小相同，只有 hash 能发现。
	replacement := []byte("FEDCBA9876543210")
	if err := os.WriteFile(blobPath, replacement, 0o600); err != nil {
		t.Fatalf("替换 blob: %v", err)
	}
	rc, _, err := store.OpenAttachment(res.Items[0].ID)
	requireAppCode(t, err, contracts.CodeVerificationFailed)
	if rc != nil {
		t.Fatalf("被替换的 blob 不能返回可读句柄")
	}

	// B. 截断：重新落地一份，再把 blob 截到一半。
	res, err = store.ResolveAttachments(sessionID, []AttachmentRequest{{Path: src}})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ResolveAttachments(第二次): err=%v res=%+v", err, res)
	}
	if err := os.Truncate(blobPath, int64(len(body)/2)); err != nil {
		t.Fatalf("截断 blob: %v", err)
	}
	rc, _, err = store.OpenAttachment(res.Items[0].ID)
	requireAppCode(t, err, contracts.CodeVerificationFailed)
	if rc != nil {
		t.Fatalf("被截断的 blob 不能返回可读句柄")
	}
	// 截断后不留下"半份内容"的可用快照。
	if fi, err := os.Stat(blobPath); err == nil && fi.Size() == int64(len(body)) {
		t.Fatalf("测试自身失效：blob 没有被截断")
	}
}
