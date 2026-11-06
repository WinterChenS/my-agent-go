-- 007_artifacts：产物索引 + 每 run 的 usage/预算/失败结论 + run 事件日志（C07）。
--
-- 编号：迁移登记表把 007 分配给 C07。runner 按"已应用版本集合"逐版本补跑，
-- 所以本脚本**不能假设 003–006 已经跑过**：它只建自己的表和索引，不引用
-- 别人的表（也没有外键）——这里用 001 已建的 workspaces 之外的任何表都会
-- 让"C05 时期的旧库直接升到 007"失败（TestMigration007WithoutLaterVersions）。
--
-- 三张表各自的权威边界：
--
--   artifacts —— 产物**索引**，不是产物副本。
--     canonical_path 只是定位信息（locator），内容是工作区里的那个文件本身。
--     每次 GetArtifact 都重新核对磁盘 size/sha256，因此这一行从不被当作
--     "当前内容"的证据；sha256/size_bytes 记的是**登记时**的事实。
--     path_key 是大小写折叠后的路径键（Windows/macOS 默认大小写不敏感）：
--     唯一性判定用它，展示仍用 canonical_path 原样。
--
--   run_usage —— 每 run 的 usage/预算/失败结论。
--     数字列可空（NULL = 不知道），不用 0 冒充：usage_availability='unavailable'
--     时 input/output/total 都是 NULL，cost_usd 也是 NULL。provider 报告的真实 0
--     则是有值的 0——两者的区别必须能被读出来（RUN06 / ART03）。
--
--   run_events —— run 事件日志（不含 message.delta）。
--     产品事件在内存里只保留最近一段用于重连重放，进程重启后就没了；
--     诊断包要"故障可定位"，就必须有落盘的 eventId/时间/错误码/工具与审批轨迹。
--     载荷在写入与导出两侧都走 internal/redact；列里存的是**脱敏后**的 JSON。
--     没有外键：run 索引行可能因为历史写者缺失而不存在，事件日志不能因此丢。
--
-- 没有 ON DELETE CASCADE：删会话/删 run 是索引层的动作，产物与诊断记录属于
-- 审计轨迹，不跟着行消失（与 005 的软删除口径一致）。

CREATE TABLE IF NOT EXISTS artifacts (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    run_id         TEXT NOT NULL DEFAULT '',
    session_id     TEXT NOT NULL DEFAULT '',
    display_name   TEXT NOT NULL DEFAULT '',
    canonical_path TEXT NOT NULL,
    path_key       TEXT NOT NULL,
    sha256         TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL,
    media_type     TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

-- 同一工作区里的同一路径只登记一次：重新导入是更新，不是新增第二份。
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_path
    ON artifacts(workspace_id, path_key);
CREATE INDEX IF NOT EXISTS idx_artifacts_run
    ON artifacts(run_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_artifacts_workspace
    ON artifacts(workspace_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_artifacts_sha256
    ON artifacts(sha256);

CREATE TABLE IF NOT EXISTS run_usage (
    run_id                TEXT PRIMARY KEY,
    session_id            TEXT NOT NULL DEFAULT '',
    workspace_id          TEXT NOT NULL DEFAULT '',
    state                 TEXT NOT NULL DEFAULT '',
    pi_version            TEXT NOT NULL DEFAULT '',
    provider              TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL DEFAULT '',
    config_revision       INTEGER NOT NULL DEFAULT 0,
    last_seq              INTEGER NOT NULL DEFAULT 0,

    -- usage：availability 与数字分开。数字为 NULL 表示"没有这个数"。
    usage_availability    TEXT NOT NULL DEFAULT 'unavailable',
    input_tokens          INTEGER,
    output_tokens         INTEGER,
    total_tokens          INTEGER,
    cost_usd              REAL,
    price_source          TEXT NOT NULL DEFAULT '',
    estimated             INTEGER NOT NULL DEFAULT 0,

    -- 预算：限额与执行情况一起记，界面才能解释"为什么停"。
    max_tool_calls        INTEGER NOT NULL DEFAULT 0,
    max_duration_ms       INTEGER NOT NULL DEFAULT 0,
    tool_calls            INTEGER NOT NULL DEFAULT 0,
    budget_exceeded       INTEGER NOT NULL DEFAULT 0,
    budget_exceeded_reason TEXT NOT NULL DEFAULT '',

    -- 失败结论：错误码是机器可读的定位键，必须落盘。
    error_code            TEXT NOT NULL DEFAULT '',
    error_message         TEXT NOT NULL DEFAULT '',
    error_retryable       INTEGER NOT NULL DEFAULT 0,

    started_at            TEXT NOT NULL DEFAULT '',
    updated_at            TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_run_usage_session
    ON run_usage(session_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_run_usage_workspace
    ON run_usage(workspace_id, updated_at);

CREATE TABLE IF NOT EXISTS run_events (
    run_id            TEXT NOT NULL,
    seq               INTEGER NOT NULL,
    event_id          TEXT NOT NULL DEFAULT '',
    session_id        TEXT NOT NULL DEFAULT '',
    type              TEXT NOT NULL,
    occurred_at       TEXT NOT NULL,
    -- 脱敏后的 JSON 载荷；payload_truncated=1 表示这段载荷被截断过。
    payload           TEXT NOT NULL DEFAULT '',
    payload_truncated INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_run_events_run_type
    ON run_events(run_id, type, seq);
CREATE INDEX IF NOT EXISTS idx_run_events_occurred
    ON run_events(occurred_at);
