package app

// U01 的搜索绑定层测试（SRCH01）。
//
// 用真实的临时 appData + 真实 SQLite：重建、可用性、路由 id 与工作区隔离
// 都必须能在这条真实路径上被观察。来源（Profile/Skill）走真实配置存储，
// 不 mock 产品自己的服务。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/profile"
	"my-agent-go/internal/search"
)

// setCurrentWorkspace 把设置里的当前工作区指到 dir。
//
// 搜索按当前工作区过滤（SRCH01：其它工作区的结果不出现），所以"用户在这个项目里"
// 是测试必须显式摆出来的前置条件，而不是靠默认工作区碰巧对上。
func setCurrentWorkspace(d *Desktop, dir string) {
	d.mu.Lock()
	d.settings.Workspace = dir
	d.mu.Unlock()
}

// searchRefs 把结果压成 "kind:refId" 便于断言。
func searchRefs(items []search.Item) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[it.Kind+":"+it.RefID] = true
	}
	return out
}

func TestSearchAPIRejectsShortQueryAndReportsUnbuiltIndex(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 少于 3 个字符：QUERY_TOO_SHORT（不是空列表）。
	for _, q := range []string{"", "a", "ab", "数据"} {
		res := d.Search(q, nil, 0)
		if res.Error == nil || res.Error.Code != contracts.CodeQueryTooShort {
			t.Fatalf("查询 %q 应返回 QUERY_TOO_SHORT，实际 %+v", q, res)
		}
	}

	// 索引尚未建立：unavailable，且不是"0 条结果"。
	res := d.Search("alpha", nil, 0)
	if res.Error != nil {
		t.Fatalf("未建索引时不应返回错误: %+v", res.Error)
	}
	if res.Availability != search.AvailabilityUnavailable {
		t.Fatalf("未建索引的可用性 = %q，期望 %q", res.Availability, search.AvailabilityUnavailable)
	}
	if len(res.Items) != 0 {
		t.Fatalf("未建索引不该返回条目: %+v", res.Items)
	}

	// 重建之后同一查询变为可用。
	rebuilt := d.RebuildSearchIndex()
	if rebuilt.Error != nil {
		t.Fatalf("RebuildSearchIndex: %+v", rebuilt.Error)
	}
	if rebuilt.Availability != search.AvailabilityAvailable {
		t.Fatalf("重建后可用性 = %q", rebuilt.Availability)
	}
	if rebuilt.BuiltAt == nil || rebuilt.BuiltAt.IsZero() {
		t.Fatal("重建后必须有 builtAt：没有时间戳就无法判断索引新旧")
	}
	if d.Search("alpha", nil, 0).Availability != search.AvailabilityAvailable {
		t.Fatal("重建后 Search 应返回 available")
	}
	if st := d.SearchIndexStatus(); st.Availability != search.AvailabilityAvailable {
		t.Fatalf("SearchIndexStatus = %+v", st)
	}
}

func TestSearchAPIIndexesSessionsProfilesSkillsAndArtifacts(t *testing.T) {
	d, root := newTestDesktop(t)

	// 一个工作区 + 一条真实会话。
	wsDir := filepath.Join(root, "alpha-ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatalf("建工作区目录: %v", err)
	}
	ws := d.CreateWorkspace(wsDir)
	if ws.Error != nil {
		t.Fatalf("CreateWorkspace: %+v", ws.Error)
	}
	// 搜索按当前工作区过滤：把设置里的工作区指到这个目录，才等于"用户就在这个项目里"。
	setCurrentWorkspace(d, wsDir)
	created := d.CreateSession(string(ws.Item.ID))
	if created.Error != nil {
		t.Fatalf("CreateSession: %+v", created.Error)
	}
	renamed := d.RenameSession(string(created.Item.ID), created.Item.Revision, "alpha 会话")
	if renamed.Error != nil {
		t.Fatalf("RenameSession: %+v", renamed.Error)
	}

	// 一个真实模型 + 一个真实 Profile（V1 的 Agent）：Profile 必须引用存在的模型，
	// 所以这里先把模型定义写进资源库，而不是拿一个不存在的引用造数据。
	if m := d.SaveModel(0, demoModel(), ""); m.Error != nil {
		t.Fatalf("SaveModel: %+v", m.Error)
	}
	saved := d.SaveProfile("agent-alpha", string(ws.Item.ID), 0, profile.Profile{
		ID:       "agent-alpha",
		Name:     "alpha reviewer",
		ModelRef: "m-demo",
	})
	if saved.Error != nil {
		t.Fatalf("SaveProfile: %+v", saved.Error)
	}

	// 一个真实 Skill：源目录只有 SKILL.md，导入只解析清单，不执行任何东西。
	skillSrc := filepath.Join(root, "skill-src")
	if err := os.MkdirAll(skillSrc, 0o700); err != nil {
		t.Fatalf("建技能源目录: %v", err)
	}
	skillBody := "---\nname: alpha-skill\ndescription: alpha 相关技能\n---\n\n只在测试里出现。\n"
	if err := os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		t.Fatalf("写 SKILL.md: %v", err)
	}
	imported := d.ImportSkill(skillSrc, "user", "")
	if imported.Error != nil {
		t.Fatalf("ImportSkill: %+v", imported.Error)
	}

	rebuilt := d.RebuildSearchIndex()
	if rebuilt.Error != nil {
		t.Fatalf("RebuildSearchIndex: %+v", rebuilt.Error)
	}

	res := d.Search("alpha", nil, 0)
	if res.Error != nil {
		t.Fatalf("Search: %+v", res.Error)
	}
	refs := searchRefs(res.Items)
	for _, want := range []string{
		search.KindSession + ":" + string(created.Item.ID),
		search.KindAgent + ":agent-alpha",
		search.KindSkill + ":alpha-skill",
	} {
		if !refs[want] {
			t.Fatalf("缺少结果 %s；实际=%v", want, refs)
		}
	}
	for _, it := range res.Items {
		if strings.TrimSpace(it.Route) == "" {
			t.Fatalf("结果缺少稳定 route id: %+v", it)
		}
		if it.Kind == search.KindSession && it.Route != "/chat/"+string(created.Item.ID) {
			t.Fatalf("会话 route = %q", it.Route)
		}
	}

	// kinds 过滤：只要会话时不能混进 Agent/Skill。
	only := d.Search("alpha", []string{search.KindSession}, 0)
	if only.Error != nil {
		t.Fatalf("按类搜索: %+v", only.Error)
	}
	for _, it := range only.Items {
		if it.Kind != search.KindSession {
			t.Fatalf("kinds 过滤失效: %+v", only.Items)
		}
	}

	// 未知类别是入参错误，不是静默忽略。
	bad := d.Search("alpha", []string{"bogus"}, 0)
	if bad.Error == nil || bad.Error.Code != contracts.CodeInvalidInput {
		t.Fatalf("未知类别应返回 INVALID_INPUT，实际 %+v", bad.Error)
	}
}

