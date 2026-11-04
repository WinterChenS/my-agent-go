package profile

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := config.NewStore(filepath.Join(t.TempDir(), "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	return New(store)
}

// seedResources 模拟"资源库已经定义了这些资源"（C02–C04 之后由各自的服务写入）。
// C01 阶段没有资源定义 UI，所以测试直接写用户层配置。
func seedResources(t *testing.T, svc *Service, body string) {
	t.Helper()
	if _, err := svc.store.CompareAndSwap(context.Background(), config.KeyUser, 0, []byte(body)); err != nil {
		t.Fatalf("写入用户层资源: %v", err)
	}
}

const resourcesBody = `{
  "schemaVersion": 1,
  "permissions": {"filesystem": "write", "network": true, "shell": true},
  "models": [{"id": "m-gpt"}, {"id": "m-local"}],
  "skills": [{"id": "s-review"}, {"id": "s-commit"}],
  "mcp": [{"id": "mcp-github"}]
}`

func TestSaveAndReload(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	saved, err := svc.Save(ctx, "p-default", "ws-1", 0, Profile{
		Name:       "日常",
		ModelRef:   "m-gpt",
		SkillIDs:   []string{"s-review"},
		MCPIDs:     []string{"mcp-github"},
		ToolPolicy: ToolPolicy{Filesystem: "read", Shell: true},
		MemoryPolicy: MemoryPolicy{
			Enabled:    true,
			MaxEntries: 200,
		},
	})
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	if saved.Revision != 1 {
		t.Fatalf("首次保存 revision 应为 1，实际 %d", saved.Revision)
	}

	got, err := svc.Get(ctx, "p-default")
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got.ModelRef != "m-gpt" || got.Name != "日常" {
		t.Fatalf("读回内容不一致: %+v", got)
	}
	if len(got.SkillIDs) != 1 || got.SkillIDs[0] != "s-review" {
		t.Fatalf("skillIDs 读回不一致: %v", got.SkillIDs)
	}
	if got.Revision != 1 {
		t.Fatalf("读回 revision 应为 1，实际 %d", got.Revision)
	}
}

// TestConflictDoesNotOverwrite 是 C01 的核心断言之一：
// 外部修改后，拿着旧 revision 的保存必须失败且不动源文件。
func TestConflictDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	first, err := svc.Save(ctx, "p1", "ws-1", 0, Profile{Name: "初版", ModelRef: "m-gpt"})
	if err != nil {
		t.Fatalf("首次保存: %v", err)
	}

	// 另一个 writer（比如另一个窗口）基于同一 revision 保存成功。
	if _, err := svc.Save(ctx, "p1", "ws-1", first.Revision, Profile{Name: "别人的改动", ModelRef: "m-local"}); err != nil {
		t.Fatalf("第二个 writer: %v", err)
	}

	// 第一个 writer 再保存：期望冲突。
	_, err = svc.Save(ctx, "p1", "ws-1", first.Revision, Profile{Name: "我的改动", ModelRef: "m-gpt"})
	if err == nil {
		t.Fatal("过期 revision 的保存必须失败")
	}
	if !IsConflict(err) {
		t.Fatalf("期望 CONFIG_CONFLICT，实际 %v", err)
	}

	// 源文件保持"别人的改动"，没有被覆盖。
	cur, err := svc.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	if cur.Name != "别人的改动" {
		t.Fatalf("冲突保存不得覆盖源文件，期望「别人的改动」，实际 %q", cur.Name)
	}
	if cur.Revision != 2 {
		t.Fatalf("冲突不得推进 revision，期望 2，实际 %d", cur.Revision)
	}
}

// TestRejectsMissingReferences 校验引用存在性。
func TestRejectsMissingReferences(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	cases := []struct {
		name string
		p    Profile
		code string
	}{
		{
			name: "模型不存在",
			p:    Profile{Name: "x", ModelRef: "m-不存在"},
			code: contracts.CodeDependencyMissing,
		},
		{
			name: "技能不存在",
			p:    Profile{Name: "x", ModelRef: "m-gpt", SkillIDs: []string{"s-review", "s-ghost"}},
			code: contracts.CodeDependencyMissing,
		},
		{
			name: "MCP 不存在",
			p:    Profile{Name: "x", ModelRef: "m-gpt", MCPIDs: []string{"mcp-ghost"}},
			code: contracts.CodeDependencyMissing,
		},
		{
			name: "modelRef 为空",
			p:    Profile{Name: "x"},
			code: contracts.CodeUnsupported,
		},
		{
			name: "filesystem 取值非法",
			p:    Profile{Name: "x", ModelRef: "m-gpt", ToolPolicy: ToolPolicy{Filesystem: "everything"}},
			code: contracts.CodeUnsupported,
		},
	}
	for _, c := range cases {
		_, err := svc.Save(ctx, contracts.ProfileID("p-"+c.name), "ws-1", 0, c.p)
		if err == nil {
			t.Fatalf("%s：应当拒绝保存", c.name)
		}
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) || appErr.Code != c.code {
			t.Fatalf("%s：期望 %s，实际 %v", c.name, c.code, err)
		}
	}

	// 被拒绝的保存不能在磁盘上留下半份文档。
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列举: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("被拒绝的保存不应落盘，实际留下 %d 条", len(list))
	}
}

