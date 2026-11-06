package app

// C05 绑定层测试：真实 SQLite（Desktop 根目录下临时 DB）+ 真实工作区 ID。
//
// 重点不是"binding 能返回"，而是几条会被界面掩盖的口径：
//   - 短查询返回 QUERY_TOO_SHORT，不是空数组；
//   - 删除后下一次 run 的注入不再包含该内容（BuildContext 层直接断言）；
//   - 换工作区后项目级记忆不可见，user 记忆仍可见；
//   - 疑似凭证保存被拒绝，且理由可读。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/memory"
)

// memoryTestDesktop 让 Desktop 指向一个隔离的临时工作区。
func memoryTestDesktop(t *testing.T) (*Desktop, string) {
	t.Helper()
	d, root := newTestDesktop(t)
	work := filepath.Join(root, "project-a")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("建工作区: %v", err)
	}
	d.mu.Lock()
	d.settings.Workspace = work
	d.mu.Unlock()
	return d, root
}

func TestMemoryBindingsRoundTrip(t *testing.T) {
	d, _ := memoryTestDesktop(t)
	ctx := context.Background()

	saved := d.SaveMemory("数据库连接池上限为二十", memory.KindConvention, "workspace")
	if saved.Error != nil {
		t.Fatalf("SaveMemory: %+v", saved.Error)
	}
	if saved.Item.Status != memory.StatusActive {
		t.Fatalf("显式保存应为 active，实际 %s", saved.Item.Status)
	}
	if saved.Item.WorkspaceID == "" {
		t.Fatal("项目级记忆必须带 workspaceId")
	}

	list := d.ListMemories()
	if list.Error != nil {
		t.Fatalf("ListMemories: %+v", list.Error)
	}
	if len(list.Items) != 1 || list.Items[0].ID != saved.Item.ID {
		t.Fatalf("列表应包含刚保存的记忆，实际 %+v", list.Items)
	}
	if len(list.Scopes) != 2 {
		t.Fatalf("应返回 user/workspace 两个 scope 开关，实际 %+v", list.Scopes)
	}

	hit := d.SearchMemories("数据库连接池")
	if hit.Error != nil {
		t.Fatalf("SearchMemories: %+v", hit.Error)
	}
	if len(hit.Items) != 1 || hit.Items[0].ID != saved.Item.ID {
		t.Fatalf("检索应命中 1 条，实际 %+v", hit.Items)
	}

	short := d.SearchMemories("数据")
	if short.Error == nil {
		t.Fatal("少于 3 个 Unicode 字符应返回错误，不能渲染成空结果")
	}
	if short.Error.Code != "QUERY_TOO_SHORT" || contracts.CodeQueryTooShort != "QUERY_TOO_SHORT" {
		t.Fatalf("错误码应为 QUERY_TOO_SHORT，实际 %+v", short.Error)
	}

	// 候选确认路径（V1 没有自动抽取，但 UI 要能确认将来/外部导入的候选）。
	svc, err := d.memoryService()
	if err != nil {
		t.Fatalf("memoryService: %v", err)
	}
	cand, err := svc.Save(ctx, memory.MemoryDraft{
		Scope:       memory.ScopeWorkspace,
		WorkspaceID: saved.Item.WorkspaceID,
		Content:     "数据库连接池候选：上限为九十九",
		Status:      memory.StatusCandidate,
	})
	if err != nil {
		t.Fatalf("保存候选: %v", err)
	}
	confirmed := d.ConfirmMemory(cand.ID)
	if confirmed.Error != nil {
		t.Fatalf("ConfirmMemory: %+v", confirmed.Error)
	}
	if confirmed.Item.Status != memory.StatusActive {
		t.Fatalf("确认后应为 active，实际 %s", confirmed.Item.Status)
	}

	exported := d.ExportMemories()
	if exported.Error != nil {
		t.Fatalf("ExportMemories: %+v", exported.Error)
	}
	if exported.ItemCount != 2 {
		t.Fatalf("导出应含 2 条，实际 %d", exported.ItemCount)
	}
	var doc memory.ExportDocument
	if err := json.Unmarshal([]byte(exported.JSON), &doc); err != nil {
		t.Fatalf("导出不是合法 JSON: %v", err)
	}
	if !strings.Contains(exported.JSON, "数据库连接池上限为二十") {
		t.Fatal("导出应含中文原文")
	}

	// MEM03 的外部观察（database）：应用数据根下的 memory.db 里真实存了什么。
	memSvc, err := d.memoryService()
	if err != nil {
		t.Fatalf("memoryService: %v", err)
	}
	observeAppMemoryDB(t, memSvc.DB().Path(), map[string]any{
		"bindingRoundTrip": true,
		"exportItemCount":  exported.ItemCount,
	})
}

