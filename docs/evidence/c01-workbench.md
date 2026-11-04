# C01 工作台骨架、工作区与 Profile

日期：2026-11-04
宿主：Windows 10 Pro 22H2（19045）/ amd64 / Intel N100
裁决：**PASS（范围见 §7；本文件不等于 G0 通过，G0 另有门禁结论）**

本文件记录的是**在真实桌面上用真实鼠标点击、并接真实大模型跑完一轮**的证据，
不是"单测通过"或"React 网页截图"。所有截图来自 `agent-desktop` 对
`MyAgent.exe` 窗口的截图；所有交互都是 `agent-desktop click / set-value / scroll`
对可访问性元素发出的真实操作。

对应计划条目：`docs/superpowers/plans/2026-11-03-resources-workbench.md` 的 C01。

## 1. 被测物

| 项 | 值 |
|---|---|
| 可执行 | `build/bin/MyAgent.exe`（Wails v3 + 内嵌前端构建产物） |
| 界面 | 五入口（工作台 / 资源库 / 记忆 / 运行记录 / 设置）+ 三栏（左项目列表 / 中内容 / 右可折叠详情） |
| 服务 | `internal/workspace`（工作区与信任）、`internal/profile`（配置档）、`internal/config`（CAS 存储与合并） |
| 模型 | **真实模型**：`deepseek` / `deepseek-2/deepseek-v4.1-flash`，经本机网关 `http://192.168.3.10:3425/v1` |
| Pi | `.tools/pi/v1.5.0`（Node 24.16.0 启动） |
| 隔离 | `-na -nc -ne -ns -np --no-themes -e builtin:mcp -e <abs>/pi-extensions/harness-policy.ts` |

启动方式（开发态未打包，`MYAGENT_HOME` 指向临时目录以免污染真实用户数据）：

```bash
cd /d/project/my-agent-go
"D:/Program Files/nodejs/npm.cmd" --prefix frontend run build
go build -o build/bin/MyAgent.exe ./cmd/myagent

MYAGENT_HOME="D:/project/my-agent-go/.tmp/e2e/home-c01" \
MYAGENT_UI_A11Y=1 \
MYAGENT_SHELL=1 \
./build/bin/MyAgent.exe
```

`MYAGENT_UI_A11Y=1` 给 WebView2 打开 `--force-renderer-accessibility`。
不开时页面在可访问性树里只剩一个空的 `Chrome Legacy Window`，任何点击只能靠坐标猜。

## 2. 交付物

| 文件 | 作用 |
|---|---|
| `internal/workspace/service.go` | 工作区登记与信任：`Abs → Clean → Stat → EvalSymlinks` 归一、`sha256(dedupeKey)[:12]` 派生 ID、`Trusted` 默认 false、`AssertRunnable` |
| `internal/profile/service.go` | 配置档：只存引用不存秘密、保存时校验引用存在、启动前用"不含本 profile 的下层配置"再查一次 |
| `internal/app/workspace_api.go` | Wails 绑定：`CallError{Code,Message,Retryable}` + 工作区/配置档 CRUD 与冲突差异 |
| `internal/config/merge.go` | `NormalizeFilesystem` 成为文件访问枚举的唯一真相源；权限合并改为"最严格者胜" |
| `internal/config/store.go` | `ListKeys(prefix)`：目录扫描而非维护索引（索引与正文是两份真相） |
| `internal/run/service.go` | `IsolationArgs()`：诊断展示与真实启动参数同源 |
| `frontend/src/app/{App,Layout,Router}.tsx` | 五入口装配、三栏布局、纯函数导航（roving tabindex + 方向键） |
| `frontend/src/components/StateViews.tsx` | 四态：加载（`role=status`）/ 空（不冒充加载）/ 失败（`role=alert` + code）/ 无权限（必带可执行动作） |
| `frontend/src/features/{workspaces,workbench,profiles,resources,runs,memory,settings}/` | 五个页面 + 侧边栏 + 右栏详情 |

计划里写的测试文件名是 `workspace_test.go` / `ProfileEditor.test.tsx`；
实际落成 `internal/workspace/service_test.go`（同包同名测试）、
`internal/app/workspace_api_test.go`、`frontend/src/features/profiles/ProfileEditor.test.tsx`。

