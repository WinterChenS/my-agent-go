-- 008_browser：浏览器 profile / 登录态记录 / 下载作业（B06；BR15–BR17）。
--
-- 编号说明（必须记录，否则会被后来的复查当成"写错文件名"）：
--   分模块计划 `docs/superpowers/plans/2026-11-03-native-browser.md` 的 B06 写的是
--   `002_browser.sql`，但 002 已被 `002_memory.sql` 占用（那份脚本头部同样记录了
--   "计划写 003、实际 002 空闲"）。迁移 runner 按**版本号**记账：
--   `schema_migrations.version` 是主键，两个 version=2 的脚本会让后一个在
--   "002 已应用"的库上被静默跳过（`applyAll` 只看已应用版本集合）。所以这里用
--   下一个空闲编号 008，而不是硬凑 002 拼出一个"看起来对、实际不执行"的迁移。
--
-- 与 007 同一纪律：本脚本**不引用**其它迁移建的表（也没有外键）。runner 按版本
-- 集合补跑，所以不能假设 001–007 都已执行过；只建自己的表，旧库直接升到 008 也能成功。
--
-- 三张表各自的权威边界：
--
--   browser_profiles —— 一个 workspace 一行。
--     path 是**引擎的 user data folder**（WebView2/WKWebView/WebKitGTK 的 profile 目录）：
--     cookie、localStorage、缓存都是引擎写在那个目录里的字节，本表只记定位与归属，
--     不复制引擎私有格式。cleared_at 非空表示用户显式清过登录态——行保留
--     （便于解释"为什么又要登录"），目录里的字节已经没了。
--
--   browser_session_cookies —— 应用侧的登录态记录，**不是**引擎 cookie 库的副本。
--     用途有两个：非引擎 HTTP（下载要用会话 cookie）与"这个 workspace 现在有没有
--     登录态"的查询，都不必去解析引擎的私有存储。值只落在这里与内存，不写日志、
--     不进证据（证据只记 name/origin 与值的 sha256）。与引擎存储由同一个 profile_id
--     绑定：ClearProfile 同时清目录与这些行。
--
--   browser_downloads —— 下载作业（状态机 + 产物指针）。
--     staged_path 在受管下载根里（**不是**用户目录/工作区目录）：用户没选目标之前
--     不会写到任何工作区或家目录。final_path 只有 saved 状态才有值。
--     artifact_id 指向 007 的 artifacts 行；没有外键——索引行缺失时作业本身仍要可解释。

CREATE TABLE IF NOT EXISTS browser_profiles (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL UNIQUE,
    path         TEXT NOT NULL,
    ephemeral    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    -- 空串 = 从未显式清除；非空 = 那个时刻被清过（UTC 固定宽度格式）
    cleared_at   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_browser_profiles_workspace
    ON browser_profiles(workspace_id);

CREATE TABLE IF NOT EXISTS browser_session_cookies (
    profile_id   TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    origin       TEXT NOT NULL,
    name         TEXT NOT NULL,
    -- 同一 origin 下同名不同 path 是两条 cookie，所以主键要带 path。
    path         TEXT NOT NULL DEFAULT '/',
    value        TEXT NOT NULL,
    secure       INTEGER NOT NULL DEFAULT 0,
    http_only    INTEGER NOT NULL DEFAULT 0,
    -- 空串 = 会话 cookie（关掉引擎就没了）
    expires_at   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (profile_id, origin, name, path)
);

CREATE INDEX IF NOT EXISTS idx_browser_session_cookies_workspace
    ON browser_session_cookies(workspace_id, origin, name);

CREATE TABLE IF NOT EXISTS browser_downloads (
    id             TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL,
    workspace_id   TEXT NOT NULL,
    tab_id         TEXT NOT NULL DEFAULT '',
    url            TEXT NOT NULL,
    suggested_name TEXT NOT NULL,
    -- awaiting_destination | saved | canceled | failed
    state          TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    sha256         TEXT NOT NULL DEFAULT '',
    staged_path    TEXT NOT NULL DEFAULT '',
    final_path     TEXT NOT NULL DEFAULT '',
    artifact_id    TEXT NOT NULL DEFAULT '',
    error_code     TEXT NOT NULL DEFAULT '',
    error_message  TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_browser_downloads_session
    ON browser_downloads(session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_browser_downloads_state
    ON browser_downloads(state, updated_at);
