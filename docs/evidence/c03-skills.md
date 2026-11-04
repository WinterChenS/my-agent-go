# C03 Skills 与自有扩展管理

日期：2026-11-04
宿主：Windows 10 Pro 22H2（19045）/ amd64 / Intel N100
裁决：**win-x64 PASS；其余五目标 BLOCKED（无环境）**

2026-11-04 focused review 收口：SEC08 的项目/祖先级隔离用真实 Pi 1.5.0 做正反两向
实测（§7，不靠注释推断）；同名冲突语义按 ADR 0003 改写并钉成真实 Pi 测试；运行记录
从「托管清单冒充实际加载清单」拆成 `managedSkills` + `skillAutoDiscovery`（§8）。
`docs/acceptance/v1-gates.md` 的 RES02 口径已同步（ADR 0003）。
本文件不等于 G0 通过：G0 要求六目标证据，本机只有 win-x64。

本文件记录的是**在真实桌面上点出来的**证据：真实 `MyAgent.exe`、真实 Pi 1.5.0、
真实 deepseek 端点。导入/编辑/启停/删除都真的落了盘；技能是否被 Pi 加载，
是去翻 Pi 自己的会话 `*.jsonl` 里 `<available_skills>` 段落确认的，不是看我们
自己拼的参数字符串。

对应计划条目：`docs/superpowers/plans/2026-11-03-resources-workbench.md` 的 C03。
验收口径：`docs/acceptance/v1-gates.md` 的 RES01–RES04。

## 0. 方向调整（2026-11-04，本轮进行中）

原始设计用 `-ns`（`--no-skills`）关闭 Pi 的技能自动发现，只让 `--skill` 显式加载
的那几条进系统提示。Winter 在本轮明确要求「**不抑制本地的 pi 的 skill，直接同步
过来**」。据此：

- `requiredIsolationFlags` 移除 `-ns`。Pi 的**用户级**技能自动发现保持打开；
  项目/祖先级发现由 `-na` 切断（2026-11-04 真实 Pi 实测，§7）。
- 托管技能仍通过 `--skill <绝对路径>` 显式加载（托管目录 `<home>/skills` 不在 Pi
  的发现路径上，不显式传就不加载）。
- 代价（已知、可接受）：禁用一条「恰好与某个自动发现副本同名」的托管技能时，那个
  副本仍会被 Pi 加载；同理，**启用**托管技能也不保证覆盖同名副本（实测副本胜出）。
  **纯粹托管（无同名副本）的技能，禁用后下一轮不加载**——这条仍然成立并由测试与
  真机共同覆盖。
- 项目级 `.pi/skills` 仍被 `-na`（`--no-approve`）挡住：clone 一个仓库不能让它
  的 `.pi/skills` 自动跑进系统提示。这是 SEC08 的项目级信任边界，不在本轮放开范围。

RES02 的「同名自动发现副本不能绕过」一条随之**放宽**：不再要求副本消失，因为按
设计它们就该同步过来。2026-11-04 实测进一步确认：同名时自动发现副本**胜出**
（Pi 先加载发现路径、再加载 `--skill`，按 name 保留先出现的那条），所以**启用**
托管技能也不保证覆盖副本。语义已写入 [ADR 0003](../adr/0003-skills-discovery-and-res02.md)，
测量原文见 §7。RES01/RES03/RES04 不受影响。

项目/祖先 `.pi/skills`、`.agents/skills` 是否真被 `-na` 切断，不再按注释判断：
2026-11-04 用真实 Pi 1.5.0 做了 `-na`/`-a` 正反两向测量（§7），结论是**被切断**。
用户级 `~/.agents/skills` 在 `-na` 下仍然同步。

## 1. 被测物