## 3. 真机操作序列

### 3.1 首次启动 → 信任 → 真实模型跑一轮

| # | 操作 | 期望 | 实际 | 截图 |
|---|---|---|---|---|
| 1 | 全新 home 启动 | 左栏列出工作区，状态未信任；主区说明"未信任不能执行代码" | 一致；**但主区与左栏曾短暂自相矛盾**（见 §5 缺陷 1） | `01-first-launch.png`、`02-first-launch-fixed.png` |
| 2 | 点左栏闭锁图标 | 变成已信任，主区黄色提示消失 | 一致；**但主区曾不刷新**（见 §5 缺陷 2） | `03-trusted.png`、`06-workbench-trusted.png` |
| 3 | `set-value` 输入"创建 real.txt，内容写 real-model-ok" → 点发送 | 真实模型决定调工具，弹出审批卡 | 模型发出 `write` 调用，审批卡显示参数 `{"content":"real-model-ok","path":"real.txt"}`，顶栏"等待授权" | `22-real-run.png` |
| 4 | 点"允许一次" | 授权后工具执行 | `write` 结果 `Successfully wrote to real.txt`，文件 13 字节 | `23-after-allow.png` |
| 5 | 模型追加 `bash` 自查 → 点"允许一次" | 命令执行，返回真实输出 | `cat real.txt` 输出 `real-model-ok` | — |
| 6 | 等待回合结束 | 界面显示模型回复，顶栏"已完成" | 回复内容与磁盘一致（"13 字节，已用 cat 校验"） | `24-real-completed.png` |
| 7 | 打开运行记录 | 数据来自事件流，不是界面推算 | `run-1793779803-1` / 已完成 / 事件序号 20 / 工具完成 2 失败 0 执行中 0 | `25-runs-real.png` |

磁盘核对（不是看截图）：

```
$ ls -la .tmp/e2e/home-c01/workspace/
-rw-r--r-- 13 2026-11-04 16:10:58 real.txt
-rw-r--r--  4 2026-11-04 15:58:02 tool-ran.txt
$ cat real.txt
real-model-ok
```

Pi 会话记录（`.tmp/e2e/home-c01/config/agents/ws-default-profile-default/sessions/*.jsonl`）
里 `write` 的 toolResult 为 `{"text":"Successfully wrote to real.txt","isError":false,"durationMs":23}`，
`bash` 的 toolResult 带 `ls -la` 的真实输出。**模型、工具、审批、事件流是同一次运行**。

### 3.2 五入口逐一真实点击（1180×820）

| 入口 | 实际内容 | 截图 |
|---|---|---|
| 工作台 | 消息流 + 工具卡 + 审批卡 + 输入框；未信任时换成无权限态并给"去设置里处理" | `06-workbench-trusted.png`、`07-trusted-ok.png` |
| 资源库 | 配置档（可用）+ 模型/技能/MCP 三块明确标 `未实现 · C02/C03/C04`，不显示任何假数据 | `05-resources-tab.png`、`40-reloaded-app.png` |
| 记忆 | 空态并说明属 C05 | `13-tab-记忆.png` |
| 运行记录 | 当前运行真实统计 + 历史标 `未实现 · C06` | `13-tab-运行记录.png` |
| 设置 | 运行环境字段、工作区信任列表、诊断只读区 | `13-tab-设置.png` |

### 3.3 诊断信息必须等于实际启动参数

`21-settings-diag.png` 读到的隔离参数：

```
-na -nc -ne -ns -np --no-themes -e builtin:mcp -e D:/project/my-agent-go/pi-extensions/harness-policy.ts
```

不含 `--no-mcp`，且带上真实加载的策略扩展。修复前的同一位置显示的是
`... --no-mcp ...`，与"审批和自有 MCP 实际都在工作"直接矛盾（见 §5 缺陷 3）。

### 3.4 最小 1024×720

窗口用 `agent-desktop resize-window --width 1024 --height 720` 调到 1024×720
（产品 `MinWidth/MinHeight` 是 880×600，1024×720 是计划要求的验收尺寸）。

