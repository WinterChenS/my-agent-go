# ADR 0003：技能发现语义（RES02）与 SEC08 项目隔离

- 日期：2026-11-04
- 状态：**已采纳**（用户 2026-11-04 明确指令；实现按本 ADR 收口，C03 仍为 REVIEW，待复核）
- 影响任务：C03、F04（SEC08）、G0/G2 的 RES02 与 CFG03.resources
- 需求追踪：RES02、SEC08、R08（隔离验证）、R10（真实集成与 mock 分开记录）

## 背景

2026-11-04 用户对本轮 C03 的明确指令：

> 不抑制本地 pi 的 skill，直接同步过来

原实现用 `-ns`（`--no-skills`）关掉 Pi 的技能自动发现，只让 `--skill` 显式加载的
托管技能进系统提示。`-ns` 是**全有或全无**：它同时关掉用户级
`~/.agents/skills`、项目级 `<workspace>/.pi/skills`、祖先目录 `.agents/skills`。
用户要求保留本地技能同步，因此 `-ns` 从 `requiredIsolationFlags` 移除。

这带来两个必须回答的问题，不能靠注释推断：

1. 移除 `-ns` 后，项目级/祖先级技能是否也一并打开（SEC08 漏洞）？
2. 同名时托管技能（`--skill`）与自动发现副本谁生效（RES02 的"不能绕过"是否还成立）？

## 实测（真实 Pi 1.5.0，2026-11-04，win-x64）

环境：`.tools/pi/v1.5.0`（`@earendil-works/pi-coding-agent@1.5.0`），Node 24.16.0；
fixture provider 只替代模型端点，Pi 是真实进程；隔离 `HOME`/`USERPROFILE`、
独立 agentDir、`t.TempDir()` 工作区。每条测量用互不相同的 sentinel。

`pi --help`（exit 0）里与技能/信任相关的**原样摘录**：

```text
  --skill <path>                 Load a skill file or directory (can be used multiple times)
  --no-skills, -ns               Disable skills discovery and loading
  --approve, -a                  Trust project-local files for this run
  --no-approve, -na              Ignore project-local files for this run
```

**没有**"只关项目/祖先技能发现、保留用户级发现"的单独参数。`--no-skills` 会连
用户级一起关；`--no-approve` 是项目级信任开关。本轮没有找到更细的开关。

测量结果（产品参数 `-na -nc -ne -np --no-themes --no-mcp`，只把 `-na` 换成 `-a`
做阳性对照）：

| 渠道 | `-na`（产品实际参数） | `-a`（阳性对照） | 结论 |
|---|---|---|---|
| 用户级 `<home>/.agents/skills/<name>/SKILL.md` | **出现**，location 指向该临时 home | 出现 | 用户级同步成立 |
| 项目级 `<workspace>/.pi/skills/<name>/SKILL.md` | **不出现**（系统提示无该 sentinel） | **出现**，location 指向项目路径 | 被 `-na` 切断，非假阴性 |
| 祖先级 `<workspace父>/.agents/skills/<name>/SKILL.md` | **不出现** | **出现**，location 指向祖先路径 | 被 `-na` 切断，非假阴性 |
| 同名：托管 `--skill` + 用户级副本 | 只出现 1 条 `dup-skill`，description/location 都是**用户级副本**；托管那份的 sentinel 不出现 | 同左 | 自动发现副本胜出 |

同名冲突的机制（读到 Pi 源码后由实测确认）：Pi 先加载自动发现路径（项目/用户级），
再加载 CLI `--skill`，按 name 去重时**保留先出现的那条**。因此：

- 禁用托管技能（不进 `--skill`）→ 同名副本仍出现（已知限制）；
- **启用**托管技能（进 `--skill`）→ 同名副本仍然胜出，托管版本被静默压过（原文档没写到的限制）。

补充观测：Windows 上 `t.TempDir()` 位于用户 home 之下。做 `-a` 阳性对照时，Pi 的
祖先目录扫描会走到 `<真实home>/.agents/skills`，因此对照运行里有 55 条技能
（1 个 marker + 54 条本机用户技能）。`-na` 下这个扫描被切断。生产环境里 HOME
就是真实 home，Pi 会把该目录按用户级去重（`projectAgentsSkillDirs` 过滤
`~/.agents/skills`），语义不变；但这一点说明祖先扫描确实会一直到卷根，不能把
`-na` 当成可选项。

原始证据：`docs/evidence/raw/c03-skill-discovery.json`（测试生成，含每次的 argv、
sentinel、`<available_skills>` 摘录与观测布尔值）。生成命令与退出码记录在
`docs/evidence/c03-skills.md` §7。

## 决策

1. **技能发现分三层，分开表述，不合并成"已切断"：**
   - **用户级（开放）**：`~/.agents/skills`、`<agentDir>/skills`、用户配置里列出的
     技能由 Pi 自动发现。这是用户 2026-11-04 要求的"本地技能直接同步过来"。
   - **项目级/祖先级（切断）**：`<workspace>/.pi/skills`、工作区祖先目录的
     `.agents/skills` 由 `-na` 切断；默认不传 `-a`。SEC08 的"不信任项目资源"
     边界不变。
   - **托管（显式）**：本产品 `<home>/skills` 不在 Pi 的发现路径上，始终通过
     `--skill <绝对路径>` 加载；禁用 = 不进 `--skill`。
