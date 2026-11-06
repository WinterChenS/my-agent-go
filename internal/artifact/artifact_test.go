package artifact

// C07 产物域测试。
//
// 这里守的是三条产品承诺（ART01/ART02）：
//  1. 导入只接受"工作区内、逐段都不是链接、确实是普通文件"的路径：`..`、
//     符号链接/目录联接、越界绝对路径、消失的文件、目录一律结构化拒绝，
//     而且拒绝发生在读字节之前——不产生索引行。
//  2. 读产物时每次重新核对磁盘：文件被改/被删要如实标成 drifted/missing，
//     并把索引里的 hash 与实际读到的 hash 同时给出来；绝不拿旧 hash 冒充现状。
//  3. 预览只服务白名单内的媒体类型；可执行文件与未知类型结构化拒绝。
//
// 全部在 t.TempDir() 里跑：临时工作区、临时导出根、临时库，不碰真实 appData。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// testWorkspaceID 是测试里唯一的工作区 id。
const testWorkspaceID = contracts.WorkspaceID("ws-c07")

type testEnv struct {
	svc    *Service
	db     *storage.DB
	root   string
	wsDir  string
	export string
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	root := t.TempDir()
	wsDir := filepath.Join(root, "workspace")
	exportRoot := filepath.Join(root, "exports")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatalf("建工作区目录: %v", err)
	}

	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移临时库: %v", err)
	}

	svc, err := New(db, Options{
		ExportRoot: exportRoot,
		WorkspaceRoot: func(id contracts.WorkspaceID) (string, error) {
			if id != testWorkspaceID {
				return "", contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
			}
			return wsDir, nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("构造产物服务: %v", err)
	}
	return testEnv{svc: svc, db: db, root: root, wsDir: wsDir, export: exportRoot}
}

// writeFile 在 dir 下写一个文件并返回绝对路径。body 为 nil 时写空文件。
func writeFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建目录 %s: %v", filepath.Dir(path), err)
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

// requireReason 断言错误文案里点名了真实原因：只对错误码做断言时，
// "被拒绝"和"因为别的原因被拒绝"看起来一模一样。
func requireReason(t *testing.T, err error, want string) {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError，实际 %T: %v", err, err)
	}
	if !strings.Contains(appErr.Message, want) {
		t.Fatalf("拒绝原因应提到 %q，实际：%s", want, appErr.Message)
	}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际没有错误", code)
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError(%s)，实际 %T: %v", code, err, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestArtifactOutsideScopeDenied 覆盖 ART01：越界/链接/消失/目录一律拒绝。
func TestArtifactOutsideScopeDenied(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	sep := string(os.PathSeparator)

	// 工作区外放两个真实文件：一个用词法越界访问，一个用绝对路径访问。
	outsideDir := filepath.Join(env.root, "outside")
	writeFile(t, outsideDir, "secret.txt", []byte("外部文件不应被登记"))
	outsideFile := filepath.Join(outsideDir, "secret.txt")

	t.Run("dotdot", func(t *testing.T) {
		// 刻意不 filepath.Join（它会 Clean 掉 `..`）：这里要的就是用户能写出的原始串。
		raw := env.wsDir + sep + ".." + sep + "outside" + sep + "secret.txt"
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: raw, WorkspaceID: testWorkspaceID})
		requireCode(t, err, contracts.CodeUnauthorized)
		requireReason(t, err, "..")
	})

	t.Run("absolute-escape", func(t *testing.T) {
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: outsideFile, WorkspaceID: testWorkspaceID})
		requireCode(t, err, contracts.CodeUnauthorized)
	})

	t.Run("relative-path", func(t *testing.T) {
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: "notes.txt", WorkspaceID: testWorkspaceID})
		requireCode(t, err, contracts.CodeUnauthorized)
	})

	t.Run("link-out", func(t *testing.T) {
		// 工作区内一条链接指向工作区外：解析后越界，必须在读取之前拒绝。
		link := filepath.Join(env.wsDir, "link-out")
		makeDirLink(t, link, outsideDir)
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{
			Path:        filepath.Join(link, "secret.txt"),
			WorkspaceID: testWorkspaceID,
		})
		requireCode(t, err, contracts.CodeUnauthorized)
		// 逐段链接判定在根内判定之后：文案必须点名"链接"，否则"被拒绝"看起来
		// 像越界，用户会去改路径而不是去掉链接。
		requireReason(t, err, "链接")
	})

	t.Run("link-inside", func(t *testing.T) {
		// 链接目标仍在工作区内也必须拒绝：产物是"用户看见的那个文件"的快照，
		// 经过链接就与索引里的路径脱钩（与 C06c 附件的口径一致）。
		inner := filepath.Join(env.wsDir, "inner")
		writeFile(t, inner, "real.txt", []byte("工作区内真实文件"))
		link := filepath.Join(env.wsDir, "link-in")
		makeDirLink(t, link, inner)
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{
			Path:        filepath.Join(link, "real.txt"),
			WorkspaceID: testWorkspaceID,
		})
		requireCode(t, err, contracts.CodeUnauthorized)
		requireReason(t, err, "链接")
	})

	t.Run("vanished", func(t *testing.T) {
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{
			Path:        filepath.Join(env.wsDir, "gone.txt"),
			WorkspaceID: testWorkspaceID,
		})
		requireCode(t, err, contracts.CodeNotFound)
	})

	t.Run("directory", func(t *testing.T) {
		dir := filepath.Join(env.wsDir, "adir")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("建目录: %v", err)
		}
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: dir, WorkspaceID: testWorkspaceID})
		requireCode(t, err, contracts.CodeUnsupported)
	})

	t.Run("unknown-workspace", func(t *testing.T) {
		p := writeFile(t, env.wsDir, "ok.txt", []byte("正常文件"))
		_, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: p, WorkspaceID: "ws-unknown"})
		requireCode(t, err, contracts.CodeNotFound)
	})

	// 被拒的导入不能留下索引行：拒绝发生在读字节与写库之前。
	rows, err := env.svc.ListArtifacts(ctx, Query{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("被拒绝的导入不应留下索引行，实际 %d 条", len(rows))
	}
}