- 工作台：发送按钮完整可见，输入框与消息区无遮挡（`30-1024-workbench.png`）
- 资源库：配置档区 + 三块未实现区分列正常（`31-1024-资源库.png`）
- 运行记录 / 记忆：内容不溢出（`31-1024-运行记录.png`、`31-1024-记忆.png`）
- 设置：两列表单不重叠，"保存设置"完整可见（`31-1024-设置.png`）；
  `PI 路径` 的输入框内容被横向裁切（输入框本身可滚动），这是长路径的正常表现

### 3.5 配置档：引用校验 → 保存 → 冲突 → 重载

先往用户层写入一个模型定义（C02 之前没有模型 UI，用配置存储的真实 CAS 写入，
见 §8 的 `seed` 工具）：

| # | 操作 | 期望 | 实际 | 截图 |
|---|---|---|---|---|
| 1 | 新建配置档，模型 ID 填 `m-not-exist`，保存 | 拒绝并说明缺什么 | `DEPENDENCY_MISSING 引用的模型 m-not-exist 不存在：请先在资源库中定义它` | `33-profile-ref-error.png` |
| 2 | 模型 ID 改成已存在的 `m-demo`，保存 | 成功 | 落盘 `config-profile-p-ui.json`，envelope `revision 1`；body 只有 `profile` + `permissions`，**不展开 models 数组** | `34-profile-saved.png` |
| 3 | 外部写者把该文档改成 revision 2（改 name 与 toolPolicy），再点"编辑" → 保存 | 冲突，逐字段展示差异，不覆盖 | 面板显示"保存被拒绝：磁盘上的版本已被改动（当前 revision 2）"，差异两行：`name`、`toolPolicy` | `37-conflict-panel.png` |
| 4 | 点"放弃我的改动，重新加载" | 恢复为磁盘版本 | 名称变"别人改过的名字"、文件访问变"只读"、底注"基于 revision 2"，列表同步刷新 | `38-reloaded.png` |
| 5 | 外部改到 revision 3 后再点"编辑" | 编辑器里是磁盘最新值，而不是列表快照 | 列表仍显示 rev 2（快照），编辑器显示"外部第三次修改 / 仅工作区 / 基于 revision 3" | `41-edit-fresh.png` |

第 5 步是修复后的行为；修复前这里显示的是列表里的旧名字、旧策略、旧 revision（见 §5 缺陷 4）。

## 4. 自动化验证（本次改动后重跑）

| 命令 | 结果 |
|---|---|
| `./scripts/devtools.sh verify --suite=static` | PASS（gofmt / go vet / 前端 typecheck） |
| `./scripts/devtools.sh verify --suite=unit` | PASS（Go 全包 + 前端 8 个测试文件 62 条） |
| `./scripts/devtools.sh verify --suite=integration --target=win-x64` | PASS（24 条必需 case，失败 0，阻塞 0） |
| `./scripts/devtools.sh gate --id=G0 --evidence=docs/evidence/runs/ca27254/win-x64` | 退出码 0 |

测试分布：

| 包 / 目录 | 条数 | 覆盖点 |
|---|---|---|
| `internal/workspace` | 8 | 四种写法同一 ID、junction 归一、未信任拒绝运行、信任持久化与撤销、坏文档报错而非跳过、`dedupeKey` 大小写 |
| `internal/profile` | 9 | 编译层被 Merge 消费（既生效又不吞下层字段）、非法枚举拒绝、不许放宽上层权限、引用缺失拒绝、密钥拒绝、冲突不覆盖 |
| `internal/app` | 14（C01 新增 6） | 无工作区/未信任拒绝启动、绑定层生命周期、结构化错误、冲突差异、**诊断等于运行时参数** |
| `internal/config` | 9（新增 1） | 无 default 层时的权限基线、最严格者胜 |
| 前端 | 62 | Router 8、StateViews 6、Layout 6（含真实 Tab 序列）、WorkbenchView 9、SettingsView 7、runState 22、ResourcesView 2、ProfileEditor 2 |

## 5. 真机抓到的缺陷（都已修，且都有测试固定）

### 缺陷 1：首屏自相矛盾

左栏说"还没有工作区"，主区域同时说"工作区 workspace 尚未信任"。
根因：`CurrentWorkspace()` 会幂等登记当前工作区，`ListWorkspaces()` 与它并行时列表先返回。
修：`useBackend.reload` 先 `await currentWorkspace()`，再并行其余四项。