| 项 | 值 |
|---|---|
| 可执行 | `build/bin/MyAgent.exe`（Wails v3 + 内嵌前端产物，`index-GWYYDIT0.js`） |
| 用户数据 | `MYAGENT_HOME=D:/project/my-agent-go/.tmp/e2e/home-c01`（临时目录，不污染真实数据） |
| 模型 | 真实 deepseek：provider `deepseek` / model `deepseek-2/deepseek-v4.1-flash` |
| Pi | `.tools/pi/v1.5.0`（Node 24.16.0 启动） |
| 本地技能源 | 真实 `C:\Users\Administrator\.agents\skills`（54 条， Winter 的工作机现状） |
| 交互方式 | `agent-desktop click / set-value / find / snapshot / screenshot`（真实 UI 操作，非坐标猜测） |

## 2. 交付物

| 层 | 文件 | 内容 |
|---|---|---|
| 通用层 | `internal/resources/configdoc.go` | `Layout`（user/workspace/extensions 目录派生 + 白名单防逃逸）、`rawField`/`upsertEncoded`/`removeEncoded` 通用资源数组读写 |
| 技能 | `internal/resources/skills.go` | 导入（stage→CAS→就位→失败撤回）、覆盖、启停、删除（rename→CAS→RemoveAll 可回退）、`CompileSkills`、frontmatter 解析与 Pi 对齐的 name/description 校验、依赖缺失标因、`sameVolume` 跨盘断言 |
| 扩展 | `internal/resources/extensions.go` | 系统策略扩展（`harness-policy`，恒启用、`CanDisable=false`）、第三方本地扩展（默认未启用）、`CompileExtensions`（系统扩展排第一） |
| 隔离 | `internal/runtime/pi/config_isolation.go` | `IsolationSpec.Skills` + `--skill`；`-ns` 已移除。通道表拆成三层：项目 `.pi/skills` 与项目/祖先 `.agents/skills` 由 `-na` 切断，用户级 `~/.agents/skills` 按设计开放（2026-11-04 真实 Pi 实测，§7） |
| 绑定 | `internal/app/skill_api.go` | `ListSkills / ImportSkill / OverwriteSkill / ReadSkill / SaveSkill / SetSkillEnabled / DeleteSkill / ListExtensions / SetExtensionEnabled / skillRuntimeState`（三态：enabled / available / pending） |
| 运行时 | `internal/app/desktop.go` | `resolveRuntimeResources`（技能 + 扩展计划，系统扩展从 ExtraExtensions 剔重）、运行时签名纳入 `sk:`/`ex:` 摘要、`Status.RuntimeSkills`/`SkillsPending` |
| 快照 | `internal/contracts/run.go`、`internal/run/service.go` | `RunSnapshot.Skills`（**2026-11-04 复核：该字段目前是托管启用清单，不等于 Pi 实际可见技能**；开启自动发现后空列表也可能有技能，§6） |
| 前端 | `frontend/src/features/skills/{SkillsSection,ExtensionsSection}.tsx` | 导入区（路径 + 作用域 + 说明「不执行脚本/不装依赖」）、列表徽标（已启用/已禁用/用户级/不可用/运行中仍是旧集合）、内联编辑、二次确认删除、系统扩展无关闭按钮、第三方启用二次确认 + 免审批说明 |
| 前端 | `types.ts` / `api.ts` / `useBackend.ts` / `runState.ts` / `RunsView.tsx` / `ResourcesView.tsx` / `App.tsx` | 技能/扩展类型与调用、`refreshSkills`（同时刷 skills/extensions/status）、运行记录「技能」行三态 |

测试：`internal/resources` 13 条单测 + 2 条真实 Pi 集成、`internal/app` 4 条、
`internal/runtime/pi` 隔离测试更新 2 条、前端 12 个文件 83 条。

## 3. 真机操作与结果

