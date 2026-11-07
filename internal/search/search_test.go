package search

// U01 的搜索索引测试（SRCH01）。
//
// 全部在 t.TempDir() 里的真实 SQLite 上跑：trigram 分词、DELETE+INSERT 重建、
// 事务可见性都不能用内存假实现证明。来源（Agent/Skill）用显式 fake，
// 因为它们的权威在配置存储里，本包只认"给我一组 Doc"。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// fakeSource 是配置存储的最小替身：只回答"有哪些 Agent/Skill"。
type fakeSource struct {
	agents []Doc
	skills []Doc
	err    error
}

func (f fakeSource) Agents(context.Context) ([]Doc, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.agents, nil
}

func (f fakeSource) Skills(context.Context) ([]Doc, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.skills, nil
}

func openIndexDB(t *testing.T, path string) *storage.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("打开索引库: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移索引库: %v", err)
	}
	// Windows 上未关闭的库会锁住临时目录里的文件（TempDir 清理会失败）。
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedWorkspaceAndSession 直接写权威表（sessions），模拟 C06 已经落库的行。
func seedSession(t *testing.T, db *storage.DB, wsID, sessionID, title string, opts ...func(*testing.T, *storage.DB, string)) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO workspaces(id, root_path, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`, wsID, "/tmp/"+wsID, now); err != nil {
		t.Fatalf("写 workspaces: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO sessions(id, workspace_id, title, pinned, archived, kind, created_at, updated_at, revision, deleted_at)
		 VALUES(?, ?, ?, 0, 0, 'chat', ?, ?, 1, '')`, sessionID, wsID, title, now, now); err != nil {
		t.Fatalf("写 sessions: %v", err)
	}
	for _, fn := range opts {
		fn(t, db, sessionID)
	}
}

func seedArtifact(t *testing.T, db *storage.DB, wsID, id, display, path string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(context.Background(), `
		INSERT INTO artifacts(id, workspace_id, run_id, session_id, display_name, canonical_path,
		                     path_key, sha256, size_bytes, media_type, created_at, updated_at)
		VALUES(?, ?, '', '', ?, ?, ?, 'deadbeef', 12, 'text/markdown', ?, ?)`,
		id, wsID, display, path, strings.ToLower(path), now, now); err != nil {
		t.Fatalf("写 artifacts: %v", err)
	}
}

func archiveSession(t *testing.T, db *storage.DB, sessionID string) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(),
		`UPDATE sessions SET archived = 1 WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("归档会话: %v", err)
	}
}

func deleteSession(t *testing.T, db *storage.DB, sessionID string) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(),
		`UPDATE sessions SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), sessionID); err != nil {
		t.Fatalf("删除会话: %v", err)
	}
}

func newService(t *testing.T, db *storage.DB, src Source) *Service {
	t.Helper()
	svc, err := New(db, src)
	if err != nil {
		t.Fatalf("构造搜索服务: %v", err)
	}
	svc.SetClock(func() time.Time { return time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC) })
	return svc
}

func itemKinds(items []Item) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Kind]++
	}
	return out
}

func searchText(t *testing.T, svc *Service, q Query) Result {
	t.Helper()
	res, err := svc.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("Search(%q): %v", q.Text, err)
	}
	if res.Availability != AvailabilityAvailable {
		t.Fatalf("Search(%q) 可用性 = %q，期望 %q", q.Text, res.Availability, AvailabilityAvailable)
	}
	return res
}

