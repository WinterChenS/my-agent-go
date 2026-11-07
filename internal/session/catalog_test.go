package session

// C06b 会话目录测试。全部在 t.TempDir() 里建临时库，不碰真实 appData。
//
// 这里守的是目录最容易"看起来能用"的四件事：
//   - CAS：同一 revision 的两个写者只能有一个成功，失败者的写入不能半落地；
//   - 分页：翻页途中插入新会话（含置顶）不能跳行、不能重复、边界可重放；
//   - 重启：标题/置顶/归档/顺序/revision 必须来自库，不是内存缓存；
//   - 删除：只打软删除标记，行、transcript 定位、产物都不跟着消失。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/history"
	"my-agent-go/internal/storage"
)

func openTestCatalog(t *testing.T) (*Catalog, *storage.DB) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移临时库: %v", err)
	}
	c, err := New(db)
	if err != nil {
		t.Fatalf("构造目录服务: %v", err)
	}
	return c, db
}

// stepClock 每次调用前进一秒：测试要的是"确定的顺序"，不是真实时钟。
func stepClock(base time.Time) func() time.Time {
	var mu sync.Mutex
	var n int
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
}

func requireAppCode(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestCatalogCASCasConcurrentWrite：两个写者拿着同一 revision 改标题，
// 只能有一个赢；输的那个必须拿到 CONFIG_CONFLICT，且它的标题不能落库。
func TestCatalogCASCasConcurrentWrite(t *testing.T) {
	c, db := openTestCatalog(t)
	ctx := context.Background()

	s, err := c.CreateSession(ctx, "ws-cas")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.Revision != 1 {
		t.Fatalf("新会话 revision 应为 1，实际 %d", s.Revision)
	}

	type outcome struct {
		session Session
		title   string
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		title := fmt.Sprintf("写者-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := c.RenameSession(ctx, s.ID, s.Revision, title)
			results <- outcome{session: got, title: title, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var wins, losses []outcome
	for r := range results {
		if r.err == nil {
			wins = append(wins, r)
		} else {
			losses = append(losses, r)
		}
	}
	if len(wins) != 1 || len(losses) != 1 {
		t.Fatalf("同一 revision 只能有一个写者成功，实际成功 %d 失败 %d", len(wins), len(losses))
	}
	requireAppCode(t, losses[0].err, contracts.CodeConfigConflict)

	var (
		title    string
		revision int64
		deleted  string
	)
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT title, revision, deleted_at FROM sessions WHERE id = ?`, string(s.ID)).
		Scan(&title, &revision, &deleted); err != nil {
		t.Fatalf("读回库里的标题: %v", err)
	}
	if title != wins[0].title {
		t.Fatalf("库里应只有赢家的标题 %q，实际 %q", wins[0].title, title)
	}
	if title == losses[0].title {
		t.Fatalf("输家的标题 %q 不能落库", losses[0].title)
	}
	if uint64(revision) != s.Revision+1 {
		t.Fatalf("只剩一次变更：revision 应为 %d，实际 %d", s.Revision+1, revision)
	}
	if deleted != "" {
		t.Fatalf("改名不该动软删除标记，实际 %q", deleted)
	}

	// 输家用旧 revision 重试必须继续被拒（不是"重试就过"）。
	_, err = c.RenameSession(ctx, s.ID, s.Revision, "偷偷重试")
	requireAppCode(t, err, contracts.CodeConfigConflict)

	// SES01 的外部观察：冲突之后库里的真实行（第二个句柄读回）。
	observeSessionDB(t, db.Path(), map[string]any{"casConflicts": len(losses)})
}

// paginationScenario 用确定时钟跑一遍"边翻页边插入"，返回每页的标签序列。
//
// 返回标签而不是 ID：ID 是随机的，跨两次运行不可能相同；能重放的边界才是
// 分页是否稳定的证据。
func paginationScenario(t *testing.T, base time.Time) [][]string {
	t.Helper()
	c, db := openTestCatalog(t)
	c.SetClock(stepClock(base))
	ctx := context.Background()

	labels := map[string]string{}
	pinned := map[string]bool{}
	var creationOrder []string
	for i := 0; i < 9; i++ {
		s, err := c.CreateSession(ctx, "ws-page")
		if err != nil {
			t.Fatalf("CreateSession #%d: %v", i, err)
		}
		labels[string(s.ID)] = fmt.Sprintf("b%d", i)
		creationOrder = append(creationOrder, string(s.ID))
		if i%3 == 0 {
			if _, err := c.PinSession(ctx, s.ID, s.Revision, true); err != nil {
				t.Fatalf("PinSession #%d: %v", i, err)
			}
			pinned[string(s.ID)] = true
		}
	}

	var pages [][]string
	var cursor string
	inserted := 0
	for page := 0; page < 20; page++ {
		res, err := c.ListSessions(ctx, SessionQuery{WorkspaceID: "ws-page", Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListSessions 第 %d 页: %v", page, err)
		}
		var pageLabels []string
		for _, g := range res.Groups {
			for _, s := range g.Sessions {
				label, ok := labels[string(s.ID)]
				if !ok {
					t.Fatalf("第 %d 页出现未知会话 %q", page, s.ID)
				}
				pageLabels = append(pageLabels, label)
			}
		}
		pages = append(pages, pageLabels)
		if !res.HasMore {
			break
		}
		if res.NextCursor == "" {
			t.Fatalf("HasMore 为真但游标为空")
		}
		cursor = res.NextCursor

		// 翻页间隙插入：新的（含置顶）会话 updated_at 比游标新，
		// 排在游标之前，因此不能出现在后续页里——否则就是重复行。
		s, err := c.CreateSession(ctx, "ws-page")
		if err != nil {
			t.Fatalf("翻页中插入会话: %v", err)
		}
		inserted++
		labels[string(s.ID)] = fmt.Sprintf("i%d", inserted)
		if inserted%2 == 1 {
			if _, err := c.PinSession(ctx, s.ID, s.Revision, true); err != nil {
				t.Fatalf("置顶插入的会话: %v", err)
			}
			pinned[string(s.ID)] = true
		}
	}

	// 看得见的每一行只能出现一次；插入的行同理（要么没出现，要么一次）。
	seen := map[string]int{}
	for _, page := range pages {
		for _, label := range page {
			seen[label]++
		}
	}
	for _, id := range creationOrder {
		if seen[labels[id]] != 1 {
			t.Fatalf("基线会话 %s 应恰好出现一次，实际 %d 次", labels[id], seen[labels[id]])
		}
	}
	for label, n := range seen {
		if n != 1 {
			t.Fatalf("标签 %s 出现了 %d 次", label, n)
		}
	}
	// SES01 的外部观察：分页期间库里真实存在哪些行（revision/pinned 从磁盘读）。
	observeSessionDB(t, db.Path(), map[string]any{"pageCount": len(pages)})
	return pages
}

// TestPaginationStableUnderInserts：置顶优先、其余按 updated_at 倒序、
// 同刻按 id 兜底；翻页途中插入不跳行不重复，两次运行边界完全一致。
func TestPaginationStableUnderInserts(t *testing.T) {
	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	first := paginationScenario(t, base)
	second := paginationScenario(t, base)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("同样的插入节奏应得到同样的分页边界：\n第一次 %v\n第二次 %v", first, second)
	}
	want := [][]string{
		{"b6", "b3", "b0"}, // 置顶三条，按 updated_at 倒序
		{"b8", "b7", "b5"},
		{"b4", "b2", "b1"},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("分页边界不符：\n实际 %v\n期望 %v", first, want)
	}
}

// snapshotSessions 读全量目录（含归档、翻完所有页），用于重启前后逐字段比较。
func snapshotSessions(t *testing.T, c *Catalog) []Session {
	t.Helper()
	var out []Session
	cursor := ""
	for i := 0; i < 50; i++ {
		res, err := c.ListSessions(context.Background(), SessionQuery{
			WorkspaceID:     "ws-restart",
			IncludeArchived: true,
			Limit:           2,
			Cursor:          cursor,
		})
		if err != nil {
			t.Fatalf("snapshot ListSessions: %v", err)
		}
		for _, g := range res.Groups {
			out = append(out, g.Sessions...)
		}
		if !res.HasMore {
			return out
		}
		cursor = res.NextCursor
	}
	t.Fatalf("snapshot 翻页没有收敛")
	return nil
}

// TestCatalogSurvivesRestart：标题/置顶/归档/顺序/revision 必须来自库。
func TestCatalogSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")

	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	c, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetClock(stepClock(time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)))

	var ids []contracts.SessionID
	for i := 0; i < 4; i++ {
		s, err := c.CreateSession(ctx, "ws-restart")
		if err != nil {
			t.Fatalf("CreateSession #%d: %v", i, err)
		}
		ids = append(ids, s.ID)
	}
	if _, err := c.RenameSession(ctx, ids[1], 1, "第二个会话"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if _, err := c.PinSession(ctx, ids[2], 1, true); err != nil {
		t.Fatalf("PinSession: %v", err)
	}
	if _, err := c.ArchiveSession(ctx, ids[3], 1, true); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	before := snapshotSessions(t, c)
	if len(before) != 4 {
		t.Fatalf("归档会话在 IncludeArchived 下应仍可见，实际 %d 条", len(before))
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭库: %v", err)
	}

	db2, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("重开库: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	c2, err := New(db2)
	if err != nil {
		t.Fatalf("重启后 New: %v", err)
	}
	after := snapshotSessions(t, c2)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("重启前后目录必须一致：\n之前 %s\n之后 %s", formatSessions(before), formatSessions(after))
	}
	// 默认查询（不含归档）确实过滤掉归档行，而不是"重启后全都露出来"。
	def, err := c2.ListSessions(ctx, SessionQuery{WorkspaceID: "ws-restart", Limit: 10})
	if err != nil {
		t.Fatalf("默认 ListSessions: %v", err)
	}
	for _, g := range def.Groups {
		for _, s := range g.Sessions {
			if s.Archived {
				t.Fatalf("默认列表不应包含归档会话 %q", s.ID)
			}
		}
	}

	// SES01 的外部观察：重启后库里的真实行（标题/置顶/归档/revision）。
	observeSessionDB(t, db2.Path(), map[string]any{"restartedCatalogSize": len(after)})
}

func formatSessions(items []Session) string {
	out := "["
	for _, s := range items {
		out += fmt.Sprintf("{id:%s title:%q pinned:%v archived:%v rev:%d updated:%s}",
			s.ID, s.Title, s.Pinned, s.Archived, s.Revision, s.UpdatedAt.Format(time.RFC3339Nano))
	}
	return out + "]"
}

// TestDeleteIsSoftAndKeepsArtifactsAndTranscript：删除只打标记。
//
// 产物索引表归 C07（007），这里用一张同样带 ON DELETE CASCADE 外键的探针表，
// 让"如果是硬删除，子行会跟着消失"成为可执行的断言，而不是注释里的承诺。
func TestDeleteIsSoftAndKeepsArtifactsAndTranscript(t *testing.T) {
	ctx := context.Background()
	c, db := openTestCatalog(t)

	s, err := c.CreateSession(ctx, "ws-delete")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 探针产物表 + 一个真实文件。
	if _, err := db.SQL().ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS artifacts_probe (
			id         TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			path       TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("建探针表: %v", err)
	}
	artifactPath := filepath.Join(t.TempDir(), "report.txt")
	content := []byte("产物内容不应被会话删除影响\n")
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatalf("写产物文件: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO artifacts_probe(id, session_id, path) VALUES('art-1', ?, ?)`,
		string(s.ID), artifactPath); err != nil {
		t.Fatalf("写探针产物行: %v", err)
	}

	// Pi transcript（独立权威，只读）：按 session id 定位。
	agentDir := t.TempDir()
	sessionDir := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("建 transcript 目录: %v", err)
	}
	transcriptPath := filepath.Join(sessionDir, string(s.ID)+".jsonl")
	if err := os.WriteFile(transcriptPath,
		[]byte(`{"type":"message","message":{"role":"user","text":"你好"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("写 transcript: %v", err)
	}
	hist, err := history.Open(ctx, db, history.Options{AgentDir: agentDir, WorkspaceID: "ws-delete"})
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	before, err := hist.Transcript(ctx, s.ID)
	if err != nil {
		t.Fatalf("删除前定位 transcript: %v", err)
	}
	if before.Path != transcriptPath {
		t.Fatalf("transcript 定位应为 %q，实际 %q", transcriptPath, before.Path)
	}

	if err := c.DeleteSession(ctx, s.ID, s.Revision); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// 1. 目录里看不到（含 IncludeArchived，说明它确实被删了而不是归档）。
	res, err := c.ListSessions(ctx, SessionQuery{WorkspaceID: "ws-delete", IncludeArchived: true, Limit: 10})
	if err != nil {
		t.Fatalf("删除后 ListSessions: %v", err)
	}
	for _, g := range res.Groups {
		for _, got := range g.Sessions {
			if got.ID == s.ID {
				t.Fatalf("删除后的会话不应出现在目录里")
			}
		}
	}
	if _, err := c.GetSession(ctx, s.ID); err == nil {
		t.Fatalf("删除后 GetSession 必须报错")
	} else {
		requireAppCode(t, err, contracts.CodeNotFound)
	}

	// 2. 行还在，只是打了标记；revision 也推进了。
	var (
		deletedAt string
		revision  int64
	)
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT deleted_at, revision FROM sessions WHERE id = ?`, string(s.ID)).
		Scan(&deletedAt, &revision); err != nil {
		t.Fatalf("软删除后行必须还在: %v", err)
	}
	if deletedAt == "" {
		t.Fatalf("删除必须写软删除标记")
	}
	if uint64(revision) != s.Revision+1 {
		t.Fatalf("删除也要推进 revision：期望 %d，实际 %d", s.Revision+1, revision)
	}

	// 3. 产物行与产物文件原封不动。
	var artifactCount int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts_probe`).Scan(&artifactCount); err != nil {
		t.Fatalf("统计探针产物: %v", err)
	}
	if artifactCount != 1 {
		t.Fatalf("会话软删除不能连带删产物行，实际剩 %d 行", artifactCount)
	}
	got, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("产物文件必须还在: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("产物文件内容被改动")
	}

	// 4. transcript 定位仍然解析得到（行还在 = 定位键还在）。
	after, err := hist.Transcript(ctx, s.ID)
	if err != nil {
		t.Fatalf("删除后仍应能定位 transcript: %v", err)
	}
	if after.Path != transcriptPath || len(after.Messages) != 1 {
		t.Fatalf("删除后 transcript 定位应保持不变，实际 %+v", after)
	}

	// 5. 已删除的会话不能再被改：旧 revision 也不行。
	_, err = c.RenameSession(ctx, s.ID, s.Revision+1, "还想改名")
	requireAppCode(t, err, contracts.CodeNotFound)

	// SES01 的外部观察：软删除标记与产物文件都还在磁盘上。
	observeSessionDB(t, db.Path(), map[string]any{"softDeleted": string(s.ID)})
	observeTree(t, filepath.Dir(artifactPath), "artifact-files.json", map[string]any{"keptAfterDelete": filepath.Base(artifactPath)})
}

// TestMigration005AppliesOnAnOldDatabase：005 只依赖 001 的 sessions 表。
//
// runner 按"已应用版本集合"补跑，所以 005 必须在只应用了旧版本（<5）的库上
// 单独跑通，并把历史行补齐。003（C08）与 004（C09）都已合入，它们是否进入已应用
// 集合由上面的通用断言（期望值来自迁移目录）覆盖，不再硬编码号码。
func TestMigration005AppliesOnAnOldDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrations, err := storage.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	var wantVersions []int
	for _, m := range migrations {
		wantVersions = append(wantVersions, m.Version)
	}
	const catalogVersion = 5
	if _, err := db.SQL().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("建迁移记录表: %v", err)
	}
	for _, m := range migrations {
		if m.Version >= catalogVersion {
			continue
		}
		if _, err := db.SQL().ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("应用旧迁移 %s: %v", m.Name, err)
		}
		if _, err := db.SQL().ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, datetime('now'))`,
			m.Version, m.Name); err != nil {
			t.Fatalf("记录旧迁移 %s: %v", m.Name, err)
		}
	}

	// 前置断言：旧库里确实没有目录列，否则这个测试什么也没证明。
	cols := tableColumns(t, db, "sessions")
	for _, name := range []string{"title", "pinned", "archived", "updated_at", "revision", "deleted_at", "kind"} {
		if cols[name] {
			t.Fatalf("旧库不应已有 sessions.%s：测试前提不成立", name)
		}
	}

	// 旧行：created_at 是 history.RecordRun 写的 RFC3339Nano（这里 7 位小数）。
	created := time.Date(2026, 10, 20, 7, 30, 0, 123456700, time.UTC)
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO workspaces(id, root_path, created_at) VALUES('ws-old', '/tmp/old', ?)`,
		created.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("写旧工作区: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO sessions(id, workspace_id, created_at) VALUES('ses-old', 'ws-old', ?)`,
		created.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("写旧会话: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("在旧库上补跑迁移: %v", err)
	}

	applied, err := appliedVersions(t, db)
	if err != nil {
		t.Fatalf("读已应用集合: %v", err)
	}
	if !reflect.DeepEqual(applied, wantVersions) {
		t.Fatalf("补跑后已应用集合应为 %v，实际 %v", wantVersions, applied)
	}

	// 旧行被补齐：固定宽度时间戳 + 默认 revision/kind，且时间语义不变。
	var (
		title, kind, updatedAt string
		pinned, archived       int
		revision               int64
	)
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT title, kind, updated_at, pinned, archived, revision FROM sessions WHERE id = 'ses-old'`).
		Scan(&title, &kind, &updatedAt, &pinned, &archived, &revision); err != nil {
		t.Fatalf("读旧行: %v", err)
	}
	if title != "" || kind != KindChat || pinned != 0 || archived != 0 || revision != 1 {
		t.Fatalf("旧行默认值不对：title=%q kind=%q pinned=%d archived=%d revision=%d",
			title, kind, pinned, archived, revision)
	}
	if wantWidth := created.UTC().Format(tsLayout); updatedAt != wantWidth {
		t.Fatalf("旧行 updated_at 应补齐为固定宽度 %q，实际 %q", wantWidth, updatedAt)
	}

	// 目录能把它读出来（时间解析不再撞格式），并且仍是普通会话。
	c, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := c.ListSessions(ctx, SessionQuery{WorkspaceID: "ws-old", Limit: 10})
	if err != nil {
		t.Fatalf("旧库上 ListSessions: %v", err)
	}
	if len(res.Groups) != 1 || len(res.Groups[0].Sessions) != 1 {
		t.Fatalf("旧会话应出现在目录里，实际 %+v", res.Groups)
	}
	if got := res.Groups[0].Sessions[0]; !got.UpdatedAt.Equal(created) || got.Revision != 1 {
		t.Fatalf("旧会话时间/revision 读错：%+v", got)
	}

	// 再来一次：迁移幂等，已应用集合不变，旧行不被二次改写。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	again, err := appliedVersions(t, db)
	if err != nil {
		t.Fatalf("重读已应用集合: %v", err)
	}
	if !reflect.DeepEqual(again, wantVersions) {
		t.Fatalf("重复迁移后已应用集合应为 %v，实际 %v", wantVersions, again)
	}
	var updatedAt2 string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT updated_at FROM sessions WHERE id = 'ses-old'`).Scan(&updatedAt2); err != nil {
		t.Fatalf("重读旧行: %v", err)
	}
	if updatedAt2 != updatedAt {
		t.Fatalf("重复迁移不该改写旧行：%q -> %q", updatedAt, updatedAt2)
	}
}

func tableColumns(t *testing.T, db *storage.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("读 %s 列: %v", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描列名: %v", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列: %v", err)
	}
	return out
}

func appliedVersions(t *testing.T, db *storage.DB) ([]int, error) {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
