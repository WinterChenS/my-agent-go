-- 003_connectors：连接器定义与追加式审计（C08a）。
--
-- 设计取舍：
--   - 定义落库而不是另发明一份 JSON 配置，因为"配置里只有 credential ref"
--     需要一个能被测试直接扫描的落点；数据库文件字节本身就是证据。
--   - revision 是 CAS 版本：并发编辑必须有一个失败，不能"后写者赢"。
--   - 审计只追加：这张表没有任何 UPDATE/DELETE 入口，触发器再兜一层，
--     防止将来有人图省事在 SQL 里改历史。
--
-- 这里不存任何凭证材料：credential_ref 只是一个引用名。

CREATE TABLE IF NOT EXISTS connector_definitions (
    id                TEXT PRIMARY KEY,
    kind              TEXT NOT NULL,
    display_name      TEXT NOT NULL,
    credential_ref    TEXT NOT NULL,          -- 只放 ref，绝不放开明文
    enabled           INTEGER NOT NULL DEFAULT 0,
    capabilities      TEXT NOT NULL DEFAULT '[]',  -- JSON 数组
    allowed_resources TEXT NOT NULL DEFAULT '[]',  -- JSON 数组（收件人/域等）
    rate_limit        TEXT NOT NULL DEFAULT '{}',  -- JSON 对象
    endpoints         TEXT NOT NULL DEFAULT '{}',  -- JSON 对象（不含密码）
    revision          INTEGER NOT NULL DEFAULT 1,
    updated_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS connector_audit (
    -- 不用 AUTOINCREMENT：它会额外建 sqlite_sequence 表，而索引侧测试要求
    -- "任何非迁移脚本声明的表"都不出现。append-only 由下面的触发器保证，
    -- 序号单调性不依赖 AUTOINCREMENT。
    seq             INTEGER PRIMARY KEY,
    at              TEXT NOT NULL,
    connector_id    TEXT NOT NULL,
    action_id       TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT '',
    decision        TEXT NOT NULL DEFAULT '',   -- allow | deny | ask | n/a
    outcome         TEXT NOT NULL DEFAULT '',   -- done/failed/outcome_unknown/denied/...
    reason          TEXT NOT NULL DEFAULT '',
    credential_ref  TEXT NOT NULL DEFAULT '',   -- 只放 ref
    idempotency_key TEXT NOT NULL DEFAULT '',
    approval_id     TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    run_id          TEXT NOT NULL DEFAULT '',
    tool_name       TEXT NOT NULL DEFAULT '',
    detail          TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_connector_audit_connector
    ON connector_audit(connector_id, seq);

-- 审计是追加式的：这里用触发器把"改/删审计"变成数据库层面的错误，
-- 而不是只靠代码自觉。
CREATE TRIGGER IF NOT EXISTS connector_audit_no_update
BEFORE UPDATE ON connector_audit
BEGIN
    SELECT RAISE(ABORT, 'connector_audit is append-only');
END;

CREATE TRIGGER IF NOT EXISTS connector_audit_no_delete
BEFORE DELETE ON connector_audit
BEGIN
    SELECT RAISE(ABORT, 'connector_audit is append-only');
END;
