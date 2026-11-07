package app

// U03 绑定层测试：单 Agent/Profile 的产品编辑面。
//
// 这里守的是四件"看起来做了、其实没做"的事：
//
//  1. 并发编辑只能暴露冲突，不能覆盖（UI 显示差异的来源是磁盘现状）；
//  2. 复制只复制引用与策略，不把秘密或过期 revision 一起带过去；
//  3. 删除只删 profile 这一份文档，被引用的模型/技能/MCP 必须还在；
//  4. 权限只能收紧：超过系统与工作区上限的草稿在写入前被结构化拒绝。
//
// 第五件事（重启后读回同样的值）用"同一个 root 再构造一个 Desktop"验证：
// 权威值在磁盘上，内存里的副本不算证据。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/profile"
)

// ---------------------------------------------------------------- helpers

const seededLayer = `{
  "schemaVersion": 1,
  "models": [{"id": "m-gpt", "providerId": "openai", "modelId": "gpt-4o"}],
  "skills": [{"id": "sk-a"}, {"id": "sk-b"}],
  "mcp": [{"id": "mcp-a"}],
  "permissions": {"filesystem": "workspace", "network": false, "shell": true}
}`

func testStore(t *testing.T, d *Desktop) *config.Store {
	t.Helper()
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		t.Fatalf("配置存储: %v", err)
	}
	return store
}

// seedLayer 直接写一层配置文档（模拟 default/user/workspace 层已有的设置）。
func seedLayer(t *testing.T, d *Desktop, key string, body string) uint64 {
	t.Helper()
	store := testStore(t, d)
	doc, err := store.CompareAndSwap(context.Background(), key, 0, json.RawMessage(body))
	if err != nil {
		t.Fatalf("写入配置层 %s: %v", key, err)
	}
	return doc.Revision
}

func saveOK(t *testing.T, res ProfileResult) profile.Profile {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("保存 profile 失败: %+v", res.Error)
	}
	return res.Item
}

func wantCode(t *testing.T, err *CallError, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望失败并返回 %s，实际成功", code)
	}
	if err.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, err.Code, err.Message)
	}
}

// restartDesktop 关闭当前实例并在同一个 root 上重建，模拟应用重启。
func restartDesktop(t *testing.T, d *Desktop, root string) *Desktop {
	t.Helper()
	d.Shutdown()
	again, err := New(root)
	if err != nil {
		t.Fatalf("重启后构造 Desktop: %v", err)
	}
	t.Cleanup(again.Shutdown)
	return again
}

func draft(over profile.Profile) profile.Profile {
	p := profile.Profile{
		Name:     "助手",
		ModelRef: "m-gpt",
		SkillIDs: []string{"sk-a"},
		MCPIDs:   []string{"mcp-a"},
		ToolPolicy: profile.ToolPolicy{
			Filesystem: "read",
			Network:    false,
			Shell:      false,
		},
		MemoryPolicy: profile.MemoryPolicy{Enabled: true, MaxEntries: 20},
	}
	if over.Name != "" {
		p.Name = over.Name
	}
	if over.ModelRef != "" {
		p.ModelRef = over.ModelRef
	}
	if over.SkillIDs != nil {
		p.SkillIDs = over.SkillIDs
	}
	if over.MCPIDs != nil {
		p.MCPIDs = over.MCPIDs
	}
	if over.ToolPolicy.Filesystem != "" {
		p.ToolPolicy = over.ToolPolicy
	}
	if over.MemoryPolicy.Enabled || over.MemoryPolicy.MaxEntries != 0 {
		p.MemoryPolicy = over.MemoryPolicy
	}
	return p
}

// ---------------------------------------------------------------- 1. CAS

