-- 005_session_catalog：会话目录字段（C06）。
--
-- 编号：迁移登记表把 005 分配给 C06（agent-client-master §数据库迁移登记）。
-- runner 按"已应用版本集合"逐版本补跑，所以 003（C08 连接器）与 004（C09 调度）
-- 此刻还没合入也不影响本迁移：它只改 001 已经建好的 sessions 表，
-- 不新建第二张会话表，也不重写既有列。
--
-- 语义：
--   - title / pinned / archived / updated_at / revision 是目录字段；
--   - deleted_at 是软删除标记（计划里的"删除为软删除标记"）。保留行等于保留
--     transcript 定位键（Pi transcript 是独立权威，只读定位靠 session id），
--     也保证 runs 等子行不被 ON DELETE CASCADE 连带删除；
--   - kind 区分普通会话（chat）与自动化/调度会话。目录只列普通会话，
--     没有这一列就无法把"用户在聊天页开的会话"与"调度器开的会话"分开，
--     而计划明确要求目录只列普通会话。C09 写自己的 kind，值由它自己登记。
--
-- updated_at 存的是**固定宽度**的 UTC 时间戳（"2006-01-02T15:04:05.000000000Z"）。
-- 顺序（pinned DESC, updated_at DESC, id DESC）依赖文本比较，而 RFC3339Nano 会
-- 裁掉尾随 0："...:00Z" 与 "...:00.5Z" 的字典序与时间序相反。下面把历史行
-- （history.RecordRun 用 RFC3339Nano 写的 created_at）补齐 9 位小数，
-- 让同一列里只有一种宽度。

ALTER TABLE sessions ADD COLUMN title      TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN pinned     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN archived   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN updated_at TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN revision   INTEGER NOT NULL DEFAULT 1;
ALTER TABLE sessions ADD COLUMN deleted_at TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN kind       TEXT    NOT NULL DEFAULT 'chat';

-- 历史行（001/002 时期写入）没有 updated_at：回填 created_at，并把小数位补齐到
-- 9 位，避免同一列里两种宽度混排导致字典序 ≠ 时间序。
UPDATE sessions
   SET updated_at = substr(created_at, 1, 19) || '.' ||
                    substr(substr(created_at, 21, instr(created_at, 'Z') - 21) || '000000000', 1, 9) || 'Z'
 WHERE updated_at = ''
   AND instr(created_at, '.') > 0
   AND substr(created_at, -1) = 'Z';

UPDATE sessions
   SET updated_at = substr(created_at, 1, 19) || '.000000000Z'
 WHERE updated_at = ''
   AND instr(created_at, '.') = 0
   AND substr(created_at, -1) = 'Z';

-- 非 UTC/非 Z 结尾的 created_at 不在这里硬转：按原样保留，
-- 读取侧按同一族格式解析（见 internal/session 的时间解析）。
UPDATE sessions SET updated_at = created_at WHERE updated_at = '';

-- 目录查询的排序索引：kind/deleted_at/workspace_id 是等值过滤，
-- 其余三列升序索引反向扫描即得 pinned DESC, updated_at DESC, id DESC。
CREATE INDEX IF NOT EXISTS idx_sessions_catalog_order
    ON sessions(kind, deleted_at, workspace_id, pinned, updated_at, id);
