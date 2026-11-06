package app

// C06b 绑定层测试：目录 API 只能是薄绑定。
//
// 这里守两件事：
//   - 工作区隔离：请求哪个工作区就只返回那个工作区的行，别的项目的会话不能漏出来；
//   - "普通会话"是真的过滤条件：归档（除非明确要）与自动化/调度会话都不在目录里，
//     而不是靠前端不显示。
//
// 全部在 t.TempDir() 里跑：临时 appData + 临时库，不碰真实配置。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"my-agent-go/internal/storage"
)

func newSessionWorkspace(t *testing.T, d *Desktop, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建工作区目录: %v", err)
	}
	res := d.CreateWorkspace(dir)
	if res.Error != nil {
		t.Fatalf("CreateWorkspace: %+v", res.Error)
	}
	return string(res.Item.ID)
}

func listSessionIDs(t *testing.T, res SessionListResult) []string {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("ListSessions 返回错误: %+v", res.Error)
	}
	var out []string
	for _, g := range res.Groups {
		for _, s := range g.Sessions {
			out = append(out, string(s.ID))
		}
	}
	return out
}

func TestSessionAPIBindsQueriesWithoutLeakingRows(t *testing.T) {
	d, root := newTestDesktop(t)
	wsA := newSessionWorkspace(t, d, root, "alpha")
	wsB := newSessionWorkspace(t, d, root, "beta")

	// 先读一次，确保会话库已建好并迁移到位。
	if res := d.ListSessions(SessionListQuery{}); res.Error != nil {
		t.Fatalf("初次 ListSessions: %+v", res.Error)
	}

	create := func(workspaceID string) string {
		t.Helper()
		res := d.CreateSession(workspaceID)
		if res.Error != nil {
			t.Fatalf("CreateSession(%q): %+v", workspaceID, res.Error)
		}
		return string(res.Item.ID)
	}
	a1 := create(wsA)
	a2 := create(wsA)
	b1 := create(wsB)

	// 归档一条：默认查询里不能出现。
	stale := d.ListSessions(SessionListQuery{WorkspaceID: wsA, IncludeArchived: true})
	if stale.Error != nil {
		t.Fatalf("ListSessions: %+v", stale.Error)
	}
	arch := d.ArchiveSession(a2, 1, true)
	if arch.Error != nil {
		t.Fatalf("ArchiveSession: %+v", arch.Error)
	}

	// 自动化/调度会话（C09 写自己的 kind）直接落在同一个库里：
	// 它不该出现在会话目录，即使工作区对得上。
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatalf("打开会话库: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO sessions(id, workspace_id, title, kind, created_at, updated_at, revision)
		VALUES('ses-scheduler', ?, '每日汇总', 'scheduler', ?, ?, 1)`, wsA, now, now); err != nil {
		t.Fatalf("写调度会话: %v", err)
	}

	// 1. 默认：只 wsA 的非归档普通会话。
	got := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: wsA, Limit: 50}))
	if len(got) != 1 || got[0] != a1 {
		t.Fatalf("wsA 默认目录应只有 %q，实际 %v", a1, got)
	}

	// 2. 明确要归档：归档行回来，调度会话仍然不回来。
	withArchived := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: wsA, IncludeArchived: true, Limit: 50}))
	if len(withArchived) != 2 {
		t.Fatalf("含归档时应有 2 条，实际 %v", withArchived)
	}
	for _, id := range withArchived {
		if id == "ses-scheduler" {
			t.Fatalf("调度会话不能出现在会话目录里")
		}
	}

	// 3. 别的项目一行都不能漏。
	other := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: wsB, IncludeArchived: true, Limit: 50}))
	if len(other) != 1 || other[0] != b1 {
		t.Fatalf("wsB 只应有 %q，实际 %v", b1, other)
	}

	// 4. 不指定工作区时按工作区分组，且仍然只含普通会话。
	all := d.ListSessions(SessionListQuery{IncludeArchived: true, Limit: 50})
	if all.Error != nil {
		t.Fatalf("全量 ListSessions: %+v", all.Error)
	}
	groups := map[string][]string{}
	total := 0
	for _, g := range all.Groups {
		for _, s := range g.Sessions {
			groups[string(g.WorkspaceID)] = append(groups[string(g.WorkspaceID)], string(s.ID))
			total++
			if s.ID == "ses-scheduler" {
				t.Fatalf("调度会话不能出现在会话目录里")
			}
		}
	}
	if total != 3 {
		t.Fatalf("全量应有 3 条普通会话，实际 %d（%v）", total, groups)
	}
	if len(groups[wsA]) == 0 || len(groups[wsB]) == 0 {
		t.Fatalf("全量结果应按工作区分组，实际 %v", groups)
	}
}

func TestSessionAPICreatesInCurrentWorkspace(t *testing.T) {
	d, _ := newTestDesktop(t)
	// 默认设置给的正是 <root>/workspace；不传 workspaceID 时应落到当前工作区，
	// 而不是造一个"无主会话"。
	res := d.CreateSession("")
	if res.Error != nil {
		t.Fatalf("CreateSession(\"\"): %+v", res.Error)
	}
	if res.Item.WorkspaceID == "" {
		t.Fatalf("会话必须归属一个工作区")
	}
	if res.Item.Revision != 1 || res.Item.Title != "" || res.Item.Pinned || res.Item.Archived {
		t.Fatalf("新会话字段不对：%+v", res.Item)
	}
	list := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: string(res.Item.WorkspaceID), Limit: 10}))
	if len(list) != 1 || list[0] != string(res.Item.ID) {
		t.Fatalf("新会话应出现在它所在工作区的目录里，实际 %v", list)
	}
	// 不存在的显式工作区要报错，而不是静默落到默认工作区。
	if bad := d.CreateSession("ws-does-not-exist"); bad.Error == nil {
		t.Fatalf("未知工作区必须报错")
	}
}
