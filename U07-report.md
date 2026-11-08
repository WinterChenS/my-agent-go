# U07 报告：可访问性 / 视觉回归 / 真机 UI 验收（UI12、UI13、I18N02–04、THEME02–05）

日期：2026-11-08。工作树：`D:\project\anvil-wt\u07`，分支 `feat/u07`。

## 状态

**REVIEW（部分 PASS、一条 FAIL、若干项 deferred）**

- 9 条 native case 有了构建后 Wails 应用的真机证据：UI12、UI13、UI12.detectors、I18N02、I18N03、
  THEME02、THEME03、THEME04、THEME05 → **PASS**。
- **I18N04 → FAIL**：en-US 下 Skills 与 MCP 两页仍是整段中文硬编码（旧分区未迁移）。这不是 U07 能
  顺手修的——它需要 U04 计划的 `SkillsPage/McpPage`。
- G3 门禁整体仍是 **exit 2（配置缺失）**：G3 的 48 条必需 native case 里，UI01–UI11、E2E01–07、
  CONN07、SCH06、RES05、SES/SRCH/BRAND/ART 等 38 条还没有 native runner（属 U01–U06/C08/C09/X01–X02
  的任务范围）。已跑的部分不等于 G3 通过。

## 提交

| commit | 内容 |
|---|---|
| `7bb1484` | `U07: gate Anvil UI accessibility and native fidelity`：预检工具链 + 9 条 native case + 审计器 + 四个真实缺陷修复 + 文档/用例索引 |
| `2e0778b` | `U07: attribute the U07 native cases to task U07`：native runner 的 case→task 归属（UI12/UI13/I18N*/THEME* → U07，原来错记成 B07/C06） |
| 本报告的提交 | `U07: record the accessibility and native UI evidence`：报告与最终数字 |

证据树（不入库，`.gitignore` 已排除）：`docs/evidence/runs/2e0778b/G3/win-x64/desktop/<CASE>/`。

## 测试摘要

| 命令 | 结果 |
|---|---|
| `npm --prefix frontend run test:e2e`（预检） | **exit 1**：183 passed / 5 failed。失败全部指向已知缺陷：`no-mock` 的 direct-color（667 条调色板类名）、`locales-themes` 的 en-US skills/mcp 中文泄漏 |
| `npm --prefix frontend test`（单测） | 314 tests / 36 files 全通过 |
| `powershell scripts/devtools.ps1 verify --suite=static` | exit 0（gofmt / go vet / 前端 typecheck） |
| `node scripts/check-ui-contract.mjs` | PASS（catalog 之外文案增量 0；40 个 Go 错误码两种语言全覆盖） |
| `powershell scripts/devtools.ps1 verify --suite=native --target=win-x64 --display=desktop --gate=G3` | 证据根 `docs/evidence/runs/2e0778b/G3/win-x64/desktop`：可跑 10 条里 9 PASS / I18N04 FAIL；另有 38 条 G3 必需 case 缺 native runner → 汇总 exit 2（配置缺失） |
| `powershell scripts/devtools.ps1 gate --id=G3 --evidence=.../runs/2e0778b` | exit 2：逐条列出 38 条缺证据 + I18N04 的 FAIL，没有任何 PASS 被算成通过 |

## 有证据的 case（构建后的 Wails 应用，`docs/evidence/runs/<rev>/G3/win-x64/desktop/`）

| case | 结果 | 关键外部判据 |
|---|---|---|
| UI12 | PASS | 五档窗口（880×600…1920×1080）× 九页；`GetClientRect` 客户区等于请求值、`GetDpiForWindow`/96 = 页面 `devicePixelRatio`；无横向滚动、无控件横向裁剪、无交互元素实质重叠、无不可达控件；`WindowFromPoint` 三个采样点 `GetAncestor(GA_ROOT)` 都是主窗口；右侧原生视图（`MyAgentNativeHostChild`）默认不可见 |
| UI13 | PASS | 九页 × zh/en × light/dark；可访问名称齐备、文本与语义 token 满足 WCAG AA；真实渲染器 `Accessibility.getFullAXTree` 非空（例：workbench 224 节点 / 141 有名字）；键盘 Tab 14 步全部 `:focus-visible` 且有焦点环 |
| UI12.detectors | PASS | 注入违规结构后 unnamed-control / low-contrast / clipped-control / overlap / truncated-label / 无焦点环 六条检查全部报警；移除注入后基线恢复 0 发现（`detector-selfcheck.json`） |
| I18N02 | PASS | 设置页切语言：`<html lang>`、侧栏九入口、页面文案即时切换；`performance.timeOrigin` 与页面随机标记不变（**没有重载**）；搜索草稿 `draft-状态-К` 原样保留；`Status` 绑定值前后一致；`GetPreferences` revision 0→1，磁盘 `config/preferences.json` = en-US |
| I18N03 | PASS | 真实 CAS 冲突（外部先写一次再点界面保存）→ 界面显示 `CONFIG_CONFLICT`，文案分别来自中/英 catalog 且互不串台；导出诊断包输出：日期 `2026年11月8日 11:28` vs `Nov 8, 2026, 11:28 AM`，数字走 `Intl.NumberFormat`，体积单位走 `units.*` |
| I18N04 | **FAIL** | 36 条 (九页 × 两语言 × 两档窗口) 布局记录全干净；但 en-US 下 skills/mcp 两页中文硬编码 → 中文泄漏 4 条（两页 × 两档窗口），证据里逐条列出原文 |
| THEME02 | PASS | light / dark / 跟随系统 → `data-theme` 只解析成 light|dark；`GetPreferences` 与磁盘都是最后一次显式选择；重启同一 `ANVIL_HOME`：首屏第一个"有内容"的采样点已是 dark（`hadContentBeforeTheme=false`） |
| THEME03 | PASS | 两主题 × 九页抽样控件 default/hover/active 对比度 ≥3.0（真实 `Input.dispatchMouseEvent` 触发 hover/active，松开前移开指针以免触发点击）；每页至少一个控件 hover 或 active 有可见变化；warning/danger 语义 token 对比度写入 `state-contrast.jsonl` |
| THEME04 | PASS | 切主题：timeOrigin/页面标记不变、草稿与右侧详情栏保持、`Status`/`BrowserState` 绑定值不变、原生子窗口集合不变（`preservation.jsonl`） |
| THEME05 | PASS | 九页 × 两主题 CDP 截图 18 张（文件字节数 + sha256）+ 偏好 revision + 窗口/DPI 事实 |

