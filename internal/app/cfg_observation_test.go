package app

// CFG03.resources 的外部观察：运行快照是否真的限制了资源加载。
//
// 观察值不是 RunSnapshot 的返回值（那是被测代码自述），而是：
//   - run-index.json：用**第二个 storage.DB 句柄**从 <appRoot>/sessions.db 读出的
//     runs/sessions 真实行（config_revision、state、关联会话），
//   - agent-tree.json：隔离 agentDir 的真实磁盘遍历（哪份配置真的落到盘上）。
//
// 与 internal/memory/observation_test.go 的契约一致：目录来自
// ANVIL_OBSERVATION_DIR，observation.json 合并写、artifact 覆盖写。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"my-agent-go/internal/storage"
)

type runIndexRow struct {
	ID             string `json:"id"`
	SessionID      string `json:"sessionId"`
	State          string `json:"state"`
	ConfigRevision int64  `json:"configRevision"`
}

type sessionIndexRow struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Revision    int64  `json:"revision"`
}

type runIndexDump struct {
	Path       string            `json:"path"`
	Exists     bool              `json:"exists"`
	Runs       []runIndexRow     `json:"runs"`
	Sessions   []sessionIndexRow `json:"sessions"`
	OpenErrors []string          `json:"openErrors,omitempty"`
}

// dumpRunIndexFile 用第二个句柄从会话库文件读出真实索引行。
func dumpRunIndexFile(t *testing.T, path string) ([]byte, *runIndexDump) {
	t.Helper()
	dump := &runIndexDump{Path: path, Runs: []runIndexRow{}, Sessions: []sessionIndexRow{}}
	if _, err := os.Stat(path); err != nil {
		data, _ := json.MarshalIndent(dump, "", "  ")
		return data, dump
	}
	dump.Exists = true
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开会话库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, qerr := db.SQL().QueryContext(ctx,
		`SELECT id, session_id, state, config_revision FROM runs ORDER BY id`)
	if qerr != nil {
		// 表可能还没被本用例的路径创建；记下原因，不伪造空结果。
		dump.OpenErrors = append(dump.OpenErrors, qerr.Error())
	} else {
		for rows.Next() {
			var r runIndexRow
			if err := rows.Scan(&r.ID, &r.SessionID, &r.State, &r.ConfigRevision); err != nil {
				t.Fatalf("扫描 runs 行失败: %v", err)
			}
			dump.Runs = append(dump.Runs, r)
		}
		_ = rows.Close()
	}

	srows, serr := db.SQL().QueryContext(ctx,
		`SELECT id, COALESCE(workspace_id, ''), COALESCE(title, ''), COALESCE(kind, ''), revision FROM sessions ORDER BY id`)
	if serr != nil {
		dump.OpenErrors = append(dump.OpenErrors, serr.Error())
	} else {
		for srows.Next() {
			var r sessionIndexRow
			if err := srows.Scan(&r.ID, &r.WorkspaceID, &r.Title, &r.Kind, &r.Revision); err != nil {
				t.Fatalf("扫描 sessions 行失败: %v", err)
			}
			dump.Sessions = append(dump.Sessions, r)
		}
		_ = srows.Close()
	}

	data, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		t.Fatalf("序列化运行索引失败: %v", err)
	}
	return data, dump
}

// observeRunIndex 把会话库里的真实索引行写进观察目录。
//
// 一条 case 的多条测试共用一个证据目录，而每条测试的库是各自的临时文件：
// artifact 因此是**按测试追加**的数组（每条都带 test 名与自己的库路径），
// 后写者不会把前一条测试的真实行覆盖掉。
func observeRunIndex(t *testing.T, root string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	_, dump := dumpRunIndexFile(t, filepath.Join(root, "sessions.db"))
	record := map[string]any{
		"test":       t.Name(),
		"path":       dump.Path,
		"exists":     dump.Exists,
		"runs":       dump.Runs,
		"sessions":   dump.Sessions,
		"openErrors": dump.OpenErrors,
	}
	artifact := appendArtifactRecord(t, "run-index.json", record)
	facts := map[string]any{
		"runIndexPath":     dump.Path,
		"runIndexExists":   dump.Exists,
		"runRows":          dump.Runs,
		"sessionRows":      dump.Sessions,
		"runIndexOpenErrs": dump.OpenErrors,
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeAppObservation(t, facts, map[string][]byte{"run-index.json": artifact})
}

// treeEntry 是磁盘遍历的一行事实。
type treeEntry struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Sha256 string `json:"sha256,omitempty"`
}

// observeTree 遍历目录（不跟随符号链接）并把事实写进 artifact。
func observeTree(t *testing.T, root, artifact string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	entries := []treeEntry{}
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
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
		entries = append(entries, entry)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if walkErr != nil && !errors.Is(walkErr, os.ErrNotExist) {
		t.Fatalf("遍历 %s 失败: %v", root, walkErr)
	}
	record := map[string]any{"test": t.Name(), "root": root, "entries": entries}
	data := appendArtifactRecord(t, artifact, record)
	facts := map[string]any{strings.TrimSuffix(artifact, ".json"): entries}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeAppObservation(t, facts, map[string][]byte{artifact: data})
}

// appendArtifactRecord 把本条测试的记录追加进已有 artifact 数组。
func appendArtifactRecord(t *testing.T, artifact string, record map[string]any) []byte {
	t.Helper()
	dir := appObservationDir()
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
