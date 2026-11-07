package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"my-agent-go/internal/contracts"
)

func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func body(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(raw)) {
		t.Fatalf("测试用 body 不是合法 JSON: %s", raw)
	}
	return json.RawMessage(raw)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestCASRejectsExternalEdit 覆盖 CFG02：同 revision 的两个 writer 只有一个成功；
// 外部编辑产生 ConfigConflict；失败路径不得覆盖源文件。
func TestCASRejectsExternalEdit(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)

	first := body(t, `{"schemaVersion":1,"permissions":{"filesystem":"read"}}`)
	doc, err := s.CompareAndSwap(ctx, KeyUser, 0, first)
	if err != nil {
		t.Fatalf("首次 CAS: %v", err)
	}
	if doc.Revision != 1 {
		t.Fatalf("首次写入 revision 应为 1，实际 %d", doc.Revision)
	}

	// 并发 writer：两人都拿 revision=1 写，只能有一个成功。
	var wg sync.WaitGroup
	wg.Add(2)
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.CompareAndSwap(ctx, KeyUser, 1, body(t, `{"schemaVersion":1,"permissions":{"filesystem":"none"}}`))
			results[i] = err
		}(i)
	}
	wg.Wait()
	okCount, conflictCount := 0, 0
	for _, err := range results {
		if err == nil {
			okCount++
			continue
		}
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) || appErr.Code != contracts.CodeConfigConflict {
			t.Fatalf("并发失败方应返回 ConfigConflict，实际 %v", err)
		}
		conflictCount++
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("同 revision 两 writer 应恰好一个成功，实际成功 %d 冲突 %d", okCount, conflictCount)
	}

	// 过期 revision 写入：必须被拒。
	_, err = s.CompareAndSwap(ctx, KeyUser, 1, body(t, `{"schemaVersion":1}`))
	wantCode(t, err, contracts.CodeConfigConflict)

	// 外部编辑：绕过存储直接改 body，保留原 checksum。
	path := filepath.Join(s.Root(), KeyUser+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置文件: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析信封: %v", err)
	}
	env["body"] = json.RawMessage(`{"schemaVersion":1,"permissions":{"filesystem":"unrestricted"}}`)
	tampered, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("序列化篡改内容: %v", err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("写入篡改内容: %v", err)
	}

	// oracle：外部编辑后 Load 与 CAS 都不得把它当有效配置，且源文件不被覆盖。
	_, err = s.Load(ctx, KeyUser)
	wantCode(t, err, contracts.CodeConfigConflict)
	_, err = s.CompareAndSwap(ctx, KeyUser, 2, body(t, `{"schemaVersion":1}`))
	wantCode(t, err, contracts.CodeConfigConflict)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("复读配置文件: %v", err)
	}
	if string(after) != string(tampered) {
		t.Fatalf("冲突路径不得覆盖源文件；期望保持篡改内容不变")
	}

	// CFG02 的外部观察：冲突之后源文件在磁盘上到底是什么（哈希/revision）。
	observeConfigTree(t, s.Root(), map[string]any{"conflictLeftSourceUntouched": true})
}

// TestCASRejectsInvalidBody 覆盖 schemaVersion 纪律：非法 body 不落盘。
func TestCASRejectsInvalidBody(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)
	cases := map[string]string{
		"空":               ``,
		"非 JSON":          `not-json`,
		"缺 schemaVersion": `{"permissions":{}}`,
		"版本不支持":           `{"schemaVersion":2}`,
	}
	for name, raw := range cases {
		_, err := s.CompareAndSwap(ctx, KeyUser, 0, json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s：非法 body 不应写入成功", name)
		}
		wantCode(t, err, contracts.CodeUnsupported)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), KeyUser+".json")); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的写入不应留下配置文件（err=%v）", err)
	}
}

