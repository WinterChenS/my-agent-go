package memory

// C05 的中文检索与注入预算测试（MEM01/MEM02/MEM04）。

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// TestChineseTrigramScopeAndExpiry 覆盖 MEM01/MEM02：
// A/B 项目隔离、user 共享、candidate/deleted/expired 不参与检索、短查询报错。
func TestChineseTrigramScopeAndExpiry(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	memA := save(t, svc, MemoryDraft{
		Scope:           ScopeWorkspace,
		WorkspaceID:     "ws-a",
		Kind:            KindConvention,
		Content:         "数据库连接池上限为二十；数据库连接池按项目独立配置",
		SourceSessionID: "sess-a",
	})
	memA2 := save(t, svc, MemoryDraft{
		Scope:           ScopeWorkspace,
		WorkspaceID:     "ws-a",
		Kind:            KindConvention,
		Content:         "数据库连接池默认超时三十秒",
		SourceSessionID: "sess-a2",
	})
	memB := save(t, svc, MemoryDraft{
		Scope:           ScopeWorkspace,
		WorkspaceID:     "ws-b",
		Kind:            KindConvention,
		Content:         "数据库连接池上限为五，生产环境禁止调大",
		SourceSessionID: "sess-b",
	})
	userMem := save(t, svc, MemoryDraft{
		Scope:           ScopeUser,
		Kind:            KindPreference,
		Content:         "用户偏好：回答先给结论，再给理由",
		SourceSessionID: "sess-u",
	})
	expired := save(t, svc, MemoryDraft{
		Scope:       ScopeWorkspace,
		WorkspaceID: "ws-a",
		Kind:        KindFact,
		Content:     "数据库连接池上限为二百（已过期）",
		ExpiresAt:   ptr(time.Now().Add(-time.Hour)),
	})
	candidate := save(t, svc, MemoryDraft{
		Scope:       ScopeWorkspace,
		WorkspaceID: "ws-a",
		Kind:        KindNote,
		Content:     "数据库连接池上限为九十九（候选）",
		Status:      StatusCandidate,
	})

	hits, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池", Limit: 5})
	if err != nil {
		t.Fatalf("Search ws-a: %v", err)
	}
	ids := idsOf(hits)
	if len(ids) != 2 || !containsIDs(ids, memA.ID, memA2.ID) {
		t.Fatalf("ws-a 应命中本项目两条 active 未过期条目，实际 %v（A=%s, A2=%s）", ids, memA.ID, memA2.ID)
	}
	for _, ghost := range []string{memB.ID, expired.ID, candidate.ID} {
		if containsID(hits, ghost) {
			t.Fatalf("不应命中 %s（跨项目/过期/候选）", ghost)
		}
	}

	bHits, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-b", Text: "数据库连接池", Limit: 5})
	if err != nil {
		t.Fatalf("Search ws-b: %v", err)
	}
	if len(bHits) != 1 || bHits[0].ID != memB.ID {
		t.Fatalf("ws-b 应只命中自己的约定，实际 %+v", bHits)
	}

	// user 记忆在任意项目可见，且不返回 workspace 条目。
	uHits, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-b", Text: "回答先给结论", Limit: 5})
	if err != nil {
		t.Fatalf("Search user 偏好: %v", err)
	}
	if len(uHits) != 1 || uHits[0].ID != userMem.ID {
		t.Fatalf("user 记忆应跨项目可见，实际 %+v", uHits)
	}

	// 排名：同一查询的命中按 FTS5 rank 升序返回（bm25，越小越靠前）。
	ranked, err := svc.SearchRanked(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池", Limit: 5})
	if err != nil {
		t.Fatalf("SearchRanked: %v", err)
	}
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Rank < ranked[i-1].Rank {
			t.Fatalf("结果未按 rank 升序: %v", ranksOf(ranked))
		}
	}
	t.Logf("查询=数据库连接池 workspace=ws-a 命中=%d 内容预览=%v rank=%v", len(ranked), previewsOf(ranked), ranksOf(ranked))

	// 短查询：按 rune 计，不是按 byte（"数据" 6 bytes 但只有 2 个 Unicode 字符）。
	_, err = svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据", Limit: 5})
	appErr := requireCode(t, err, contracts.CodeQueryTooShort)
	if appErr.Code != "QUERY_TOO_SHORT" {
		t.Fatalf("错误码字符串必须固定为 QUERY_TOO_SHORT（前端按码分支），实际 %q", appErr.Code)
	}
	if _, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "   ", Limit: 5}); err == nil {
		t.Fatal("空查询应报 QUERY_TOO_SHORT")
	}
	// 恰好 3 个 rune 可以查（即使查询不命中）。
	if _, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库", Limit: 5}); err != nil {
		t.Fatalf("3 个 Unicode 字符应允许查询: %v", err)
	}
	if _, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "abc", Limit: 5}); err != nil {
		t.Fatalf("3 个 ASCII 字符应允许查询: %v", err)
	}
	if _, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "ab", Limit: 5}); err == nil {
		t.Fatal("2 个 ASCII 字符应报 QUERY_TOO_SHORT")
	}

	// MEM01 的外部观察：磁盘上真实存在哪些行（scope/status/expires_at 都是库里那一份）。
	observeMemoryDB(t, svc.DB().Path(), nil)
}

