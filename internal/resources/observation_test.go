package resources

// RES01/RES03/RES04 的外部观察值输出。
//
// 契约与 internal/run/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写（不清空：RES04 是跨包两步 case），artifact 覆盖写；
// 没设置该变量（本地 focused 运行）时什么也不写。
//
// 观察值必须来自磁盘/子进程：
//   - skills-tree.json：filepath.Walk 托管技能目录，记录 {relpath, sha256, mode, size}；
//   - agent-config.json：产品写到隔离 agentDir 里的配置文件，读回来核对；
//   - compiled-args.json / worker-argv.json：由 fakeworker 子进程写出它**实际收到**的
//     argv（-record-argv），测试只读文件，不自己拼一份"应该是什么"。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var resourcesObservationMu sync.Mutex

func resourcesObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// skillsTreeEntry 是托管目录里的一个真实条目。
type skillsTreeEntry struct {
	RelPath string `json:"relpath"`
	IsDir   bool   `json:"isDir"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
}

// recordSkillsTree 从磁盘 walk 托管技能目录，把快照追加进 skills-tree.json，
// 并把本次观察到的事实合并进 observation.json。
//
// 追加而不是覆盖：同一个 case 里"删除前/删除后"是两份不同的真实状态，
// 只留最后一份会让删除那一步的证据消失。
func recordSkillsTree(t *testing.T, root string, label string, facts map[string]any) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	skillsRoot := filepath.Join(root, "skills")
	entries := make([]skillsTreeEntry, 0, 16)
	walkErr := filepath.Walk(skillsRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录不存在（例如全部删掉）也是一份真实状态
			}
			return err
		}
		rel, relErr := filepath.Rel(skillsRoot, p)
		if relErr != nil {
			return relErr
		}
		e := skillsTreeEntry{
			RelPath: filepath.ToSlash(rel),
			IsDir:   info.IsDir(),
			Mode:    info.Mode().String(),
			Size:    info.Size(),
		}
		if !info.IsDir() {
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(raw)
			e.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
		}
		entries = append(entries, e)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("从磁盘 walk 托管技能目录 %s 失败: %v", skillsRoot, walkErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })

	snapshot := map[string]any{
		"test":       t.Name(),
		"label":      label,
		"root":       filepath.ToSlash(skillsRoot),
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
		"entries":    entries,
	}
	merged := readResourcesArtifact(t, "skills-tree.json")
	snaps, _ := merged["snapshots"].([]any)
	merged["snapshots"] = append(snaps, snapshot)
	writeResourcesArtifact(t, "skills-tree.json", merged)

	fileCount := 0
	for _, e := range entries {
		if !e.IsDir {
			fileCount++
		}
	}
	if facts == nil {
		facts = map[string]any{}
	}
	// 集合类事实跨测试累积（导入/删除/CAS 拒绝分散在不同测试里，
	// 后写入者直接覆盖会只剩最后一条测试观察到的那个技能）。
	for _, key := range []string{"importedSkills", "deletedSkills", "casRejected"} {
		facts[key] = unionResourcesListFact(t, key, facts[key])
	}
	facts["skillsRoot"] = filepath.ToSlash(skillsRoot)
	facts["fileCount"] = fileCount
	facts["treeEntries"] = len(entries)
	facts["treeLabel"] = label
	facts["treeSnapshotCount"] = len(merged["snapshots"].([]any))
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"skillsRoot":  filepath.ToSlash(skillsRoot),
		"fileCount":   fileCount,
		"treeEntries": len(entries),
	}
	writeResourcesObservation(t, facts, nil)
}

// writeResourcesObservation 把观察值与 artifact 合并写进 ANVIL_OBSERVATION_DIR。
func writeResourcesObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := resourcesObservationDir()
	if dir == "" {
		return
	}
	resourcesObservationMu.Lock()
	defer resourcesObservationMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}
	merged := readResourcesArtifact(t, "observation.json")
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
	}
	merged["tests"] = tests
	writeResourcesArtifact(t, "observation.json", merged)
}

// readResourcesArtifact 读取已有 artifact（不存在时返回空对象）。
func readResourcesArtifact(t *testing.T, name string) map[string]any {
	t.Helper()
	out := map[string]any{}
	raw, err := os.ReadFile(filepath.Join(resourcesObservationDir(), name))
	if err != nil {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// 不静默重建：已有文件损坏时继续写会把"合并"变成"混脏数据"。
		t.Fatalf("已有 %s 不是合法 JSON 对象: %v", name, err)
	}
	return out
}

// writeResourcesArtifact 原子感地写一个 JSON artifact。
func writeResourcesArtifact(t *testing.T, name string, value any) {
	t.Helper()
	dir := resourcesObservationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", name, err)
	}
}

// unionResourcesListFact 把本次观察到的列表与已有 observation.json 里的同名列表取并集。
func unionResourcesListFact(t *testing.T, key string, cur any) any {
	t.Helper()
	if resourcesObservationDir() == "" {
		return cur
	}
	prior := readResourcesArtifact(t, "observation.json")
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
	out := make([]any, 0, 4)
	seen := map[string]bool{}
	for _, src := range []any{prior[key], cur} {
		var items []any
		switch v := src.(type) {
		case []any:
			items = v
		case nil:
			continue
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			if err := json.Unmarshal(b, &items); err != nil {
				continue
			}
		}
		for _, item := range items {
			k := encode(item)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, item)
		}
	}
	return out
}

// mergeSkillsFact 追加一条"这个技能被导入/删除/被 CAS 拒绝"的事实（并集，跨测试累积）。
func mergeSkillsFact(t *testing.T, facts map[string]any, key, id string) {
	t.Helper()
	cur, _ := facts[key].([]string)
	for _, v := range cur {
		if v == id {
			return
		}
	}
	facts[key] = append(append([]string{}, cur...), id)
}

// sha256File 返回磁盘文件的哈希（agentDir 配置核对用）。
func sha256File(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