### 缺陷 2：点了信任，界面不更新

后端已落盘（`trusted:true, revision 2`），按钮 `aria-label` 已变"取消信任"，但主区域黄色提示还在。
根因：`refreshWorkspaces` 只刷 `workspaces` 数组，没刷 `currentWorkspace`。
修：两份一起刷。

### 缺陷 3：诊断信息在说谎（最严重）

设置页显示 `... --no-mcp`，而实际运行确实启用了 harness MCP（审批与自有 MCP 都在工作）。
根因：`Status()` 和 `ensureRuntime()` 各自构造一份 `IsolationSpec`，一处开了 `HarnessMCP`，另一处没开。
修：`isolationSpec()` 作为唯一构造点；`Status()` 在已启动时**直接读运行时那份 spec**
（`run.Service.IsolationArgs()`），未启动时才按同一构造点推算并先解析策略扩展路径。

测试 `TestStatusIsolationIsBoundToRuntimeSpec` 故意让运行时 spec 与 `isolationSpec(ext)` 不同，
断言展示值必须等于运行时值。**对抗性验证**：把 `Status()` 改回自拼参数后该测试立即转红，
输出正是 `展示 "...-e builtin:mcp..." / 实际 "...--no-mcp..."` 这一矛盾。

### 缺陷 4：编辑配置档读的是列表快照

列表里的配置档是加载时的对象。磁盘被外部改过之后点"编辑"，编辑器里是旧名字、旧策略、旧 revision。
保存时 CAS 会拦住覆盖（没有丢数据），但用户看到的值不是真的。
修：`openExisting()` 先 `api.profile(id)` 重读磁盘再进编辑器，读取失败显示错误码而不是打开空表单。

测试 `ResourcesView.test.tsx` 两条。**对抗性验证**：把 `openExisting` 换回 `setEditing(p)` 后两条同时转红。

### 缺陷 5：验证工具自身的两个坑

- `verify --suite=unit` 在前端测试**通过时没有任何输出**，
  日志里"前端测试跑了并通过"与"前端测试压根没被调度"长得一模一样。
  修：补一行 `[PASS] 前端测试`。
- 前端测试并发执行时多个 worker 同时写 vitest 自己的临时模块缓存
  （`os.tmpdir()/<id>/web/<hash>`，不可配置），本机随机撞上 EPERM：
  报的是"未处理错误"，更糟的是**那一轮里部分测试文件根本没执行**——
  同一份代码三次跑出 38 / 54 / 60 条。修：`fileParallelism: false`（全部跑完约 10 秒）。
  连续三次 `verify --suite=unit` 退出码均为 0，稳定 62 条。

## 6. 顺带修掉的真实缺陷（非 UI）

`TestPermissionsBaselineWithoutDefaultLayer` 是这次新写的对抗性用例，它一写就红了：
`default` 层不写 permissions 时，旧实现的 `false && true` 会永久锁死 network/shell，
filesystem 也无法从 `none` 升级。修：权限合并改为"最严格者胜"，
用哨兵（`fsRank=-1`、`netSet/shellSet`）表达"还没有任何层说过话"，取代对 default 层的特判。

同一类问题还有一例：`internal/profile` 曾经用 `filesystem: "write"` 这个**在 Merge 里不存在的枚举**，
被静默降级成 `none`——UI 接受 `write`、实际按"不访问文件"执行。
修：抽出 `config.NormalizeFilesystem` 作为唯一真相源，非法值直接拒绝保存。

## 7. 限制与未覆盖（不要据此推断已完成）

1. **模型/技能/MCP 未实现**（C02/C03/C04）：资源库里这三块只显示"未实现"说明，
   配置档的模型/技能/MCP 需要手填 ID，且必须在配置层里已存在。
2. **添加工作区要手输路径**：没有目录选择器，侧栏已写明这一点。
3. **记忆（C05）与历史运行（C06）未实现**：记忆是空态说明；运行记录里"历史"明确标未实现，
   事件只在内存里保留（最多 20000 条），重启后不可查。