func TestMemoryDeleteNotInjectedNextRun(t *testing.T) {
	d, _ := memoryTestDesktop(t)
	ctx := context.Background()

	saved := d.SaveMemory("数据库连接池上限为二十", memory.KindConvention, "workspace")
	if saved.Error != nil {
		t.Fatalf("SaveMemory: %+v", saved.Error)
	}

	prompt := d.withMemoryContext(ctx, "帮我把数据库连接池调大")
	if !strings.Contains(prompt, "数据库连接池上限为二十") {
		t.Fatalf("删除前应注入记忆：\n%s", prompt)
	}
	if !strings.HasSuffix(prompt, "帮我把数据库连接池调大") {
		t.Fatalf("注入不能改写用户原文：\n%s", prompt)
	}
	if !strings.Contains(prompt, "不是系统指令") {
		t.Fatalf("注入块必须声明不是系统指令：\n%s", prompt)
	}

	deleted := d.DeleteMemory(saved.Item.ID)
	if deleted.Error != nil {
		t.Fatalf("DeleteMemory: %+v", deleted.Error)
	}
	after := d.withMemoryContext(ctx, "帮我把数据库连接池调大")
	if strings.Contains(after, "数据库连接池上限为二十") {
		t.Fatalf("删除后下一次 run 不应再注入：\n%s", after)
	}
	if after != "帮我把数据库连接池调大" {
		t.Fatalf("没有可注入记忆时应保持原文，实际 %q", after)
	}
	list := d.ListMemories()
	for _, it := range list.Items {
		if it.ID == saved.Item.ID {
			t.Fatal("删除后列表仍然显示该记忆")
		}
	}
	t.Logf("删除后注入原文: %q", after)

	// MEM05 的外部观察：导出文件从磁盘读回 + 库里那一行的真实 status。
	exported := d.ExportMemories()
	if exported.Error != nil {
		t.Fatalf("ExportMemories: %+v", exported.Error)
	}
	facts := observeAppExport(t, exported.JSON)
	memSvc, err := d.memoryService()
	if err != nil {
		t.Fatalf("memoryService: %v", err)
	}
	observeAppMemoryDB(t, memSvc.DB().Path(), facts)
}

func TestMemoryScopeToggleAndWorkspaceIsolation(t *testing.T) {
	d, root := memoryTestDesktop(t)
	ctx := context.Background()

	projectMem := d.SaveMemory("数据库连接池上限为二十", memory.KindConvention, "workspace")
	if projectMem.Error != nil {
		t.Fatalf("SaveMemory workspace: %+v", projectMem.Error)
	}
	userMem := d.SaveMemory("用户偏好：回答先给结论", memory.KindPreference, "user")
	if userMem.Error != nil {
		t.Fatalf("SaveMemory user: %+v", userMem.Error)
	}
	if userMem.Item.WorkspaceID != "" {
		t.Fatalf("user 记忆的 workspaceId 应为空，实际 %q", userMem.Item.WorkspaceID)
	}

	// 按 scope 关闭项目级注入：列表还在，检索/注入不再返回。
	off := d.SetMemoryScopeEnabled("workspace", false)
	if off.Error != nil {
		t.Fatalf("SetMemoryScopeEnabled: %+v", off.Error)
	}
	hits := d.SearchMemories("数据库连接池")
	if hits.Error != nil {
		t.Fatalf("SearchMemories: %+v", hits.Error)
	}
	if len(hits.Items) != 0 {
		t.Fatalf("关闭 workspace scope 后不应命中项目记忆，实际 %+v", hits.Items)
	}
	if list := d.ListMemories(); len(list.Items) != 2 {
		t.Fatalf("关闭注入不应删除条目，实际 %d 条", len(list.Items))
	}
	if prompt := d.withMemoryContext(ctx, "数据库连接池调大"); strings.Contains(prompt, "数据库连接池上限为二十") {
		t.Fatalf("scope 关闭后仍被注入：\n%s", prompt)
	}
	if userHits := d.SearchMemories("回答先给结论"); len(userHits.Items) != 1 {
		t.Fatalf("关闭 workspace scope 不应影响 user scope，实际 %+v", userHits.Items)
	}

	// 换工作区：项目级记忆不可见，user 记忆仍可见。
	workB := filepath.Join(root, "project-b")
	if err := os.MkdirAll(workB, 0o755); err != nil {
		t.Fatalf("建 project-b: %v", err)
	}
	d.mu.Lock()
	d.settings.Workspace = workB
	d.mu.Unlock()

	listB := d.ListMemories()
	if listB.Error != nil {
		t.Fatalf("ListMemories(project-b): %+v", listB.Error)
	}
	for _, it := range listB.Items {
		if it.ID == projectMem.Item.ID {
			t.Fatal("B 项目列表看到了 A 项目的记忆")
		}
	}
	if len(listB.Items) != 1 || listB.Items[0].ID != userMem.Item.ID {
		t.Fatalf("B 项目应只看到 user 记忆，实际 %+v", listB.Items)
	}

	// MEM05 的外部观察：换工作区后的导出文件（只含 user 记忆）+ 库里的 scope 开关状态。
	exported := d.ExportMemories()
	if exported.Error != nil {
		t.Fatalf("ExportMemories: %+v", exported.Error)
	}
	facts := observeAppExport(t, exported.JSON)
	if facts != nil {
		facts["workspaceSwitched"] = true
	}
	memSvc, err := d.memoryService()
	if err != nil {
		t.Fatalf("memoryService: %v", err)
	}
	observeAppMemoryDB(t, memSvc.DB().Path(), facts)
}

