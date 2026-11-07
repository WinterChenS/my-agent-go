package app

// ART01/ART02 的绑定层外部观察。
//
// 与 internal/artifact 的域内观察互补：这里读的是**产品 appData 里的会话库**
// （<appRoot>/sessions.db）与产品真实写下的导出根，而不是被测函数的返回值。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"my-agent-go/internal/storage"
)

type appArtifactRow struct {
	ID            string `json:"id"`
	RunID         string `json:"runId"`
	SessionID     string `json:"sessionId"`
	CanonicalPath string `json:"canonicalPath"`
	Sha256        string `json:"sha256"`
	SizeBytes     int64  `json:"sizeBytes"`
}

// observeAppArtifacts 用第二个句柄从会话库读产物索引行，并把导出根的真实遍历一起落盘。
func observeAppArtifacts(t *testing.T, root, exportRoot string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	path := filepath.Join(root, "sessions.db")
	rows := []appArtifactRow{}
	if _, err := os.Stat(path); err == nil {
		ctx := context.Background()
		db, oerr := storage.Open(ctx, path)
		if oerr != nil {
			t.Fatalf("用第二个句柄打开会话库 %s 失败: %v", path, oerr)
		}
		defer func() { _ = db.Close() }()
		res, qerr := db.SQL().QueryContext(ctx, `
			SELECT id, COALESCE(run_id, ''), COALESCE(session_id, ''),
			       canonical_path, sha256, size_bytes
			FROM artifacts ORDER BY id`)
		if qerr == nil {
			for res.Next() {
				var r appArtifactRow
				if err := res.Scan(&r.ID, &r.RunID, &r.SessionID, &r.CanonicalPath, &r.Sha256, &r.SizeBytes); err != nil {
					t.Fatalf("扫描 artifacts 行失败: %v", err)
				}
				rows = append(rows, r)
			}
			_ = res.Close()
		}
	}
	dump, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("序列化产物索引失败: %v", err)
	}
	// 导出根的真实遍历复用本包的 observeTree（会按测试追加记录）。
	if exportRoot != "" {
		observeTree(t, exportRoot, "artifact-exports.json", extra)
	}
	facts := map[string]any{"appArtifactRows": rows}
	for k, v := range extra {
		facts[k] = v
	}
	writeAppObservation(t, facts, map[string][]byte{"artifacts-api-dump.json": dump})
}
