# Agent Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付使用宿主 Pi、支持本地资源管理及右侧可操作浏览器的跨平台 Go 桌面 Agent。

**Architecture:** Wails 承载 React，Go 管 Harness 和自有 MCP，Pi RPC 子进程负责执行。独立 NativeBrowserHost 显示网页，六目标分开打包并在目标系统验收。

**Tech Stack:** Go 1.27.3、Node 24.16.0/npm 11.13.0、Wails v3.0.0 初始基线、React/TS/Vite/Tailwind/shadcn、SQLite、Pi v1.5.0 初始认证版本。

**Spec:** `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md`；参考 `docs/wails-browser-design.md`、`docs/gooeypi-browser-review.md`。

## Global Constraints

- R01–R10 与基线所有数值、状态、排除项适用于全部子任务。
- 初始只有文档；下文命令、路径和类型是待实现契约，不是已存在的工具或通过记录。
- 用户未要求本轮实现，交付任务书后停止，不自动执行开发。
- agent 禁止自行更换 Wails/Pi、扩大功能、删除平台、放宽验收或将当前文档描述成实测。
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
| Pi RPC 文档与 release 可能不一致 | 精确认证 v1.5.0，捕获真实协议 fixtures | F02/F03 | 修正适配，保留失败证据 |
| Pi 自动安装是否依赖 Node | 独立包先验证，基础包不带 Node | F02/F06 | G0 BLOCKED，提出替代方案 |
| 原有 Pi 登录如何复用 | 不隐式迁移；V1 客户端 API key | C02 | 缺 OAuth 明确列为未支持 |
| 工具权限是否完整 | 所有可执行通道必须执行前检查 | F04 | 未覆盖工具禁用，不给全通过 |
| 记忆是否自动写入 | 手动/明确请求保存，候选确认；默认不自动抽取 | C05 | 不增加背景模型调用 |
| 跨平台范围 | 六目标 + Ubuntu双会话 | F00/X03 | 缺环境 BLOCKED，不删目标 |
| 应用升级与 Pi 升级 | 应用手动覆盖保留数据，Pi 独立版本切换 | F06/X04 | 数据损坏/回滚失败禁止发行 |

## 执行顺序与模块所有权

| 计划 | 任务 | 输入门禁 | 允许产物 |
|---|---|---|---|
| [基础与 Pi](2026-11-03-foundation-runtime.md) | F00–F07 | 文档基线 | 工具链、契约、协议、进程、权限、安装、存储 |
| [浏览器](2026-11-03-native-browser.md) | B00–B07 | F00/F01；B04以后需F04/F07 | 原生host、DOM工具、接管、下载、UI集成 |
| [资源与工作台](2026-11-03-resources-workbench.md) | C01–C07 | G0；写操作依赖F04；集成浏览器需G1 | 页面、模型、skills/MCP/工具、记忆、上下文/产物 |
| [集成与发行](2026-11-03-integration-release.md) | X01–X05 | G1/G2/G3依次 | 真模型E2E、安装包、迁移、正式发行 |

建议关键路径：F00 → F01 → F02/F03 → F04 → G0；随后 B00→B01→B02→B03→G1。原生风险先消除，再补完整工作台；不要先做一套精美 UI 后发现三端浏览器无法交付。

上表不授权自行使用多 agent。只有用户选择并行执行后，才按文件所有权委派；contracts/schema 和 master gate 由主执行者串行维护。每个任务的依赖以对应计划为准，不能把表内斜杠当成全部可以并行。

## 门禁阶段

| Gate | 通过条件 | 必要证据 | 失败阻断 |
|---|---|---|---|
| G0 | F00–F04 的版本、契约、真实Pi/RPC/权限前置均通过；六目标资源可用性记录 | 锁文件、真实Pi协议记录、hook副作用计数、环境清单 | 产品功能集成；基础模块可独立补齐 |
| G1 | B00–B03 三端真实网页与最低DOM操作、焦点/弹层/截图通过 | 目标系统日志、录屏、平台与引擎版本、native fixture结果 | 浏览器完成声明、G3/G4 |
| G2 | F05、F07、C02–C05、B04–B06 deterministic suites全通过（F06 安装器推迟到 G4，见 ADR 0002） | Go/TS报告、负例副作用计数 | 完整UI/E2E验收 |
| G3 | C01/C06/C07/B07、X01/X02 真实端到端闭环和故障演练通过 | UI证据、真实Pi+模型结果、受控网页结果 | 打包候选版本验收 |
| G4 | X03/X04 六目标安装包在干净目标环境安装、升级/恢复、启动使用通过；F06 的 INS01–07（下载/校验/激活/回滚/不覆盖外部Pi）通过 | 包哈希、安装后清单、屏幕/日志、数据校验、安装器拒绝负例记录 | 宣称V1可交付 |
| G5 | X05 正式签名/公证/来源校验完成 | 签名验证、公证/staple、发布清单 | 面向用户公开正式发行 |

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

- [ ] 执行者已阅读本基线、对应子计划、验收矩阵及现场AGENTS.md。
- [ ] 清楚当前任务的唯一产物和明确排除项。
- [ ] 明确所需目标机器、模型凭证和签名资源是否具备。
- [ ] 确认本轮只是计划；只有进入开发执行时才安装依赖、初始化工程或修改产品代码。

本轮已做文档自审，尚无任何开发 gate 通过；详细用例见 `docs/acceptance/v1-gates.md`，任务/依赖/需求追踪机器索引见 `docs/acceptance/v1-plan-index.json`。索引是计划元数据，不是已实现测试runner或完成状态。