// TestAgentProfileStaleRevisionSurfacesConflictAndOverwritesNothing：
// 并发编辑必须暴露 CONFIG_CONFLICT，且磁盘上的值仍是别人写的那份。
func TestAgentProfileStaleRevisionSurfacesConflictAndOverwritesNothing(t *testing.T) {
	d, root := newTestDesktop(t)
	seedLayer(t, d, config.KeyUser, seededLayer)

	created := saveOK(t, d.SaveAgentProfile("agent-cas", "ws-1", 0, draft(profile.Profile{Name: "初版"})))
	if created.Revision == 0 {
		t.Fatal("首次保存后 revision 必须 > 0")
	}

	other := draft(profile.Profile{Name: "别人的改动"})
	other.SkillIDs = []string{"sk-b"}
	if res := d.SaveAgentProfile("agent-cas", "ws-1", created.Revision, other); res.Error != nil {
		t.Fatalf("第二个写入者: %+v", res.Error)
	}

	mine := draft(profile.Profile{Name: "我的改动"})
	stale := d.SaveAgentProfile("agent-cas", "ws-1", created.Revision, mine)
	wantCode(t, stale.Error, contracts.CodeConfigConflict)

	// 冲突现场必须能拿到"磁盘现在是什么"，否则 UI 只能让用户盲选。
	conflict := d.ProfileConflict("agent-cas", mine)
	if conflict.Error != nil {
		t.Fatalf("取冲突差异: %+v", conflict.Error)
	}
	if conflict.Current.Name != "别人的改动" {
		t.Fatalf("冲突里的当前版本应是磁盘上的版本，实际 %q", conflict.Current.Name)
	}

	// 重启后读回：被拒绝的那次写入没有改动任何东西。
	restarted := restartDesktop(t, d, root)
	got := restarted.GetProfile("agent-cas")
	if got.Error != nil {
		t.Fatalf("重启后读取: %+v", got.Error)
	}
	if got.Item.Name != "别人的改动" {
		t.Fatalf("过期写入不得覆盖源文件，实际 %q", got.Item.Name)
	}
	if names := []string{got.Item.SkillIDs[0]}; names[0] != "sk-b" {
		t.Fatalf("过期写入不得覆盖引用列表，实际 %v", got.Item.SkillIDs)
	}
}

// ---------------------------------------------------------------- 2. 复制

// TestAgentProfileDuplicateCopiesReferencesNotSecrets：
// 复制的是引用与策略；文档里的秘密字段与 revision 都不跟过去。
func TestAgentProfileDuplicateCopiesReferencesNotSecrets(t *testing.T) {
	d, _ := newTestDesktop(t)
	seedLayer(t, d, config.KeyUser, seededLayer)

	// 直接写一份"曾经被手工塞过密钥"的源文档：复制路径不能把它带过去，
	// 也不能因为解不出来而失败（它是普通 JSON，只是不该有秘密）。
	const sentinel = "SENTINEL-DO-NOT-COPY"
	body := `{"schemaVersion":1,"profile":{"id":"agent-src","name":"源 profile",` +
		`"modelRef":"m-gpt","skillIDs":["sk-a"],"mcpIDs":["mcp-a"],` +
		`"toolPolicy":{"filesystem":"read","network":false,"shell":false},` +
		`"memoryPolicy":{"enabled":true,"maxEntries":20},` +
		`"apiKey":"` + sentinel + `","credentialRef":"cred-should-not-copy"}}`
	store := testStore(t, d)
	if _, err := store.CompareAndSwap(context.Background(), config.ProfileKey("agent-src"), 0, json.RawMessage(body)); err != nil {
		t.Fatalf("写源文档: %v", err)
	}

	dup := d.DuplicateAgentProfile("agent-src", "agent-dup", "ws-1")
	if dup.Error != nil {
		t.Fatalf("复制 profile: %+v", dup.Error)
	}
	if dup.Item.ID != "agent-dup" {
		t.Fatalf("复制后的 ID 应为新 ID，实际 %q", dup.Item.ID)
	}
	if dup.Item.Revision != 1 {
		t.Fatalf("复制是新建文档（revision 从 1 开始），不得继承源 revision，实际 %d", dup.Item.Revision)
	}
	if dup.Item.ModelRef != "m-gpt" || len(dup.Item.SkillIDs) != 1 || dup.Item.SkillIDs[0] != "sk-a" ||
		len(dup.Item.MCPIDs) != 1 || dup.Item.MCPIDs[0] != "mcp-a" {
		t.Fatalf("复制必须带上引用，实际 %+v", dup.Item)
	}
	if dup.Item.ToolPolicy.Filesystem != "read" || !dup.Item.MemoryPolicy.Enabled || dup.Item.MemoryPolicy.MaxEntries != 20 {
		t.Fatalf("复制必须带上策略，实际 %+v / %+v", dup.Item.ToolPolicy, dup.Item.MemoryPolicy)
	}

	raw, err := os.ReadFile(filepath.Join(d.root, "config", config.ProfileKey("agent-dup")+".json"))
	if err != nil {
		t.Fatalf("读复制后的文档: %v", err)
	}
	for _, banned := range []string{sentinel, "apiKey", "credentialRef"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("复制文档不得包含 %s：%s", banned, string(raw))
		}
	}

	// 源 profile 保持原样（复制不是剪切，也不是"顺手清理"）。
	src := d.GetProfile("agent-src")
	if src.Error != nil {
		t.Fatalf("读取源 profile: %+v", src.Error)
	}
	if src.Item.Name != "源 profile" {
		t.Fatalf("复制不得改动源 profile，实际 %q", src.Item.Name)
	}

	// 复制到已存在的 ID 必须是冲突，不是覆盖。
	again := d.DuplicateAgentProfile("agent-src", "agent-dup", "ws-1")
	wantCode(t, again.Error, contracts.CodeConfigConflict)
}

