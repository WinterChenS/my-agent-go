# Anvil V1 Implementation Master Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付正式品牌 Anvil：使用宿主 Pi、覆盖本地核心工作流、真实连接器/调度/资源管理、签名应用更新及右侧可操作原生浏览器的跨平台桌面 Agent。

**Architecture:** Wails 承载 React，Go 管 Harness 和自有 MCP，Pi RPC 子进程负责执行。独立 NativeBrowserHost 显示网页，六目标分开打包并在目标系统验收。

**Tech Stack:** Go 1.27.3、Node 24.16.0/npm 11.13.0、Wails v3.0.0 初始基线、React/TS/Vite/Tailwind/shadcn、SQLite、Pi `>=1.5.0 <2.0.0` 候选范围 + 隔离契约探测。

**Spec:** `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md`、`docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md`；参考 `docs/wails-browser-design.md`、`docs/gooeypi-browser-review.md`。

## Global Constraints

- R01–R14 与基线所有数值、状态、排除项适用于全部子任务。
- 这是增量计划：已有代码、任务提交和证据保持原义；新增范围必须产生自己的测试和证据，旧 PASS 不自动覆盖新 case。
- 静态 UI 原型仅是视觉输入，不是生产实现或验收证据。
- agent 禁止自行更换 Wails/Pi、扩大功能、删除平台、放宽验收或将文档描述成实测。
- 同一字段/类型以 `internal/contracts` 为唯一 Go 定义；前端从应用 DTO 生成/导出 TS 类型，不手写另一套同名协议。

## Review Focus

1. 中文/空格路径与多套 Node/Pi PATH：F00/F02 的 TestLocateWindowsShimWithSpaces，不能启动错误程序。
2. 导航和接管竞态：B04/B05 的 TestQueuedActionRevokedOnTakeover，过期动作不得稍后写页面。
3. 工具超时后实际上已写远端：F05/X02 的 TestInterruptedRunDoesNotReplayUnknownWrite，不自动重试。
4. 独立配置偷偷加载项目脚本或旧凭证：F04/C02 的 TestUntrustedProjectDoesNotExecuteMarker、TestNoImplicitAuthImport。
5. 六目标“编译通过”冒充安装通过：X03 的 MissingNativeEvidenceFailsGate，在目标系统缺失记录时返回非零。

## 模糊点如何被收敛

| 原模糊点 | 本轮决策 | 验证任务 | 不可行时 |
|---|---|---|---|
| UI 未单独冻结 | React/shadcn 为执行默认 | F00/C01 | 提交 ADR，不自换库 |
| Wails 右侧真实浏览器 | NativeBrowserHost 三端同窗口 | B01–B03 | G1 BLOCKED，不改成浮窗/截图流 |
| Pi RPC 文档与 release 可能不一致 | 1.5.0 为最低版本，`>=1.5.0 <2.0.0` 只进入隔离契约探测；2.x 默认拒绝 | F02/F08 | 探测失败标 incompatible，不凭版本号放行 |
| Pi 自动安装是否依赖 Node | 独立包先验证，基础包不带 Node；外部 Pi 不覆盖 | F06/F08 | G2/G4 BLOCKED，提出替代方案 |
| 原有 Pi 登录如何复用 | 不隐式迁移；V1 客户端 API key | C02 | 缺 OAuth 明确列为未支持 |
| 工具权限是否完整 | 所有可执行通道必须执行前检查 | F04 | 未覆盖工具禁用，不给全通过 |
| 记忆是否自动写入 | 手动/明确请求保存，候选确认；默认不自动抽取 | C05 | 不增加背景模型调用 |
| 跨平台范围 | 六目标 + Ubuntu双会话 | F00/X03 | 缺环境 BLOCKED，不删目标 |
| 应用升级与 Pi 升级 | 两套独立事务；应用后台下载、下次启动安装，Pi 使用托管 active 指针 | F06/F09/X04 | 任一回滚失败禁止发行 |
| UI 原型如何落地 | Anvil 生产组件 + Go API，zh-CN/en-US × Light/Dark | U00–U07 | 只有静态页或 mock 状态时 G3 失败 |
| 外部协作能力 | 六类 connector 走 app-owned MCP 与统一 policy | C08 | 缺 OAuth/账户标 BLOCKED，不显示已连接 |
| 无人值守任务 | 调度器创建普通 RunService 会话，审批规则不变 | C09 | DST/重入/越权任一失败阻断 G2 |