| # | 操作 | 结果 | 证据 |
|---|---|---|---|
| 1 | 导入 `D:\...\skills\demo-skill`（含 `references/notes.md`） | 成功：`config-user.json` revision 7，`skills[0]={id:demo-skill,scope:user,dir:skills/demo-skill,enabled:true,origin:imported}`；`home-c01/skills/demo-skill/{SKILL.md,references/notes.md}` 都在 | `03-imported-ok.png` |
| 2 | 跨盘 rename 缺陷（首轮真机） | 失败：暂存目录在 C 盘、目标在 D 盘，`os.Rename` 报 `cannot move the file to a different disk drive`；`skills` 变 `[]`、目录空 | 修法：`stageTree` 改用 `os.MkdirTemp(filepath.Dir(dst),...)` + `sameVolume()` 显式断言 + `TestSameVolumeGuard` |
| 3 | 编辑 SKILL.md（改 description 加「（已编辑）」）并保存 | 落盘成功；revision 不变（内容变更不 bump 配置 revision），但内容摘要从 `9becce59e672de94` → `e608073bf15ea1d2`、大小 415 B → 480 B，UI 描述同步更新 | `09-after-disable-active.png` 区块可见新摘要 |
| 4 | 启用 demo-skill，跑一轮 run | Pi 会话 `*.jsonl` 的系统提示里含 `<available_skills><skill><name>demo-skill</name>...<location>...home-c01\skills\demo-skill\SKILL.md</location>`；运行记录「技能」行 = `demo-skill` | `04-run-with-skill.png` |
| 5 | 禁用 demo-skill（revision 8），跑一轮 run | `skills` 条目 `enabled:false`、文件仍在；新一轮 Pi 会话 `<available_skills>` 段为空（demo-skill 不出现）；运行记录「技能」行 = `未加载任何技能` | `05-disabled.png` / `06-run-without-skill.png` |
| 6 | 抓 Pi 子进程命令行（demo-skill 启用 + hello 扩展启用） | `... -na -nc -ne -ns(无) -np --no-themes -e builtin:mcp -e ...harness-policy.ts -e ...extensions\hello.ts --skill ...home-c01\skills\demo-skill\SKILL.md --provider deepseek ...`；`-ns` 不在；`--skill` 在 | `.tmp/e2e/shots/pi-args.txt` |
| 7 | 第三方扩展 `hello.ts` 放进 `home-c01/extensions/` | 列表出现，默认「未启用」+ 信任边界说明「启用即信任…但不会让它的工具调用免审批」 | `10-sync-through-final.png` |
| 8 | 点 hello 的「启用」 | 弹出二次确认（「确认启用」/「取消」）；确认后 `config-user.json` revision 9，`extensions[0]={id:hello,enabled:true}` | `07-extension-confirm.png` |
| 9 | 系统策略扩展 | 恒显示「系统资源 · 不可关闭」徽标，**不渲染关闭按钮**（`e.canDisable &&` 才画）；绑定层 `SetExtensionEnabled` 对 `harness-policy` 直接返回 `CodeUnsupported` | `10-sync-through-final.png` |
| 10 | 方向调整后：demo-skill 禁用、无 `--skill`、无 `-ns`，跑一轮 | Pi 系统提示 `<skill>` 数 = 54，全是 `C:\Users\Administrator\.agents\skills` 的本地技能（agent-desktop / cad / …）；demo-skill 不出现；工作区 `.pi/skills/demo-skill` 副本不出现（被 `-na` 挡住） | 会话 `2026-11-04T10-40-49...jsonl` |
| 11 | demo-skill 启用 + 本地技能同步，跑一轮 | Pi 系统提示 `<skill>` 数 = **55** = 54 本地 + 1 托管（demo-skill，location 指向 `home-c01\skills\demo-skill`）；「本地技能直接同步过来」成立 | 会话 `2026-11-04T10-42-21...jsonl` |

## 4. 验收口径对照（RES01–RES04）