// ---------------------------------------------------------------- 3. 删除

// TestAgentProfileDeleteNeedsRevisionAndKeepsReferencedResources：
// 删除要确认（对齐 revision），且不连带删掉被引用的模型/技能/MCP。
func TestAgentProfileDeleteNeedsRevisionAndKeepsReferencedResources(t *testing.T) {
	d, _ := newTestDesktop(t)
	seedLayer(t, d, config.KeyUser, seededLayer)

	created := saveOK(t, d.SaveAgentProfile("agent-del", "ws-1", 0, draft(profile.Profile{Name: "待删除"})))

	// 过期 revision 的删除：必须是 CONFIG_CONFLICT，profile 还在。
	wantCode(t, d.DeleteAgentProfile("agent-del", created.Revision+7).Error, contracts.CodeConfigConflict)
	if got := d.GetProfile("agent-del"); got.Error != nil {
		t.Fatalf("过期删除不得真的删掉 profile: %+v", got.Error)
	}

	if res := d.DeleteAgentProfile("agent-del", created.Revision); res.Error != nil {
		t.Fatalf("按正确 revision 删除: %+v", res.Error)
	}
	if got := d.GetProfile("agent-del"); got.Error == nil {
		t.Fatal("删除后读取应为 NOT_FOUND")
	} else {
		wantCode(t, got.Error, contracts.CodeNotFound)
	}

	// 被引用的资源必须原样还在：用户层的模型/技能/MCP 定义不得被连带删除。
	doc, err := testStore(t, d).Load(context.Background(), config.KeyUser)
	if err != nil {
		t.Fatalf("读取用户层: %v", err)
	}
	var layer struct {
		Models []struct{ ID string } `json:"models"`
		Skills []struct{ ID string } `json:"skills"`
		MCP    []struct{ ID string } `json:"mcp"`
	}
	if err := json.Unmarshal(doc.Body, &layer); err != nil {
		t.Fatalf("解析用户层: %v", err)
	}
	if len(layer.Models) != 1 || layer.Models[0].ID != "m-gpt" ||
		len(layer.Skills) != 2 || len(layer.MCP) != 1 {
		t.Fatalf("删除 profile 不得动被引用的资源，实际 %+v", layer)
	}

	// 引用仍然可解析：同一批引用还能再存一份新 profile。
	if res := d.SaveAgentProfile("agent-after-del", "ws-1", 0, draft(profile.Profile{Name: "删后新建"})); res.Error != nil {
		t.Fatalf("被引用资源应仍然可解析: %+v", res.Error)
	}
}

