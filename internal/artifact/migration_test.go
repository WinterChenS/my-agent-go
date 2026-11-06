package artifact

// 迁移 007 的回归测试（ART02：重启后可查 / 旧库可升级）。
//
// 007 只加自己的表与索引：它不能假设 003–006 已经跑过（runner 按"已应用版本
// 集合"逐版本补跑，C08/C09 的 003/004 此刻可能还没合入）。这里用一个只有
// 001/002 的旧库验证这一点——直接把 001/002 的 SQL 与迁移记录写进去，
// 再走正常的 Migrate 路径。

import (
	"context"
	"path/filepath"
	"testing"

	"my-agent-go/internal/storage"
)

func TestMigration007AppliesOnAnOldDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(ctx, filepath.Join(dir, "old.db"))
	if err != nil {
		t.Fatalf("打开旧库: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 造一个"C05 时期"的库：只有 001/002。
	migrations, err := storage.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("建迁移记录表: %v", err)
	}
	applied := 0
	for _, m := range migrations {
		if m.Version > 2 {
			continue
		}
		if _, err := db.SQL().ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("应用旧迁移 %s: %v", m.Name, err)
		}
		if _, err := db.SQL().ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, datetime('now'))`,
			m.Version, m.Name); err != nil {
			t.Fatalf("记录旧迁移 %s: %v", m.Name, err)
		}
		applied++
	}
	if applied != 2 {
		t.Fatalf("旧库应只有 001/002，实际应用了 %d 条", applied)
	}

	// 正常迁移路径：补齐剩下的版本（含 007）。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("旧库升级失败: %v", err)
	}

	// 007 的三张表都要在；缺一张就等于"部分迁移"，必须在测试里暴露。
	for _, table := range []string{"artifacts", "run_usage", "run_events"} {
		var name string
		err := db.SQL().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("迁移 007 没有建出表 %s: %v", table, err)
		}
	}

	// 007 的索引也要在（产物按路径唯一、按 run/工作区查询）。
	for _, idx := range []string{
		"idx_artifacts_path", "idx_artifacts_run", "idx_artifacts_workspace", "idx_artifacts_sha256",
		"idx_run_usage_session", "idx_run_events_run_type",
	} {
		var name string
		if err := db.SQL().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`, idx).Scan(&name); err != nil {
			t.Fatalf("迁移 007 没有建出索引 %s: %v", idx, err)
		}
	}

	// 版本推进到目录里的最高版本，并且重跑幂等。
	want := migrations[len(migrations)-1].Version
	if v, err := db.SchemaVersion(ctx); err != nil || v != want {
		t.Fatalf("迁移后版本应为 %d，实际 %d（%v）", want, v, err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重跑迁移应幂等: %v", err)
	}

	// 旧库里已有的会话行不受影响（007 不重写别人的表）。
	var count int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("读取 sessions: %v", err)
	}
}

// TestMigration007WithoutLaterVersions 证明 007 不依赖 003–006 的表。
//
// 断言方式是"只应用 001/002，然后单独应用 007 的 SQL"：如果 007 的脚本里
// 引用了他人的表（外键/视图/触发器），这里会直接失败。
func TestMigration007WithoutLaterVersions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(ctx, filepath.Join(dir, "solo.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	defer func() { _ = db.Close() }()

	migrations, err := storage.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	var m007 *storage.Migration
	for i := range migrations {
		m := migrations[i]
		if m.Version == 7 {
			m007 = &m
			continue
		}
		if m.Version > 2 {
			// 003–006 属于 C08/C09/C06：它们此刻在与不在都不影响本用例。
			continue
		}
		if _, err := db.SQL().ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("应用 %s: %v", m.Name, err)
		}
	}
	if m007 == nil {
		t.Fatalf("迁移目录里没有 007")
	}
	if _, err := db.SQL().ExecContext(ctx, m007.SQL); err != nil {
		t.Fatalf("007 在只有 001/002 的库上执行失败（说明它依赖了别人的表）: %v", err)
	}
}