| 门 | 要求 | 结论 | 证据 |
|---|---|---|---|
| RES01 | 本地 skill 导入校验、编辑、删除、启停真实落盘；不改源目录、不执行导入脚本 | PASS | §3 #1/#3；`TestImportCopiesWithoutExecutingSource`（marker 文件不存在 + 源目录快照不变）；`TestImportRejectsInvalidSkills`（7 子）/`TestImportRejectsBrokenUTF8`/`TestImportRejectsSymlinkEscape` |
| RES02 | 禁用 skill 后新 run 不加载；同名自动发现副本不能绕过；旧 run 保持快照 | **部分 / 语义已变更（ADR 0003）** | 纯托管技能禁用后新 run 不加载：§3 #5、`TestDisabledManagedSkillAbsentInNextRun`（2026-11-04 复跑 exit 0）。「同名自动发现副本不能绕过」**不再成立**：按用户指令同步，真实 Pi 实测同名时自动发现副本胜出（禁用不消失、启用也压过托管版），见 §7。旧 run 快照由现有快照结构承载；运行记录把托管清单当实际加载列表的问题由另一 workstream 收口（§8；本轮未复跑其用例），RES02 最终判定留给复核者 |
| RES03 | skill 依赖缺失显示原因；恶意路径/损坏 frontmatter 被拒；不自动安装外部工具 | PASS | `TestMissingDependencyMarksUnavailable`（注入永远失败的 `LookPath` → `unavailableReason`）；`TestImportRejectsSymlinkEscape`/`TestImportRejectsBrokenUTF8`/frontmatter 校验对齐 Pi（name≤64、`/^[a-z0-9-]+$/`、无 `--`、description≤1024）；导入链路只有 WalkDir/Open/Read/Write，无 `LookPath` 后执行 |
| RES04 | 系统 policy 扩展不可关闭；工具启停影响实际执行，不只是菜单可见性 | PASS | §3 #6/#9；`TestSystemExtensionCannotBeDisabled`（绑定层 + 资源层双拦）；`TestEnableThirdPartyExtensionAffectsArgs`（启用后 `-e <path>` 真的进 Pi 参数）；`TestSkillToggleChangesRuntimeArgs`（启停后运行时重建、`--skill` 随之增减） |

## 5. 单测与静态

| 套件 | 命令 | 结果 |
|---|---|---|
| Go 单测 | `go test ./...` | 全绿（`internal/resources` 4.7s 含 2 条真实 Pi 集成） |
| Go vet | `go vet ./...` | 干净 |
| 真实 Pi 集成 | `MYAGENT_PI_WORKER_INTEGRATION=1 go test ./internal/resources/ -run 'TestEnabledSkillAppearsInRealPiPrompt|TestDisabledManagedSkillAbsentInNextRun'` | 2/2 PASS（1.80s / 1.78s） |
| Go 单测（2026-11-04 复核） | `go test ./internal/resources ./internal/runtime/pi -count=1` | exit 0：resources 1.260s、runtime/pi 15.311s（真实 Pi 用例默认 skip） |
| 真实 Pi（2026-11-04 复核，3 个顶层/4 个子测试） | `MYAGENT_PI_WORKER_INTEGRATION=1 MYAGENT_NODE_PATH='D:\Program Files\nodejs\node.exe' go test ./internal/resources -run 'TestEnabledSkillAppearsInRealPiPrompt|TestDisabledManagedSkillAbsentInNextRun|TestSkillDiscoveryLayersAgainstRealPi' -count=1 -v` | exit 0：`TestSkillDiscoveryLayersAgainstRealPi` 14.99s + 两个旧用例 1.82s/1.56s，全部 PASS |
| 测量证据 | 上一条命令另设 `MYAGENT_C03_EVIDENCE_OUT` 生成 | `docs/evidence/raw/c03-skill-discovery.json`（12,635 B，sha256 `09c86e0629f9421282e866b5a53a2142da726ab640e6090a21d4fc1a8607fbdc`） |
| 前端类型 | `npx tsc --noEmit` | 干净 |
| 前端单测 | `npx vitest run` | 83/83 PASS（12 文件） |

对抗性验证（守门价值最高的那几条）：
- `TestSkillToggleChangesRuntimeArgs`：禁用后 `svc2 != svc`（运行时重建）、参数里没有该技能。
- `TestDisabledManagedSkillAbsentInNextRun`：真起 Pi，禁用后下一轮系统提示里看不到托管技能的 sentinel。
- 方向调整前曾做过的「删 `-ns` 即转红」对抗：本轮重写为「`-ns` 不应出现」的正向断言，删掉 `--skill` 会让 `TestEnabledSkillAppearsInRealPiPrompt` 转红。

