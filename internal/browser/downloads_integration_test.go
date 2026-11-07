package browser_test

// B06：下载必须先选目标，确认后 hash 对得上，取消后不留最终文件，落盘即登记产物（BR17）。
//
// 这条用例守的是"网页不能替用户挑本机路径"这条边界，因此判据全部来自磁盘与库：
//   - 用户没选目标之前，工作区目录与（伪造的）家目录**一个文件都不能多**；
//     下载的字节只能待在受管下载根的 staging 里；
//   - 确认后目标文件的 sha256 必须等于服务端给出的那份内容（测试自己算，不问被测服务）；
//   - 取消后 staging 与最终文件都不存在；
//   - 保存成功后 artifacts 表里必须有一行指向该文件，且 sha256/size 与磁盘一致——
//     这一行由**第二个 storage.DB 句柄**读回，不是服务自报。
//
// 程序化上传本轮**没有**实现：file input 仍然只能由用户通过原生文件选择器手动喂，
// 本用例不产生任何"上传成功"的断言（假实现比缺功能更危险）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/artifact"
	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// downloadBody 是 fixture 的 /__download/report.csv 响应体。
//
// 期望的 hash 由**测试自己**对这份字节算出来，不读服务端/下载器的说法。
const downloadBody = "id,name\n1,fixture\n2,browser\n"

// artifactBridge 把 artifact.Service 适配成 browser.ArtifactRegistrar。
//
// 适配器刻意留在调用方（测试/应用装配层）而不是让 internal/browser 直接依赖
// internal/artifact：下载域只需要"把已落盘文件登记成产物"这一个动作，不需要
// 把产物域的导出/预览/媒体类型全都拖进浏览器包的依赖图里。
type artifactBridge struct{ svc *artifact.Service }

func (b artifactBridge) RegisterFile(ctx context.Context, req browser.ArtifactRequest) (browser.ArtifactRef, error) {
	item, err := b.svc.ImportArtifact(ctx, artifact.ImportRequest{
		Path: req.Path, WorkspaceID: req.WorkspaceID, RunID: req.RunID,
		SessionID: req.SessionID, DisplayName: req.DisplayName,
	})
	if err != nil {
		return browser.ArtifactRef{}, err
	}
	return browser.ArtifactRef{ID: item.ID, SHA256: item.SHA256, Size: item.Size}, nil
}

type downloadEnv struct {
	mgr      *browser.DownloadManager
	artifact *artifact.Service
	fixture  *browserfixture.Fixture
	dbPath   string
	root     string // 受管下载根（staging 在它下面）
	wsRoot   string
	home     string
	execA    contracts.ExecutionContext
	execB    contracts.ExecutionContext
	wsB      contracts.WorkspaceID
}

