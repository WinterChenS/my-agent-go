-- 002_memory：补足记忆表字段 + FTS5 trigram 索引与按 scope 的注入开关。
--
-- 编号说明：分模块计划里写的是 003_memory.sql；实际 002 空闲，故用 002，
-- 下一个可用编号是 003。
--
-- 001 已经建过 memories 与 memory_fts。本迁移只做增量：
--   - kind / source_session_id / updated_at（计划里 Memory 的字段）；
--   - workspace 维度的索引（跨项目检索必须按 workspace_id 先过滤）；
--   - memory_scope_settings：按 scope 关闭注入（不是删除条目）；
--   - memory_fts 仍然必须是 trigram 分词器。这里用 IF NOT EXISTS 兜底，
--     但真正的校验在 internal/memory 的启动探测里（不可用即拒绝启动），
--     本迁移不提供任何 LIKE 降级。

ALTER TABLE memories ADD COLUMN kind TEXT NOT NULL DEFAULT 'fact';
ALTER TABLE memories ADD COLUMN source_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memories ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';

-- 旧行没有更新过：把 updated_at 回填成 created_at，避免导出出现空时间。
UPDATE memories SET updated_at = created_at WHERE updated_at = '';

CREATE INDEX IF NOT EXISTS idx_memories_workspace_scope_status
    ON memories(workspace_id, scope, status);

CREATE TABLE IF NOT EXISTS memory_scope_settings (
    scope        TEXT NOT NULL,           -- user | workspace
    workspace_id TEXT NOT NULL DEFAULT '',-- user 恒为空
    enabled      INTEGER NOT NULL DEFAULT 1,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (scope, workspace_id)
);

-- 001 已建；这里重复声明是为了让本迁移单独可读，并保证索引定义存在。
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
    content,
    tokenize = 'trigram'
);