// TestRejectsSecrets profile 里出现密钥字段必须拒绝，并给出替代做法。
func TestRejectsSecrets(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	// 借道 name 之外的结构化字段模拟误用：直接把 apiKey 塞进 memoryPolicy。
	// 这里用原始 body 走 buildLayerBody 的检查路径。
	p := Profile{Name: "x", ModelRef: "m-gpt"}
	raw := map[string]any{
		"id": p.ID, "name": p.Name, "modelRef": p.ModelRef,
		"toolPolicy": map[string]any{"filesystem": "none", "apiKey": "sk-live-should-not-be-here"},
	}
	if _, ok := findSecret(raw, "profile"); !ok {
		t.Fatal("深层 apiKey 应被识别为密钥字段")
	}

	// 正常保存不受影响：credentialRef 这种指针必须放行。
	if _, err := svc.Save(ctx, "p-ok", "ws-1", 0, Profile{Name: "ok", ModelRef: "m-gpt"}); err != nil {
		t.Fatalf("正常保存不应被密钥检查误伤: %v", err)
	}
}

// TestCompiledLayerIsConsumedByMerge 确认 profile 落盘形态真能被五层合并消费。
//
// 这条比"保存成功"重要，而且要同时守住两件相反的事：
//   - permissions 必须生效（否则保存了等于没设）
//   - 下层模型的连接参数必须不被清空（否则"选个模型"会把 provider 抹掉）
func TestCompiledLayerIsConsumedByMerge(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, `{
	  "schemaVersion": 1,
	  "permissions": {"filesystem": "workspace", "network": true, "shell": true},
	  "models": [{"id": "m-gpt", "provider": "openai", "baseUrl": "https://api.example.com/v1"}],
	  "skills": [{"id": "s-commit"}],
	  "mcp": [{"id": "mcp-github"}]
	}`)

	if _, err := svc.Save(ctx, "p1", "ws-1", 0, Profile{
		Name:       "受限",
		ModelRef:   "m-gpt",
		SkillIDs:   []string{"s-commit"},
		MCPIDs:     []string{"mcp-github"},
		ToolPolicy: ToolPolicy{Filesystem: "read", Network: false, Shell: false},
	}); err != nil {
		t.Fatalf("保存: %v", err)
	}

	eff, err := svc.store.Compile(ctx, config.CompileRequest{WorkspaceID: "ws-1", ProfileID: "p1"})
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	var values struct {
		Permissions struct {
			Filesystem string `json:"filesystem"`
			Network    bool   `json:"network"`
			Shell      bool   `json:"shell"`
		} `json:"permissions"`
		Models []struct {
			ID      string                     `json:"id"`
			Enabled *bool                      `json:"enabled"`
			Attrs   map[string]json.RawMessage `json:"attrs"`
		} `json:"models"`
	}
	if err := json.Unmarshal(eff.Values, &values); err != nil {
		t.Fatalf("解析有效配置: %v", err)
	}

	// 权限只能收紧：上层给 workspace/true/true，profile 收紧为 read/false/false。
	if values.Permissions.Filesystem != "read" {
		t.Fatalf("filesystem 应被收紧为 read，实际 %q", values.Permissions.Filesystem)
	}
	if values.Permissions.Network || values.Permissions.Shell {
		t.Fatalf("network/shell 应被收紧为 false，实际 %+v", values.Permissions)
	}

	// 关键：profile 选了 m-gpt，但不能把下层的 provider/baseUrl 冲掉。
	var found bool
	for _, m := range values.Models {
		if m.ID != "m-gpt" {
			continue
		}
		found = true
		if len(m.Attrs) == 0 {
			t.Fatalf("下层模型的连接参数被 profile 层清空了：%+v", m)
		}
		var provider string
		if raw, ok := m.Attrs["provider"]; ok {
			_ = json.Unmarshal(raw, &provider)
		}
		if provider != "openai" {
			t.Fatalf("下层的 provider 丢失，实际 %q（attrs=%v）", provider, m.Attrs)
		}
	}
	if !found {
		t.Fatal("下层模型没有进入有效配置")
	}
	if eff.AgentDir == "" {
		t.Fatal("有效配置必须给出独立 agentDir")
	}
}