// ---------------------------------------------------------------- 4. 权限上限

// TestAgentProfileRejectsWideningBeyondCeiling：
// 放宽权限必须被结构化拒绝，params 里带足够 UI 说清"哪一项、上限多少、由谁决定"。
func TestAgentProfileRejectsWideningBeyondCeiling(t *testing.T) {
	d, _ := newTestDesktop(t)
	// 工作区层把文件系统收到 read，网络关闭。
	seedLayer(t, d, config.WorkspaceKey("ws-1"), `{"schemaVersion":1,"permissions":{"filesystem":"read","network":false,"shell":false}}`)
	seedLayer(t, d, config.KeyUser, seededLayer)

	wider := draft(profile.Profile{Name: "越权草稿"})
	wider.ToolPolicy.Filesystem = "workspace" // read < workspace
	res := d.SaveAgentProfile("agent-wide", "ws-1", 0, wider)
	wantCode(t, res.Error, contracts.CodeInvalidInput)
	if res.Error.Params["field"] != "permissions.filesystem" {
		t.Fatalf("params 必须点名字段，实际 %+v", res.Error.Params)
	}
	if res.Error.Params["requested"] != "workspace" || res.Error.Params["ceiling"] != "read" {
		t.Fatalf("params 必须给出请求值与上限，实际 %+v", res.Error.Params)
	}
	if res.Error.Params["source"] != "workspace" {
		t.Fatalf("params 必须给出上限来源层，实际 %+v", res.Error.Params)
	}

	// 拒绝了就必须真的没写：不是"写了再收紧"。
	if got := d.GetProfile("agent-wide"); got.Error == nil {
		t.Fatalf("被拒的越权草稿不得落地: %+v", got.Item)
	}

	// shell 同理：上限来自设置（未授予）+ 层（显式 false）。
	withShell := draft(profile.Profile{Name: "开 shell"})
	withShell.ToolPolicy.Shell = true
	wantCode(t, d.SaveAgentProfile("agent-shell", "ws-1", 0, withShell).Error, contracts.CodeInvalidInput)

	// 网络在 V1 是系统级关闭（effectivePermissions 固定 false），
	// profile 打开它只会得到一个"看起来允许"的假开关。
	withNet := draft(profile.Profile{Name: "开网络"})
	withNet.ToolPolicy.Network = true
	wantCode(t, d.SaveAgentProfile("agent-net", "ws-1", 0, withNet).Error, contracts.CodeInvalidInput)

	// 收紧是允许的：read 之下还有 none。
	tighter := draft(profile.Profile{Name: "收紧"})
	tighter.ToolPolicy.Filesystem = "none"
	if res := d.SaveAgentProfile("agent-tighter", "ws-1", 0, tighter); res.Error != nil {
		t.Fatalf("收紧权限应被允许: %+v", res.Error)
	}

	// 没有任何层说话时上限来自系统（filesystem=workspace、shell=设置值）。
	empty, _ := newTestDesktop(t)
	unrestricted := draft(profile.Profile{Name: "不限"})
	unrestricted.ToolPolicy.Filesystem = "unrestricted"
	noLayer := empty.SaveAgentProfile("agent-nolayer", "ws-1", 0, unrestricted)
	wantCode(t, noLayer.Error, contracts.CodeInvalidInput)
	if noLayer.Error.Params["source"] != "system" {
		t.Fatalf("没有层说话时上限来源应为 system，实际 %+v", noLayer.Error.Params)
	}
	if noLayer.Error.Params["ceiling"] != "workspace" {
		t.Fatalf("系统上限应为 workspace，实际 %+v", noLayer.Error.Params)
	}
}

