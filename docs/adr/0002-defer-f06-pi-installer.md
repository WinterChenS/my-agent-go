# ADR 0002：推迟 F06（Pi 安装器），并把 INS01–INS07 从 G2 移到 G4

- 日期：2026-11-04
- 状态：**范围部分已被取代**（2026-11-05）。用户批准的 `docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md` 把 F06 改为 V1 核心，INS01–07 保留在 G4，并新增 G2 的 PI_INS/PI_UPG 原子 case。**本 ADR 的本机安全约束继续有效**：开发机上只写临时托管根，不碰 `.tools/pi/`、`~/.pi` 与真实数据目录（见 `docs/superpowers/plans/2026-11-05-pi-lifecycle-updater.md`）。**顺序未完全保留**：PI_INS/PI_UPG 在 G2，F06 只能排在 G2 阶段后端功能之后、UI 集成之前；若要排到全部功能之后，需要用户决定把 PI_INS/PI_UPG 移回 G4。原状态：已采纳（2026-11-04 用户确认，要求先复核）
- 影响任务：F06、C06、C07、X01，门禁 G2 / G3 / G4
- 需求追踪：R03（安装与升级）

## 背景

用户 2026-11-04 的指令：

> F06 先不需要处理，后面等所有的功能完成，才需要处理，因为会影响现在电脑上的 pi 的功能

F06 是「Pi 一键安装、升级与回滚」。它会在用户机器上写入 Pi 的安装目录、
改写 active 指针、并可能清理旧版本。当前这台开发机同时被三种东西使用：

| 使用者 | 路径 |
|---|---|
| 本仓库的 `.tools/pi/v1.5.0` | 所有真实 Pi 集成测试的被执行体 |
| 用户全局 `~/.pi` | 用户自己的 Pi 会话 |
| 应用隔离 agentDir | `MYAGENT_HOME` 下的 `config/agents/<ws>-<profile>` |

`activate()` 的语义（INS05）是「保留 1 个旧版可回滚」，落地必然要移动/替换目录。
在功能开发期间引入这个动作，风险是**为了让安装器自测通过而破坏现有 Pi 环境**，
且失败模式不容易在事后发现（旧版被清掉时就无法回滚）。

## 观测到的事实：依赖链会死锁

现有计划与验收矩阵形成一条闭环：

```text
cases.json: INS01–INS07  phase=G2  →  G2 必需集合包含 INS（v1-gates.md §3）
plan-index: C06 dependsOn [C01, F05, F04, C02, G2]
            C07 dependsOn [C03, C04, C05, C06, F05, F07]
            X01 dependsOn [C01..C07, B07, G2]
            G3  dependsOn [G2, C01, C06, C07, B07, X01, X02]
```

若 F06 推迟而 INS 留在 G2：

- G2 **永远无法关闭** → C06 不允许开工 → C07 / X01 / G3 全部阻塞
- 结果与用户意图（先把功能做完）直接冲突：功能永远做不完

master plan「变更与停止条件」明确：受影响 gate 保持 BLOCKED，独立任务可以继续。
但这里不是「某个 case 暂时缺证据」，而是**必需集合本身与执行顺序矛盾**，
必须改集合，不能靠豁免。

## 决策

1. **F06 推迟到全部功能完成之后**，在 `foundation-runtime.md` 标注延后，
   保留任务与需求（R03）不删除。
2. **INS01–INS07 的 phase / gates 从 G2 改为 G4**，即安装器的自测与安装包验收
   放在同一阶段。理由：G4 本来就是「六目标安装包在干净目标环境安装、升级/恢复、
   启动使用」，INS 的断言（hash 一致、跨盘解压拒绝、激活中断可恢复、外部 Pi 不可覆盖）
   在那里的语义更完整，也不会为了过 G2 而提前动真机目录。
3. **G2 对 Pi 来源的要求降为一条前提记录**：本轮 Pi 为外部已装，
   记录精确版本与 hash（`build/versions.lock.json` 已有），不要求自研安装器。

## 影响

