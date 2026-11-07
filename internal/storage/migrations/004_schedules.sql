-- 004_schedules：本地持久定时任务的定义与运行 occurrence（C09）。
--
-- 编号：迁移登记表把 004 分配给 C09。runner 按"已应用版本集合"逐版本补跑，
-- 所以本迁移**不能**假设 003/005/007 已经跑过：它只建自己的两张表，
-- 不改 sessions/runs/connectors/artifacts 的任何列，也不引用它们的外键。
--
-- 设计取舍：
--   - schedule_occurrences 的主键是 (schedule_id, scheduled_at)，这就是
--     "时钟回跳 / 进程重启 / DST 两侧"只触发一次的地基：调度器每次触发都先
--     插一条 occurrence，插不进去就说明这一次已经发生过，绝不重放。
--   - 删除是**软删除**（deleted_at）。任务删掉之后，它已经产生的 run、会话、
--     产物都还在（SCH07），occurrence 行也还在，证据不会随任务一起消失。
--   - revision 是 CAS 版本：并发编辑必须有一个失败。调度器自己的记账
--     （last/next/current/failures）**不**推进 revision，否则用户每次打开
--     编辑框都可能因为后台 tick 而冲突；自动暂停是唯一由调度器发起的 CAS。
--   - schedule_occurrence_events 是只追加的状态轨迹：occurrence 的当前状态
--     从真实 run 事件投影而来，轨迹让"它到底走过哪几步"可被测试与取证。
--
-- 时间列统一是固定宽度的 UTC 文本（"2006-01-02T15:04:05.000000000Z"）：
-- scheduled_at 既参与唯一键比较，也参与排序，宽度不固定会让字典序不等于时间序。

CREATE TABLE IF NOT EXISTS schedules (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL DEFAULT '',
    cron                 TEXT NOT NULL,
    time_zone            TEXT NOT NULL,
    workspace_id         TEXT NOT NULL,
    profile_id           TEXT NOT NULL DEFAULT '',
    prompt               TEXT NOT NULL,
    permission_ref       TEXT NOT NULL DEFAULT '',
    output_target        TEXT NOT NULL DEFAULT '',
    misfire_policy       TEXT NOT NULL DEFAULT 'catch_up',
    catch_up_limit       INTEGER NOT NULL DEFAULT 1,
    enabled              INTEGER NOT NULL DEFAULT 1,
    revision             INTEGER NOT NULL DEFAULT 1,
    last_scheduled_at    TEXT NOT NULL DEFAULT '',
    next_scheduled_at    TEXT NOT NULL DEFAULT '',
    current_session_id   TEXT NOT NULL DEFAULT '',
    current_run_id       TEXT NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    deleted_at           TEXT NOT NULL DEFAULT '',
    CHECK (misfire_policy IN ('catch_up', 'skip')),
    CHECK (catch_up_limit >= 0)
);

CREATE INDEX IF NOT EXISTS idx_schedules_wakeup
    ON schedules(enabled, deleted_at, next_scheduled_at);

CREATE TABLE IF NOT EXISTS schedule_occurrences (
    schedule_id  TEXT NOT NULL REFERENCES schedules(id),
    scheduled_at TEXT NOT NULL,
    state        TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    run_id       TEXT NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (schedule_id, scheduled_at),
    CHECK (state IN ('queued','running','waiting_approval','completed','failed',
                     'cancelled','skipped','outcome_unknown'))
);

CREATE INDEX IF NOT EXISTS idx_schedule_occurrences_run
    ON schedule_occurrences(run_id);
CREATE INDEX IF NOT EXISTS idx_schedule_occurrences_state
    ON schedule_occurrences(state, scheduled_at);

CREATE TABLE IF NOT EXISTS schedule_occurrence_events (
    -- 不用 AUTOINCREMENT：append-only 的单调性由自增 rowid 提供即可，
    -- 额外的 sqlite_sequence 表只会让"库里有哪些表"更难解释。
    seq          INTEGER PRIMARY KEY,
    schedule_id  TEXT NOT NULL,
    scheduled_at TEXT NOT NULL,
    at           TEXT NOT NULL,
    state        TEXT NOT NULL,
    previous     TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    run_id       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_schedule_occurrence_events_key
    ON schedule_occurrence_events(schedule_id, scheduled_at, seq);

-- 轨迹只追加：把"改历史"变成数据库层面的错误，而不是只靠代码自觉。
CREATE TRIGGER IF NOT EXISTS schedule_occurrence_events_no_update
BEFORE UPDATE ON schedule_occurrence_events
BEGIN
    SELECT RAISE(ABORT, 'schedule_occurrence_events is append-only');
END;

CREATE TRIGGER IF NOT EXISTS schedule_occurrence_events_no_delete
BEFORE DELETE ON schedule_occurrence_events
BEGIN
    SELECT RAISE(ABORT, 'schedule_occurrence_events is append-only');
END;