// TestAgentFilesystemOrderMatchesMerge 防止本地宽严表与 Merge 分叉。
//
// app 层需要判断"草稿是否比上限更宽"，而 config 的 rank 表是私有的。
// 本地这张表必须与 Merge 的收紧语义一致，否则会出现
// "UI 认为合规、Merge 却按更严处理"的静默降级。
func TestAgentFilesystemOrderMatchesMerge(t *testing.T) {
	all := []string{"none", "read", "workspace", "unrestricted"}
	for _, ceiling := range all {
		for _, want := range all {
			merged, err := config.Merge([]config.Layer{
				{Name: config.LayerDefault, Body: permissionsBody(ceiling, false, false)},
				{Name: config.LayerUser, Body: permissionsBody(want, false, false)},
			})
			if err != nil {
				t.Fatalf("Merge(%s,%s): %v", ceiling, want, err)
			}
			allowed := filesystemAtMost(want, ceiling)
			if allowed {
				// 不越界 = 草稿原样生效（可能更严）。
				if merged.Values.Permissions.Filesystem != want {
					t.Fatalf("filesystemAtMost(%s,%s)=true，但 Merge 得到 %s", want, ceiling, merged.Values.Permissions.Filesystem)
				}
				continue
			}
			// 越界 = Merge 会把它夹回上限（静默降级），正是我们拒绝的理由。
			if merged.Values.Permissions.Filesystem != ceiling {
				t.Fatalf("filesystemAtMost(%s,%s)=false，但 Merge 得到 %s（应夹回 %s）",
					want, ceiling, merged.Values.Permissions.Filesystem, ceiling)
			}
		}
	}
}

// ---------------------------------------------------------------- 5. 改名与重启

// TestAgentProfileRenameAndRestartReadBack：
// 改名走 CAS；重启后从磁盘读回同一组值（不只是内存里还记得）。
func TestAgentProfileRenameAndRestartReadBack(t *testing.T) {
	d, root := newTestDesktop(t)
	seedLayer(t, d, config.KeyUser, seededLayer)

	created := saveOK(t, d.SaveAgentProfile("agent-rn", "ws-1", 0, draft(profile.Profile{Name: "旧名"})))

	renamed := d.RenameAgentProfile("agent-rn", "ws-1", created.Revision, "新名")
	if renamed.Error != nil {
		t.Fatalf("改名: %+v", renamed.Error)
	}
	if renamed.Item.Name != "新名" {
		t.Fatalf("改名后应回读新名，实际 %q", renamed.Item.Name)
	}
	if renamed.Item.Revision == created.Revision {
		t.Fatal("改名必须是一次 CAS 写入（revision 递增）")
	}
	// 旧 revision 再改名必须冲突，不能覆盖。
	wantCode(t, d.RenameAgentProfile("agent-rn", "ws-1", created.Revision, "再改").Error, contracts.CodeConfigConflict)

	restarted := restartDesktop(t, d, root)
	state := restarted.AgentEditorState("agent-rn", "ws-1")
	if state.Error != nil {
		t.Fatalf("重启后读编辑器状态: %+v", state.Error)
	}
	if state.Profile.Name != "新名" {
		t.Fatalf("重启后应读回同样的名字，实际 %q", state.Profile.Name)
	}
	if state.Profile.Revision != renamed.Item.Revision {
		t.Fatalf("重启后 revision 应一致：%d ≠ %d", state.Profile.Revision, renamed.Item.Revision)
	}
	if state.Profile.ModelRef != "m-gpt" || state.Profile.ToolPolicy.Filesystem != "read" ||
		!state.Profile.MemoryPolicy.Enabled || state.Profile.MemoryPolicy.MaxEntries != 20 {
		t.Fatalf("重启后应读回同样的引用与策略，实际 %+v", state.Profile)
	}
	// 说明/system prompt/思考级别/工具白名单还没进领域模型：界面必须显示 unavailable，
	// 不能假装已保存（写了会被 JSON 反序列化静默丢掉）。
	for _, field := range []string{"description", "systemPrompt", "reasoningLevel", "toolIDs"} {
		if !containsString(state.UnavailableFields, field) {
			t.Fatalf("%s 应报为 unavailable（领域模型尚未承载），实际 %v", field, state.UnavailableFields)
		}
	}
}

// ---------------------------------------------------------------- 6. 来源

