package session

// SES01–SES03 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写、artifact 覆盖写；未设置时不写任何文件。
//
// 事实来源刻意分成两种，都不问被测服务"你记对了吗"：
//   - session-dump.json：第二个 storage.DB 句柄从同一个库文件读出 sessions/artifacts/
//     runs 的真实行（revision、archived、deleted 标记、产物定位）；
//   - *-tree.json：对磁盘目录的真实遍历（blob store、导出目录、工作区），
//     附每个文件的 sha256；"被拒的附件没有产生 blob""HTML 里没有脚本"
//     这类事实只能从文件读出来。

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

type sessionDumpRow struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Title       string `json:"title"`
	Pinned      int64  `json:"pinned"`
	Archived    int64  `json:"archived"`
	Kind        string `json:"kind"`
	Revision    int64  `json:"revision"`
	DeletedAt   string `json:"deletedAt,omitempty"`
}

type artifactDumpRow struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	DisplayName string `json:"displayName"`
	Sha256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type runDumpRow struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	LastSeq int64  `json:"lastSeq"`
}

type sessionDump struct {
	Path      string            `json:"path"`
	Exists    bool              `json:"exists"`
	Sessions  []sessionDumpRow  `json:"sessions"`
	Artifacts []artifactDumpRow `json:"artifacts"`
	Runs      []runDumpRow      `json:"runs"`
	Errors    []string          `json:"errors,omitempty"`
}

// dumpSessionFile 用第二个句柄从库文件读真实行。
func dumpSessionFile(t *testing.T, path string) *sessionDump {
	t.Helper()
	dump := &sessionDump{Path: path, Sessions: []sessionDumpRow{}, Artifacts: []artifactDumpRow{}, Runs: []runDumpRow{}}
	if _, err := os.Stat(path); err != nil {
		return dump
	}
	dump.Exists = true
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开会话库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, qerr := db.SQL().QueryContext(ctx, `
		SELECT id, COALESCE(workspace_id, ''), COALESCE(title, ''), pinned, archived,
		       COALESCE(kind, ''), revision, COALESCE(deleted_at, '')
		FROM sessions ORDER BY id`)
	if qerr != nil {
		dump.Errors = append(dump.Errors, "sessions: "+qerr.Error())
	} else {
		for rows.Next() {
			var r sessionDumpRow
			if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Title, &r.Pinned, &r.Archived, &r.Kind, &r.Revision, &r.DeletedAt); err != nil {
				t.Fatalf("扫描 sessions 行失败: %v", err)
			}
			dump.Sessions = append(dump.Sessions, r)
		}
		_ = rows.Close()
	}

	arows, aerr := db.SQL().QueryContext(ctx, `
		SELECT id, COALESCE(session_id, ''), COALESCE(run_id, ''), COALESCE(display_name, ''),
		       sha256, size_bytes
		FROM artifacts ORDER BY id`)
	if aerr != nil {
		dump.Errors = append(dump.Errors, "artifacts: "+aerr.Error())
	} else {
		for arows.Next() {
			var r artifactDumpRow
			if err := arows.Scan(&r.ID, &r.SessionID, &r.RunID, &r.DisplayName, &r.Sha256, &r.SizeBytes); err != nil {
				t.Fatalf("扫描 artifacts 行失败: %v", err)
			}
			dump.Artifacts = append(dump.Artifacts, r)
		}
		_ = arows.Close()
	}

	rrows, rerr := db.SQL().QueryContext(ctx, `SELECT id, state, last_seq FROM runs ORDER BY id`)
	if rerr != nil {
		dump.Errors = append(dump.Errors, "runs: "+rerr.Error())
	} else {
		for rrows.Next() {
			var r runDumpRow
			if err := rrows.Scan(&r.ID, &r.State, &r.LastSeq); err != nil {
				t.Fatalf("扫描 runs 行失败: %v", err)
			}
			dump.Runs = append(dump.Runs, r)
		}
		_ = rrows.Close()
	}
	return dump
}

// observeSessionDB 把库里的真实行写进观察目录。
func observeSessionDB(t *testing.T, path string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	dump := dumpSessionFile(t, path)
	record := map[string]any{
		"test":      t.Name(),
		"path":      dump.Path,
		"exists":    dump.Exists,
		"sessions":  dump.Sessions,
		"artifacts": dump.Artifacts,
		"runs":      dump.Runs,
		"errors":    dump.Errors,
	}
	facts := map[string]any{
		"sessionPath":   dump.Path,
		"sessionExists": dump.Exists,
		"sessions":      dump.Sessions,
		"artifacts":     dump.Artifacts,
		"runs":          dump.Runs,
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeSessionObservation(t, facts, map[string][]byte{"session-dump.json": appendSessionRecord(t, "session-dump.json", record)})
}

type treeEntry struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Sha256 string `json:"sha256,omitempty"`
}

// scanTree 遍历目录（不跟随符号链接），返回按路径排序的磁盘事实。
func scanTree(t *testing.T, root string) []treeEntry {
	t.Helper()
	out := []treeEntry{}
	if _, err := os.Stat(root); err != nil {
		return out
	}
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		entry := treeEntry{Path: filepath.ToSlash(rel), Bytes: int(info.Size())}
		if body, rerr := os.ReadFile(path); rerr == nil {
			sum := sha256.Sum256(body)
			entry.Sha256 = hex.EncodeToString(sum[:])
		}
		out = append(out, entry)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历 %s 失败: %v", root, walkErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// observeTree 把某个目录的磁盘遍历写进 artifact（按测试追加，后写者不覆盖前一条）。
func observeTree(t *testing.T, root, artifact string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	entries := scanTree(t, root)
	record := map[string]any{"test": t.Name(), "root": root, "entries": entries}
	facts := map[string]any{strings.TrimSuffix(artifact, ".json"): entries}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeSessionObservation(t, facts, map[string][]byte{artifact: appendSessionRecord(t, artifact, record)})
}

// sentinelHits 从磁盘读每个文件，数出命中 sentinel 的文件（SES03 的硬事实）。
func sentinelHits(t *testing.T, entries []treeEntry, root, sentinel string) []string {
	t.Helper()
	hits := []string{}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.Path)))
		if err != nil {
			continue
		}
		if strings.Contains(string(body), sentinel) {
			hits = append(hits, e.Path)
		}
	}
	return hits
}

// appendSessionRecord 把本条测试的记录追加进已有 artifact 数组。
func appendSessionRecord(t *testing.T, artifact string, record map[string]any) []byte {
	t.Helper()
	dir := observationDir()
	records := []json.RawMessage{}
	if dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, artifact)); err == nil {
			if uerr := json.Unmarshal(raw, &records); uerr != nil {
				t.Fatalf("已有 %s 不是合法 JSON 数组: %v", artifact, uerr)
			}
		}
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化 %s 记录失败: %v", artifact, err)
	}
	records = append(records, json.RawMessage(body))
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", artifact, err)
	}
	return data
}

// writeSessionObservation 合并写 observation.json，并覆盖写 artifact。
func writeSessionObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
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