## 6. 范围与限制

- **Git/npm 下载技能后置**：本轮只做本地目录导入。从远端拉技能涉及网络与凭证，不在 C03。
- **`disable-model-invocation` 未真机验**：frontmatter 该字段为 true 时 Pi 不主动提示（仍可手动 read）。单测覆盖了解析，真机未单独跑一条。
- **本地技能同步的边界（2026-11-04 实测，§7）**：用户级 `~/.agents/skills` 同步成立（真实 Pi 系统提示里出现，location 指向该目录）；项目级 `<workspace>/.pi/skills` 与工作区祖先 `.agents/skills` 由 `-na` 切断（`-na` 下 marker 不出现，换 `-a` 阳性对照后出现）。Pi 1.5.0 **没有**只关项目/祖先技能、保留用户级发现的单独参数：`--no-skills`/`-ns` 会把用户级一起关掉，所以不能用它来做项目级隔离。
- **同名冲突（新发现的限制）**：托管 `--skill` 与用户级自动发现副本同名时，Pi 先加载发现路径、再加载 `--skill`，按 name 保留先出现的那条。实测结果是**副本胜出**：托管禁用时副本仍在（原已知行为），**托管启用时托管版本也被静默压过**（本次新确认）。RES02 的「同名副本不能绕过」因此不成立，已由 ADR 0003 记录并由真实 Pi 测试钉住（`TestSkillDiscoveryLayersAgainstRealPi/SameNameAutoCopyWinsOverManagedSkill`）。
- **运行快照口径（另一 workstream）**：旧实现把托管启用 ID 当 Pi 实际可见技能；开启自动发现后空列表也可能有技能。另一 workstream 已在 §8 记录其口径修正（拆成 `managedSkills` + `skillAutoDiscovery`），本轮 focused review 未复跑其 Go/前端用例，不代替其验收。
- **祖先扫描会到卷根**：Windows 的 `t.TempDir()` 位于用户 home 下，`-a` 阳性对照会扫到真实 `<home>/.agents/skills`（对照运行 55 条 = 1 marker + 54 本机技能）。`-na` 下该扫描被切断。生产环境 HOME 即真实 home，Pi 会按用户级去重，语义不变；但这说明 `-na` 不能省。
- **运行中改技能的 pending 提示**：单测 `TestSkillChangeIsDeferredDuringRun` 覆盖；真机因 run 太短、审批时序复杂，未抓到「pending 徽标 + activeRun 同框」的稳定截图——这是 UI 时序问题，不影响「运行中沿用旧集合」这条不变量（由快照与 `skillsPending` 字段保证）。
- **物理键盘可达性**：沿用 C01 的已知限制（agent-desktop 拒绝物理 Tab 注入），键盘序列只有 `Layout.test.tsx` 的 `userEvent.tab()` 证据。

## 7. 项目级与祖先级技能隔离（2026-11-04 真实测量）

目的：确认移除 `-ns` 后，项目级/祖先级技能是否被 `-na` 切断，以及同名冲突的真实语义。
不靠注释推断；每条测量用互不相同的 sentinel。

环境与命令：

| 项 | 值 |
|---|---|
| Pi | `.tools/pi/v1.5.0`（`@earendil-works/pi-coding-agent@1.5.0`） |
| Node | `D:\Program Files\nodejs\node.exe`（v24.16.0） |
| 版本命令 | `node <cli.js> --version` → `1.5.0`，exit 0 |
| 帮助命令 | `node <cli.js> --help` → exit 0（技能/信任相关原样摘录见下） |
| 测试命令 | `MYAGENT_PI_WORKER_INTEGRATION=1 MYAGENT_NODE_PATH='D:\Program Files\nodejs\node.exe' go test ./internal/resources -run TestSkillDiscoveryLayersAgainstRealPi -count=1 -v` |
| 退出码 | `0`（4 个子测试全 PASS，14.5–15.7s） |
| 原始证据 | `docs/evidence/raw/c03-skill-discovery.json`（sha256 `09c86e06…8607fbdc`） |

