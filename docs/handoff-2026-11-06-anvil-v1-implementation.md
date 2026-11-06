# Anvil V1 实施交接（2026-11-06）

> **已被 `docs/handoff-2026-11-06-anvil-v1-continuation.md` 续接**：F05-A…E、F01-GOV05、F08-A/B 落地后，
> 当前 HEAD、验证状态、剩余任务与坑以续接页为准；本页保留方案冻结时的坐标与强制边界。

本文件是当前 Anvil V1 方案冻结后的执行交接页。它汇总接手实施所需坐标，不替代规格、分模块计划、验收矩阵或原始证据；发生冲突时按下方权威顺序处理。

## 1. 交接坐标

| 项 | 当前值 |
|---|---|
| 仓库 | `D:\project\my-agent-go` |
| 分支 | `feat/v1-foundation` |
| 本轮起始 HEAD | `06280a7`（本交接和语义修订在后续独立提交中；接手时以 `git log -1` 为准） |
| 正式产品名 | Anvil |
| 证据根 | `docs/evidence/runs/<revision>/<gate>/<target>[/<display>]/<case>/` |
| case 数 | 198（以 `tests/acceptance/cases.json` 与机器索引实时一致性检查为准） |
| 工作方式 | 单任务：失败用例 → 最小实现 → focused 验证 → 原始证据 → Task ID 独立提交 |

接手后的第一条命令必须是：

```powershell
git status --short
git log --oneline -5
```

工作树可能包含用户工作。不得 reset、checkout 或覆盖不属于当前 Task 的改动。

## 2. 权威顺序

1. 用户明确指令；
2. `docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md`；
3. `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md` 中未被增量规格替换的部分；
4. `docs/superpowers/plans/*.md`；
5. `docs/acceptance/v1-gates.md`、`tests/acceptance/cases.json`、`docs/acceptance/v1-plan-index.json`；
6. 历史方案、旧交接和调研文档。

现场规则见 `AGENTS.md`。旧交接中的 F06 延后、MyAgent 品牌、case 数或任务状态若与当前文档冲突，以当前文档为准。

## 3. 当前真实状态

### 已完成并有既有实现/证据

- F00–F05、F07；但 F01、F05 各有 2026-11-05 增量未闭合项，见下节。
- C01–C05。
- B00、B01（含 B01b 的 BR04–BR06，提交 `fa96b64`）。
- 产品规格、分模块计划、198 条 case 追踪、G0–G5 门禁结构和外部 oracle 约束。
- UI 原型只是设计输入，`docs/ui/Agent-客户端产品原型/` 不能作为生产功能或验收证据。

### 尚未开始实现

- F06、F08、F09。
- C06–C09。
- U00–U07。
- B04–B07。
- X01–X05。

B02/B03 因缺 macOS/Linux 真机而 BLOCKED。计划文件的 `[x]`、索引里的 `state` 不是权威完成状态；状态以代码、当前 revision 的原始证据和 gate 输出为准。

### 已知前置欠账

1. **F05 未闭合项**：RUN05 崩溃后未知写入不重放；每 run 100 次工具调用/30 分钟预算；usage 缺失显示 `unavailable`；UI 断线拒绝审批；5 秒终止升级与 10 秒清理的可控时钟测试；RUN07；压缩/重试期间不得提前 completed。必须在 C06 开工前完成。
2. **F01 GOV05**：证据必须记录并校验 OS、版本/build、架构、display；当前开发机 Windows 10 22H2 低于最低 Windows 11 24H2，本机证据只能算开发证据。
3. **F01 oracle 欠账**：`tests/acceptance/oracle-allowlist.json` 仍有 38 条旧 `test-process` case。逐条改为 `external-observation`，每移除一条同步下调 `legacyProcessOracleCeiling`；G4 前必须清零。

## 4. 2026-11-06 语义复核后的强制边界

以下不是可选优化，实施时不得退回原来的模糊设计：