4. **键盘可达性只做到 DOM 级验证**：本机 `agent-desktop press` 送物理按键被拒
   （`ACTION_FAILED: Application has no verified keyboard focus for key delivery`），
   所以"Tab 可达主要操作"由 `Layout.test.tsx` 用 `userEvent.tab()` 断言的**真实 Tab 序列**覆盖
   （详情开关 → 当前 tab → 添加工作区 → 发送），而不是真机按键。
   同一原因，方向键在 tablist 内移动焦点的验证也在单测里。
5. **只验证了 Windows/amd64**；mac/linux/arm 统一后处理。
6. **本机是 Windows 10 19045**，低于基线要求的验收环境，不能替代目标机验收。
7. **审批时序观察（未修，不属 C01 范围）**：一轮里模型同时发出两个工具调用时，
   第一个已批准的工具要等第二个也被决策后才执行（Pi 侧批量调度行为）。
   界面在此期间把已批准的那张卡显示为"执行中"，严格说不准确。已记录，留给 C06 一并处理。
8. **F06（Pi 安装器）按 ADR 0002 推迟**到全部功能完成后再做。

## 8. 复现命令

```bash
# 1) 构建并启动（真实模型配置已在 .tmp/e2e/home-c01/settings.json）
"D:/Program Files/nodejs/npm.cmd" --prefix frontend run build
go build -o build/bin/MyAgent.exe ./cmd/myagent
MYAGENT_HOME="D:/project/my-agent-go/.tmp/e2e/home-c01" MYAGENT_UI_A11Y=1 MYAGENT_SHELL=1 \
  ./build/bin/MyAgent.exe

# 2) 驱动真实桌面
export AGENT_DESKTOP_HOME="C:\Users\<user>\AppData\Local\adhome-myagent"
agent-desktop list-windows --app MyAgent.exe
agent-desktop find --window-id <id> --role textfield          # snapshot 会裁剪深层节点，用 find 定位
agent-desktop set-value <ref> "创建 real.txt，内容写 real-model-ok"
agent-desktop click <发送按钮 ref>
agent-desktop find --window-id <id> --role button --name 允许一次
agent-desktop screenshot --window-id <id> out.png

# 3) 校验
./scripts/devtools.sh verify --suite=static
./scripts/devtools.sh verify --suite=unit
./scripts/devtools.sh verify --suite=integration --target=win-x64
./scripts/devtools.sh gate --id=G0 --evidence=docs/evidence/runs/<rev>/win-x64
```

`AGENT_DESKTOP_HOME` 必须指到仅当前用户可访问的目录（如 `%LOCALAPPDATA%`）。
指向 `D:\project\...` 会报 `PERM_DENIED: private file DACL grants an untrusted principal`。

§3.5 用到的 `seed` 工具是一次性的（`go run ./.tmp/seed/main.go <configRoot> <key> <bodyJSON>`，
不入库）：它走 `config.Store.CompareAndSwap` 而不是手写 JSON，
因为 envelope 带 revision + checksum，手写必然触发"被外部编辑"从而测不到想测的东西。

## 9. 截图索引

| 文件 | 内容 |
|---|---|
| `01-first-launch.png` / `02-first-launch-fixed.png` | 首屏（修复前后） |
| `03-trusted.png` / `06-workbench-trusted.png` / `07-trusted-ok.png` | 信任后 |
| `05-resources-tab.png` | 资源库 |
| `08-approval.png` / `11-approval-pending.png` / `12-allowed-completed.png` | 审批卡与允许后 |
| `10-restart-persisted-trust.png` | 重启后信任仍在 |
| `13-tab-*.png` | 五入口逐一 |
| `20-restarted.png` / `21-settings-diag.png` | 重启后首屏、诊断区 |
| `22-real-run.png` / `23-after-allow.png` / `24-real-completed.png` | 真实模型一轮（审批 → 允许 → 完成） |
| `25-runs-real.png` | 运行记录里的真实统计 |
| `30-1024-workbench.png`、`31-1024-*.png` | 1024×720 |
| `32-profile-editor.png` / `33-profile-ref-error.png` / `34-profile-saved.png` | 配置档新建、引用校验、保存 |
| `36-profile-edit-open.png` / `37-conflict-panel.png` / `38-reloaded.png` | 编辑、冲突差异、重载 |
| `40-reloaded-app.png` / `41-edit-fresh.png` | 重启后列表、编辑前重读磁盘 |