// TestGetArtifactReportsDrift 覆盖 ART02：每次读都重新核对磁盘。
func TestGetArtifactReportsDrift(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	body := []byte("第一版产物内容\n")
	path := writeFile(t, env.wsDir, "report.txt", body)
	imported, err := env.svc.ImportArtifact(ctx, ImportRequest{
		Path:        path,
		WorkspaceID: testWorkspaceID,
		RunID:       "run-1",
		SessionID:   "sess-1",
	})
	if err != nil {
		t.Fatalf("ImportArtifact: %v", err)
	}
	if imported.SHA256 != sha256Hex(body) {
		t.Fatalf("导入 hash 不符：期望 %s，实际 %s", sha256Hex(body), imported.SHA256)
	}
	if imported.Size != int64(len(body)) {
		t.Fatalf("导入 size 不符：期望 %d，实际 %d", len(body), imported.Size)
	}
	if imported.RunID != "run-1" {
		t.Fatalf("run 归属丢失：%q", imported.RunID)
	}

	// 刚导入：verified，且 observed 与索引一致。
	fresh, err := env.svc.GetArtifact(ctx, imported.ID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if fresh.Verification == nil || fresh.Verification.Status != StatusVerified {
		t.Fatalf("刚导入应为 verified，实际 %+v", fresh.Verification)
	}
	if fresh.Verification.ObservedSHA256 != imported.SHA256 {
		t.Fatalf("verified 时 observed 应等于索引 hash")
	}

	// 改动文件：必须报 drifted，并同时给出索引 hash 与实际 hash。
	changed := []byte("第二版：内容被外部改写了\n")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatalf("改写产物: %v", err)
	}
	drifted, err := env.svc.GetArtifact(ctx, imported.ID)
	if err != nil {
		t.Fatalf("GetArtifact(drift): %v", err)
	}
	if drifted.Verification == nil || drifted.Verification.Status != StatusDrifted {
		t.Fatalf("文件被改写应报 drifted，实际 %+v", drifted.Verification)
	}
	if drifted.SHA256 != imported.SHA256 {
		t.Fatalf("索引 hash 不应被改写：%s != %s", drifted.SHA256, imported.SHA256)
	}
	if drifted.Verification.ObservedSHA256 != sha256Hex(changed) {
		t.Fatalf("drifted 必须给出实际 hash：期望 %s，实际 %s",
			sha256Hex(changed), drifted.Verification.ObservedSHA256)
	}
	if drifted.Verification.ObservedSHA256 == drifted.SHA256 {
		t.Fatalf("drifted 时实际 hash 不应等于索引 hash")
	}
	if drifted.Verification.ObservedSize == nil || *drifted.Verification.ObservedSize != int64(len(changed)) {
		t.Fatalf("drifted 必须给出实际大小：%+v", drifted.Verification.ObservedSize)
	}
	if drifted.Verification.Reason == "" {
		t.Fatalf("drifted 必须给出可读原因")
	}

	// 删除文件：报 missing，仍然带着索引 hash（"曾经登记的是哪一份"）。
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除产物: %v", err)
	}
	missing, err := env.svc.GetArtifact(ctx, imported.ID)
	if err != nil {
		t.Fatalf("GetArtifact(missing): %v", err)
	}
	if missing.Verification == nil || missing.Verification.Status != StatusMissing {
		t.Fatalf("文件被删除应报 missing，实际 %+v", missing.Verification)
	}
	if missing.SHA256 != imported.SHA256 {
		t.Fatalf("missing 时索引 hash 仍应保留")
	}
	if missing.Verification.ObservedSHA256 != "" {
		t.Fatalf("missing 时不应给出实际 hash（没有读到任何字节）")
	}

	// 列表不做磁盘核对：Verification 必须为空（不核对就不能声称 verified）。
	list, err := env.svc.ListArtifacts(ctx, Query{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应只有一条产物，实际 %d", len(list))
	}
	if list[0].Verification != nil {
		t.Fatalf("列表不应自称已核对：%+v", list[0].Verification)
	}
	if list[0].SHA256 != imported.SHA256 {
		t.Fatalf("列表应返回索引 hash")
	}

	// 重新导入同一路径：复用同一行（同一 id），hash 更新为当前内容。
	reimported, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: path, WorkspaceID: testWorkspaceID})
	if err != nil {
		// 文件刚被删除：先写回来再导入，这是"同一路径重新登记"的常规路径。
		writeFile(t, env.wsDir, "report.txt", changed)
		reimported, err = env.svc.ImportArtifact(ctx, ImportRequest{Path: path, WorkspaceID: testWorkspaceID})
	}
	if err != nil {
		t.Fatalf("重新导入: %v", err)
	}
	if reimported.ID != imported.ID {
		t.Fatalf("同一路径应复用同一产物 id：%s != %s", reimported.ID, imported.ID)
	}
	if reimported.SHA256 != sha256Hex(changed) {
		t.Fatalf("重新导入应更新 hash：期望 %s，实际 %s", sha256Hex(changed), reimported.SHA256)
	}
}

