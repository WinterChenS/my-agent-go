# F05 桌面端 Windows 首次端到端（真实点击）

日期：2026-11-03
宿主：Windows 10 Pro 22H2（19045）/ amd64 / Intel N100
裁决：**PASS（范围见 §6 限制；不等价于 G0 通过）**

本文件记录的是**在真实桌面上跑起来并用真实鼠标操作**的证据，不是单测结论。
所有截图来自 `agent-desktop` 对 `MyAgent.exe` 窗口的截图；所有交互都是
`agent-desktop click / set-value` 对可访问性元素发出的真实操作。

## 1. 被测物

| 项 | 值 |
|---|---|
| 可执行 | `build/bin/MyAgent.exe`（Wails v3.0.0 + 内嵌前端） |
| 前端 | Vite 构建产物，深色会话界面（消息流 / 工具卡 / 审批条 / 设置抽屉） |
| 运行时 | `internal/run.RunService`（状态机 + 事件 seq + 审批桥） |
| 模型 | **受控 fixture provider**（`tests/fixtures/provider`，脚本化回放，不是真实模型） |
| Pi | `.tools/pi/v1.5.0`（npm 形式，Node 24.16.0 启动） |
| 隔离 | `-na -nc -ne -ns -np --no-themes --no-mcp -e <abs>/pi-extensions/harness-policy.ts` |

启动方式（未打包，开发态直接运行）：

```powershell
$env:MYAGENT_HOME       = "D:\project\my-agent-go\.tmp\app-home"
$env:MYAGENT_PI_PATH    = "D:\project\my-agent-go\.tools\pi\v1.5.0\node_modules\@earendil-works\pi-coding-agent\dist\bundle\cli.js"
$env:MYAGENT_NODE_PATH  = "D:\Program Files\nodejs\node.exe"
$env:MYAGENT_PROVIDER   = "fixture"
$env:MYAGENT_BASE_URL   = "http://127.0.0.1:<fixture 端口>/v1"
$env:MYAGENT_MODEL      = "fixture-model"
$env:MYAGENT_WORKSPACE  = "D:\project\my-agent-go\.tmp\workspace"
$env:MYAGENT_SHELL      = "1"
$env:MYAGENT_UI_A11Y    = "1"   # 给 WebView2 打开渲染器可访问性，UI 自动化才能看到 DOM
.\build\bin\MyAgent.exe
```

## 2. 为什么需要 `MYAGENT_UI_A11Y`

不开这个开关时，WebView2 在可访问性树里只暴露一个空的

```
group | Chrome Legacy Window
```

即页面内容对 UIA 不可见，任何"点按钮"都只能靠坐标猜。打开
`--force-renderer-accessibility` 后，同一窗口出现：

```
document | MyAgent | http://wails.localhost/
  group
    statictext | MyAgent
    statictext | windows/amd64
    statictext | 空闲
    button     | 设置
  group
    statictext | 说点什么，让 agent 在工作目录里干活。
  group
    textfield  | 输入指令，Enter 发送，Shift+Enter 换行
    button     | 发送
```

原始树见 `docs/evidence/raw/f05-desktop/ui-tree.json`。

这不是为测试临时改的行为：读屏软件同样依赖它。默认关闭只是因为在普通使用下
会增加渲染进程常驻开销。

## 3. 操作序列（全部为真实点击/输入）

| # | 动作 | agent-desktop 命令 | 结果 |
|---|---|---|---|
| 1 | 输入指令 | `set-value @…:e15 "请回答：[[scenario:tool-file]]"` | `delivered_verified`，值回到 `post_state.value` |
| 2 | 点发送 | `click @…:e20` | `InvokePattern.Invoke` → 触发真实 run |
| 3 | 等审批出现 | `snapshot` 轮询 | 第 1 次轮询即找到 `允许一次` |
| 4 | 点拒绝 | `click @…:e26` | 审批卡消失，run 继续到 completed |
| 5 | 第二轮输入 | `set-value @…:e32 "[[scenario:tool-file]] 再试一次"` | 成功 |
| 6 | 点发送 | `click @…:e38` | 成功 |
| 7 | 点允许一次 | `click @…:e43` | 成功 |
| 8 | 展开工具卡 | `click @…:e51` | 折叠区展开，显示参数与结果 |

## 4. 结果判定（落在文件系统上，不靠界面文案）

