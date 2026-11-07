# ADR 0005：本轮验收平台收敛为 win-x64 / Windows 10 22H2

- 日期：2026-11-06
- 状态：已采纳（本轮范围，可被后续 ADR 取代）
- 影响任务：F01 门禁矩阵、G1 原生矩阵、X03/X04 打包与发布口径
- 相关文件：`build/platforms.json`、`internal/devtools/{gate,platforms,native}.go`、`tests/acceptance/cases.json`、`docs/acceptance/v1-gates.md`

## 背景

基线把六目标（win-x64/win-arm64/mac-x64/mac-arm64/linux-x64/linux-arm64）都列为 V1 验收范围，并把
win-x64 的最低验收系统定为 Windows 11 24H2（build 26100）。本机是 Windows 10 22H2（build 19045），
因此 GOV05 会拒绝本机产生的**全部** win-x64 证据：`gate --id=G0` 的 42 条拒绝全部是这一条，
即便功能与证据本身都成立。

用户 2026-11-06 明确要求：这一轮只需要 Windows 10。

## 决策

1. **本轮验收目标只有 `win-x64`**，最低系统为 Windows 10 22H2（`minOSVersion="10"`、`minOSBuild=19045`）。
   本机因此可以产出**验收级**证据。
2. 其余五个目标保留在 `build/platforms.json` 里，但 `active=false` 且必须写明 `deferredBy`。
   它们的必需位在 gate 报告里逐条列为 **deferred**：既不算缺失、也不算通过。
3. `scope=ALL`/`NATIVE` 只展开到 active 目标；`LNX`/`Windows两架构`/`macOS两架构` 与 active 集合求交。
   交集为空时该 case 只剩 deferred 记录，不允许静默消失。
4. 某个 gate 的必需位**全部**落在停用目标上时，该 gate 判**非通过**（`exit 2`）并说明原因：
   "没有可评估的证据面"不是"通过"。

## 后果

- 本机能给出 win-x64 的验收结论；G0/G1/G2 的判死理由只剩真实缺口，不再有"宿主系统偏低"。
- **不得**据此宣称六目标或跨平台支持。发布说明、安装包与 X03/X04 的口径必须写明：本轮只交付/验证 win-x64。
- 停用目标上的代码路径（tar.gz 解包、POSIX `#!`、WKWebView/WebKitGTK、Linux X11/Wayland 双会话）本轮**没有**
  端到端证据；恢复这些目标时必须先撤本 ADR 并重跑对应矩阵。
- 恢复方式：把目标改回 `active=true`、把 `minOSVersion/minOSBuild` 调回基线值，并重跑该目标的全部证据。
  任何"只把 active 改回 true 而不补证据"的做法都会被 gate 判死（缺必需位）。

## 为什么不选其它方案

| 方案 | 不选的原因 |
|---|---|
| 保留 Win11 最低线、把本机证据标成开发证据 | 本轮永远无法得到任何验收结论，功能完成度无法度量 |
| 只改 win-x64 最低线、保留六目标必需位 | 本机永远缺另外五个目标的证据，gate 仍恒为 exit 2，等于没解决 |
| 直接删掉五个目标 | 丢掉"这些目标曾计划验收、本轮被停用"的事实；以后恢复时无从对照 |
| 跳过 gate 直接宣布通过 | 违反 AGENTS.md §6：PASS 由证据确定，不由"看起来能用"确定 |
