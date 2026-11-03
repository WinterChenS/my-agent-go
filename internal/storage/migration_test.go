package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"my-agent-go/internal/contracts"
)

func openMigrated(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "myagent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	appErr, ok := err.(*contracts.AppError)
	if !ok {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestMigrateCreatesSchemaAndIsIdempotent 覆盖 DB01：初建正确，重跑幂等。
func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)

	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("初始 schema 版本应为 1，实际 %d", v)
	}
	for _, table := range []string{"workspaces", "sessions", "runs", "memories", "memory_fts"} {
		var name string
		err := db.SQL().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("表 %s 未创建: %v", table, err)
		}
	}

	// 重跑：幂等，不报错、版本不变、不重复记录。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	var rows int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("统计迁移记录: %v", err)
	}
	if rows != len(mustMigrations(t)) {
		t.Fatalf("迁移记录数应为 %d，实际 %d", len(mustMigrations(t)), rows)
	}
}

func mustMigrations(t *testing.T) []Migration {
	t.Helper()
	ms, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	return ms
}

// TestMigrateFailureLeavesNoHalfState 覆盖 DB01：脚本中途失败必须整体回滚。
func TestMigrateFailureLeavesNoHalfState(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "myagent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.ensureMigrationTable(ctx); err != nil {
		t.Fatalf("建迁移表: %v", err)
	}

	bad := []Migration{{
		Version: 1,
		Name:    "001_bad.sql",
		// 前半句合法、后半句语法错误：验证不会留下"建了一半"的表。
		SQL: `CREATE TABLE half_created (id TEXT);
		      CREATE TABLE broken (;`,
	}}
	err = db.applyAll(ctx, bad)
	if err == nil {
		t.Fatalf("坏迁移应报错")
	}
	if _, ok := err.(*contracts.AppError); !ok {
		t.Fatalf("坏迁移应返回 AppError，实际 %v", err)
	}

	var name string
	scanErr := db.SQL().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name='half_created'`).Scan(&name)
	if scanErr == nil {
		t.Fatalf("失败迁移留下了半成品表 half_created")
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 0 {
		t.Fatalf("失败迁移不应推进 schema 版本，实际 %d", v)
	}

	// 失败后仍可正常应用真实迁移（不留锁/死事务）。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("失败迁移后应仍可正常迁移: %v", err)
	}
}

// TestOpenRejectsMemoryDB 覆盖 DB02：不允许偷偷换内存库。
func TestOpenRejectsMemoryDB(t *testing.T) {
	if _, err := Open(context.Background(), ":memory:"); err == nil {
		t.Fatalf("内存库应被拒绝")
	} else {
		requireCode(t, err, contracts.CodeUnsupported)
	}
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatalf("空路径应被拒绝")
	} else {
		requireCode(t, err, contracts.CodeConfigMissing)
	}
	db := openMigrated(t)
	// 落盘事实：数据库文件必须真实存在。
	if _, err := os.Stat(db.Path()); err != nil {
		t.Fatalf("数据库文件不存在: %v", err)
	}
}

// TestFTS5TrigramRealSupport 覆盖 DB02：锁定驱动真实支持 FTS5 trigram，中文可命中。
func TestFTS5TrigramRealSupport(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)

	// VerifyFTSTrigram 已在 Migrate 中执行过一次；这里再独立调用，确保它是真实验证。
	if err := db.VerifyFTSTrigram(ctx); err != nil {
		t.Fatalf("FTS5 trigram 不可用: %v", err)
	}

	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO memories(id, scope, workspace_id, status, content, created_at)
		VALUES ('m1','workspace','ws','active','每周整理项目周报与风险清单',datetime('now'))`); err != nil {
		t.Fatalf("插入记忆: %v", err)
	}
	if err := db.RebuildSearchIndex(ctx); err != nil {
		t.Fatalf("重建索引: %v", err)
	}
	hits, err := db.SearchMemories(ctx, "整理项目", 5)
	if err != nil {
		t.Fatalf("中文子串检索: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("中文 trigram 应命中 1 条，实际 %d", len(hits))
	}

	// 短查询按基线拒绝，不静默降级为全库扫描。
	if _, err := db.SearchMemories(ctx, "周报", 5); err == nil {
		t.Fatalf("少于 3 字符的查询应返回 QueryTooShort")
	} else {
		requireCode(t, err, contracts.CodeQueryTooShort)
	}
}

// TestRebuildIndexDoesNotTouchConfigOrTranscript 覆盖 DB03：
// 重建索引不改动配置 JSON，也不改动 Pi transcript。
func TestRebuildIndexDoesNotTouchConfigOrTranscript(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	configPath := filepath.Join(root, "config-user.json")
	configBody := []byte(`{"schemaVersion":1,"revision":3,"body":{"permissions":{"filesystem":"read"}}}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	transcriptPath := filepath.Join(root, "session.jsonl")
	transcriptBody := []byte("{\"type\":\"accepted\",\"id\":1}\n{\"type\":\"delta\",\"text\":\"hi\"}\n")
	if err := os.WriteFile(transcriptPath, transcriptBody, 0o600); err != nil {
		t.Fatalf("写 transcript: %v", err)
	}
	before := hash(configBody) + "|" + hash(transcriptBody)

	db, err := Open(ctx, filepath.Join(root, "myagent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO memories(id, scope, status, content, created_at)
		VALUES ('m1','user','active','重建索引不应改动其它权威数据',datetime('now'))`); err != nil {
		t.Fatalf("插入记忆: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := db.RebuildSearchIndex(ctx); err != nil {
			t.Fatalf("第 %d 次重建索引: %v", i+1, err)
		}
	}

	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("复读配置: %v", err)
	}
	transcriptAfter, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("复读 transcript: %v", err)
	}
	after := hash(configAfter) + "|" + hash(transcriptAfter)
	if after != before {
		t.Fatalf("重建索引改动了其它权威数据：before=%s after=%s", before, after)
	}
	// 索引本身仍可用。
	hits, err := db.SearchMemories(ctx, "重建索引不应", 5)
	if err != nil {
		t.Fatalf("重建后检索: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("重建后应仍能命中，实际 %d", len(hits))
	}
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
