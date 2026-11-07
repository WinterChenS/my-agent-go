-- 006_search_index：全局搜索索引（U01）。
--
-- 编号：迁移登记表把 006 分配给 U01（2026-11-03-agent-client-master §数据库迁移登记）。
-- runner 按"已应用版本集合"逐版本补跑，所以本脚本不能假设 003–005、007 已经跑过：
-- 它只建自己的表，不访问别人的表，也不加外键。重建索引时才去读 sessions/artifacts，
-- 那时代码会自己判断"权威表在不在"。
--
-- 语义（SRCH01）：
--   - index 是**派生数据**：Search 从不写权威表，Rebuild 只读权威表；
--   - 索引未建立（search_meta 里没有 built_at）时 Search 必须回答 unavailable，
--     不能用"空结果"冒充"搜过了没有命中"；
--   - trigram 分词器与 C05 的记忆检索一致：中文按 3 字符滑窗切分，
--     小于 3 个字符的查询由服务层拒绝（QUERY_TOO_SHORT），不在这里降级成 LIKE。
--
-- 为什么把元数据（kind/route/workspace_id）放在 FTS 表的 UNINDEXED 列里：
-- 它们必须和命中行是同一行，任何"另一张表按 id 再查一次"的写法都会在重建
-- 中途读到半成品；UNINDEXED 列不参与分词，但仍可放进 WHERE 做 scope/kind 过滤，
-- 过滤发生在 ranking 与 LIMIT 之前（SRCH01 的"其它工作区的结果不出现"）。
--
-- 没有外键、没有 ON DELETE CASCADE：索引是派生数据，行消失只意味着"下次重建
-- 不再收录它"，不应该反过来约束权威表。

CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(
    title,
    subtitle,
    body,
    doc_id       UNINDEXED,
    kind         UNINDEXED,
    ref_id       UNINDEXED,
    workspace_id UNINDEXED,
    route        UNINDEXED,
    updated_at   UNINDEXED,
    tokenize = 'trigram'
);

-- 索引状态：built_at 为空/缺失 = 索引尚未建立（Search 返回 unavailable）。
-- 其余键是最近一次重建的逐类计数，供诊断确认"重建真的发生了"。
CREATE TABLE IF NOT EXISTS search_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