**检查器自身的红证**：每个断言都先在 `frontend/e2e/fixtures/violations/` 的故意违规夹具上失败
（`fixtures.spec.ts`：7 条，含干净对照页必须零发现），再对生产页面运行；native 侧另有
`UI12.detectors` 在真机引擎里注入违规结构复证同一批规则。

## 这次任务里修掉的真实缺陷

1. **语言切换只换一半**：`setLocale` 改模块变量不会触发 React 重渲染 → 新增 `onLocaleChange` 订阅，
   App 订阅后整棵树重画。
2. **语言偏好重启后丢失**：`initLocale` 不读本地镜像、启动后也不与后端 `Preferences` 对齐 → 新增
   `anvil.locale` 镜像 + 启动后按后端对齐（与主题同一套做法）。
3. **对话页在侧栏不高亮、`<main aria-label>` 退回应用名**：`/chat` 的路由 id `chat-index` 与侧栏入口
   id `chat` 不一致 → 合并为同一个 id（Router 测试相应改为"路径唯一 + id 与组件一一对应"）。
4. **`agents.missingRefs` 消息 key 缺失**（两种语言都没有）：界面上会直接显示 `agents.missingRefs`。
5. **`max-w-[85%]` 被静态扫描误判为硬编码指标**：任意值宽度不是产品指标，扫描器排除方括号内匹配。
6. 证据通道：`ANVIL_UI_CDP_PORT`（默认关闭，仅当显式设置时给主 WebView2 打开 DevTools 协议），
   并在日志里留痕。没有它就无法在**构建后的应用**里读布局/可访问性树/对比度。

## Deferred / 未覆盖（未伪造）

| 项 | 原因 |
|---|---|
| Windows 150% / 200% 系统缩放 | 本机 96 DPI，改显示缩放需注销/重启；ADR 0005 本轮只验收 win-x64 100% |
| macOS Retina、Linux X11 / Wayland | ADR 0005：目标停用，本轮无证据（gate 里是 deferred，不是通过） |
| 审批风险态（waiting_approval 卡片）的对比度/可见性 | 需要一次真实 run；本机 PATH 上的 Pi 是 1.4.0（最低 1.5.0），无法启动运行 → 用 warning/danger 语义 token 对比度作为色彩基线，卡片本身未产生 |
| 费用格式化（`formatCost`） | 需要 provider 真实 usage/计价（缺 Pi 与模型凭证） |
| 主题切换"不取消下载、不改人工接管归属" | 需要真实 run + 浏览器标签页；已证明页面不重载、原生子窗口集合不变、`BrowserState` 绑定值不变 |
| 跟随系统在运行中变化（不重载） | 需交互式改系统配色（本机做不到）；代码路径是 `matchMedia` 订阅，单测覆盖 |
| 读屏软件本体（NVDA/Narrator） | 未接入；用真实渲染器可访问性树 + `:focus-visible` 键盘序列替代 |
| UI01–UI11 / E2E01–07 / CONN07 / SCH06 等 38 条 G3 native case | 不属于 U07 范围，没有 native runner → 门禁按配置缺失（exit 2）处理 |

## Concerns（需要后续任务处理，不是 U07 的收尾项）

1. **U04 的 Skills/MCP 生产页没有落地**：路由渲染的仍是 `SkillsSection`/`ExtensionsSection`/
   `MCPSection`/`ToolsSection`，其中 171 处用户可见中文是硬编码（`scripts/ui-copy-baseline.json`
   里登记的就是它们），并大量使用深色调色板类名。I18N04（本条 FAIL）与 THEME01 的 667 条违规都由
   此产生。修法：按 U04 计划写 `SkillsPage/McpPage`，把文案迁进 catalog、颜色迁成语义 token，并同步
   下调 `ui-copy-baseline.json` 的条目。
2. **THEME01 的静态规则目前是红的**：`frontend/e2e/no-mock.spec.ts` 的 direct-color 报 667 条，
   分布在 15 个文件（`app/DetailPanel.tsx`、`components/StateViews.tsx` 等壳层组件也在其中）。
   这不是"新引入的回归"，而是 U00 的静态规则一直没被真正执行过。
3. **文档标题是 `MyAgent`**：`frontend/index.html` 的 `<title>` 未随品牌迁移改成 Anvil，它会成为
   页面的可访问名称（本报告的证据里可见 AX 树 RootWebArea name = "MyAgent"）。属 BRAND 范围，
   U07 未改。
4. **G3 远未闭合**：本次只补了 U07 的 9 条 case；UI01–UI11 等 38 条没有 native 执行方式。
   建议在 U01–U06 各自的收尾里补 native runner，否则 G3 永远停在"配置缺失"。
5. **Playwright 浏览器二进制 741 MB** 落在 `.tools/pw-browsers`（gitignore）。CI 需要
   `npm --prefix frontend run test:e2e:install`，否则预检会以"浏览器缺失"失败——这是配置缺失，
   不是通过。
