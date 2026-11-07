# Anvil V1 交接（2026-11-06 · F01 欠账清零 + C06–C09 + U00 + 证据接线）

本文件接在 `docs/handoff-2026-11-06-anvil-v1-oracle-zero.md` 之后，记录 **C06a–f、C07、C08a/b、C09、U00、
GOV05 runner、E01 证据接线** 落地之后的真实状态。它不替代规格、分模块计划、验收矩阵或原始证据；
冲突时按 `AGENTS.md` §1 的权威顺序处理。

## 1. 交接坐标

| 项 | 当前值 |
|---|---|
| 仓库 | `D:\project\my-agent-go` |
| 分支 | `feat/v1-foundation` |
| **当前 HEAD** | 以 `git log -1` 为准（本文写成时 `0769d6a`） |
| 工作树 | 干净 |
| case 数 | 198（`tests/acceptance/cases.json`） |
| `oracle-allowlist.json` | 空（`legacyProcessOracleCeiling = 0`） |
| 有 canonical runner 的必需原子 case | 198 − 109 = 89 |
| 证据根 | `docs/evidence/runs/<revision>/<gate>/<target>[/<display>]/<case>/`（gitignore） |
| 本机台账 | `C:\Users\Administrator\AppData\Local\Temp\anvil-sdd\progress.md`（机器本地） |

本机四个套件的真实状态（`0769d6a`，Windows 10 22H2 build 19045）：

| 命令 | 退出码 | 说明 |
|---|---|---|
| `verify --suite=static` | 0 | gofmt / go vet / 前端 typecheck / 文案门禁 |
| `verify --suite=unit` | 0 | `go test ./internal/...` 全绿 + 前端 148 测试 |
| `verify --suite=integration --target=win-x64 --gate=G0` | 0 | 31/31 PASS（external-observation） |
| `verify --suite=native --target=win-x64 --display=desktop` | 2 | BR00/01/04/05/06 PASS；缺 runner：`UI04.native`、`BR07.native` |
| `gate --id=G0`（证据树 `runs/0769d6a`） | 2 | 142 必需证据位：0 个 case 缺 runner 证据位、100 个非 win-x64 目标缺位、42 个因宿主 build 19045 < 26100 被拒 |

**本机证据一律是开发证据**：`build/platforms.json` 要求 win-x64 最低 Windows 11 build 26100。在 Win11 24H2
或合规 CI 上重跑同一套命令，G0 的拒绝理由才会消失。不得把本机任何结果当作 win-x64 验收结论。

## 2. 本轮完成（每个任务都有提交 + 原始证据 / 报告）

### 2.1 C06：会话/运行域（六段）

| 段 | 提交 | 要点 |
|---|---|---|
| C06a | `dc6de61`→`fc5028b` | per-run `ExecutionBinding`（admission 解析并冻结）；一个全局 admission queue（`globalMaxActive=2`），排队 run 在 `run.state` 事件上报 `queuePosition`；策略上限在 admission 校验（越权结构化失败，不建 run/不占槽/不起 worker） |
| C06b | `86e3c9a`→`9224c96` | 迁移 005（title/pinned/archived/updated_at/revision/deleted_at/kind）；keyset 分页（插入下稳定）；每个 mutation CAS revision；软删除保留 transcript 与产物 |
| C06c | `51de3f1`/`8c219eb` | 附件 content-addressed blob store（`<appRoot>/blobs/aa/<sha256>`）+ 只读 broker：同一打开句柄校验 size/sha256 后才返回字节；symlink/junction/越界/并发替换/能力过期 fail closed |
| C06e | `a240826`→`3970d7a` | 会话导出（html/markdown/json）取自 Pi transcript；HTML 结构化转义；`internal/redact` 共享脱敏规则（含与 `worker.go` 的漂移比对测试）；原子写 + 越界拒绝 |
| C06f | `318fe72`→`8e9c2fc`、`5c17d84` | 附件投递（loopback+令牌只读端点，run 结束即关）与澄清桥（`anvil_ask_user` + typed `clarification.requested` + tagged union 答复）；权限隔离按**工具身份**不按名字；顺带修掉"正常结束的 run 从不关 worker"的真 bug |

### 2.2 C07：产物 / usage / 诊断

`59ed7b5`…`35e7f54` + `8476169`：迁移 007（artifacts / run_usage / run_events）；`GetArtifact` 每次读重新核对
磁盘 → `drifted`/`missing`；usage 三态（无记录 / 有 tokens 无价格 / 两者都有），`unavailable` 绝不当 0；
诊断导出复用 `internal/redact`，sentinel 命中 0 且保留错误码/版本/时间/事件 ID。

**关键修正 `8476169`**：`run.Options.History` 只有一个槽位，C07 把诊断记录器装进去后 `history.Service`
没有写者（真实运行过的会话查不到）。改为复合 sink：索引先写、诊断后写，一个坏了不把另一个判死。

### 2.3 C08：连接器

- C08a `76d27b4`…`21efae9`：框架（driver 接口、catalog、追加式审计、迁移 003）、loopback PKCE 鉴权、
  SMTP/IMAP/OAuth fixture、mail 驱动、MCP 暴露、app 绑定。凭证只进 OS 凭证库，其余位置只有 ref。
