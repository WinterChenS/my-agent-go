package app

// CFG01 的外部观察：五层合并 + 来源可查询，事实从**磁盘上的层文档**与落盘 artifact 读回。
//
// 为什么不能只用 internal/config 的进程内断言：那是"函数返回值正确"，证明不了
// 层文档真的落在磁盘上、也证明不了界面能查到来源。这里用真实的配置存储写五层，
// 再用**第二个 store 实例**把层文档从磁盘读回来写成 artifact，最后用第二个
// DB/文件句柄读取 binding 的结果，事实全部从文件里取。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"my-agent-go/internal/config"
)

// writeConfigLayers 用绑定层写五层，返回各层 revision。
func writeConfigLayers(t *testing.T, d *Desktop, bodies map[string]string) {
	t.Helper()
	order := []string{"default", "user", "workspace", "profile", "session"}
	for _, name := range order {
		body, ok := bodies[name]
		if !ok {
			continue
		}
		if _, err := d.SaveConfigLayer(name, 0, body); err != nil {
			t.Fatalf("写图层 %s: %+v", name, err)
		}
	}
}

// dumpLayerDocs 用第二个 store 实例把层文档从磁盘读回来。
func dumpLayerDocs(t *testing.T, root string) []map[string]any {
	t.Helper()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("第二个 store 实例: %v", err)
	}
	out := make([]map[string]any, 0, 5)
	for _, l := range []struct{ Name, Key string }{
		{"default", "config.default"},
		{"user", "config.user"},
		{"workspace", "config.workspace"},
		{"profile", "config.profile"},
		{"session", "config.session"},
	} {
		doc, loadErr := store.Load(t.Context(), l.Key)
		if loadErr != nil {
			continue
		}
		if doc.Revision == 0 || len(doc.Body) == 0 || string(doc.Body) == "null" {
			continue // 该层没有文档（store 对缺失 key 返回空文档而不是错误）
		}
		out = append(out, map[string]any{
			"layer":    l.Name,
			"revision": doc.Revision,
			"body":     json.RawMessage(doc.Body),
		})
	}
	return out
}

func TestEffectiveConfigProvenanceIsQueryableAndObservedFromDisk(t *testing.T) {
	d, root := newTestDesktop(t)

	writeConfigLayers(t, d, map[string]string{
		"default": `{"schemaVersion":1,
			"permissions":{"filesystem":"unrestricted","network":false,"shell":false},
			"credentialRef":"default:key",
			"models":[{"id":"m-a","model":"base-a","provider":"openai"},{"id":"m-b","model":"base-b"}],
			"skills":[{"id":"s-1","path":"/skills/1"}]}`,
		// 项目层（workspace）试图放宽权限并替换凭证：必须被忽略并留痕。
		"workspace": `{"schemaVersion":1,
			"permissions":{"filesystem":"unrestricted","shell":true},
			"credentialRef":"workspace:stolen",
			"skills":[{"id":"s-2","path":"/skills/2"}]}`,
		"session": `{"schemaVersion":1,"permissions":{"filesystem":"workspace"}}`,
	})

	res := d.EffectiveConfig()
	if res.Error != nil {
		t.Fatalf("EffectiveConfig: %+v", res.Error)
	}

	// 来源可查询：每一层的贡献都要能指回具体层。
	for path, want := range map[string]string{
		"models.m-a":    "default",
		"models.m-b":    "default",
		"skills.s-1":    "default",
		"skills.s-2":    "workspace",
		"credentialRef": "default",
	} {
		if got := res.Provenance[path]; got != want {
			t.Fatalf("provenance[%s] = %q，期望 %q（来源必须可查询）", path, got, want)
		}
	}
	// 项目层不能扩大权限：会话层把 filesystem 收到 workspace，最终必须更严。
	if res.Values.Permissions.Filesystem != "workspace" {
		t.Fatalf("filesystem 最终权限 = %q，期望 workspace（更严的一层生效）", res.Values.Permissions.Filesystem)
	}
	if res.Values.Permissions.Shell || res.Values.Permissions.Network {
		t.Fatalf("权限被项目层放宽了（只能收紧）：最终 %+v", res.Values.Permissions)
	}
	if res.Values.CredentialRef != "default:key" {
		t.Fatalf("credentialRef = %q，期望 default:key（项目层不得替换凭证）", res.Values.CredentialRef)
	}

	// 观察 artifact：磁盘上的层文档（第二个 store 实例读出）+ binding 结果。
	layers := dumpLayerDocs(t, root)
	if len(layers) != 3 {
		t.Fatalf("磁盘上应有 3 层文档（default/workspace/session），实际 %d: %v", len(layers), layers)
	}
	var names []string
	for _, l := range layers {
		names = append(names, l["layer"].(string))
	}
	sort.Strings(names)
	if names[0] != "default" || names[1] != "session" || names[2] != "workspace" {
		t.Fatalf("磁盘层文档 = %v，期望 default/session/workspace", names)
	}

	writeCFGObservation(t, map[string]any{
		"layersOnDisk":       names,
		"provenance":         res.Provenance,
		"filesystemFinal":    res.Values.Permissions.Filesystem,
		"shellFinal":         res.Values.Permissions.Shell,
		"networkFinal":       res.Values.Permissions.Network,
		"credentialRefFinal": res.Values.CredentialRef,
		"models":             len(res.Values.Models),
		"skills":             len(res.Values.Skills),
		"warnings":           res.Warnings,
	}, map[string][]byte{
		"layer-docs.json": mustJSON(t, layers),
		"effective-config.json": mustJSON(t, map[string]any{
			"values":     res.Values,
			"provenance": res.Provenance,
			"layers":     res.Layers,
		}),
	})
}

// writeCFGObservation 只在设置了 ANVIL_OBSERVATION_DIR 时落盘（与其它包同一契约）。
func writeCFGObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := observationDirApp()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatalf("写 artifact %s: %v", name, err)
		}
	}
	// observation.json 是合并写：同一条 case 的多条测试各自加自己的事实。
	path := filepath.Join(dir, "observation.json")
	existing := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &existing)
	}
	for k, v := range facts {
		existing[k] = v
	}
	raw, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写 observation: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	return raw
}

// observationDirApp 读集成 runner 传进来的观察目录（与其它包同一环境变量）。
func observationDirApp() string {
	return os.Getenv("ANVIL_OBSERVATION_DIR")
}