`pi --help` 相关参数原样摘录：

```text
  --skill <path>                 Load a skill file or directory (can be used multiple times)
  --no-skills, -ns               Disable skills discovery and loading
  --approve, -a                  Trust project-local files for this run
  --no-approve, -na              Ignore project-local files for this run
```

结论：**没有**只关项目/祖先技能发现而保留用户级发现的参数。

测量矩阵（产品参数 `-na -nc -ne -np --no-themes --no-mcp`；对照组只把 `-na` 换成 `-a`）：

| # | 渠道 / sentinel | `-na`（产品参数） | `-a`（阳性对照） | 判定 |
|---|---|---|---|---|
| a | 用户级 `<home>/.agents/skills/user-marker`（`SENTINEL-C03-USER-9f21c`） | 出现，location 指向临时 home 的该文件 | 出现 | 用户级同步成立 |
| b | 项目级 `<workspace>/.pi/skills/proj-marker`（`SENTINEL-C03-PROJ-4a7be`） | **不出现**（系统提示无该 sentinel） | 出现，location 指向项目路径 | 被 `-na` 切断，非假阴性 |
| c | 祖先 `<parent>/.agents/skills/anc-marker`（`SENTINEL-C03-ANC-c1d38`） | **不出现** | 出现，location 指向祖先路径 | 被 `-na` 切断，非假阴性 |
| d | 同名：托管 `--skill`（`SENTINEL-C03-MANAGED-5e0b2`）+ 用户级副本（`SENTINEL-C03-USERCOPY-8b3d7`） | 只 1 条 `dup-skill`，description/location = **用户级副本**；托管 sentinel 不出现 | 同左 | 自动发现副本胜出 |

同名冲突的两个方向都要记住：

- 托管**禁用**（不进 `--skill`）→ 用户级副本仍出现（已知、按设计同步）；
- 托管**启用**（进 `--skill`）→ 用户级副本仍胜出，托管版本被静默压过（本次新确认）。

机制：Pi 的 `--skill` 路径排在自动发现路径之后，按 name 去重时保留先出现的那条。
因此 RES02 不能再以「同名副本不能绕过」为断言；改为「纯托管禁用不加载」+
「运行记录必须如实反映实际可见技能」。详见 `docs/adr/0003-skills-discovery-and-res02.md`。

## 8. 运行记录口径（另一 workstream，2026-11-04）

**运行记录「技能」行的口径已修正（2026-11-04 收口）**：`RunSnapshot` 拆成两段事实——`managedSkills`（这一轮通过 `--skill` 显式加载的托管技能 ID；空数组 = 确实没有显式加载）与 `skillAutoDiscovery`（`enabled`/`disabled`；Pi 自动发现到的清单产品没有可信来源，界面显示「已开启（清单未知）」，未记录时显示「未知」）。旧记录（含 §3 #5 的「未加载任何技能」与旧的 `RunSnapshot.Skills` 字段）里「技能行 = 这一轮实际加载清单」的口径据此作废：空数组只说明托管侧没有显式加载，不能推断 Pi 没有加载它自己发现的用户级/祖先目录技能。`Status.runtimeSkills` 与 `skillsPending` 同步改为「托管且启用」的语义，`Status.skillAutoDiscovery` 单独暴露自动发现开关。回归用例：Go `TestSnapshotSeparatesManagedSkillsFromPiAutoDiscovery`、`TestSnapshotLeavesAutoDiscoveryUnknownWhenNotProvided`、`TestRunSnapshotDoesNotClaimNothingLoadedWhenAutoDiscoveryEnabled`；前端 `frontend/src/features/runs/RunsView.test.tsx`（有托管 / 托管空+发现开 / 发现关 / 未记录）与 `runState.test.ts` 的两段文案用例。本条只修正运行记录口径，不改 §0/§3 的历史结论，也不等于重申 C03 整体 PASS。