func TestSearchIndexesSessionsAgentsSkillsArtifacts(t *testing.T) {
	db := openIndexDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	seedSession(t, db, "ws-a", "s-1", "alpha 会话标题")
	seedArtifact(t, db, "ws-a", "art-1", "alpha-report.md", "/work/ws-a/alpha-report.md")

	src := fakeSource{
		agents: []Doc{{
			Kind: KindAgent, RefID: "agent-1", Route: RouteFor(KindAgent, "agent-1"),
			Title: "alpha reviewer",
		}},
		skills: []Doc{{
			Kind: KindSkill, RefID: "skill-1", Route: RouteFor(KindSkill, "skill-1"),
			Title: "alpha skill", Subtitle: "alpha 相关技能",
		}},
	}
	svc := newService(t, db, src)
	summary, err := svc.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if summary.Sessions != 1 || summary.Agents != 1 || summary.Skills != 1 || summary.Artifacts != 1 {
		t.Fatalf("重建计数不对: %+v", summary)
	}

	res := searchText(t, svc, Query{Text: "alpha"})
	kinds := itemKinds(res.Items)
	for _, kind := range []string{KindSession, KindAgent, KindSkill, KindArtifact} {
		if kinds[kind] != 1 {
			t.Fatalf("类别 %s 命中 %d 条，期望 1；全部结果=%+v", kind, kinds[kind], res.Items)
		}
	}

	// 稳定 route id：前端静态路由目录按它解析，改形状等于让旧链接失效。
	routes := map[string]string{}
	for _, it := range res.Items {
		routes[it.Kind] = it.Route
	}
	if routes[KindSession] != "/chat/s-1" {
		t.Errorf("会话路由 = %q，期望 /chat/s-1", routes[KindSession])
	}
	if routes[KindAgent] != "/agents?agent=agent-1" {
		t.Errorf("Agent 路由 = %q", routes[KindAgent])
	}
	if routes[KindSkill] != "/skills?skill=skill-1" {
		t.Errorf("Skill 路由 = %q", routes[KindSkill])
	}
	if routes[KindArtifact] != "/library?artifact=art-1" {
		t.Errorf("产物路由 = %q", routes[KindArtifact])
	}

	// 中文 trigram：3 个字符的子串必须真的命中（不是 LIKE 退化）。
	res = searchText(t, svc, Query{Text: "会话标"})
	if len(res.Items) != 1 || res.Items[0].RefID != "s-1" {
		t.Fatalf("中文 3 字查询没有命中会话: %+v", res.Items)
	}
}

func TestSearchIndexSurvivesRestartAndRebuildIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := openIndexDB(t, path)
	seedSession(t, db, "ws-a", "s-1", "alpha 会话")
	svc := newService(t, db, fakeSource{})
	if _, err := svc.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	first := searchText(t, svc, Query{Text: "alpha"})
	if len(first.Items) != 1 {
		t.Fatalf("首次检索命中 %d 条，期望 1", len(first.Items))
	}
	db.Close()

	// 模拟重启：同一个文件重新打开，索引仍在（它是持久化派生数据）。
	reopened := openIndexDB(t, path)
	svc2 := newService(t, reopened, nil)
	res, err := svc2.Search(context.Background(), Query{Text: "alpha"})
	if err != nil {
		t.Fatalf("重启后 Search: %v", err)
	}
	if res.Availability != AvailabilityAvailable {
		t.Fatalf("重启后索引应仍然可用，实际 %q", res.Availability)
	}
	if len(res.Items) != 1 || res.Items[0].RefID != "s-1" {
		t.Fatalf("重启后结果不一致: %+v", res.Items)
	}

	// 重启后可以再重建一次，且不会产生重复行（doc_id 稳定）。
	if _, err := svc2.Rebuild(context.Background()); err != nil {
		t.Fatalf("重启后 Rebuild: %v", err)
	}
	again := searchText(t, svc2, Query{Text: "alpha"})
	if len(again.Items) != 1 {
		t.Fatalf("重建后出现重复结果: %+v", again.Items)
	}
}