| 项 | 影响 |
|---|---|
| V1 范围 | **不变**。R03 仍在 G4 验收，F06 仍是必须交付项 |
| G2 | 必需集合少 7 条（INS）；其余 CFG/DB/RUN/MOD/RES/MCP/MEM/SEC/BR 不变 |
| G4 | 必需集合多 7 条（INS）；PKG/PERF 不变 |
| C06 及之后 | 解开阻塞，可在 G2 通过后正常开工 |
| 开发机 Pi | 在本轮全程**不被安装器触碰**，现有 `.tools/pi` 与 `~/.pi` 保持原样 |
| 已知代价 | 安装/升级/回滚的真实缺陷会推迟到 G4 才暴露；届时需在干净机器上重跑，不能拿开发机冒充 |

## 被否决的替代方案

- **B：允许 C06 在 G2 BLOCKED 状态下先做。** 违反 master plan「gate 失败阻断完整 UI/上线验收」，
  且会让 G3 建立在一个未关闭的门禁之上，证据链断裂。
- **C：把 F06 拆成「只读探测 + 不激活」先做。** 能做的只有 INS02/03/06 的一部分；
  INS01/05 的核心就是激活与回滚语义，拆完仍无法通过，反而制造「部分完成」的假象。
- **D：把 INS 标 UNSUPPORTED_EXPECTED 暂过。** 这是卸载断言，不是负例豁免，
  属于「删断言换绿灯」，被验收纪律禁止。

## 验证方式

改完后必须满足：

1. `go run ./cmd/devtools gate --id=G2 --evidence=<任意完整证据>` 不再把 INS 列为缺失项；
2. `go run ./cmd/devtools gate --id=G4 --evidence=<任意完整证据>` 把 INS 列为必需，
   缺证据时返回非 0；
3. `cases.json` 里没有任何 case 的 `gates` 为空数组（避免改动时误清空）；
4. F06 进度看板仍显示未完成，不得因为「从 G2 移走」被读成已完成。

## 落地复核（2026-11-04）

改之前先确认了门禁**不硬编码**必需集合：`gate.go` 的 `ResolveRequiredAtomicCases`
完全按 `cases.json` 的 `required`/`gates` 求值，复合 case 按子 case 的 `gates` 展开；
`internal/devtools/*_test.go` 里没有任何硬编码的 `INS` 或 `G2`/`G4` 断言。
因此代码无需改动，但**有四处必须同步，只改 JSON 会留下自相矛盾的依赖图**：

| 位置 | 改动 |
|---|---|
| `tests/acceptance/cases.json` | INS01–07：`phase` G2→G4，`gates` ["G2"]→["G4"] |
| `docs/acceptance/v1-gates.md` §3 | G2 列表移除 INS01–07；G4 列表加入 INS01–07 |
| `docs/superpowers/plans/…master.md` 门禁表 | G2 通过条件 `F05–F07` → `F05、F07`；G4 加 INS01–07 |
| `docs/acceptance/v1-plan-index.json` | G2.`dependsOn` 移除 `F06`；G4.`dependsOn` 加入 `F06` |

最后一条尤其容易漏：case 归属改了、但任务依赖没改，依赖图仍然声称 G2 需要 F06，
等于决策没有真正生效。

## 验证结果

```text
gate --id=G2 --evidence=<d3300a3/win-x64>   grep INS → 0 处（不再要求）
gate --id=G4 --evidence=<d3300a3/win-x64>   INS01–07 全部列为必需，found=false
go test ./internal/devtools/ -count=1       ok
```

必需条数（按门禁实际展开，复合 case 不计父项）：

| 门禁 | 改前 | 改后 |
|---|---|---|
| G0 | 24 | 24（全部有证据） |
| G1 | 9 | 9 |
| G2 | 57 | **50** |
| G3 | 15 | 15 |
| G4 | 11 | **18** |

## 遗留

- F06 推迟后，C06 的 `dependsOn [.., G2]` 不需要改——G2 仍要关闭，只是不含 INS。
- G4 现在依赖 `F06` 与 `G3`：执行顺序成为「功能完 → G3 → 做安装器 → 打包 X03/X04 → G4」。
  这个顺序是本次调整的直接后果，**不是**原来的设计意图，实施时若发现安装包需要提前，另开 ADR。