// TestLoadRecoversFromInterruptedWrite 覆盖 CFG04：原子写入中断后，
// 主文件仍是上一个完整版本，残留的 .tmp 残片不得被当成有效配置。
func TestLoadRecoversFromInterruptedWrite(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)

	if _, err := s.CompareAndSwap(ctx, KeyUser, 0, body(t, `{"schemaVersion":1,"permissions":{"filesystem":"read"}}`)); err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	path := filepath.Join(s.Root(), KeyUser+".json")

	// 模拟断电：temp 文件写到一半，进程没来得及 rename。
	if err := os.WriteFile(path+".tmp", []byte(`{"schemaVersion":1,"revision":2,"body":{"schemaVer`), 0o600); err != nil {
		t.Fatalf("写临时残片: %v", err)
	}

	doc, err := s.Load(ctx, KeyUser)
	if err != nil {
		t.Fatalf("残留 .tmp 不应影响 Load: %v", err)
	}
	if doc.Revision != 1 {
		t.Fatalf("应恢复到 revision=1 的完整版本，实际 %d", doc.Revision)
	}
	var got struct {
		Permissions struct {
			Filesystem string `json:"filesystem"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(doc.Body, &got); err != nil {
		t.Fatalf("解析恢复内容: %v", err)
	}
	if got.Permissions.Filesystem != "read" {
		t.Fatalf("恢复内容错误: %s", doc.Body)
	}

	// 残片存在时仍可继续正常写入（下一次 CAS 覆盖自己的 tmp）。
	if _, err := s.CompareAndSwap(ctx, KeyUser, 1, body(t, `{"schemaVersion":1,"permissions":{"filesystem":"workspace"}}`)); err != nil {
		t.Fatalf("残留 .tmp 后 CAS 应可继续: %v", err)
	}

	// CFG04 的外部观察：断电残片与恢复后的有效配置都在磁盘上。
	observeConfigTree(t, s.Root(), map[string]any{"recoveredFromTornWrite": true})
}

// TestMergePrecedenceAndDisableByID 覆盖 CFG01：五层优先级、数组按 ID 整项覆盖、
// 显式 enabled=false 生效、来源可查询。
func TestMergePrecedenceAndDisableByID(t *testing.T) {
	layers := []Layer{
		{Name: LayerDefault, Body: body(t, `{
			"schemaVersion":1,
			"permissions":{"filesystem":"unrestricted","network":true,"shell":true},
			"credentialRef":"default:key",
			"models":[{"id":"m-a","model":"base-a","provider":"openai"},{"id":"m-b","model":"base-b"}],
			"skills":[{"id":"s-1","path":"/skills/1"}]}`)},
		{Name: LayerUser, Body: body(t, `{
			"schemaVersion":1,
			"credentialRef":"user:key",
			"models":[{"id":"m-a","model":"user-a"}],
			"skills":[{"id":"s-2","path":"/skills/2"}]}`)},
		{Name: LayerWorkspace, Body: body(t, `{
			"schemaVersion":1,
			"skills":[{"id":"s-1","enabled":false}]}`)},
		{Name: LayerProfile, Body: body(t, `{
			"schemaVersion":1,
			"models":[{"id":"m-c","model":"profile-c"}]}`)},
		{Name: LayerSession, Body: body(t, `{
			"schemaVersion":1,
			"models":[{"id":"m-a","model":"session-a","enabled":false}]}`)},
	}
	merged, err := Merge(layers)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// 同 ID 高层覆盖整项：m-a 只剩会话层字段，用户层的 model=user-a 不得残留。
	var foundA *Resource
	for i := range merged.Values.Models {
		if merged.Values.Models[i].ID == "m-a" {
			foundA = &merged.Values.Models[i]
		}
	}
	if foundA == nil {
		t.Fatalf("m-a 丢失")
	}
	if got := string(foundA.Attrs["model"]); got != `"session-a"` {
		t.Fatalf("m-a 应按会话层整项覆盖，实际 model=%s（整项覆盖不保留低层字段）", got)
	}
	if _, hasProvider := foundA.Attrs["provider"]; hasProvider {
		t.Fatalf("整项覆盖后不应残留低层 provider 字段，实际 %v", foundA.Attrs)
	}
	if foundA.Enabled == nil || *foundA.Enabled {
		t.Fatalf("会话层显式 enabled=false 应生效，实际 %+v", foundA.Enabled)
	}

	// 显式禁用必须保留在列表里（不能用"消失"表达禁用）。
	var s1 *Resource
	for i := range merged.Values.Skills {
		if merged.Values.Skills[i].ID == "s-1" {
			s1 = &merged.Values.Skills[i]
		}
	}
	if s1 == nil {
		t.Fatalf("s-1 被禁用后仍应出现在列表中（显式 enabled=false）")
	}
	if s1.Enabled == nil || *s1.Enabled {
		t.Fatalf("s-1 应为 enabled=false，实际 %+v", s1.Enabled)
	}
	// s-2 来自用户层，保留。
	if len(merged.Values.Skills) != 2 {
		t.Fatalf("skills 数量应为 2，实际 %d", len(merged.Values.Skills))
	}

	// 来源可查询。
	for path, want := range map[string]string{
		"models.m-a":    "session",
		"skills.s-1":    "workspace",
		"skills.s-2":    "user",
		"models.m-c":    "profile",
		"models.m-b":    "default",
		"credentialRef": "user",
	} {
		if got := merged.Provenance[path]; got != want {
			t.Fatalf("provenance[%s] 应为 %s，实际 %q", path, want, got)
		}
	}

	// 凭证：用户层覆盖默认层，项目层不得替换。
	if merged.Values.CredentialRef != "user:key" {
		t.Fatalf("credentialRef 应为 user:key，实际 %q", merged.Values.CredentialRef)
	}
}

// TestPermissionsCannotBeWidenedByProjectLayer 覆盖 CFG01 的权限收紧规则：
// 项目层把 filesystem 设为 unrestricted 也不能扩大用户层已收紧的权限。
func TestPermissionsCannotBeWidenedByProjectLayer(t *testing.T) {
	merged, err := Merge([]Layer{
		{Name: LayerDefault, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"unrestricted","network":true,"shell":true}}`)},
		{Name: LayerUser, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"read","network":true,"shell":false}}`)},
		{Name: LayerWorkspace, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"unrestricted","network":true,"shell":true}}`)},
		{Name: LayerSession, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"unrestricted","network":true,"shell":true}}`)},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Values.Permissions.Filesystem != "read" {
		t.Fatalf("项目层不得放宽 filesystem，期望 read 实际 %s", merged.Values.Permissions.Filesystem)
	}
	if merged.Values.Permissions.Shell {
		t.Fatalf("项目层不得把 shell 从 false 放宽为 true")
	}
	if !merged.Values.Permissions.Network {
		t.Fatalf("network 未被收紧时应保持 true")
	}
	if len(merged.Warnings) != 0 {
		t.Fatalf("权限收紧是规则内行为，不应产生 warning: %v", merged.Warnings)
	}
}

// TestCredentialRefCannotBeReplacedByProjectLayer 覆盖 CFG01：项目层设置 credentialRef 被忽略并留痕。
func TestCredentialRefCannotBeReplacedByProjectLayer(t *testing.T) {
	merged, err := Merge([]Layer{
		{Name: LayerDefault, Body: body(t, `{"schemaVersion":1,"credentialRef":"default:key"}`)},
		{Name: LayerWorkspace, Body: body(t, `{"schemaVersion":1,"credentialRef":"project:evil"}`)},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Values.CredentialRef != "default:key" {
		t.Fatalf("项目层不得替换 credentialRef，实际 %q", merged.Values.CredentialRef)
	}
	if len(merged.Warnings) != 1 {
		t.Fatalf("项目层设置 credentialRef 必须留痕，实际 warnings=%v", merged.Warnings)
	}
}

// TestCompileIsolatesAgentDir 覆盖 CFG04：编译结果使用独立 agentDir，
// 不触碰用户真实 ~/.pi 目录。
func TestCompileIsolatesAgentDir(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)

	if _, err := s.CompareAndSwap(ctx, KeyDefault, 0, body(t, `{"schemaVersion":1,"permissions":{"filesystem":"workspace"}}`)); err != nil {
		t.Fatalf("写默认层: %v", err)
	}
	if _, err := s.CompareAndSwap(ctx, WorkspaceKey("ws/1"), 0, body(t, `{"schemaVersion":1,"skills":[{"id":"s","enabled":true}]}`)); err != nil {
		t.Fatalf("写工作区层: %v", err)
	}

	eff, err := s.Compile(ctx, CompileRequest{WorkspaceID: "ws/1", ProfileID: "p1"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if eff.Revision != 1 {
		t.Fatalf("有效 revision 应为 1，实际 %d", eff.Revision)
	}
	if eff.Hash == "" || eff.Hash[:7] != "sha256:" {
		t.Fatalf("hash 缺失或格式错误: %q", eff.Hash)
	}
	if !filepath.IsAbs(eff.AgentDir) {
		t.Fatalf("agentDir 应为绝对路径，实际 %q", eff.AgentDir)
	}
	if rel, err := filepath.Rel(s.Root(), eff.AgentDir); err != nil || rel == "." || rel[:2] == ".." {
		t.Fatalf("agentDir 必须位于配置存储根目录下，实际 %q", eff.AgentDir)
	}
	// oracle：用户 HOME 下不得出现 .pi 目录（编译本身不得写真实 Pi 配置）。
	if _, err := os.Stat(filepath.Join(fakeHome, ".pi")); !os.IsNotExist(err) {
		t.Fatalf("编译过程不得创建/改写用户 ~/.pi 目录（err=%v）", err)
	}
	// 非 ASCII / 非法字符的 workspace ID 不得产生路径分隔符。
	if filepath.Dir(eff.AgentDir) != filepath.Join(s.Root(), "agents") {
		t.Fatalf("agentDir 目录层级错误: %q", eff.AgentDir)
	}

	// CFG04 的外部观察：伪造 HOME 与配置根的磁盘事实。
	observeHomeTree(t, fakeHome, map[string]any{
		"agentDirUnderConfigRoot": strings.HasPrefix(filepath.ToSlash(eff.AgentDir), filepath.ToSlash(s.Root())),
	})
	observeConfigTree(t, s.Root(), map[string]any{"compiledWorkspace": "ws/1"})
}

// TestCompileSessionOverridesWins 覆盖会话覆盖层：优先级最高，且不落盘。
func TestCompileSessionOverridesWins(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)
	if _, err := s.CompareAndSwap(ctx, KeyUser, 0, body(t, `{"schemaVersion":1,"models":[{"id":"m","model":"user"}]}`)); err != nil {
		t.Fatalf("写用户层: %v", err)
	}
	eff, err := s.Compile(ctx, CompileRequest{
		WorkspaceID:      "ws",
		ProfileID:        "p",
		SessionOverrides: body(t, `{"schemaVersion":1,"models":[{"id":"m","model":"session"}]}`),
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var values Values
	if err := json.Unmarshal(eff.Values, &values); err != nil {
		t.Fatalf("解析有效配置: %v", err)
	}
	if len(values.Models) != 1 || string(values.Models[0].Attrs["model"]) != `"session"` {
		t.Fatalf("会话覆盖应生效，实际 %s", eff.Values)
	}
	if eff.Provenance["models.m"] != "session" {
		t.Fatalf("来源应为 session，实际 %q", eff.Provenance["models.m"])
	}
	// 会话覆盖不得落盘。
	if _, err := os.Stat(filepath.Join(s.Root(), "config-session.json")); !os.IsNotExist(err) {
		t.Fatalf("会话覆盖不应写入磁盘（err=%v）", err)
	}
}

// TestPermissionsBaselineWithoutDefaultLayer 守住一个曾经真实存在的缺陷：
// 当 default 层没有写 permissions 时，第一个写 permissions 的层会被
// 当成"当前是 none"来收紧，导致：
//   - filesystem 无法从 none 升到 workspace
//   - network/shell 的 true 被 false && true 永久锁死
//
// 结果是用户在设置里打开的开关永远不生效，而且没有任何报错。
func TestPermissionsBaselineWithoutDefaultLayer(t *testing.T) {
	// default 层只给模型，不谈权限。
	merged, err := Merge([]Layer{
		{Name: LayerDefault, Body: body(t, `{"schemaVersion":1,"models":[{"id":"m1"}]}`)},
		{Name: LayerUser, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"workspace","network":true,"shell":true}}`)},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Values.Permissions.Filesystem != "workspace" {
		t.Fatalf("default 层未设 filesystem 时，用户层的 workspace 必须生效，实际 %q", merged.Values.Permissions.Filesystem)
	}
	if !merged.Values.Permissions.Network || !merged.Values.Permissions.Shell {
		t.Fatalf("default 层未设时，用户层的 true 必须生效，实际 %+v", merged.Values.Permissions)
	}
	if merged.Provenance["permissions.filesystem"] != string(LayerUser) {
		t.Fatalf("来源应指向用户层，实际 %q", merged.Provenance["permissions.filesystem"])
	}

	// 上层已开启后，profile 层仍只能收紧。
	narrowed, err := Merge([]Layer{
		{Name: LayerUser, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"workspace","network":true,"shell":true}}`)},
		{Name: LayerProfile, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"read","network":false,"shell":false}}`)},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if narrowed.Values.Permissions.Filesystem != "read" || narrowed.Values.Permissions.Network || narrowed.Values.Permissions.Shell {
		t.Fatalf("profile 层应能把权限收紧为 read/false/false，实际 %+v", narrowed.Values.Permissions)
	}
	if narrowed.Provenance["permissions.network"] != string(LayerProfile) {
		t.Fatalf("收紧后来源应指向实际收紧的那一层，实际 %q", narrowed.Provenance["permissions.network"])
	}

	// 反过来：上层收紧后，下层不能放宽。
	widened, err := Merge([]Layer{
		{Name: LayerUser, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"read","network":false,"shell":false}}`)},
		{Name: LayerProfile, Body: body(t, `{"schemaVersion":1,"permissions":{"filesystem":"unrestricted","network":true,"shell":true}}`)},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if widened.Values.Permissions.Filesystem != "read" || widened.Values.Permissions.Network || widened.Values.Permissions.Shell {
		t.Fatalf("profile 层不得放宽权限，实际 %+v", widened.Values.Permissions)
	}
	if widened.Provenance["permissions.filesystem"] != string(LayerUser) {
		t.Fatalf("未放宽时来源应保持用户层，实际 %q", widened.Provenance["permissions.filesystem"])
	}

	// 没有任何层提到权限时仍是安全默认 none。
	bare, err := Merge([]Layer{{Name: LayerDefault, Body: body(t, `{"schemaVersion":1}`)}})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if bare.Values.Permissions.Filesystem != "none" || bare.Values.Permissions.Network || bare.Values.Permissions.Shell {
		t.Fatalf("无任何权限设置时应为 none/false/false，实际 %+v", bare.Values.Permissions)
	}
}