## 执行顺序与模块所有权

| 计划 | 任务 | 输入门禁 | 允许产物 |
|---|---|---|---|
| [基础与 Pi](2026-11-03-foundation-runtime.md) | F00–F07 | 文档基线 | 已有工具链、契约、协议、进程、权限、存储基线 |
| [Pi 生命周期与应用更新](2026-11-05-pi-lifecycle-updater.md) | F08、重定范围 F06、F09 | F02/F03/F04/F07 | 多来源发现、兼容协商、Pi 安装回滚、Anvil 更新 |
| [浏览器](2026-11-03-native-browser.md) | B00–B07 | F00/F01；B04以后需F04/F07 | 原生host、DOM工具、接管、下载、Chat 右栏集成 |
| [资源与工作台](2026-11-03-resources-workbench.md) | C01–C07 | G0；写操作依赖F04；集成浏览器需G1 | 已有资源域与工作台能力，按 U 任务接入新页面 |
| [连接器与调度](2026-11-05-connectors-scheduler.md) | C08–C09 | F04/F05/F07；UI 依赖U00/U01 | 六类真实 adapter、自有 MCP 工具、本地调度器及页面 |
| [Anvil 产品 UI](2026-11-05-anvil-ui.md) | U00–U07 | 已有域 API；F08/F09/C08/C09 分阶段接入 | 品牌、i18n、主题、九页生产 UI、无障碍与真机证据 |
| [集成与发行](2026-11-03-integration-release.md) | X01–X05 | G1/G2/G3依次 | 真模型E2E、Anvil 安装包、迁移、更新与正式发行 |

建议关键路径：保留已完成的 F/C/B 基础 → F08 → U00 → C06/C07 → U01 → C08→C09→F09→F06 → U02–U06 → U07 → 扩展 G2/G3 → X03/X04 → G4/G5。U03–U05 不依赖 G2，可提前；U02 经 C06 依赖 G2。F06 是 G2 阶段最后一个后端功能，开发机上只用临时托管根（ADR 0002 安全约束）。共享文件 `internal/app/desktop.go`/`cmd/myagent/main.go` 按 F08→C08→C09→F09→F06 串行。B01–B07、macOS/Linux 原生矩阵可在不争用共享契约文件时并行推进；任何一端缺失仍保持对应 gate BLOCKED。

上表不授权自行使用多 agent。只有用户选择并行执行后，才按文件所有权委派；contracts/schema 和 master gate 由主执行者串行维护。每个任务的依赖以对应计划为准，不能把表内斜杠当成全部可以并行。

### 依赖语义

`v1-plan-index.json` 用两个字段区分两种关系。`dependsOn` 是开工前提：依赖任务在当前可用目标上已有证据后才开工；X03/X05 的打包与发行在这里直接写 gate，表示硬性等待。`acceptAfter` 是验收顺序：该任务的 case 只有在那个 gate 全矩阵通过后才能计为 PASS，但不阻止先在单机上开发并产出 win-x64 证据。这与 C01–C05 的实际做法一致；两种关系混在一个字段里会产生假环（2026-11-05 实测 F06→F09→C09→C08→U01→C06→G2→F06）。`internal/devtools` 的 `TestPlanIndexIsConsistent` 检查无环、每个 case 的 gate 确实等待其全部 owner、每个任务都有 case 且追踪到 R 需求。