// TestPreviewRejectsUnknownMIME 覆盖 PreviewArtifact 的白名单。
func TestPreviewRejectsUnknownMIME(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	textPath := writeFile(t, env.wsDir, "notes.txt", []byte("普通文本预览内容\n"))
	textArtifact, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: textPath, WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("导入文本: %v", err)
	}
	preview, err := env.svc.PreviewArtifact(ctx, textArtifact.ID)
	if err != nil {
		t.Fatalf("文本预览应通过: %v", err)
	}
	if preview.Kind != PreviewKindText {
		t.Fatalf("文本预览 kind 应为 %s，实际 %s", PreviewKindText, preview.Kind)
	}
	if !strings.Contains(preview.Text, "普通文本预览内容") {
		t.Fatalf("文本预览内容缺失：%q", preview.Text)
	}

	// 索引里记的是可执行文件类型：预览必须拒绝（不能用"扩展名看起来是文本"绕过）。
	exePath := writeFile(t, env.wsDir, "dropper.bin", []byte("MZ\x90\x00payload"))
	exeArtifact, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: exePath, WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("导入二进制: %v", err)
	}
	if _, err := env.db.SQL().ExecContext(ctx,
		`UPDATE artifacts SET media_type = ? WHERE id = ?`,
		"application/x-msdownload", exeArtifact.ID); err != nil {
		t.Fatalf("改写索引媒体类型: %v", err)
	}
	if _, err := env.svc.PreviewArtifact(ctx, exeArtifact.ID); err == nil {
		t.Fatalf("application/x-msdownload 必须被预览拒绝")
	} else {
		requireCode(t, err, contracts.CodeUnsupported)
	}

	// 未知媒体类型同样拒绝，并且错误里点名类型。
	if _, err := env.db.SQL().ExecContext(ctx,
		`UPDATE artifacts SET media_type = ? WHERE id = ?`,
		"application/x-anvil-unknown", exeArtifact.ID); err != nil {
		t.Fatalf("改写索引媒体类型: %v", err)
	}
	_, err = env.svc.PreviewArtifact(ctx, exeArtifact.ID)
	requireCode(t, err, contracts.CodeUnsupported)
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && !strings.Contains(appErr.Message, "application/x-anvil-unknown") {
		t.Fatalf("拒绝原因应点名媒体类型：%s", appErr.Message)
	}

	// 预览也要核对磁盘：文件被改后不能拿旧内容冒充。
	if err := os.WriteFile(textPath, []byte("被改写的预览内容\n"), 0o600); err != nil {
		t.Fatalf("改写文本产物: %v", err)
	}
	if _, err := env.svc.PreviewArtifact(ctx, textArtifact.ID); err == nil {
		t.Fatalf("文件被改写后预览必须失败")
	} else {
		requireCode(t, err, contracts.CodeVerificationFailed)
	}
}