1. **RunService 全局 admission**：C06 将 session/workspace/policy 从 `run.Service` 实例级配置改成 per-run 输入；应用进程只装配一个全局 FIFO queue，交互会话和 C09 调度共同受 active 上限 2 约束。C08/C09 不得创建自己的 worker queue。
2. **连接器单点权威授权**：C08 为每个 run 使用短期 capability，在 harness MCP 服务端绑定 C06 冻结的 `run.ExecutionBinding`。capability 只标识 run；connector handler 是 connector namespace 的唯一权威 policy enforcement point。app-owned harness server 名称在 Pi 配置编译时保留并拒绝 collision；generic hook 只有从 authenticated policybridge 收到基于服务端注册表和固定 connector catalog 的 `delegate-to-handler` 决策时才委托，不能信任工具名前缀或调用方 source。直接 POST、同名外部 MCP、跨 run token、过期 token 的副作用计数必须为 0，一次合法动作只能产生一次审批和一次 driver 调用。
3. **Clarification 不等于 approval**：C06 提供不可关闭的内置 `anvil_ask_user` 系统扩展工具；受控 provider 真实调用后，扩展通过 Pi `extension_ui_request` 产生 `clarification.requested`，并由独立 `AnswerClarification` tagged union 返回 value、confirmed 或 cancelled；timeout/run abort 自动 cancel。只有该内置签名工具按 catalog 视为本地无副作用；第三方同名工具不能绕过 policy。回答不得创建 approval、session grant 或工具/站点权限，U02 不能从普通 assistant 文本猜测 clarification。
4. **普通会话附件**：不扩大 scratch 权限。用户显式选择后由 Go 校验路径并复制进 scratch 外的 app-owned content-addressed blob store；Pi 只通过只读 broker/capability 流式读取同一打开句柄校验过的字节，不取得可写路径。并发替换/截断、hash 不符或 capability 过期均 fail closed，且不回写源文件。这是 Anvil policy 边界内的 integrity-checked snapshot，不宣称 OS 沙箱或抵抗已取得同用户任意写权限的攻击者。
5. **Pi 预认证**：`ready_verified` 要求版本与 executable SHA-256 匹配嵌入的预认证清单，且当前 executable 完整契约探测通过；fixture hash 只保护测试数据。自报 1.5.0、hash 未登记但探测通过的候选最多为 `ready_compatible`，探测失败必须为 `incompatible`。
6. **Pi 生命周期状态**：F06/F08 后端正式提供 `installing` 与 `rollback_available`；U06 不从进度事件推断业务状态。
7. **Updater apply-time 验证**：F09 helper 把 journal 和 staging 视为不可信。交换前重新验 manifest、target/schema、package length/hash/signature，安装目标由 helper 自己推导，拒绝路径逃逸。健康确认必须在启动、必要迁移和健康检查后才提交，否则程序和数据库一起回滚。
8. **OAuth 秘密边界**：client secret 不编进 Anvil。按平台选择 device flow/public client + PKCE、用户提供的细粒度 token 或用户应用注册；平台不支持桌面 public client 且没有注册/broker 时为 BLOCKED。
9. **Skill URL 与 dry-run**：链接检查只允许 public destination，按维护中的 IANA special-purpose 范围表拒绝私网、loopback、CGNAT、benchmark/reserved、IPv6 unique-local/link-local 等地址，并对每个 DNS answer、重定向和实际 peer 复核，阻断 DNS rebinding；同时限制响应、归档、展开体积和文件数。dry-run 使用空 scratch，只挂载临时 Skill 副本，禁用网络、浏览器、连接器、外部 MCP、shell 和写工具。
10. **PI_DISC02 测试**：fixture 使用安全目录中的 `pi.cmd`，但令其解析到微信开发者工具目录中的 Node；候选必须记录 launcher/runtime executable chain，按实际 resolved Node 路径拒绝并产生 `unexpected-node-origin` diagnostic，不得只检查 launcher 或比较不存在的 origin 枚举值。