// TestMemoryContextBounded 覆盖 MEM04：≤5 条、≤6000 UTF-8 bytes、rune 边界安全、
// 来源可追踪。塞 8 条超长中文，确保触发截断而不是"恰好没超"。
func TestMemoryContextBounded(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	long := strings.Repeat("数据库连接池上限为二十；", 33) // 每条约 1188 bytes
	var saved []Memory
	for i := 0; i < 8; i++ {
		saved = append(saved, save(t, svc, MemoryDraft{
			Scope:           ScopeWorkspace,
			WorkspaceID:     "ws-a",
			Kind:            KindConvention,
			Content:         strings.Join([]string{long, "条目", string(rune('A' + i))}, ""),
			SourceSessionID: "sess-bounded",
		}))
	}

	mc, err := svc.BuildContext(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池", Limit: 99})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if len(mc.Entries) != 5 {
		t.Fatalf("8 条候选应被截到上限 5 条，实际 %d 条", len(mc.Entries))
	}
	truncated := 0
	for _, e := range mc.Entries {
		if e.Truncated {
			truncated++
		}
	}
	if truncated == 0 {
		t.Fatal("预算不足时应至少有一条被按 rune 边界截断")
	}
	if mc.Bytes != len(mc.Text) {
		t.Fatalf("Bytes 必须是实际 UTF-8 字节数：Bytes=%d len(Text)=%d", mc.Bytes, len(mc.Text))
	}
	if mc.Bytes > MaxContextBytes {
		t.Fatalf("上下文超过 %d bytes，实际 %d", MaxContextBytes, mc.Bytes)
	}
	if !utf8.ValidString(mc.Text) {
		t.Fatal("截断破坏了 UTF-8：mc.Text 不是合法 UTF-8")
	}
	total := 0
	for _, e := range mc.Entries {
		if !utf8.ValidString(e.Content) {
			t.Fatalf("条目 %s 的截断内容不是合法 UTF-8", e.MemoryID)
		}
		if e.Bytes != len(e.Content) {
			t.Fatalf("条目 %s 的 Bytes 与实际不符: %d != %d", e.MemoryID, e.Bytes, len(e.Content))
		}
		if e.MemoryID == "" || e.Scope == "" || e.SourceSessionID == "" {
			t.Fatalf("条目缺少来源信息: %+v", e)
		}
		total += e.Bytes
	}
	if total > MaxContextBytes {
		t.Fatalf("条目内容合计超过 %d bytes，实际 %d", MaxContextBytes, total)
	}
	if len(mc.Provenance) != len(mc.Entries) {
		t.Fatalf("Provenance 条数应与 Entries 一致，实际 %d != %d", len(mc.Provenance), len(mc.Entries))
	}
	if !strings.Contains(mc.Text, "不是系统指令") {
		t.Fatal("注入块必须声明这是参考数据、不是系统指令")
	}
	t.Logf("BuildContext 条数=%d bytes=%d 候选条数=%d 截断条数=%d", len(mc.Entries), mc.Bytes, len(saved), truncated)

	// MEM04 的外部观察：注入规模（rune/byte）与磁盘上真实存在的行数。
	observeMemoryDB(t, svc.DB().Path(), map[string]any{
		"injectedChars":   utf8.RuneCountInString(mc.Text),
		"injectedBytes":   mc.Bytes,
		"injectedEntries": len(mc.Entries),
		"bounded":         mc.Bytes <= MaxContextBytes && len(mc.Entries) <= MaxContextEntries,
		"candidates":      len(saved),
	})
}