func TestSearchRejectsShortQueryAndReportsUnavailable(t *testing.T) {
	db := openIndexDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	seedSession(t, db, "ws-a", "s-1", "alpha 会话")
	svc := newService(t, db, fakeSource{})

	// 索引未建立：unavailable，不是"0 条结果"。
	res, err := svc.Search(context.Background(), Query{Text: "alpha"})
	if err != nil {
		t.Fatalf("未建索引时 Search 不应报错: %v", err)
	}
	if res.Availability != AvailabilityUnavailable {
		t.Fatalf("未建索引的可用性 = %q，期望 %q", res.Availability, AvailabilityUnavailable)
	}
	if len(res.Items) != 0 {
		t.Fatalf("未建索引不该返回条目: %+v", res.Items)
	}

	for _, text := range []string{"a", "ab", "数据", "  a "} {
		_, err := svc.Search(context.Background(), Query{Text: text})
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) || appErr.Code != contracts.CodeQueryTooShort {
			t.Fatalf("查询 %q 应返回 QUERY_TOO_SHORT，实际 %v", text, err)
		}
	}
	// 3 个字符是允许的（边界不能多一个）。
	if _, err := svc.Search(context.Background(), Query{Text: "abc"}); err != nil {
		t.Fatalf("3 字符查询不该被拒: %v", err)
	}

	// 未知类别是入参错误，不是静默忽略。
	_, err = svc.Search(context.Background(), Query{Text: "alpha", Kinds: []string{"bogus"}})
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeInvalidInput {
		t.Fatalf("未知类别应返回 INVALID_INPUT，实际 %v", err)
	}
	// profile 是 agent 的别名（规格写的是 Agent/Profile）。
	if kinds, err := ParseKinds([]string{"profile", "skill", "skill"}); err != nil {
		t.Fatalf("ParseKinds: %v", err)
	} else if len(kinds) != 2 || kinds[0] != KindAgent || kinds[1] != KindSkill {
		t.Fatalf("别名归一化结果不对: %v", kinds)
	}
}

func TestSearchExcludesDeletedArchivedAndOtherWorkspaces(t *testing.T) {
	db := openIndexDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	seedSession(t, db, "ws-a", "s-a", "alpha 在 A")
	seedSession(t, db, "ws-b", "s-b", "alpha 在 B")
	seedSession(t, db, "ws-a", "s-del", "alpha 已删除")
	seedSession(t, db, "ws-a", "s-arch", "alpha 已归档")
	deleteSession(t, db, "s-del")
	archiveSession(t, db, "s-arch")

	svc := newService(t, db, fakeSource{})
	if _, err := svc.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// 不限工作区：删除与归档不出现，其它工作区出现。
	res := searchText(t, svc, Query{Text: "alpha"})
	refs := map[string]bool{}
	for _, it := range res.Items {
		refs[it.RefID] = true
	}
	if refs["s-del"] || refs["s-arch"] {
		t.Fatalf("已删除/已归档的会话出现在结果里: %+v", refs)
	}
	if !refs["s-a"] || !refs["s-b"] {
		t.Fatalf("正常会话缺失: %+v", refs)
	}

	// 限定工作区 A：B 的结果不出现。
	res = searchText(t, svc, Query{Text: "alpha", WorkspaceID: "ws-a"})
	for _, it := range res.Items {
		if it.RefID == "s-b" {
			t.Fatalf("其它工作区的会话出现在结果里: %+v", res.Items)
		}
	}

	// kinds 过滤 + limit 上限。
	seedSession(t, db, "ws-a", "s-a2", "alpha 在 A 二号")
	if _, err := svc.Rebuild(context.Background()); err != nil {
		t.Fatalf("重建: %v", err)
	}
	only := searchText(t, svc, Query{Text: "alpha", Kinds: []string{KindSession}, Limit: 1})
	if len(only.Items) != 1 {
		t.Fatalf("limit=1 应只返回 1 条，实际 %d", len(only.Items))
	}
	if only.Items[0].Kind != KindSession {
		t.Fatalf("kinds 过滤失效: %+v", only.Items[0])
	}
}

func TestSearchReportsUnavailableSourceInsteadOfZero(t *testing.T) {
	db := openIndexDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	seedSession(t, db, "ws-a", "s-1", "alpha 会话")
	// 来源报错：重建必须失败，而不是把 Agent/Skill 静默记成 0 条。
	svc := newService(t, db, fakeSource{err: errors.New("配置存储不可读")})
	if _, err := svc.Rebuild(context.Background()); err == nil {
		t.Fatal("来源失败时重建必须报错，不能发布残缺索引")
	}
	// 失败的重建不改变索引状态。
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Availability != AvailabilityUnavailable {
		t.Fatalf("失败的重建不应把索引标成可用: %+v", st)
	}
}