对应修订已写入：

- `docs/superpowers/plans/2026-11-03-resources-workbench.md`（C06）；
- `docs/superpowers/plans/2026-11-05-connectors-scheduler.md`（C08/C09）；
- `docs/superpowers/plans/2026-11-05-pi-lifecycle-updater.md`（F08/F06/F09）；
- `docs/superpowers/plans/2026-11-05-anvil-ui.md`（U02/U04）；
- `docs/acceptance/v1-external-dependencies.md`（OAuth 外部资源）。

## 5. 建议实施顺序

严格按共享写点串行推进：

1. F05 未闭合项。
2. F01 GOV05；38 条 oracle 欠账可与后续任务交替清理，但 G4 前必须为 0。
3. F08：Pi 发现、真实 executable 兼容探测、PI_DISC/PI_COMP runner。
4. U00：Anvil 品牌、错误契约、i18n、主题、偏好和非破坏数据迁移。
5. C06：全局 RunService、会话目录、clarification、附件复制、导出。
6. C07：产物、usage/预算、诊断。
7. U01：应用壳、历史、工作台、搜索。
8. C08：六类连接器、per-run MCP capability、服务端 policy gate。
9. C09：消费全局 queue 的持久调度器。
10. F09：签名更新与 apply-time 重验证。
11. F06：托管 Pi 安装、升级、回滚（仍受 ADR 0002 的本机安全约束）。
12. U02–U06、B04–B07、U07。
13. X01/X02、扩展 G2/G3；随后 X03/X04、G4；最后 X05/G5。

开工依赖以 `docs/acceptance/v1-plan-index.json` 的 `dependsOn` 为准；`acceptAfter` 只是验收顺序。不得把 gate 等待误读成禁止在单机开发独立模块。

## 6. 首个新增功能任务 F08 的开工清单

F05/GOV05 收口后，从 F08 开始：

- [ ] 阅读 F08 计划、规格 §8.1、现有 `internal/runtime/pi` 和 F02/F03 证据。
- [ ] `git status --short` 干净或已明确隔离用户改动。
- [ ] 先写错误 PATH、假 1.5.0、范围内但契约失败、未知 patch 探测成功的失败测试。
- [ ] 发现结果保留全部 candidate 的路径、版本、发行形式、来源和 executable hash。
- [ ] `ready_verified` 与 `ready_compatible` 都运行实际隔离探测；版本范围不直接放行。
- [ ] 新 runner 写 `external-observation` oracle，附 candidate/hash/probe artifact。
- [ ] 只跑隔离 agentDir/workspace/provider，不读写真实用户 Pi 配置。
- [ ] focused tests、win-x64 integration、证据和 F08 独立提交后再进入 U00。

## 7. 共享写点与迁移登记

### 共享写点

| 文件/目录 | 写者顺序 |
|---|---|
| `internal/app/desktop.go`、`cmd/myagent/main.go` | F08 → U00 → C06 → C08 → C09 → F09 → F06 |
| `frontend/src/app/Router.tsx`、`navigation.ts` | U01 建全部路由；C08/C09 只替换自己的占位页 |
| `internal/contracts/*`、`schemas/*` | 单一写者串行；U00 改错误契约，U03 改 Profile |
| `tests/acceptance/cases.json`、`internal/devtools` | 主执行者串行；U07 期间归 U07 |

### SQLite 迁移

| 编号 | 内容 | Task |
|---|---|---|
| 001 | 初始结构 | F07（已有） |
| 002 | 记忆 scope 与检索 | C05（已有） |
| 003 | 连接器定义 | C08 |
| 004 | 调度与 occurrence | C09 |
| 005 | 会话目录 | C06 |
| 006 | 全局搜索索引 | U01 |
| 007 | 产物索引与 run usage | C07 |

迁移 runner 按已应用集合判定，编号与合入顺序不一致不会自动跳过；仍不得自行占用未登记编号。已合入迁移禁止改号。

