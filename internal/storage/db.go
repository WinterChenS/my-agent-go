// Package storage 是 workspace/session/run/记忆 等索引与结构化记录的权威存储（基线 §5）。
//
// 权威边界：SQLite 只承载索引与结构化记录；配置 JSON 与 Pi transcript 各自是
// 独立权威，本包不得改写它们（DB03）。
package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"

	_ "modernc.org/sqlite" // 纯 Go 驱动，无需 cgo；锁定版本见 build/versions.lock.json
)

// DriverName 是 modernc.org/sqlite 注册的驱动名。
const DriverName = "sqlite"

// DB 是存储句柄。它永远绑定到一个真实文件路径——不存在"悄悄换成内存库"的降级路径。
type DB struct {
	sql  *sql.DB
	path string
}

// Open 打开（必要时创建）指定路径的数据库。
//
// 明确拒绝内存库：DB02 要求锁/权限错误可诊断，而不是偷偷换内存 DB 让测试变绿。
func Open(ctx context.Context, path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "数据库路径为空", false)
	}
	if path == ":memory:" || strings.Contains(path, "mode=memory") {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "不允许内存数据库：v1 必须落盘到受管路径", false)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "解析数据库路径失败: "+err.Error(), false)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建数据库目录失败: "+abs+" : "+err.Error(), false)
	}

	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	sqlDB, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "打开数据库失败: "+abs+" : "+err.Error(), false)
	}
	// 单连接即可满足迁移与索引写入的顺序性；并发读由上层排队。
	sqlDB.SetMaxOpenConns(1)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, contracts.NewAppError(
			contracts.CodeInternal,
			"数据库不可用（锁占用或权限不足）: "+abs+" : "+err.Error(),
			true,
		)
	}
	return &DB{sql: sqlDB, path: abs}, nil
}

// Path 返回数据库文件的绝对路径。
func (db *DB) Path() string { return db.path }

// Close 关闭数据库。
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

// SQL 暴露底层句柄，供同包与上层仓储使用。
func (db *DB) SQL() *sql.DB { return db.sql }

// RebuildSearchIndex 从 memories 重建 FTS 索引（DB03 的"重建索引"路径）。
//
// 它只动索引表，不触碰配置 JSON，也不触碰 Pi transcript。
func (db *DB) RebuildSearchIndex(ctx context.Context) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "开启重建事务失败: "+err.Error(), false)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts`); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "清空检索索引失败: "+err.Error(), false)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_fts(rowid, content)
		SELECT rowid, content FROM memories WHERE status = 'active'`); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "重建检索索引失败: "+err.Error(), false)
	}
	if err := tx.Commit(); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "提交重建事务失败: "+err.Error(), false)
	}
	return nil
}

// SearchMemories 用 FTS5 trigram 检索；少于 3 个字符的查询按基线返回 QueryTooShort。
func (db *DB) SearchMemories(ctx context.Context, query string, limit int) ([]string, error) {
	if len([]rune(query)) < 3 {
		return nil, contracts.NewAppError(contracts.CodeQueryTooShort, "检索关键词至少 3 个字符", false)
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT m.content FROM memory_fts f
		JOIN memories m ON m.rowid = f.rowid
		WHERE memory_fts MATCH ? AND m.status = 'active'
		ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "检索失败: "+err.Error(), false)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取检索结果失败: "+err.Error(), false)
		}
		out = append(out, content)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "检索结果异常: "+err.Error(), false)
	}
	return out, nil
}
