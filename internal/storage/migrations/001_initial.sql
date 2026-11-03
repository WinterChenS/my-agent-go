-- 001_initial：workspace / session / run 索引 + 记忆与检索。
--
-- 权威边界（基线 §5）：本库是索引与结构化记录权威，不复制配置 JSON，
-- 也不复制 Pi transcript 的内容——只保存定位信息。
--
-- 记忆检索使用 FTS5 trigram（中文按 3 字符 trigram 切分）；驱动不支持时
-- 迁移必须失败，而不是退化成 LIKE 扫描（DB02）。

CREATE TABLE IF NOT EXISTS workspaces (
    id          TEXT PRIMARY KEY,
    root_path   TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_workspace ON sessions(workspace_id);

CREATE TABLE IF NOT EXISTS runs (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    state           TEXT NOT NULL,
    last_seq        INTEGER NOT NULL DEFAULT 0,
    config_revision INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_session ON runs(session_id);

CREATE TABLE IF NOT EXISTS memories (
    id           TEXT PRIMARY KEY,
    scope        TEXT NOT NULL,              -- user | workspace
    workspace_id TEXT,
    status       TEXT NOT NULL,              -- candidate | active | deleted
    content      TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_memories_scope_status ON memories(scope, status);

-- 独立 FTS5 表（非 external content）：索引由 RebuildSearchIndex 显式重建，
-- 这样"重建索引"是可观察、可重复的动作，不会顺手改动 memories 本体。
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
    content,
    tokenize = 'trigram'
);