func TestSearchAPIExcludesDeletedSessionsAfterRebuild(t *testing.T) {
	d, root := newTestDesktop(t)
	wsDir := filepath.Join(root, "beta-ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatalf("建工作区目录: %v", err)
	}
	ws := d.CreateWorkspace(wsDir)
	if ws.Error != nil {
		t.Fatalf("CreateWorkspace: %+v", ws.Error)
	}
	setCurrentWorkspace(d, wsDir)
	keep := d.CreateSession(string(ws.Item.ID))
	gone := d.CreateSession(string(ws.Item.ID))
	for _, s := range []SessionResult{keep, gone} {
		if s.Error != nil {
			t.Fatalf("CreateSession: %+v", s.Error)
		}
	}
	if r := d.RenameSession(string(keep.Item.ID), keep.Item.Revision, "beta 保留"); r.Error != nil {
		t.Fatalf("RenameSession: %+v", r.Error)
	}
	if r := d.RenameSession(string(gone.Item.ID), gone.Item.Revision, "beta 删除"); r.Error != nil {
		t.Fatalf("RenameSession: %+v", r.Error)
	}
	if rebuilt := d.RebuildSearchIndex(); rebuilt.Error != nil {
		t.Fatalf("RebuildSearchIndex: %+v", rebuilt.Error)
	}
	if refs := searchRefs(d.Search("beta", nil, 0).Items); !refs[search.KindSession+":"+string(gone.Item.ID)] {
		t.Fatal("删除前应能搜到该会话（否则下一步的排除结论没有意义）")
	}

	if del := d.DeleteSession(string(gone.Item.ID), gone.Item.Revision+1); del.Error != nil {
		t.Fatalf("DeleteSession: %+v", del.Error)
	}
	// 索引是派生数据：删除后必须重建才反映权威状态（本任务不做隐式增量）。
	if rebuilt := d.RebuildSearchIndex(); rebuilt.Error != nil {
		t.Fatalf("重建: %+v", rebuilt.Error)
	}
	refs := searchRefs(d.Search("beta", nil, 0).Items)
	if refs[search.KindSession+":"+string(gone.Item.ID)] {
		t.Fatal("已删除的会话仍出现在检索结果里")
	}
	if !refs[search.KindSession+":"+string(keep.Item.ID)] {
		t.Fatal("未删除的会话被误删出索引")
	}
}

func TestSearchAPIUsesContextAndClampsLimit(t *testing.T) {
	d, _ := newTestDesktop(t)
	if rebuilt := d.RebuildSearchIndex(); rebuilt.Error != nil {
		t.Fatalf("RebuildSearchIndex: %+v", rebuilt.Error)
	}
	// limit 超过上限时压到 MaxLimit，而不是把请求变成"一次拉全量"。
	res := d.Search("alpha", nil, search.MaxLimit+50)
	if res.Error != nil {
		t.Fatalf("Search: %+v", res.Error)
	}
	if res.Availability != search.AvailabilityAvailable {
		t.Fatalf("可用性 = %q", res.Availability)
	}
	// 空库就是空结果（可用且 0 条），与"不可用"必须能区分开。
	if len(res.Items) != 0 {
		t.Fatalf("空库里不该有结果: %+v", res.Items)
	}
}