判定文件是 `<workspace>/tool-ran.txt`，由 `bash` 执行
`echo ran > tool-ran.txt` 产生。用文件存在与否区分"跑了"和"被拦了"，
比读工具参数字符串可靠——参数里本来就含有同一个文件名。

| 场景 | 界面终态 | `tool-ran.txt` | 结论 |
|---|---|---|---|
| 拒绝授权 | 已完成，工具卡标 `失败` | **不存在** | 策略拒绝真的挡住了命令 |
| 允许一次 | 已完成，工具卡标 `完成` | **存在**，内容 `ran` | 授权后命令真的执行了 |

```text
--- 拒绝后 ---
$ ls .tmp/workspace/
（空）

--- 允许后 ---
$ ls -la .tmp/workspace/
-rw-r--r-- 1 Administrator 197121 4 tool-ran.txt
$ cat .tmp/workspace/tool-ran.txt
ran
```

正向对照不可省：没有"允许后确实执行"，"拒绝后没执行"完全可能是模型压根没调用工具。

## 5. 截图索引

| 文件 | 内容 |
|---|---|
| `01-launch.png` | 冷启动：顶栏 `windows/amd64` / `空闲` / `fixture / fixture-model` |
| `02-input.png` | 输入后发送按钮由灰变亮（受控组件的 onChange 确实被触发） |
| `03-approval-pending.png` | 等待授权：bash 卡片 `执行中` + 审批卡显示 `{"command":"echo ran > tool-ran.txt"}` |
| `04-denied-completed.png` | 拒绝后：顶栏 `已完成`，工具卡 `失败` |
| `05-allowed-completed.png` | 允许后：工具卡 `完成`，run 变为 `run-…-2` |
| `06-tool-expanded.png` | 工具卡展开：参数与结果 `(no output)` |

## 6. 限制（不得当作已通过的范围）

1. **模型是 fixture**，不是真实云端模型。它验证的是"链路通"，不是"模型质量"或
   PI03 的真实压缩。真实模型需要用户自备端点与 key。
2. **上下文不跨 run**：当前每个 run 起一个 worker，两轮对话之间没有 Pi 会话延续。
   RUN07（重启可查 transcript / 单会话无并发 writer）尚未实现。
3. **事件未持久化**：`Replay` 只在内存里服务同一次进程生命周期；重启后历史事件丢失。
   DB 索引与 events 表属 G2 范围。
4. **无安装包**：本机没有 NSIS / MakeAppx / SignTool，`package` 仍为未实现（G4）。
5. **本机是 Windows 10 19045**，低于基线要求的验收环境 Windows 11 24H2。
   因此这条证据只能证明"在 Win10 上能跑通"，不能替代 ENV02 的目标机验收。
6. **UI 自动化依赖 `MYAGENT_UI_A11Y=1`**：默认关闭时 DOM 不进入可访问性树，
   agent-desktop 只能看到空 pane。
7. 状态文案里 fixture 回放的第二段话（"命令已执行完毕，本轮结束。"）是 fixture
   脚本的固定文本，不是模型判断；不要据此推断产品会这样措辞。

## 7. 复现命令

```bash
# 1) 起受控模型端点
go run ./tests/fixtures/provider -addr 127.0.0.1:0 -addr-out .tmp/addr.txt -record-dir .tmp/rec

# 2) 构建并启动
D:/Program\ Files/nodejs/npm.cmd --prefix frontend run build
go build -o build/bin/MyAgent.exe ./cmd/myagent
# 按 §1 设置环境变量后运行 build/bin/MyAgent.exe

# 3) 驱动
export AGENT_DESKTOP_HOME="C:\Users\<user>\AppData\Local\adhome-myagent"
agent-desktop list-windows --app MyAgent.exe
agent-desktop snapshot --window-id <id> -i
agent-desktop set-value <textfield-ref> "[[scenario:tool-file]]"
agent-desktop click <send-button-ref>
agent-desktop screenshot --window-id <id> out.png
```

`AGENT_DESKTOP_HOME` 必须指到用户目录（如 `%LOCALAPPDATA%`）。指向 `D:\project\...`
会报 `PERM_DENIED: private file DACL grants an untrusted principal`——因为 D 盘目录
继承了 `BUILTIN\Users` / `Authenticated Users` 的访问权，而 agent-desktop 只接受
仅当前用户可访问的状态根。
