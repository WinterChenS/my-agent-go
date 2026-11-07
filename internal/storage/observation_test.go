package storage

// DB01/DB02/DB03 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go、internal/run/observation_test.go 一致：
//   - 目录来自 ANVIL_OBSERVATION_DIR（集成 runner 传本 case 的证据目录）；
//   - observation.json 是**合并**写（同 key 覆盖，累积 tests 列表）；
//   - artifact 覆盖写；没设置该变量时什么也不写（本地 focused 运行不落盘）。
//
// 观察值的来源必须是磁盘，而不是被测句柄自述：
//   - db-schema.json 由**第二个 storage.DB 句柄**从同一个库文件里读出
//     （表清单、每张表的 DDL、schema_migrations 的真实行、半成品表是否存在）；
//   - fts-probe.json 同样用第二个句柄实际执行一次 trigram MATCH，
//     "驱动支持 FTS5 trigram"因此不是编译期常量，而是这一行 SQL 的结果；
//   - authority-hashes.json 是对配置 JSON / Pi transcript 的磁盘遍历结果，
//     DB03 要靠它证明重建索引没有改动这两个权威。

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

	"my-agent-go/internal/contracts"
)

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

type schemaDump struct {
	Path       string   `json:"path"`
	SchemaVer  int      `json:"schemaVersion"`
	Tables     []string `json:"tables"`
	DDL        []string `json:"ddl"`
	Migrations []string `json:"migrations"`
	// HalfCreated 记录"失败迁移有没有留下半成品表"——DB01 的核心外部事实。
	HalfCreated bool `json:"halfCreated"`
}

// dumpSchemaFile 用第二个句柄从库文件读真实结构。
func dumpSchemaFile(t *testing.T, path string) ([]byte, *schemaDump) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	dump := &schemaDump{Path: path, Tables: []string{}, DDL: []string{}, Migrations: []string{}}
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT name, COALESCE(sql, '') FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		t.Fatalf("从磁盘读取表清单失败: %v", err)
	}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatalf("扫描表清单失败: %v", err)
		}
		dump.Tables = append(dump.Tables, name)
		dump.DDL = append(dump.DDL, strings.Join(strings.Fields(ddl), " "))
		if name == "half_created" {
			dump.HalfCreated = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表清单失败: %v", err)
	}
	_ = rows.Close()

	// schema_migrations 不一定存在（半迁移用例的库只有迁移表或什么都没有）。
	if dump.Contains("schema_migrations") {
		mrows, merr := db.SQL().QueryContext(ctx,
			`SELECT version || ':' || name FROM schema_migrations ORDER BY version`)
		if merr == nil {
			for mrows.Next() {
				var rec string
				if err := mrows.Scan(&rec); err != nil {
					t.Fatalf("扫描迁移记录失败: %v", err)
				}
				dump.Migrations = append(dump.Migrations, rec)
			}
			_ = mrows.Close()
		}
	}
	if v, verr := db.SchemaVersion(ctx); verr == nil {
		dump.SchemaVer = v
	}

	data, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		t.Fatalf("序列化结构快照失败: %v", err)
	}
	return data, dump
}

func (d *schemaDump) Contains(table string) bool {
	for _, t := range d.Tables {
		if t == table {
			return true
		}
	}
	return false
}

// observeSchema 把库结构写进观察目录。extra 由各条测试补充自己的事实。
func observeSchema(t *testing.T, path string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	artifact, dump := dumpSchemaFile(t, path)
	facts := map[string]any{
		"dbPath":        dump.Path,
		"schemaVersion": dump.SchemaVer,
		"tables":        dump.Tables,
		"migrations":    dump.Migrations,
		"halfCreated":   dump.HalfCreated,
	}
	for k, v := range extra {
		facts[k] = v
	}
	writeStorageObservation(t, facts, map[string][]byte{"db-schema.json": artifact})
}

// observeFTS 用第二个句柄真实执行一次 trigram 检索，并把结果落盘。
func observeFTS(t *testing.T, path string, query string) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	probe := map[string]any{"query": query}
	var version, fts5 string
	if err := db.SQL().QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err == nil {
		probe["sqliteVersion"] = version
	}
	// ENABLE_FTS5 是编译期选项，只有真实连接能回答。
	if err := db.SQL().QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err == nil {
		probe["fts5Compiled"] = fts5
	}
	hits, herr := db.SearchMemories(ctx, query, 5)
	if herr != nil {
		probe["error"] = herr.Error()
	} else {
		found := append([]string{}, hits...)
		sort.Strings(found)
		probe["hits"] = found
	}
	// 短查询必须仍被拒绝：这是"没有偷偷降级成全库扫描"的磁盘侧证据。
	if _, serr := db.SearchMemories(ctx, "周报", 5); serr != nil {
		var appErr *contracts.AppError
		if errors.As(serr, &appErr) && appErr != nil {
			probe["shortQueryCode"] = appErr.Code
		}
	}

	data, err := json.MarshalIndent(probe, "", "  ")
	if err != nil {
		t.Fatalf("序列化 FTS 探测失败: %v", err)
	}
	writeStorageObservation(t, map[string]any{
		"ftsProbe": probe,
	}, map[string][]byte{"fts-probe.json": data})
}

// observeAuthority 记录权威文件与库结构的真实磁盘状态（DB03）。
//
// 事实全部从盘上读：每个文件的 sha256/大小 + 第二个句柄看到的库内容。
func observeAuthority(t *testing.T, files []string, dbPath string) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	hashes := make([]map[string]any, 0, len(files))
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取权威文件 %s 失败: %v", name, err)
		}
		sum := sha256.Sum256(body)
		hashes = append(hashes, map[string]any{
			"file":   filepath.Base(name),
			"bytes":  len(body),
			"sha256": hex.EncodeToString(sum[:]),
		})
	}
	data, err := json.MarshalIndent(hashes, "", "  ")
	if err != nil {
		t.Fatalf("序列化权威哈希失败: %v", err)
	}
	facts := map[string]any{"authorities": hashes}
	artifacts := map[string][]byte{"authority-hashes.json": data}
	if dbPath != "" {
		schemaArtifact, dump := dumpSchemaFile(t, dbPath)
		facts["dbTables"] = dump.Tables
		facts["schemaVersion"] = dump.SchemaVer
		artifacts["db-schema.json"] = schemaArtifact
	}
	writeStorageObservation(t, facts, artifacts)
}

// writeStorageObservation 合并写 observation.json，并覆盖写 artifact。
func writeStorageObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
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