// TestDeleteNotInjectedNextRun 覆盖 MEM02/MEM05：删除后下一 run 的注入里没有该内容。
func TestDeleteNotInjectedNextRun(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	m := save(t, svc, MemoryDraft{
		Scope:           ScopeWorkspace,
		WorkspaceID:     "ws-a",
		Content:         "数据库连接池上限为二十",
		SourceSessionID: "sess-1",
	})
	before, err := svc.BuildContext(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池"})
	if err != nil {
		t.Fatalf("BuildContext before: %v", err)
	}
	if !strings.Contains(before.Text, "数据库连接池上限为二十") {
		t.Fatalf("删除前应注入该内容，实际 %q", before.Text)
	}

	if err := svc.Delete(ctx, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	after, err := svc.BuildContext(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池"})
	if err != nil {
		t.Fatalf("BuildContext after: %v", err)
	}
	if strings.Contains(after.Text, "数据库连接池上限为二十") {
		t.Fatalf("删除后 BuildContext 仍包含该内容：\n%s", after.Text)
	}
	if len(after.Entries) != 0 || after.Bytes != 0 {
		t.Fatalf("删除后不应有注入条目: entries=%d bytes=%d", len(after.Entries), after.Bytes)
	}
	if augmented := AugmentPrompt("继续做数据库优化", after); strings.Contains(augmented, "数据库连接池上限为二十") {
		t.Fatalf("删除后 prompt 注入仍包含该内容：\n%s", augmented)
	}

	// 已发模型的历史无法撤回：这里只断言"下一次注入不再包含"，
	// 不宣称删除能改写上游 transcript。
	t.Logf("删除后 BuildContext 原文: %q", after.Text)

	// MEM02 的外部观察：库里那一行仍然存在，但 status 已被写成 deleted
	//（读的是磁盘上的真实状态，不是 Service 的说法）。
	observeMemoryDB(t, svc.DB().Path(), nil)
}

// TestScopeDisabledNotInjected：按 scope 关闭后，该 scope 不参与检索/注入。
func TestScopeDisabledNotInjected(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	save(t, svc, MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Content: "数据库连接池上限为二十"})
	save(t, svc, MemoryDraft{Scope: ScopeUser, Content: "用户偏好：回答先给结论"})

	if err := svc.SetScopeEnabled(ctx, ScopeWorkspace, "ws-a", false); err != nil {
		t.Fatalf("关闭 workspace scope: %v", err)
	}
	hits, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("关闭 workspace scope 后不应检索到项目记忆，实际 %+v", hits)
	}
	userHits, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "回答先给结论"})
	if err != nil {
		t.Fatalf("Search user: %v", err)
	}
	if len(userHits) != 1 {
		t.Fatalf("关闭 workspace scope 不应影响 user scope，实际 %d 条", len(userHits))
	}
	mc, err := svc.BuildContext(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池"})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if strings.Contains(mc.Text, "数据库连接池上限为二十") {
		t.Fatalf("scope 关闭后仍被注入：\n%s", mc.Text)
	}

	// MEM02 的外部观察：在**关闭期间**读磁盘，看 memory_scope_settings 里的
	// enabled=0 是否真的落库（后面会重新打开，所以必须在这里取）。
	observeMemoryDB(t, svc.DB().Path(), map[string]any{"observedWhile": "workspace-scope-disabled"})

	if err := svc.SetScopeEnabled(ctx, ScopeWorkspace, "ws-a", true); err != nil {
		t.Fatalf("重新启用 workspace scope: %v", err)
	}
	hits, err = svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "数据库连接池"})
	if err != nil {
		t.Fatalf("Search after enable: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("重新启用后应能检索到 1 条，实际 %d", len(hits))
	}
}

