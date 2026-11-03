package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"my-agent-go/internal/contracts"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migration 是一个待应用的迁移脚本。
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// LoadMigrations 读取内嵌迁移脚本，按版本号升序返回。
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取迁移目录失败: "+err.Error(), false)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取迁移脚本失败: "+e.Name(), false)
		}
		out = append(out, Migration{Version: version, Name: e.Name(), SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// parseMigrationVersion 从 "001_initial.sql" 解析出 1。
func parseMigrationVersion(name string) (int, error) {
	base := strings.SplitN(name, "_", 2)[0]
	v, err := strconv.Atoi(base)
	if err != nil || v <= 0 {
		return 0, contracts.NewAppError(contracts.CodeInternal, "迁移文件名必须以数字版本开头: "+name, false)
	}
	return v, nil
}

// SchemaVersion 返回当前已应用的最高迁移版本；空库为 0。
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	if err := db.ensureMigrationTable(ctx); err != nil {
		return 0, err
	}
	var v sql.NullInt64
	err := db.sql.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, contracts.NewAppError(contracts.CodeInternal, "读取 schema 版本失败: "+err.Error(), false)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// Migrate 应用所有未执行的迁移（DB01）。
//
// 每个迁移在**自己的事务**里执行：脚本失败即回滚，不留半迁移状态；
// 已应用的版本被跳过，重复运行幂等。
func (db *DB) Migrate(ctx context.Context) error {
	if err := db.ensureMigrationTable(ctx); err != nil {
		return err
	}
	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}
	if err := db.applyAll(ctx, migrations); err != nil {
		return err
	}
	// 迁移完成后立即验证 FTS5 trigram 可用：驱动不支持就在这里失败，
	// 不允许带着坏索引继续跑（DB02）。
	return db.VerifyFTSTrigram(ctx)
}

// applyAll 按版本顺序应用未执行的迁移。独立成方法，便于用注入的脚本验证
// "失败迁移不留半状态"——真实迁移脚本都是好的，负例只能靠注入。
func (db *DB) applyAll(ctx context.Context, migrations []Migration) error {
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}
	sorted := make([]Migration, len(migrations))
	copy(sorted, migrations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	for _, m := range sorted {
		if applied[m.Version] {
			continue
		}
		if err := db.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) ensureMigrationTable(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建迁移记录表失败: "+err.Error(), false)
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取已应用迁移失败: "+err.Error(), false)
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取迁移版本失败: "+err.Error(), false)
		}
		out[v] = true
	}
	return out, rows.Err()
}

func (db *DB) applyMigration(ctx context.Context, m Migration) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "开启迁移事务失败: "+m.Name+" : "+err.Error(), false)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return contracts.NewAppError(
			contracts.CodeInternal,
			fmt.Sprintf("迁移 %s 执行失败（已回滚，不留半迁移）: %v", m.Name, err),
			false,
		)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, datetime('now'))`,
		m.Version, m.Name); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "记录迁移版本失败: "+m.Name+" : "+err.Error(), false)
	}
	if err := tx.Commit(); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "提交迁移失败: "+m.Name+" : "+err.Error(), false)
	}
	return nil
}

// VerifyFTSTrigram 验证锁定驱动真的支持 FTS5 trigram：既检查编译选项，
// 也做一次真实的 trigram 命中查询（中文 3 字以上）。
func (db *DB) VerifyFTSTrigram(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`)
	if err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "无法查询 SQLite 编译选项: "+err.Error(), false)
	}
	var enabled int
	if rows.Next() {
		if err := rows.Scan(&enabled); err != nil {
			rows.Close()
			return contracts.NewAppError(contracts.CodeUnsupported, "读取编译选项失败: "+err.Error(), false)
		}
	}
	rows.Close()
	if enabled != 1 {
		return contracts.NewAppError(contracts.CodeUnsupported, "当前 SQLite 构建未启用 FTS5，拒绝继续", false)
	}

	// 真实命中测试：trigram 对中文按字符三元组切分。查询串必须是文本的真实子串，
	// 否则测到的是"查不存在的词"，而不是分词器可用性。
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "开启 trigram 验证事务失败: "+err.Error(), false)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS temp.trigram_probe USING fts5(content, tokenize='trigram')`); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "FTS5 trigram 分词器不可用: "+err.Error(), false)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO temp.trigram_probe(content) VALUES('每周整理项目周报与风险清单')`); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "trigram 索引写入失败: "+err.Error(), false)
	}
	var hits int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM temp.trigram_probe WHERE trigram_probe MATCH '整理项目'`).Scan(&hits); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "trigram 查询失败: "+err.Error(), false)
	}
	if hits == 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "trigram 未命中中文子串，视为不可用", false)
	}
	return nil
}