### 共享写点（单写者）

| 文件 | 写者顺序 |
|---|---|
| `internal/app/desktop.go`、`cmd/myagent/main.go` | F08 → U00 → C06 → C08 → C09 → F09 → F06。新增 API 放在独立的 `internal/app/*_api.go`（同一 `Desktop` 服务会自动绑定），只有结构字段与启动/关闭装配才改 desktop.go |
| `frontend/src/app/Router.tsx`、`navigation.ts` | U01 建立全部九个路由，C08/C09 只替换自己的占位页 |
| `internal/contracts/*`、`schemas/*` | 当前持有任务独写（U00 改错误契约，U03 改 Profile），其余任务不碰 |
| `tests/acceptance/cases.json`、`internal/devtools` | 主执行者串行；U07 期间归 U07 |

### 数据库迁移登记

| 编号 | 内容 | 任务 |
|---|---|---|
| 001 | workspace/session/run/memory 初始结构 | F07（已有） |
| 002 | 记忆 scope 与检索 | C05（已有） |
| 003 | 连接器定义 | C08 |
| 004 | 调度与 occurrence | C09 |
| 005 | 会话目录字段 | C06 |
| 006 | 全局搜索索引 | U01 |
| 007 | 产物索引与 run usage | C07 |

编号按实际合入顺序仍顺序执行；任务顺序与编号不一致时由主执行者重编号未合入的迁移，已合入的迁移不得改号。

### 外部依赖

机器、账户、OAuth/应用注册、签名证书、更新托管与密钥的清单见 [v1-external-dependencies.md](../../acceptance/v1-external-dependencies.md)。缺失项对应的 case 记 BLOCKED，不删 case、不换口径。

## 门禁阶段

| Gate | 通过条件 | 必要证据 | 失败阻断 |
|---|---|---|---|
| G0 | 工具链/隔离、Pi 多来源发现、版本识别、兼容探测、RPC/扩展/MCP/权限契约通过；六目标资源可用性记录 | 锁文件、候选路径/version/hash、探测原始记录、hook副作用计数、环境清单 | 产品功能集成；独立模块可继续补齐 |
| G1 | 六目标原生浏览器真实网页、DOM、截图、焦点、缩放、接管和弹层协调通过 | 目标系统日志、录屏、平台与引擎版本、native fixture结果 | 浏览器完成声明、G3/G4 |
| G2 | F05/F06/F07、C02–C05、C08/C09、B04–B06 的必需原子 case 全通过 | Go/TS报告、Pi 安装/回滚、连接器副作用计数、调度可控时钟与负例 oracle | 完整UI/E2E验收 |
| G3 | C01/C06/C07/B07、U00–U07、X01/X02 的 Anvil UI、i18n、主题、资源库、观测和真实闭环通过 | 构建后 Wails UI、真实 Go API/DB/文件/进程 oracle、真实Pi+模型结果 | 打包候选版本验收 |
| G4 | X03/X04 六目标干净环境安装、首次 Pi 安装、真实应用更新、升级恢复和数据保留通过；PI_PKG/UPD/PKG/PERF 必需 case 齐全 | 旧/新包版本与hash、包内容扫描、安装后清单、屏幕/日志、数据校验、回滚记录 | 宣称V1可交付 |
| G5 | X05 正式签名/公证/来源校验和发布清单完成 | 签名验证、公证/staple、同 revision 发布清单 | 面向用户公开正式发行 |

G4 可以出内部测试候选包，不能声称已过 G5。未签名测试包必须标 internal，不能用用户关闭系统保护后的运行结果替代正式发行验收。

## 统一命令契约（F01实现后才可用）

在仓库根运行，所有子进程保留退出码，不允许 || true：

