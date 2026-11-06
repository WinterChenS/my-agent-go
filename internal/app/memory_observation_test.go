package app

// MEM03/MEM05 的绑定层外部观察值输出。
//
// 契约与 internal/run/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 是合并写（不清空，因为 MEM03/MEM05 都是跨包两步 case），
// artifact 覆盖写；没设置该变量时什么也不写。
//
// 观察值来自磁盘：memory-dump.json 由**第二个 storage.DB 句柄**从应用数据根下的
// memory.db 读出真实行。这里刻意不复用 internal/memory 包里的测试辅助——它是
// _test.go 里的包内私有函数，跨包不可见；共享它需要把测试辅助提成非测试代码，
// 代价比这段重复更大（与 internal/history/observation_test.go 同一取舍）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/storage"
)

var appObservationMu sync.Mutex

func appObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

type appMemoryRow struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// observeAppMemoryDB 从应用数据根下的 memory.db 读出真实行并落盘观察值。
func observeAppMemoryDB(t *testing.T, path string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	artifact, facts := dumpAppMemoryFile(t, path)
	for k, v := range extra {
		facts[k] = v
	}
	own := map[string]any{}
	for k, v := range facts {
		own[k] = v
	}
	facts["byTest:"+t.Name()] = own
	writeAppObservation(t, facts, map[string][]byte{"memory-dump.json": artifact})
}

func dumpAppMemoryFile(t *testing.T, path string) ([]byte, map[string]any) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开应用记忆库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows := make([]appMemoryRow, 0, 8)
	res, err := db.SQL().QueryContext(ctx, `
		SELECT id, scope, COALESCE(workspace_id, ''), COALESCE(kind, ''), status,
		       COALESCE(created_at, ''), COALESCE(expires_at, '')
		FROM memories ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("从磁盘读取 memories 行失败: %v", err)
	}
	for res.Next() {
		var r appMemoryRow
		if err := res.Scan(&r.ID, &r.Scope, &r.WorkspaceID, &r.Kind, &r.Status, &r.CreatedAt, &r.ExpiresAt); err != nil {
			res.Close()
			t.Fatalf("扫描 memories 行失败: %v", err)
		}
		rows = append(rows, r)
	}
	res.Close()

	var indexRows int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_fts`).Scan(&indexRows); err != nil {
		t.Fatalf("从磁盘读取 memory_fts 行数失败: %v", err)
	}

	scopes := map[string]int{}
	activeRows := 0
	expiredExcluded := 0
	deletedIDs := make([]string, 0, 2)
	workspaces := map[string]int{}
	now := time.Now().UTC()
	for _, r := range rows {
		scopes[r.Scope]++
		if r.WorkspaceID != "" {
			workspaces[r.WorkspaceID]++
		}
		switch r.Status {
		case "active":
			activeRows++
			if r.ExpiresAt != "" {
				if ts, err := time.Parse(time.RFC3339, r.ExpiresAt); err == nil && !ts.After(now) {
					expiredExcluded++
				}
			}
		case "deleted":
			deletedIDs = append(deletedIDs, r.ID)
		}
	}

	settings := make([]map[string]any, 0, 2)
	res, err = db.SQL().QueryContext(ctx, `
		SELECT scope, COALESCE(workspace_id, ''), enabled FROM memory_scope_settings
		ORDER BY scope, workspace_id`)
	if err != nil {
		t.Fatalf("从磁盘读取 memory_scope_settings 失败: %v", err)
	}
	for res.Next() {
		var scope, ws string
		var enabled int
		if err := res.Scan(&scope, &ws, &enabled); err != nil {
			res.Close()
			t.Fatalf("扫描 memory_scope_settings 失败: %v", err)
		}
		settings = append(settings, map[string]any{"scope": scope, "workspaceId": ws, "enabled": enabled})
	}
	res.Close()

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	artifact, err := json.MarshalIndent(map[string]any{
		"database":      abs,
		"dumpedAt":      now.Format(time.RFC3339),
		"memories":      rows,
		"indexRows":     indexRows,
		"scopeSettings": settings,
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 memory-dump.json 失败: %v", err)
	}
	sum := sha256.Sum256(artifact)
	return artifact, map[string]any{
		"dbPath":          abs,
		"dumpSha256":      "sha256:" + hex.EncodeToString(sum[:]),
		"rows":            len(rows),
		"scopes":          scopes,
		"workspaces":      workspaces,
		"activeRows":      activeRows,
		"expiredExcluded": expiredExcluded,
		"probeRows":       indexRows,
		"deletedRows":     deletedIDs,
		"scopeSettings":   settings,
	}
}

func writeAppObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := appObservationDir()
	if dir == "" {
		return
	}
	appObservationMu.Lock()
	defer appObservationMu.Unlock()
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
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