// TestExportArtifactVerifiesSource 覆盖 ExportArtifact：只导出已核对过的字节。
func TestExportArtifactVerifiesSource(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	body := []byte("导出用的产物内容\n")
	path := writeFile(t, env.wsDir, "out.md", body)
	item, err := env.svc.ImportArtifact(ctx, ImportRequest{Path: path, WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatalf("导入: %v", err)
	}

	res, err := env.svc.ExportArtifact(ctx, item.ID, "out.md")
	if err != nil {
		t.Fatalf("导出: %v", err)
	}
	if res.SHA256 != sha256Hex(body) {
		t.Fatalf("导出 hash 应为源内容 hash：%s != %s", res.SHA256, sha256Hex(body))
	}
	if res.Path != filepath.Join(env.export, "out.md") {
		t.Fatalf("导出目标应在导出根内：%s", res.Path)
	}
	written, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("读导出文件: %v", err)
	}
	if string(written) != string(body) {
		t.Fatalf("导出内容与源内容不一致")
	}

	// 越界目标（含 `..`）拒绝。
	if _, err := env.svc.ExportArtifact(ctx, item.ID, ".."+string(os.PathSeparator)+"escape.md"); err == nil {
		t.Fatalf("导出目标不允许越界")
	} else {
		requireCode(t, err, contracts.CodeUnauthorized)
	}

	// 源文件被改写：导出必须失败，而不是把新内容当成登记的产物。
	if err := os.WriteFile(path, []byte("被改写的内容\n"), 0o600); err != nil {
		t.Fatalf("改写源文件: %v", err)
	}
	if _, err := env.svc.ExportArtifact(ctx, item.ID, "again.md"); err == nil {
		t.Fatalf("源文件被改写后导出必须失败")
	} else {
		requireCode(t, err, contracts.CodeVerificationFailed)
	}
	if _, statErr := os.Stat(filepath.Join(env.export, "again.md")); statErr == nil {
		t.Fatalf("导出失败不应留下目标文件")
	}
}

// TestListRunArtifactsScopesByRun 守"按 run 列产物"只返回该 run 的行。
func TestListRunArtifactsScopesByRun(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	for _, tc := range []struct{ name, run string }{
		{"a.txt", "run-1"},
		{"b.txt", "run-1"},
		{"c.txt", "run-2"},
	} {
		p := writeFile(t, env.wsDir, tc.name, []byte("内容 "+tc.name))
		if _, err := env.svc.ImportArtifact(ctx, ImportRequest{
			Path: p, WorkspaceID: testWorkspaceID, RunID: contracts.RunID(tc.run),
		}); err != nil {
			t.Fatalf("导入 %s: %v", tc.name, err)
		}
	}

	got, err := env.svc.ListRunArtifacts(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListRunArtifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("run-1 应有 2 条产物，实际 %d", len(got))
	}
	for _, item := range got {
		if item.RunID != "run-1" {
			t.Fatalf("串到了别的 run：%+v", item)
		}
	}
	none, err := env.svc.ListRunArtifacts(ctx, "run-none")
	if err != nil {
		t.Fatalf("ListRunArtifacts(空): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("没有产物的 run 应返回空切片，实际 %d", len(none))
	}
}
