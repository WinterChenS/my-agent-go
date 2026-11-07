package connectors

// CONN03/CONN04/CONN08 的外部观察值输出。
//
// 契约与 internal/memory|run|config 的 observation 文件一致：目录来自
// ANVIL_OBSERVATION_DIR，observation.json 合并写、artifact 按测试追加；未设置时什么也不写。
//
// 事实来源都不问被测代码"你记对了吗"：
//   - connector-db.json：用**第二个** storage.DB 句柄打开同一个连接器库文件，
//     直接读 connector_definitions / connector_audit 的真实行（定义里的 credential ref、
//     审计里的 decision/outcome/risk 相关字段）；
//   - connector-sentinel-scan.json：重新扫描库文件与连接器根目录的字节，找明文 sentinel
//     （含 base64 变体）。哨兵只记 sha256，绝不把秘密写进证据。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/storage"
)

func observeDir() string { return ObservationDir() }

// connectorDBDump 是第二个句柄从库文件里读出的真实行。
type connectorDBDump struct {
	Test        string           `json:"test"`
	Label       string           `json:"label"`
	DBPath      string           `json:"dbPath"`
	DBExists    bool             `json:"dbExists"`
	Definitions []map[string]any `json:"definitions"`
	Audits      []map[string]any `json:"audits"`
	OpenError   string           `json:"openError,omitempty"`
}

// sentinelScan 是一次"重新扫描磁盘找明文"的记录。
type sentinelScan struct {
	Test           string   `json:"test"`
	Label          string   `json:"label"`
	Roots          []string `json:"roots"`
	ScannedFiles   int      `json:"scannedFiles"`
	SentinelSHA256 []string `json:"sentinelSha256"`
	HitCount       int      `json:"hitCount"`
	Hits           []string `json:"hits"`
}

// observeConnectorStore 读连接器库的真实行并扫盘找 sentinel，写进观察目录。
//
// scanRoots 通常是连接器根目录（库文件、WAL/SHM、JSONL 审计镜像都在里面）。
func observeConnectorStore(t *testing.T, label, dbPath string, scanRoots []string, secrets []string, extra map[string]any) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}

	dump := connectorDBDump{
		Test:        t.Name(),
		Label:       label,
		DBPath:      filepath.ToSlash(dbPath),
		Definitions: []map[string]any{},
		Audits:      []map[string]any{},
	}
	if _, statErr := os.Stat(dbPath); statErr == nil {
		dump.DBExists = true
		// 第二个句柄：不复用被测服务手里的那个连接，确保证据来自文件本身。
		db, err := storage.Open(context.Background(), dbPath)
		if err != nil {
			dump.OpenError = err.Error()
		} else {
			defer func() { _ = db.Close() }()
			if rows, qerr := dumpTable(db.SQL(), `SELECT id, kind, display_name, credential_ref, enabled, revision FROM connector_definitions ORDER BY id`); qerr != nil {
				dump.OpenError = qerr.Error()
			} else {
				dump.Definitions = rows
			}
			if rows, qerr := dumpTable(db.SQL(), `SELECT seq, connector_id, action_id, kind, decision, outcome, reason, credential_ref, idempotency_key, run_id, tool_name, detail FROM connector_audit ORDER BY seq`); qerr != nil {
				dump.OpenError = qerr.Error()
			} else {
				dump.Audits = rows
			}
		}
	}
	appendArtifact(t, "connector-db.json", dump)

	scan := sentinelScan{
		Test:           t.Name(),
		Label:          label,
		Roots:          make([]string, 0, len(scanRoots)),
		SentinelSHA256: make([]string, 0, len(secrets)),
		Hits:           []string{},
	}
	needles := make([][]byte, 0, 4*len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		sum := sha256.Sum256([]byte(secret))
		scan.SentinelSHA256 = append(scan.SentinelSHA256, "sha256:"+hex.EncodeToString(sum[:]))
		needles = append(needles,
			[]byte(secret),
			[]byte(base64.StdEncoding.EncodeToString([]byte(secret))),
			[]byte(base64.RawStdEncoding.EncodeToString([]byte(secret))),
			[]byte(base64.URLEncoding.EncodeToString([]byte(secret))),
		)
	}
	for _, root := range scanRoots {
		scan.Roots = append(scan.Roots, filepath.ToSlash(root))
		walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			scan.ScannedFiles++
			for _, n := range needles {
				if len(n) > 0 && strings.Contains(string(body), string(n)) {
					rel, relErr := filepath.Rel(root, path)
					if relErr != nil {
						rel = path
					}
					scan.Hits = append(scan.Hits, filepath.ToSlash(rel))
					break
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("扫描 %s 失败: %v", root, walkErr)
		}
	}
	sort.Strings(scan.Hits)
	scan.HitCount = len(scan.Hits)
	appendArtifact(t, "connector-sentinel-scan.json", scan)

	facts := map[string]any{
		"label":             label,
		"connectorDBPath":   dump.DBPath,
		"connectorDBExists": dump.DBExists,
		"definitionRows":    dump.Definitions,
		"auditRows":         dump.Audits,
		"auditRowCount":     len(dump.Audits),
		"scannedFiles":      scan.ScannedFiles,
		"sentinelHitCount":  scan.HitCount,
		"sentinelHits":      scan.Hits,
	}
	if dump.OpenError != "" {
		facts["dumpError"] = dump.OpenError
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"definitionRows":   dump.Definitions,
		"auditRowCount":    len(dump.Audits),
		"scannedFiles":     scan.ScannedFiles,
		"sentinelHitCount": scan.HitCount,
		"sentinelHits":     scan.Hits,
	}
	mergeObservation(t, facts)
}

// appendArtifact 把一条记录追加进 artifact 的 JSON 数组（同 case 多条测试共用文件）。
func appendArtifact(t *testing.T, artifact string, record any) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, artifact)
	records := []json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if uerr := json.Unmarshal(raw, &records); uerr != nil {
			t.Fatalf("已有 %s 不是合法 JSON 数组: %v", artifact, uerr)
		}
	}
	one, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化 %s 记录失败: %v", artifact, err)
	}
	records = append(records, one)
	out, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", artifact, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", artifact, err)
	}
}

// mergeObservation 合并写 observation.json（同 key 覆盖，累积 tests 列表）。
func mergeObservation(t *testing.T, facts map[string]any) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "observation.json")
	merged := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if uerr := json.Unmarshal(raw, &merged); uerr != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON 对象: %v", uerr)
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
	merged["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// dumpTable 把一条查询的每行读成列名 → 值 的映射（值从文件里读，不做模型化解释）。
func dumpTable(db *sql.DB, query string) ([]map[string]any, error) {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			switch v := cells[i].(type) {
			case []byte:
				row[name] = string(v)
			case nil:
				row[name] = nil
			default:
				row[name] = v
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取行失败: %w", err)
	}
	return out, nil
}
