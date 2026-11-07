package artifact

// ART01/ART02 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写、artifact 覆盖写；未设置时不写任何文件。
//
// 事实从两侧读：
//   - artifacts-dump.json：第二个 storage.DB 句柄读 artifacts 真实行（canonical_path/
//     sha256/size/run_id），"被拒的导入没有留下索引行"由此回答；
//   - artifact-files.json：工作区与导出根的真实遍历（含 sha256），
//     "文件被改后索引里的旧 hash 不再有效"由磁盘字节回答。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"my-agent-go/internal/storage"
)

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

type artifactRow struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspaceId"`
	RunID         string `json:"runId"`
	SessionID     string `json:"sessionId"`
	CanonicalPath string `json:"canonicalPath"`
	Sha256        string `json:"sha256"`
	SizeBytes     int64  `json:"sizeBytes"`
	MediaType     string `json:"mediaType"`
}

// dumpArtifactsFile 用第二个句柄从库文件读产物索引行。
func dumpArtifactsFile(t *testing.T, path string) []artifactRow {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows := []artifactRow{}
	res, qerr := db.SQL().QueryContext(ctx, `
		SELECT id, workspace_id, run_id, session_id, canonical_path, sha256, size_bytes, media_type
		FROM artifacts ORDER BY id`)
	if qerr != nil {
		return rows
	}
	defer func() { _ = res.Close() }()
	for res.Next() {
		var r artifactRow
		if err := res.Scan(&r.ID, &r.WorkspaceID, &r.RunID, &r.SessionID, &r.CanonicalPath, &r.Sha256, &r.SizeBytes, &r.MediaType); err != nil {
			t.Fatalf("扫描 artifacts 行失败: %v", err)
		}
		rows = append(rows, r)
	}
	return rows
}

type fileFact struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Sha256 string `json:"sha256"`
}

// scanFiles 遍历目录并算出每个文件的磁盘事实。
func scanFiles(t *testing.T, root string) []fileFact {
	t.Helper()
	out := []fileFact{}
	if _, err := os.Stat(root); err != nil {
		return out
	}
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		fact := fileFact{Path: filepath.ToSlash(rel), Bytes: int(info.Size())}
		if body, rerr := os.ReadFile(path); rerr == nil {
			sum := sha256.Sum256(body)
			fact.Sha256 = hex.EncodeToString(sum[:])
		}
		out = append(out, fact)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历 %s 失败: %v", root, walkErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// observeArtifacts 把产物索引行与工作区/导出根的磁盘事实写进观察目录。
func observeArtifacts(t *testing.T, dbPath, wsDir, exportRoot string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	rows := dumpArtifactsFile(t, dbPath)
	dump, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("序列化产物索引失败: %v", err)
	}
	files := map[string]any{
		"workspaceFiles": scanFiles(t, wsDir),
		"exportFiles":    scanFiles(t, exportRoot),
	}
	filesJSON, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		t.Fatalf("序列化磁盘树失败: %v", err)
	}
	facts := map[string]any{
		"artifactRows":   rows,
		"workspaceFiles": files["workspaceFiles"],
		"exportFiles":    files["exportFiles"],
	}
	for k, v := range extra {
		facts[k] = v
	}
	writeArtifactObservation(t, facts, map[string][]byte{
		"artifacts-dump.json": dump,
		"artifact-files.json": filesJSON,
	})
}

// writeArtifactObservation 合并写 observation.json，并覆盖写 artifact。
func writeArtifactObservation(t *testing.T, facts map[string]any, artifacts map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range artifacts {
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