// TestRejectsUnknownFilesystemValue 守住枚举一致性：
// 只认 Merge 认识的取值。"write" 看起来合理，但 Merge 会按 none 处理，
// 放过它等于让用户以为给了写权限而实际没有。
func TestRejectsUnknownFilesystemValue(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	for _, v := range []string{"write", "readwrite", "everything"} {
		_, err := svc.Save(ctx, "p1", "ws-1", 0, Profile{
			Name: "x", ModelRef: "m-gpt", ToolPolicy: ToolPolicy{Filesystem: v},
		})
		if err == nil {
			t.Fatalf("filesystem=%q 应被拒绝：Merge 不认识这个取值，会静默降级为 none", v)
		}
	}

	// 空值归一成 none，不算错误。
	saved, err := svc.Save(ctx, "p2", "ws-1", 0, Profile{Name: "x", ModelRef: "m-gpt"})
	if err != nil {
		t.Fatalf("空 filesystem 应归一为 none: %v", err)
	}
	if saved.ToolPolicy.Filesystem != "none" {
		t.Fatalf("空 filesystem 应归一为 none，实际 %q", saved.ToolPolicy.Filesystem)
	}
}

// TestCannotLoosenUpstreamPermissions 是上一条的反向确认：
// 上层本来就禁止 shell，profile 写 true 也不能放开。
func TestCannotLoosenUpstreamPermissions(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, `{
	  "schemaVersion": 1,
	  "permissions": {"filesystem": "none", "network": false, "shell": false},
	  "models": [{"id": "m-gpt"}]
	}`)

	if _, err := svc.Save(ctx, "p1", "ws-1", 0, Profile{
		Name: "想开权限", ModelRef: "m-gpt",
		ToolPolicy: ToolPolicy{Filesystem: "unrestricted", Network: true, Shell: true},
	}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	eff, err := svc.store.Compile(ctx, config.CompileRequest{WorkspaceID: "ws-1", ProfileID: "p1"})
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	var values struct {
		Permissions struct {
			Filesystem string `json:"filesystem"`
			Network    bool   `json:"network"`
			Shell      bool   `json:"shell"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(eff.Values, &values); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if values.Permissions.Shell || values.Permissions.Network {
		t.Fatalf("profile 不得放宽上层权限，实际 %+v", values.Permissions)
	}
	if values.Permissions.Filesystem != "none" {
		t.Fatalf("filesystem 不得从 none 放宽，实际 %q", values.Permissions.Filesystem)
	}
}

// TestAssertRunnable 覆盖"可保存但禁止启动缺失模型的 run"。
func TestAssertRunnable(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	if _, err := svc.Save(ctx, "p1", "ws-1", 0, Profile{Name: "ok", ModelRef: "m-gpt"}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	if err := svc.AssertRunnable(ctx, "p1", "ws-1"); err != nil {
		t.Fatalf("引用完整时应可运行: %v", err)
	}

	// 模型在保存之后被移除：profile 本身仍然合法，但不能再启动。
	seedResources2 := `{
	  "schemaVersion": 1,
	  "permissions": {"filesystem": "write"},
	  "models": [{"id": "m-local"}]
	}`
	cur, err := svc.store.Load(ctx, config.KeyUser)
	if err != nil {
		t.Fatalf("读取用户层: %v", err)
	}
	if _, err := svc.store.CompareAndSwap(ctx, config.KeyUser, cur.Revision, []byte(seedResources2)); err != nil {
		t.Fatalf("改写用户层: %v", err)
	}

	err = svc.AssertRunnable(ctx, "p1", "ws-1")
	if err == nil {
		t.Fatal("模型已被移除时必须拒绝启动")
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeDependencyMissing {
		t.Fatalf("期望 DEPENDENCY_MISSING，实际 %v", err)
	}
	if !strings.Contains(appErr.Message, "m-gpt") {
		t.Fatalf("错误必须点明缺失的模型，实际 %q", appErr.Message)
	}
}

// TestSaveIgnoresCallerRevisionField 确认 body 里的 Revision 不会冒充 expectedRevision。
func TestSaveIgnoresCallerRevisionField(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	seedResources(t, svc, resourcesBody)

	first, err := svc.Save(ctx, "p1", "ws-1", 0, Profile{Name: "a", ModelRef: "m-gpt"})
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	// 传入伪造的高 Revision，但 expectedRevision 仍是过期值。
	_, err = svc.Save(ctx, "p1", "ws-1", first.Revision-1, Profile{Name: "b", ModelRef: "m-gpt", Revision: 999})
	if !IsConflict(err) {
		t.Fatalf("过期的 expectedRevision 必须冲突，实际 %v", err)
	}
}
