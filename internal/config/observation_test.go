package config

// CFG02/CFG04 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写、artifact 覆盖写；未设置时不写任何文件。
//
// 事实全部从磁盘读：
//   - config-tree.json：配置存储根目录的真实遍历（文件名/字节数/sha256/解析出的
//     revision 与 schemaVersion），用于证明"失败的 CAS 没有覆盖源文件"和
//     "断电残片没有把有效配置弄丢"；
//   - home-tree.json：伪造用户 HOME 的真实遍历，用于证明编译没有创建/改写 ~/.pi
//     （CFG04 的外部判据不是函数返回值，而是用户目录里到底出现了什么）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

type fileFacts struct {
	Name          string `json:"name"`
	Bytes         int    `json:"bytes"`
	Sha256        string `json:"sha256"`
	Revision      uint64 `json:"revision,omitempty"`
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	Valid         bool   `json:"validJson"`
}

// describeTree 遍历目录（不跟随符号链接），返回每个文件的磁盘事实。
func describeTree(t *testing.T, root string) []fileFacts {
	t.Helper()
	out := []fileFacts{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name())
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("读取 %s 失败: %v", path, rerr)
		}
		sum := sha256.Sum256(body)
		fact := fileFacts{
			Name:   e.Name(),
			Bytes:  len(body),
			Sha256: hex.EncodeToString(sum[:]),
		}
		var env envelope
		if json.Unmarshal(body, &env) == nil {
			fact.Valid = true
			fact.Revision = env.Revision
			fact.SchemaVersion = env.SchemaVersion
		}
		out = append(out, fact)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// observeConfigTree 把配置存储根的磁盘事实写进观察目录。
func observeConfigTree(t *testing.T, root string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	tree := describeTree(t, root)
	data, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		t.Fatalf("序列化配置树失败: %v", err)
	}
	facts := map[string]any{"configFiles": tree}
	for k, v := range extra {
		facts[k] = v
	}
	writeConfigObservation(t, facts, map[string][]byte{"config-tree.json": data})
}

// observeHomeTree 记录伪造 HOME 的磁盘事实（CFG04：编译不得写用户 ~/.pi）。
func observeHomeTree(t *testing.T, home string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("列出伪造 HOME 失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	data, err := json.MarshalIndent(map[string]any{"home": home, "entries": names}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 HOME 快照失败: %v", err)
	}
	facts := map[string]any{"homeEntries": names}
	for k, v := range extra {
		facts[k] = v
	}
	writeConfigObservation(t, facts, map[string][]byte{"home-tree.json": data})
}

// writeConfigObservation 合并写 observation.json，并覆盖写 artifact。
func writeConfigObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}
	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	self := t.Name()
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == self {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, self)
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