- C08b `dc607dd`…`0af6a41`：钉钉/飞书/Slack/Notion/GitHub 五个适配器；每个写操作在 catalog 显式声明幂等性
  （五个平台都是 `none`，重试必须重新审批）；每平台限流；跨适配器同一道策略门（反证日志证明去掉门即红）。

### 2.4 C09：调度器

`18c1503`/`4b07358`：occurrence 唯一键 `(schedule_id, scheduled_at)` 让 DST 双向、时钟回跳、重启都不重复触发；
misfire 最多补 `CatchUpLimit` 次；同任务不重叠（`skipped` + 稳定 reason）；无人值守审批 60s 超时后失败且
副作用 0、不重放；连续两次失败 CAS 自动暂停；派发走**唯一** RunService（Runner 接口就是 `*run.Service` 的
方法集）；调度会话 `kind=scheduled`，删除是软删除且保留产物。

### 2.5 U00：品牌 / 错误契约 / i18n / 主题

`b32b2cc`、`0f37424`：`AppError`/`CallError` 增量字段（`params`/`operation`/`cause`，全 `omitempty`）；
`Preferences{Locale,Theme}` 走既有配置 CES；`UnsavedScopes()` 供 F09；`envLookup` 优先 `ANVIL_` 并对
`MYAGENT_` 记一次弃用；品牌迁移一次性、幂等、不删旧目录、不碰 `~/.pi`；i18n 两语言 key 集一致且覆盖全部
40 个 Go 错误码；主题 system/light/dark + 跟随系统；`AsyncState`/`StatusBadge`/`useUnsavedChanges`；
`scripts/check-ui-contract.mjs` 作为文案门禁（历史 19 页 571 条文案列入基线，只许缩小）。

### 2.6 证据与工具链

| 提交 | 内容 |
|---|---|
| `9f4e490` | **GOV05 runner**：真实 devtools 二进制在四棵证据树上的退出码（最低线 0；缺环境/OS 不符/低于最低 build 各 2 并点名），事实从 `gate-run.json` 读回 |
| `9c4aee6`…`0769d6a` | **E01 证据接线**：DB01–03、CFG02/04、CFG03.resources、SES01–03、ART01–03+SEC07.diagnostics、SCH01–05/07、CONN01/02/06 共 22 条 case 接上 runner 与 external observation |
| `a81b040` | 前端 runner 改用基线 Node 的 PATH（首个 `node` 是旧微信构建，vitest 会误红）；`verify --suite=unit` 从此 exit 0 |
| `aef1deb`、`c16bcff` | flake 根治：`Close` 等索引写在飞行的写入排空；`run.error` 有界等待；piinstall 安全快照只按内容哈希并做差异归因；clarification 测试替身的数据竞争 |

## 3. 仍未闭合（按影响排序）

1. **109 条必需原子 case 没有 runner**：UI01–UI13（U01–U07 前端页面/搜索/资源库/记忆/观测）、
   E2E01–07、PKG01–07/UPD01–14/PERF01–04/REL01–04（X03/X04/F09 打包与发布）、BR02/03/07–17（原生浏览器）、
   INS01–07/PI_PKG01–02（F06 G4 半边）、CONN03/04/05/07/08、MOD01–04、MCP01–05、SCH06、
   SEC03/04.browser、SEC07.store、CFG01、RES05、RUN01/04/06.ui。
2. **签名预认证清单不存在**：没有任何发行私钥就没有 `ready_verified`（BLOCKED，需 X05）。
3. **托管包五个目标没有精确 `sizeBytes`**：清单写 0 且 loader 拒绝（需发行流程回填后重新签名）。
4. **文案与主题的逐页迁移**：U00 只建了地基，19 页历史文案仍在基线里，旧组件仍硬编码颜色。
5. **`anvil://` 逻辑路由**与偏好首屏接线归 U01（`main.tsx` 目前只用本地镜像）。
6. **macOS/Linux 无真机证据**：POSIX `#!`、tar.gz 解包、真实 Pi 契约探测、`Get-Clipboard` 等价物都要在对应
   目标机重跑；六目标矩阵是 G0 失分的主要原因（本机只能出 win-x64）。

## 4. 建议的下一步顺序

`U01`（壳层/路由/搜索/工作台，`desktop.go` 串行写点）→ `U02`–`U07`（页面，逐页把文案收进 catalog）→
`E01` 续（MCP/ MOD / CONN08 / BR07.tool 等"装置已具备"的 case）→ `F09`（签名更新服务）→
`X03/X04`（打包/发行）→ 在 Win11 24H2 机或合规 CI 重跑 G0→G1→G2 → `G4`。

纪律不变：失败用例 → 最小实现 → focused 验证 → 原始证据 → 可独立审查的提交；**先提交、工作树完全干净
（含未跟踪文件）再取证**（否则证据 `worktreeDirty` 会被门禁判死）；缺机器/凭证/签名就写 BLOCKED，不改口径、
不伪造证据。
