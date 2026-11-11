# ADR 0008：Rust 迁移后的验收平台仍是 win-x64 / Windows 10 22H2

- 日期：2026-11-11
- 状态：已采纳
- 取代：无（确认并延续 [ADR 0005](0005-narrow-acceptance-to-win-x64-windows-10.md)）
- 影响任务：R0-RS03（Rust 门禁）、R0-RS04（删除 Go 后的全量复验）、GOV05 的 Rust 实现

## 背景

ADR 0005 已把本轮验收收敛为 `win-x64`、最低系统 Windows 10 22H2（`minOSVersion="10"`、
`minOSBuild=19045`），并写明"本机因此可以产出验收级证据"。ADR 0007 换成 Rust + Tauri 后，
两处文档没有跟着改：

- `AGENTS.md` §4 仍写着"本机是 Windows 10 22H2，低于基线最低验收系统 Windows 11 24H2，
  本机证据只是开发证据"；
- `docs/acceptance/v1-external-dependencies.md` 仍把"Windows 11 24H2 验收机"列为缺失外部依赖。

这两条与 ADR 0005 冲突，且实际效果是**任何** win-x64 结论都无法成立。用户 2026-11-11 明确：
"当前 Windows 就可以作为验收"。

## 决策

1. 本轮 win-x64 的验收平台就是本机 Windows 10 22H2（build 19045），本机产生的证据是
   **验收证据**，不再标注为开发证据。
2. Rust 门禁实现 GOV05 时按 `minOSBuild >= 19045`（同族版本 `10.0.x`）判定，不得沿用
   Windows 11 24H2 / build 26100 的旧基线；低于 19045 或其他架构的证据仍拒绝。
3. 其余五个目标维持 ADR 0005 的 deferred 语义：既不缺失、也不通过，不得据此宣称跨平台。
4. 证据里必须如实记录 `os.name`/`os.version`/`os.build`/`arch`（Rust devtools 已从注册表读取，
   `result.json` 的 `os` 字段就是这台机器的真实值）；把 Win10 当验收平台**不允许**顺手改这些字段。

## 后果

- 本机可以给出 win-x64 的验收结论与门禁判定，判死理由只剩真实功能缺口。
- `AGENTS.md` §4 与 `v1-external-dependencies.md` 已按本 ADR 修正。
- 回复用户时不得再说"这只是开发证据"；也不得因为平台合规就跳过证据本身（PASS 仍由证据确定）。
- 恢复 Windows 11 基线的方式：撤本 ADR 与 ADR 0005，改回 `26100` 并重跑全部 win-x64 证据。

## 为什么不选其它方案

| 方案 | 不选的原因 |
|---|---|
| 继续要求 Windows 11 24H2 机器 | 用户明确否决；本机已能覆盖 win-x64 的全部功能与落盘验收，机器不构成风险 |
| 只在口头说明里改，不动文档 | 下次会话仍会读到旧口径，等于没有决定 |
| 顺手把 `os.build` 写成 26100 以"符合基线" | 伪造证据，违反 AGENTS.md §6 |