// TestAgentEditorStateReportsProvenance：每个值来自哪一层要能查，查不到就是 unavailable。
func TestAgentEditorStateReportsProvenance(t *testing.T) {
	d, _ := newTestDesktop(t)
	seedLayer(t, d, config.KeyUser, seededLayer)
	seedLayer(t, d, config.WorkspaceKey("ws-1"), `{"schemaVersion":1,"mcp":[{"id":"mcp-a"}],"permissions":{"filesystem":"read"}}`)

	saveOK(t, d.SaveAgentProfile("agent-src", "ws-1", 0, draft(profile.Profile{Name: "来源"})))

	state := d.AgentEditorState("agent-src", "ws-1")
	if state.Error != nil {
		t.Fatalf("读编辑器状态: %+v", state.Error)
	}
	want := map[string]string{
		"modelRef/m-gpt": "user",
		"skillIDs/sk-a":  "user",
		"mcpIDs/mcp-a":   "workspace",
	}
	for _, src := range state.Sources {
		key := src.Field + "/" + src.Value
		if expect, ok := want[key]; ok {
			if src.Source != expect {
				t.Fatalf("%s 的来源应为 %s，实际 %s", key, expect, src.Source)
			}
			delete(want, key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("来源缺项: %v（实际 %+v）", want, state.Sources)
	}

	// 上限也带来源：workspace 层的 read 比 user 层的 workspace 更严，source 指 workspace。
	if state.Ceiling.Filesystem != "read" {
		t.Fatalf("上限应为 read，实际 %q", state.Ceiling.Filesystem)
	}
	if got := state.Ceiling.Sources["permissions.filesystem"]; got != "workspace" {
		t.Fatalf("上限来源应为 workspace，实际 %q", got)
	}

	// 技能在保存后被上层删掉：值还在 profile 里，但来源已不可查 → unavailable。
	seed := testStore(t, d)
	doc, err := seed.Load(context.Background(), config.KeyUser)
	if err != nil {
		t.Fatalf("读用户层: %v", err)
	}
	if _, err := seed.CompareAndSwap(context.Background(), config.KeyUser, doc.Revision,
		json.RawMessage(`{"schemaVersion":1,"models":[{"id":"m-gpt"}],"skills":[{"id":"sk-b"}],"mcp":[{"id":"mcp-a"}]}`)); err != nil {
		t.Fatalf("改写用户层: %v", err)
	}
	after := d.AgentEditorState("agent-src", "ws-1")
	if after.Error != nil {
		t.Fatalf("改层后读编辑器状态: %+v", after.Error)
	}
	var sawUnavailable bool
	for _, src := range after.Sources {
		if src.Field == "skillIDs" && src.Value == "sk-a" {
			if src.Source != "unavailable" {
				t.Fatalf("已删除的技能来源应为 unavailable，实际 %q", src.Source)
			}
			sawUnavailable = true
		}
	}
	if !sawUnavailable {
		t.Fatalf("来源里应仍有 sk-a（标 unavailable），实际 %+v", after.Sources)
	}
	if len(after.Missing) == 0 || after.Missing[0].ID != "sk-a" {
		t.Fatalf("缺失引用要单独列出，实际 %+v", after.Missing)
	}
}

// TestAgentEditorStateReportsNotFound：不存在的 profile 必须结构化 NOT_FOUND。
func TestAgentEditorStateReportsNotFound(t *testing.T) {
	d, _ := newTestDesktop(t)
	wantCode(t, d.AgentEditorState("nope", "ws-1").Error, contracts.CodeNotFound)
	if res := d.DeleteAgentProfile("nope", 0); res.Error == nil {
		t.Fatal("删除不存在的 profile 应失败")
	} else {
		wantCode(t, res.Error, contracts.CodeNotFound)
	}
}

// ---------------------------------------------------------------- utils


// permissionsBody 构造只含权限字段的一层配置 body（供上限比较的一致性测试用）。
func permissionsBody(filesystem string, network, shell bool) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"permissions": map[string]any{
			"filesystem": filesystem,
			"network":    network,
			"shell":      shell,
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}