2. **RES02 语义变化**：原口径"同名自动发现副本不能绕过禁用"在方向调整后**不成立**，
   不再作为门禁断言。新口径：
   - 纯托管技能禁用后新 run 不加载（仍成立，真实 Pi 测试覆盖）；
   - 同名自动发现副本按设计同步，禁用不保证消失；
   - 启用托管技能也不保证覆盖同名副本（实测顺序决定）；
   - 因此运行快照/UI **不得**把"托管启用清单"冒充"Pi 实际可见技能"；没有可信
     来源时必须显示未知/差异，而不是空列表。
3. **信任边界**：用户级技能是用户自己机器上的资源，按用户指令视为可信；
   项目/祖先技能是"当前打开的项目"带入的不可信输入，继续由 `-na` 切断。
   用户级同步**不**自动授权项目资源，两者不是同一条通道。
4. **不新增启动拒绝**：实测 `-na` 已能切断项目/祖先技能发现，因此不需要在
   `internal/app` 里为"祖先含 `.pi/skills`/`.agents/skills`"拒绝启动。若未来 Pi
   升级使 `-na` 失效，再按基线 §5 的"无法阻止时不信任工作区禁止运行"处理。

## 影响

| 项 | 影响 |
|---|---|
| SEC08 | 项目/祖先技能通道仍被 `-na` 切断（真实 Pi 正反两向测量）；其余项目通道参数不变 |
| RES02 | 由"同名副本不能绕过"改为"按设计同步 + 运行记录必须可见"；纯托管禁用仍必须不加载 |
| 隔离参数 | `requiredIsolationFlags` 不含 `-ns`；`-na/-nc/-ne/-np/--no-themes` 不变 |
| 证据表 | `.pi/skills` 与项目/祖先 `.agents/skills` 标 `-na`；用户级技能单独标"按设计开放" |
| 兼容性 | 依赖"禁用即消失"的用户预期需要改为"禁用托管项，但本机同名技能仍可用" |
| 旧证据 | `docs/evidence/raw/f04-policy-integration.json` 保持历史原样（含 `-ns`），不改写；新参数的新运行写新文件 |

## 被否决的替代方案

- **保留 `-ns`。** 直接违反用户 2026-11-04 指令，用户级技能无法同步。
- **一律传 `-a` 信任项目。** 等于让 clone 下来的 `.pi/skills`、扩展、MCP 自动进入
  执行上下文，违反 SEC08 与基线 §5。
- **为"祖先含技能目录"的工作区一律拒绝启动。** `-na` 已能切断，拒绝会造成大量
  正常仓库无法打开；属于过度限制，且与实测结论不符。
- **分叉/补丁 Pi 增加"只关项目技能"的开关。** 违反 R05（不重写 Pi）与版本认证策略；
  需要时先升级/认证新 Pi 版本，而不是维护私有分支。
- **在 Harness 侧过滤系统提示。** 系统提示由 Pi 组装，Harness 没有可靠注入点；
  过滤等于猜 Pi 的内部格式，升级即坏。

## 如果 Pi 升级改变行为，哪条测试会先红

按以下顺序定位，测试名即断言：

| 行为变化 | 先红的测试 |
|---|---|
| `-na` 不再切断项目 `.pi/skills` | `TestSkillDiscoveryLayersAgainstRealPi/ProjectPiSkillsBlockedByNoApprove`（负例断言失败） |
| `-na` 不再切断祖先 `.agents/skills` | `TestSkillDiscoveryLayersAgainstRealPi/AncestorAgentsSkillsBlockedByNoApprove` |
| 用户级 `~/.agents/skills` 不再自动同步 | `TestSkillDiscoveryLayersAgainstRealPi/UserLevelAgentsSkillsSync` |
| 同名时改成 `--skill` 优先 / 出现两条 | `TestSkillDiscoveryLayersAgainstRealPi/SameNameAutoCopyWinsOverManagedSkill` |
| `--skill` 不再显式加载托管技能 | `TestEnabledSkillAppearsInRealPiPrompt` |
| 纯托管禁用后仍被加载 | `TestDisabledManagedSkillAbsentInNextRun` |
| 参数表被改回/悄悄加 `-ns` | `TestIsolationArgsCoverEveryProjectChannel`、`TestSkillsKeepAutoDiscoveryOpen` |

升级 Pi 时必须同时重跑 `pi --help` 并核对本 ADR 的"没有单独开关"结论；若出现
项目级专用开关，应优先改用它而不是 `-na`，并更新通道表与证据。

## 验证方式

```powershell
# 单元/静态（不需要真实 Pi）
go test ./internal/runtime/pi -count=1
go test ./internal/resources -count=1

# 真实 Pi（默认 skip；显式开启）
$env:MYAGENT_PI_WORKER_INTEGRATION = '1'
$env:MYAGENT_NODE_PATH = 'D:\Program Files\nodejs\node.exe'
go test ./internal/resources -run 'TestSkillDiscoveryLayersAgainstRealPi|TestEnabledSkillAppearsInRealPiPrompt|TestDisabledManagedSkillAbsentInNextRun' -count=1 -v
```

实际退出码与产见 `docs/evidence/c03-skills.md` §5/§7。C03 的最终 PASS 不由本 ADR
宣布，须由复核者按 `docs/acceptance/v1-gates.md` 判定。