## 8. 外部阻塞与待用户决定

完整清单见 `docs/acceptance/v1-external-dependencies.md`。主要 BLOCKED 项：

- 其余五个 OS/CPU 目标、Windows 11 24H2 x64、Linux X11/Wayland、干净安装环境；
- 六类连接器测试账户与应用注册；
- Windows/Apple/Linux 签名资源；
- 应用更新签名密钥和 HTTPS 托管；
- 推送/触发 CI 与公开发布授权。

仍需用户在对应任务开工前明确：

1. F06 时序：当前计划采用 A（留在 G2，排在 C08→C09→F09 后），但登记仍标等待最终确认；若选 B，必须同步修改已批准规格和 gate 归属。
2. X03 的 `productIdentifier` 与 `companyName`。
3. `anvil://` 是否保持应用内逻辑地址；当前计划采用 A，不注册 OS 协议。改变需 ADR。

本轮用户回复的“A”是对“修计划并新增单一交接页”的方案 A 授权，不自动解释为上述三个产品决策的最终确认。

## 9. 环境事实

- Go 1.27.3。
- Node 必须显式使用 `D:\Program Files\nodejs\node.exe`（24.16.0）。
- npm 使用 `D:\Program Files\nodejs\npm.cmd`。
- PATH 上 Pi 1.4.0 不兼容；测试用 `.tools/pi/v1.5.0`，但 F06 测试不得改 `.tools/pi/`。
- WebView2 156.0.4314.58。
- 本机有 MinGW-W64 gcc 16.2.0，但换机器仍先探测。
- 当前主机 Windows 10 22H2 build 19045，低于 Windows 验收最低版本。
- MSYS 调用 cmd 使用 `cmd //c`。
- G1 native suite 会打开桌面窗口并操作剪贴板/输入法，运行前必须征得用户同意。

## 10. 验证命令

使用包装脚本保真退出码：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=unit
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G0 --evidence=<absolute-evidence-root>
```

文档/索引修订至少运行：

```powershell
go test ./internal/devtools -run 'TestIndexCoversEveryMatrixCase|TestIndexEntriesAreActionable|TestOracleKindIsDeclaredOrGrandfathered|TestPlanIndexIsConsistent' -count=1
go test ./internal/... -count=1
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static
```

证据必须在干净工作区生成。临时输出写 `$TEMP`；写入仓库会让 `worktreeDirty=true`。旧 revision、重复证据、缺 oracle、空 suite、全 SKIP、symlink artifact 和实现自报成功均应被 gate 拒绝。

## 11. 禁止事项

- 不重写或撤销历史 PASS；旧 PASS 不覆盖新增范围。
- 不用原型、mock、截图、toast、loading 或硬编码统计冒充功能。
- 不把 Pi/Node/npm/Chromium/模型权重塞进基础包或更新包。
- 不覆盖外部 Pi，不修改 PATH，不迁移用户秘密。
- 不把 OAuth client secret 编进桌面程序。
- 不创建连接器或调度的旁路 runtime/queue/policy。
- 不降低门禁阈值、不删除目标、不以交叉编译冒充真机。
- 不在缺账户、机器、签名或网络时伪造 PASS；使用 BLOCKED。
- 未经用户授权不推送、不触发 CI、不公开发布。
- 未过 G4 不称 Anvil V1 可交付；未过 G5 不称正式发行完成。

## 12. 接手完成定义

下一位执行者只有在以下条件同时满足时，才可以把一个 Task 标为 PASS：

- Task 的失败测试在实现前确实失败；
- 实现后 focused tests 与要求的 suite 退出 0；
- case runner 已接入且 oracle 来自实现边界之外；
- 证据绑定当前干净 revision、正确 target/display；
- 外部依赖缺失部分明确记 BLOCKED；
- 修改只包含该 Task 的所有权文件并已独立提交；
- 报告列出命令、退出码、证据路径/hash 和限制。