func newDownloadEnv(t *testing.T) *downloadEnv {
	t.Helper()
	fixture, err := browserfixture.New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	t.Cleanup(func() { _ = fixture.Close() })

	base := t.TempDir()
	wsRoot := filepath.Join(base, "workspace")
	if err := os.MkdirAll(filepath.Join(wsRoot, "downloads"), 0o700); err != nil {
		t.Fatalf("建工作区: %v", err)
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o700); err != nil {
		t.Fatalf("建伪造家目录: %v", err)
	}
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	ctx := context.Background()
	dbPath := filepath.Join(base, "app.db")
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移临时库: %v", err)
	}

	profiles, err := browser.NewProfileStore(browser.ProfileOptions{
		Root: filepath.Join(base, "appdata", "browser", "profiles"), DB: db,
	})
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	artifactSvc, err := artifact.New(db, artifact.Options{
		ExportRoot: filepath.Join(base, "exports"),
		WorkspaceRoot: func(id contracts.WorkspaceID) (string, error) {
			if id != "ws-alpha" {
				return "", contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
			}
			return wsRoot, nil
		},
	})
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}

	mgr, err := browser.NewDownloadManager(browser.DownloadOptions{
		Root: filepath.Join(base, "appdata", "browser", "downloads"),
		DB:   db, Cookies: profiles, Artifacts: artifactBridge{svc: artifactSvc},
		WorkspaceRoot: func(id contracts.WorkspaceID) (string, error) {
			if id != "ws-alpha" {
				return "", contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
			}
			return wsRoot, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDownloadManager: %v", err)
	}
	return &downloadEnv{
		mgr: mgr, artifact: artifactSvc, fixture: fixture, dbPath: dbPath,
		root: filepath.Join(base, "appdata", "browser", "downloads"), wsRoot: wsRoot, home: home,
		execA: contracts.ExecutionContext{WorkspaceID: "ws-alpha", SessionID: "sess-a", RunID: "run-1", ConfigRevision: 1},
		execB: contracts.ExecutionContext{WorkspaceID: "ws-alpha", SessionID: "sess-b", RunID: "run-2", ConfigRevision: 1},
		wsB:   contracts.WorkspaceID("ws-beta"),
	}
}

// ---- BR17 ----

func TestDownloadRequiresDestination(t *testing.T) {
	if testing.Short() {
		t.Fatal("BR17 需要真实磁盘、真实 SQLite 与真实 HTTP：不允许用 -short 跳过（不写 t.Skip）")
	}
	env := newDownloadEnv(t)
	ctx := context.Background()

	downloadURL := env.fixture.Primary.URL + "/__download/report.csv"
	wantSHA := sha256Hex([]byte(downloadBody))

	var jobRows []map[string]any
	recordJobs := func(step string) {
		second := openSecondHandle(t, env.dbPath)
		rows, err := second.SQL().Query(
			`SELECT id, session_id, workspace_id, tab_id, url, suggested_name, state,
			        size_bytes, sha256, final_path, artifact_id, error_code
			   FROM browser_downloads ORDER BY created_at, id`)
		if err != nil {
			t.Fatalf("读下载行: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, session, ws, tab, url, name, state, sha, final, artifactID, errCode string
			var size int64
			if err := rows.Scan(&id, &session, &ws, &tab, &url, &name, &state, &size, &sha, &final, &artifactID, &errCode); err != nil {
				t.Fatalf("扫描下载行: %v", err)
			}
			jobRows = append(jobRows, map[string]any{
				"step": step, "id": id, "sessionId": session, "workspaceId": ws, "tabId": tab,
				"url": url, "suggestedName": name, "state": state, "size": size,
				"sha256": sha, "finalPath": final, "artifactId": artifactID, "errorCode": errCode,
			})
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("遍历下载行: %v", err)
		}
	}

	wsBefore := walkTree(t, env.wsRoot)
	homeBefore := walkTree(t, env.home)

	// 建议名带路径穿越：必须被清成单纯的文件名，而不是拼到磁盘上某个目录里。
	job, err := env.mgr.Start(ctx, env.execA, browser.DownloadRequest{
		TabID: "tab-a", URL: downloadURL, SuggestedName: `..\..\..\Users\public\report.csv`,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	recordJobs("started")
	if job.State != browser.DownloadAwaitingDestination {
		t.Fatalf("下载开始后的状态 %q，期望 %q", job.State, browser.DownloadAwaitingDestination)
	}
	if job.SuggestedName != "report.csv" {
		t.Fatalf("建议名没有被清理：%q", job.SuggestedName)
	}
	if job.Size != int64(len(downloadBody)) {
		t.Fatalf("下载大小 %d，期望 %d", job.Size, len(downloadBody))
	}
	if job.SHA256 != wantSHA {
		t.Fatalf("staging hash %q，期望 %q", job.SHA256, wantSHA)
	}
	if n := env.fixture.Primary.Counter(browserfixture.CounterDownload); n != 1 {
		t.Fatalf("服务端下载计数 %d，期望 1", n)
	}

	// 未选目标：工作区与家目录里一个文件都不能多。
	wsAfterStart := walkTree(t, env.wsRoot)
	homeAfterStart := walkTree(t, env.home)
	if !sameTree(wsBefore, wsAfterStart) {
		t.Fatalf("未选目标就写了工作区目录: before=%+v after=%+v", wsBefore, wsAfterStart)
	}
	if !sameTree(homeBefore, homeAfterStart) {
		t.Fatalf("未选目标就写了家目录: before=%+v after=%+v", homeBefore, homeAfterStart)
	}
	stagingTree := walkTree(t, filepath.Join(env.root, "staging"))
	if len(stagingTree) != 1 {
		t.Fatalf("受管 staging 里应恰好有一个文件，实际 %+v", stagingTree)
	}
	if !pathWithin(env.root, filepath.Join(env.root, "staging", stagingTree[0].Path)) {
		t.Fatalf("staging 文件不在受管下载根里: %+v", stagingTree)
	}

	// 目标落在家目录之外的工作区：确认后文件 hash 必须与服务端内容一致。
	dest := filepath.Join(env.wsRoot, "downloads", job.SuggestedName)
	saved, err := env.mgr.SaveDownload(ctx, env.execA, job.ID, dest)
	if err != nil {
		t.Fatalf("SaveDownload: %v", err)
	}
	recordJobs("saved")
	if saved.State != browser.DownloadSaved {
		t.Fatalf("保存后的状态 %q，期望 %q（错误: %s）", saved.State, browser.DownloadSaved, saved.ErrorCode)
	}
	if saved.ArtifactID == "" {
		t.Fatal("保存后没有产物 id：下载必须登记为 artifact")
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读保存后的文件: %v", err)
	}
	if got := sha256Hex(raw); got != wantSHA {
		t.Fatalf("保存后的文件 hash %q，期望 %q", got, wantSHA)
	}
	if saved.SHA256 != wantSHA || saved.Size != int64(len(downloadBody)) {
		t.Fatalf("作业记录的 hash/size 与磁盘不符: %+v", saved)
	}
	if tree := walkTree(t, filepath.Join(env.root, "staging")); len(tree) != 0 {
		t.Fatalf("保存后 staging 没有清理: %+v", tree)
	}

	// 产物行必须能从第二句柄读回，并且 sha256/size 与磁盘一致。
	second := openSecondHandle(t, env.dbPath)
	var artID, artPath, artSHA, artMedia string
	var artSize int64
	if err := second.SQL().QueryRow(
		`SELECT id, canonical_path, sha256, size_bytes, media_type FROM artifacts WHERE id = ?`,
		saved.ArtifactID).Scan(&artID, &artPath, &artSHA, &artSize, &artMedia); err != nil {
		t.Fatalf("第二句柄读产物行 %s: %v", saved.ArtifactID, err)
	}
	if artSHA != wantSHA || artSize != int64(len(downloadBody)) {
		t.Fatalf("产物行 hash/size 与内容不符: %s/%d", artSHA, artSize)
	}
	if !pathWithin(env.wsRoot, artPath) {
		t.Fatalf("产物路径不在工作区: %s", artPath)
	}
	listed, err := env.artifact.ListArtifacts(ctx, artifact.Query{WorkspaceID: "ws-alpha"})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != saved.ArtifactID {
		t.Fatalf("产物列举里找不到刚保存的文件: %+v", listed)
	}

	// 取消：不留最终文件、不留 staging，产物数不变。
	jobCancel, err := env.mgr.Start(ctx, env.execA, browser.DownloadRequest{
		TabID: "tab-a", URL: downloadURL, SuggestedName: "cancelled.csv",
	})
	if err != nil {
		t.Fatalf("Start(cancel): %v", err)
	}
	if _, err := env.mgr.CancelDownload(ctx, env.execA, jobCancel.ID); err != nil {
		t.Fatalf("CancelDownload: %v", err)
	}
	recordJobs("cancelled")
	if _, statErr := os.Stat(filepath.Join(env.wsRoot, "downloads", "cancelled.csv")); !os.IsNotExist(statErr) {
		t.Fatalf("取消后最终文件仍在: %v", statErr)
	}
	if tree := walkTree(t, filepath.Join(env.root, "staging")); len(tree) != 0 {
		t.Fatalf("取消后 staging 没有清理: %+v", tree)
	}
	if listed, err := env.artifact.ListArtifacts(ctx, artifact.Query{WorkspaceID: "ws-alpha"}); err != nil || len(listed) != 1 {
		t.Fatalf("取消改变了产物列举: %v (%+v)", err, listed)
	}

	// 跨 session 不能保存别人的下载；也不能把文件写到工作区之外。
	if _, err := env.mgr.SaveDownload(ctx, env.execB, jobCancel.ID, filepath.Join(env.wsRoot, "downloads", "x.csv")); err == nil {
		t.Fatal("session B 保存了 A 的下载")
	} else if code := errCode(err); code != contracts.CodeUnauthorized {
		t.Fatalf("跨 session 保存的错误码 %q，期望 UNAUTHORIZED", code)
	}
	jobOutside, err := env.mgr.Start(ctx, env.execA, browser.DownloadRequest{
		TabID: "tab-a", URL: downloadURL, SuggestedName: "outside.csv",
	})
	if err != nil {
		t.Fatalf("Start(outside): %v", err)
	}
	outside := filepath.Join(env.home, "Downloads", "outside.csv")
	if _, err := env.mgr.SaveDownload(ctx, env.execA, jobOutside.ID, outside); err == nil {
		t.Fatalf("下载被写到工作区之外: %s", outside)
	} else if code := errCode(err); code != contracts.CodeUnauthorized {
		t.Fatalf("越界目标的错误码 %q，期望 UNAUTHORIZED（消息: %s）", code, err.Error())
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("被拒的越界目标仍被写入: %v", statErr)
	}
	recordJobs("outside-rejected")

	// hash 对不上：staging 被改动之后保存必须失败，且不留最终文件、不登记产物。
	jobTamper, err := env.mgr.Start(ctx, env.execA, browser.DownloadRequest{
		TabID: "tab-a", URL: downloadURL, SuggestedName: "tampered.csv",
	})
	if err != nil {
		t.Fatalf("Start(tamper): %v", err)
	}
	staged := filepath.Join(env.root, "staging", jobTamper.ID+".part")
	f, err := os.OpenFile(staged, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开 staging 篡改: %v", err)
	}
	if _, err := f.WriteString("tampered"); err != nil {
		_ = f.Close()
		t.Fatalf("篡改 staging: %v", err)
	}
	_ = f.Close()
	tamperedDest := filepath.Join(env.wsRoot, "downloads", "tampered.csv")
	if _, err := env.mgr.SaveDownload(ctx, env.execA, jobTamper.ID, tamperedDest); err == nil {
		t.Fatal("staging 被篡改后保存仍然成功")
	} else if code := errCode(err); code != contracts.CodeVerificationFailed {
		t.Fatalf("篡改后的错误码 %q，期望 VERIFICATION_FAILED（消息: %s）", code, err.Error())
	}
	if _, statErr := os.Stat(tamperedDest); !os.IsNotExist(statErr) {
		t.Fatalf("hash 不符时仍留下了最终文件: %v", statErr)
	}
	if listed, err := env.artifact.ListArtifacts(ctx, artifact.Query{WorkspaceID: "ws-alpha"}); err != nil || len(listed) != 1 {
		t.Fatalf("hash 不符时仍登记了产物: %v (%+v)", err, listed)
	}
	recordJobs("tampered")

	// cookie 是按 workspace 用的：ws-alpha 的登录态能下到已登录页面，没有 cookie 的
	// workspace 拿不到（401 → 明确失败，不写成"下载成功"）。
	profiles, err := browser.NewProfileStore(browser.ProfileOptions{Root: filepath.Join(filepath.Dir(env.root), "profiles"), DB: openSecondHandle(t, env.dbPath)})
	if err != nil {
		t.Fatalf("NewProfileStore(cookie): %v", err)
	}
	if err := profiles.SaveCookies(ctx, env.execA.WorkspaceID, []browser.StoredCookie{{
		Origin: env.fixture.Primary.URL, Name: browserfixture.CookieName, Value: "ok:fixture", Path: "/",
	}}); err != nil {
		t.Fatalf("SaveCookies: %v", err)
	}
	mgrWithCookies, err := browser.NewDownloadManager(browser.DownloadOptions{
		Root: env.root, DB: openSecondHandle(t, env.dbPath), Cookies: profiles,
		Artifacts: artifactBridge{svc: env.artifact},
		WorkspaceRoot: func(id contracts.WorkspaceID) (string, error) {
			if id != "ws-alpha" {
				return "", contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
			}
			return env.wsRoot, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDownloadManager(cookies): %v", err)
	}
	privateURL := env.fixture.Primary.URL + "/private"
	if job, err := mgrWithCookies.Start(ctx, env.execA, browser.DownloadRequest{URL: privateURL}); err != nil {
		t.Fatalf("带 cookie 的下载失败: %v", err)
	} else if job.Size == 0 {
		t.Fatalf("带 cookie 的下载得到空内容: %+v", job)
	}
	badExec := contracts.ExecutionContext{WorkspaceID: "ws-beta", SessionID: "sess-c", RunID: "run-3"}
	if _, err := mgrWithCookies.Start(ctx, badExec, browser.DownloadRequest{URL: privateURL}); err == nil {
		t.Fatal("没有该 workspace 登录态的下载居然成功了（cookie 隔离不成立）")
	} else if code := errCode(err); code != browser.CodeDownloadFailed {
		t.Fatalf("未登录下载的错误码 %q，期望 %q（消息: %s）", code, browser.CodeDownloadFailed, err.Error())
	}
	recordJobs("cookie-scoped")

	observeBR17(t, env, jobRows, map[string]any{
		"downloadURL":      downloadURL,
		"expectedSHA256":   wantSHA,
		"expectedSize":     len(downloadBody),
		"savedArtifactID":  saved.ArtifactID,
		"savedPath":        dest,
		"serverCounter":    env.fixture.Primary.Counter(browserfixture.CounterDownload),
		"workspaceTreeRaw": wsAfterStart,
		"homeTreeRaw":      homeAfterStart,
		"stagingAvailable": true,
	})
}

// TestSuggestedNamesSanitized 锁住建议名的清理规则（桌面/移动端共用一套）。
func TestSuggestedNamesSanitized(t *testing.T) {
	long := strings.Repeat("a", 200) + ".txt"
	cases := []struct{ raw, want string }{
		{"../../etc/passwd", "passwd"},
		{`..\..\Windows\boot.ini`, "boot.ini"},
		{`C:\Windows\System32\evil.exe`, "evil.exe"},
		{"  report.csv  ", "report.csv"},
		{"", "download"},
		{"..", "download"},
		{"....", "download"},
		{"con.txt", "_con.txt"},
		{"NUL", "_NUL"},
		{"evil.txt.", "evil.txt"},
		{"a/b/c", "c"},
		{"bad\x00name.csv", "badname.csv"},
		{`name:with*chars?.csv`, "name_with_chars_.csv"},
		{long, strings.Repeat("a", browser.MaxSuggestedNameLength-4) + ".txt"},
	}
	for _, c := range cases {
		if got := browser.SanitizeSuggestedName(c.raw); got != c.want {
			t.Errorf("SanitizeSuggestedName(%q) = %q，期望 %q", c.raw, got, c.want)
		}
	}
}

// ---------- BR17 证据 ----------

func observeBR17(t *testing.T, env *downloadEnv, jobRows []map[string]any, facts map[string]any) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	browserObsWrite(t, "downloads.jsonl", mustJSONL(t, anySlice(jobRows)))

	second := openSecondHandle(t, env.dbPath)
	type artifactRow struct {
		ID          string `json:"id"`
		Path        string `json:"canonicalPath"`
		SHA256      string `json:"sha256"`
		Size        int64  `json:"size"`
		DisplayName string `json:"displayName"`
	}
	rows, err := second.SQL().Query(
		`SELECT id, canonical_path, sha256, size_bytes, display_name FROM artifacts ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("读产物行: %v", err)
	}
	defer rows.Close()
	var artifacts []artifactRow
	for rows.Next() {
		var a artifactRow
		if err := rows.Scan(&a.ID, &a.Path, &a.SHA256, &a.Size, &a.DisplayName); err != nil {
			t.Fatalf("扫描产物行: %v", err)
		}
		artifacts = append(artifacts, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历产物行: %v", err)
	}
	browserObsWrite(t, "artifact-row.json", mustJSON(t, map[string]any{
		"dbPath": env.dbPath, "artifacts": artifacts,
		"artifactCount": len(artifacts),
	}))

	final := map[string]any{}
	for _, path := range []string{
		filepath.Join(env.wsRoot, "downloads", "report.csv"),
		filepath.Join(env.wsRoot, "downloads", "cancelled.csv"),
		filepath.Join(env.wsRoot, "downloads", "tampered.csv"),
		filepath.Join(env.home, "Downloads", "outside.csv"),
	} {
		entry := map[string]any{"path": path}
		if info, statErr := os.Stat(path); statErr == nil {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("读 %s: %v", path, readErr)
			}
			sum := sha256.Sum256(raw)
			entry["exists"] = true
			entry["size"] = info.Size()
			entry["sha256"] = hex.EncodeToString(sum[:])
		} else {
			entry["exists"] = false
		}
		final[filepath.Base(path)] = entry
	}
	browserObsWrite(t, "download-files.json", mustJSON(t, map[string]any{
		"workspaceRoot": env.wsRoot,
		"workspaceTree": walkTree(t, env.wsRoot),
		"homeTree":      walkTree(t, env.home),
		"stagingTree":   walkTree(t, filepath.Join(env.root, "staging")),
		"expected":      final,
	}))
	browserObsWrite(t, "no-writes-before-destination.json", mustJSON(t, map[string]any{
		"workspaceBefore": facts["workspaceTreeRaw"],
		"homeBefore":      facts["homeTreeRaw"],
		"stagingAfter":    walkTree(t, filepath.Join(env.root, "staging")),
		"note":            "未选目标时工作区与家目录的遍历结果必须与开始时完全相同（由 downloads.jsonl 的 started 行与两份树对照）",
	}))
	browserObsMerge(t, prefixFacts("BR17", facts))
}

func anySlice(rows []map[string]any) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out
}