```text
go run ./cmd/devtools verify --suite=static
go run ./cmd/devtools verify --suite=unit
go run ./cmd/devtools verify --suite=integration --target=<targetId>
go run ./cmd/devtools verify --suite=native --target=<targetId> --display=<session>
go run ./cmd/devtools verify --suite=e2e --target=<targetId>
go run ./cmd/devtools package --target=<targetId> --channel=internal
go run ./cmd/devtools verify --suite=package --target=<targetId> --artifact=<absolute-path>
go run ./cmd/devtools gate --id=G0 --evidence=<absolute-evidence-root>
```

suite=static：gofmt差异检查、go vet、前端 typecheck/lint、锁文件/依赖来源检查。suite=unit：go test -json ./internal/... 与 npm --prefix frontend run test -- --run；race 在具备工具链的runner独立运行并记录，不能以race无法运行跳过其他测试。

native/e2e 必须实际启动构建后的 Wails 程序，不是 Playwright 浏览器渲染 React 替代。Playwright 可验证前端组件，但不能据其结果宣称 native child view 工作。

每个 suite 由 `tests/acceptance/cases.json` 显式列出用例、目标、runner、自动/人工、required。计划要求的用例不在清单、runner不存在、没有执行、空结果、未知target、unknown gate均返回2并阻止通过；断言失败返回1；只有当前版本匹配且全部要求满足返回0。人工用例要有真实机器元数据、步骤、结果、录屏/截图、审查人；不能填一个布尔值冒充证据。

`cmd/devtools` 只编排真实检查，不能用写出 passed.json 代替执行。gate 必须校验原始报告哈希与用例索引，并重新运行轻量可重放检查；证据哈希保证一致性而非证明机器真实存在，关键原生与发行结果还需独立审查。

## 单任务执行规则

每个任务必须填：Task ID、基线revision、依赖状态、修改文件、测试命令及退出码、原始证据路径、未覆盖项、下一允许任务。每个任务的第一个失败测试来自真实可观测结果；测试不是“函数不存在”即可满足所有验收。

完成任务后提交与任务关联的变更（用户代码不混入），记录 code revision。未初始化 Git 时先记录输入文件清单并在用户授权的开发范围内初始化；不在本轮文档编写时创建仓库。

任务状态 TODO/DOING/REVIEW/PASS/FAIL/BLOCKED。PASS 由验收证据确定，不由实现者“看起来能用”确定。任务中途暂停保持 DOING 或 BLOCKED，禁止勾掉任务清单。

## 变更与停止条件

以下必须先记录 ADR 并提出影响，不继续依赖工作：更换桌面壳/浏览器承载方式、基础包捆绑 Pi/Node/Chromium、删除目标平台、要求私有API/关闭隔离、迁移用户原凭证、降低失败测试或阈值、增加后置功能。用户已有明确授权范围内的小实现选择自行解决并记录，不把每个库函数变成权限请求。

外部资源不足时允许继续独立任务；受影响 gate 保持 BLOCKED。禁止为了显示全绿安装临时假 Pi、使用 mock 截图、隐藏 failed/skip、修改 fixture 使其迁就错误实现。

## 实施前自检

- [ ] 执行者已阅读两份 V1 规格、对应子计划、验收矩阵及现场 AGENTS.md。
- [ ] 已检查工作树，保护用户现有和未提交改动；已完成任务不因 UI 重构退回 TODO。
- [ ] 清楚当前 Task 的唯一写者、产物、依赖、原子 case 和明确排除项。
- [ ] 明确所需目标机器、OAuth/模型凭证、测试账户和签名资源是否具备；缺失时预先标 BLOCKED。
- [ ] 新增共享契约、schema、case 或 gate 改动由主执行者串行完成。

当前仓库有代码和部分任务证据，但新增 F08/F09/C08/C09/U00–U07 及扩展 case 均不得借用旧证据判 PASS。详细用例见 `docs/acceptance/v1-gates.md`，任务/依赖/需求追踪索引见 `docs/acceptance/v1-plan-index.json`。索引是计划元数据，不是完成状态。