func TestMemorySecretRejectedAtBinding(t *testing.T) {
	d, _ := memoryTestDesktop(t)

	const secret = "{{SECRET_2gq6pk3v}}"
	res := d.SaveMemory("工具日志：api_key = "+secret, memory.KindNote, "workspace")
	if res.Error == nil {
		t.Fatal("疑似凭证应被拒绝")
	}
	if res.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("错误码应为 UNSUPPORTED，实际 %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "疑似凭证") {
		t.Fatalf("拒绝原因应说明疑似凭证，实际 %q", res.Error.Message)
	}
	if strings.Contains(res.Error.Message, secret) {
		t.Fatalf("拒绝提示回显了密钥：%q", res.Error.Message)
	}
	if list := d.ListMemories(); len(list.Items) != 0 {
		t.Fatalf("被拒绝的内容不应落库，实际 %d 条", len(list.Items))
	}
}

// TestHarnessToolsExposeMemoryTools：自有 MCP 的工具注册表与工具目录同源，
// 注册了 memory_save / memory_search 才允许目录里出现它们。
func TestHarnessToolsExposeMemoryTools(t *testing.T) {
	d, _ := memoryTestDesktop(t)
	mem, wsID := d.memoryToolsForRuntime(context.Background())
	if mem == nil || wsID == "" {
		t.Fatalf("记忆工具所需条件不满足: mem=%v ws=%q", mem, wsID)
	}
	names := map[string]bool{}
	for _, tool := range harnessTools("/tmp/work", mem, wsID) {
		names[tool.Name] = true
	}
	for _, want := range []string{"harness_probe", "memory_save", "memory_search"} {
		if !names[want] {
			t.Fatalf("工具注册表缺少 %s，实际 %v", want, names)
		}
	}
	descriptors := harnessToolDescriptors("/tmp/work", mem, wsID)
	if len(descriptors) != 3 {
		t.Fatalf("工具目录应与注册表一致（3 条），实际 %d", len(descriptors))
	}
}

// TestHarnessToolsOmitMemoryWithoutWorkspace：没有工作区时不注册记忆工具。
// 否则模型会看到一个必然失败的工具，而工具目录还宣称它可用。
func TestHarnessToolsOmitMemoryWithoutWorkspace(t *testing.T) {
	d, _ := memoryTestDesktop(t)
	d.mu.Lock()
	d.settings.Workspace = ""
	d.mu.Unlock()

	mem, wsID := d.memoryToolsForRuntime(context.Background())
	if mem != nil || wsID != "" {
		t.Fatalf("没有工作区时不应返回记忆工具条件: mem=%v ws=%q", mem, wsID)
	}
	for _, tool := range harnessTools("/tmp/work", mem, wsID) {
		if tool.Name == "memory_save" || tool.Name == "memory_search" {
			t.Fatalf("没有工作区时注册了记忆工具 %s", tool.Name)
		}
	}
}