// TestAugmentPromptKeepsUserText：注入是"参考数据块 + 原用户消息"，
// 不把记忆改写成系统提示，也不改动用户原文。
func TestAugmentPromptKeepsUserText(t *testing.T) {
	mc := MemoryContext{
		Entries: []ContextEntry{
			{MemoryID: "m1", Scope: ScopeWorkspace, WorkspaceID: "ws-a", SourceSessionID: "sess-1", Content: "数据库连接池上限为二十", Bytes: len("数据库连接池上限为二十")},
		},
		Text: "参考块",
	}
	got := AugmentPrompt("帮我加一个查询", mc)
	if !strings.HasSuffix(got, "帮我加一个查询") {
		t.Fatalf("用户原文必须完整保留在末尾，实际 %q", got)
	}
	if !strings.Contains(got, "不是系统指令") {
		t.Fatalf("注入块缺少数据来源/非系统指令声明：%q", got)
	}
	if !strings.Contains(got, "m1") || !strings.Contains(got, "sess-1") {
		t.Fatalf("注入块应带来源（id/session）：%q", got)
	}
	if empty := AugmentPrompt("原文", MemoryContext{}); empty != "原文" {
		t.Fatalf("无记忆时不应改写 prompt，实际 %q", empty)
	}
}

func idsOf(items []Memory) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		out = append(out, m.ID)
	}
	return out
}

func previewsOf(items []RankedMemory) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		r := []rune(m.Content)
		if len(r) > 12 {
			r = r[:12]
		}
		out = append(out, string(r))
	}
	return out
}

func containsIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}

func ranksOf(items []RankedMemory) []float64 {
	out := make([]float64, 0, len(items))
	for _, m := range items {
		out = append(out, m.Rank)
	}
	return out
}

func containsID(items []Memory, id string) bool {
	for _, m := range items {
		if m.ID == id {
			return true
		}
	}
	return false
}

func ptr(t time.Time) *time.Time { return &t }

// TestBuildContextRecallsFromSentence 覆盖 run 级注入的真实形态：
// 用户消息是一整句自然语言，记忆是另一句表述，严格短语不会命中。
// 允许用 trigram 召回，但必须仍然走 FTS5、仍然按 rank、仍然受预算约束。
func TestBuildContextRecallsFromSentence(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	pool := save(t, svc, MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Content: "数据库连接池上限为二十"})
	save(t, svc, MemoryDraft{Scope: ScopeUser, Content: "用户偏好：回答先给结论"})

	mc, err := svc.BuildContext(ctx, MemoryQuery{
		WorkspaceID: "ws-a",
		Text:        "帮我把数据库连接池调大一点",
	})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if len(mc.Entries) != 1 || mc.Entries[0].MemoryID != pool.ID {
		t.Fatalf("整句消息应召回相关的 1 条记忆，实际 %+v", mc.Entries)
	}

	// 不相关的消息不应把记忆硬塞进 prompt。
	none, err := svc.BuildContext(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "帮我写一份周报模板"})
	if err != nil {
		t.Fatalf("BuildContext 不相关: %v", err)
	}
	if len(none.Entries) != 0 {
		t.Fatalf("不相关消息不应注入记忆，实际 %+v", none.Entries)
	}
}
