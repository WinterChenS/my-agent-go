# Anvil V1 验收矩阵与门禁

日期：2026-11-05。当前状态：增量矩阵；各 case 状态以绑定当前 revision 的原始证据为准。新增 F08/F09/C08/C09/U00–U07 case 初始均为 NOT_RUN，历史任务证据不得自动覆盖。

## 1. 判定规则

状态仅 PASS/FAIL/BLOCKED/NOT_RUN；用例可额外标 UNSUPPORTED_EXPECTED，但必须是本文件明确规定的负例，且断言“无副作用+明确错误”通过后才能归为PASS。required用例不得SKIP。缺设备、网络、key、签名或依赖=BLOCKED；实现错误/断言不符=FAIL；二者都不能通过所属gate。

执行顺序G0→G1→G2→G3→G4→G5，但独立任务可先完成。前置失败不应浪费时间运行必然失败的依赖用例，也不能标通过。gate针对同一产品revision、版本锁hash、fixture版本和平台矩阵；代码变更后按影响映射重跑相关suite，重编后的最终包必须重新跑package suite。保留未改模块报告时，需说明对应源码tree hash不变，不能简单把旧revision字符串改成当前值。

### 1.1 门禁判定的证据维度（2026-11-04 修正）

gate 按 **case × target × display** 三个维度判定，不按 case 汇总。一个 case 在某个目标上的 PASS 不能代表其它目标；同一 case 两个目标一成一败时，gate 判死。

`scope` 列翻译成必需证据位（`tests/acceptance/cases.json`）：

| scope | 必需 target × display |
|---|---|
| ALL | 六个目标；display 不区分（非 Linux 归一化为 desktop） |
| NATIVE | 六个目标；Linux 的 x11 与 wayland 各一份，其余 desktop |
| HOST | 至少一个已知目标；若出现多份，每一份都必须通过（不能挑绿的） |
| HOST+ALL路径集成 | 六个目标；display 不区分 |
| LNX / Linux两架构 | linux-x64、linux-arm64，各 x11 + wayland |
| Windows两架构 | win-x64、win-arm64；display 不区分 |
| macOS两架构 | mac-x64、mac-arm64；display 不区分 |

未知 scope、原子 case 缺 scope 一律返回 2，不静默变成「不需要证据」。另四条判死规则：

1. 同一必需位出现两份证据 → 判死（结论不确定，不挑一份绿的）。
2. 证据 `gateId` 与请求的 gate 不一致 → 判死（不能把 G1 的报告塞进 G0 证据树）。
3. 证据 `worktreeDirty=true` → 判死。dirty 工作区的代码不是 `sourceRevision` 那一份，贴上去就是假归属；正式证据必须在干净工作区生成。
4. 必需 case 的**其它**证据（不在必需集合里的 target）也必须有效：win-arm64 失败不会因为没有排在必需集合里而被忽略。

gate 报告末尾的目标汇总是诊断视图，不是另一个通过口径：某目标 18/18 只说明它本身齐了，全矩阵才是结论。本机只有单一目标时，缺失目标记 BLOCKED，不能当通过。


平台范围缩写：ALL=六目标；LNX=两个Linux目标各X11/Wayland；HOST=当前测试宿主的纯逻辑测试，仍由六目标完整构建检查补充；NATIVE=ALL且LNX双会话。公开版本需G5，内部版本至少G4。

## 2. 用例与断言

### 环境和门禁自身

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| ENV01 | F00 | ALL | Go/Node/npm/Wails及nativeSDK来源、精确版本、命令路径入锁文件，支持该目标构建；旧Node路径检测报错 |
| ENV02 | F00 | ALL | 矩阵中每目标有可访问的真实运行环境和架构证据；Linux两个display有检查资源；资源缺失保持BLOCKED |
| ENV03 | F00 | HOST | frontend lock/go.sum/versions.lock一致，依赖来源可追溯；重复安装构建不漂移版本 |
| GOV01 | F01 | HOST | 必需case缺失/空suite/unknown target返回2，不能PASS |
| GOV02 | F01 | HOST | 旧版本锁/错误revision/报告或 artifact hash 篡改被拒；PASS 缺目录内 oracle、oracle 越界或 symlink、副作用 case 用 test-process 自证、external oracle 缺 artifact/非法 source 均被拒；人工证据缺真实机器/review字段被拒 |
| GOV03 | F01 | HOST | 命令断言失败返回1、配置缺失返回2、成功0；子进程失败不会被日志成功覆盖 |
| GOV04 | F01 | HOST | 所有本矩阵case都在机器索引且有owner/已注册runner；重复 case ID 被拒；新 case 未声明 external oracle 或已声明仍留在 allowlist 被拒；未实现 case runner 使 suite 非零；仅SKIP的native报告不能通过 |
| GOV05 | F01 | HOST | 证据记录实际 OS、版本、build、架构与 display；缺失、与 target 不符或低于 `build/platforms.json` 最低系统的证据被拒，符合时通过 |

### Pi、RPC、权限

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| PI01 | F02 | ALL | 精确路径优先、中文/空格/npm shim正确、多Pi不静默切换、失效路径报NotFound |
| PI02 | F02 | ALL | probe用隔离agentDir，不访问真实provider、不启动用户MCP，用户配置marker和hash不变 |
| PI03 | F02 | ALL | 真实Pi认证版本完成RPC/MCP/extension探测，行为与release契约一致；原始流可追溯，mock不得替代 |
| PI04 | F02 | ALL | 无Node环境的官方独立Pi包通过最低版本基线探测；其它候选在 F08 兼容协商前不得自动运行；npm安装形式单独记录 |
| PI_DISC01 | F08 | ALL | 用户指定、保存路径、托管目录、PATH、常见目录的候选全部列出实际路径/version/origin/hash，确定优先级且不静默切换 |
| PI_DISC02 | F08 | ALL | 错误 PATH、微信 Node/Pi shim、失效保存路径和架构错误候选不被选中；外部文件与 PATH 不被修改 |
| PI_COMP01 | F08 | ALL | 最低 1.5.0 与认证 fixture/hash 一致，完整 RPC/MCP/extension/停止/清理探测后为 ready_verified |
| PI_COMP02 | F08 | ALL | 范围内新补丁版本只有在隔离契约探测全部通过后为 ready_compatible，版本号本身不能放行 |
| PI_COMP03 | F08 | ALL | 范围内但 settled/MCP/hook/abort 任一不兼容时为 incompatible，真实 run 不启动且修复信息准确 |
| PI_COMP04 | F08 | ALL | 低于 1.5.0 显示 upgrade_available；2.x 和未知主版本在无新清单前保持 incompatible |
| RPC01 | F03 | HOST | LF随机分块/CRLF/U+2028/UTF8/128KiB记录完整，16MiB允许、超1字节失败，不截断JSON |
| RPC02 | F03 | HOST | 并发乱序response按id匹配，重复/未知id不误归属，取消Call能结束 |
| RPC03 | F03 | ALL | stderr持续洪泛不堵stdout，长输出/写背压不死锁，日志缓冲不无限增长 |
| RPC04 | F03 | HOST | malformed/未知事件有明确诊断不崩溃，未知最终完成语义不报completed |
| RPC05 | F03 | ALL | 退出10秒内owned子孙均清理，无关Pi/Node进程仍在；CleanupFailed不会被显示成清理成功（真实模型下的复核归 E2E05） |
| SEC01 | F04 | HOST | 许可绑定参数/范围/session/run/有效期，改变参数或重复消费拒绝；60秒到期有效 |
| SEC02 | F04 | ALL | builtin write/edit/bash逐一拒绝时文件/命令marker为0；允许一次只产生一次副作用 |
| SEC03 | F04/B05 | ALL | 自有和外部MCP、扩展、嵌套tool路径均执行前授权；未覆盖通道禁用且实际不能调用 |
| SEC04 | F04/B05 | ALL | 跨session/错误令牌/Origin请求/过期或撤销授权到不了handler，fixture counter为0 |
| SEC05 | F04 | HOST+ALL路径集成 | symlink/junction/UNC/规范化路径越界被拒，许可只针对实际资源范围；不宣称shell OS隔离 |
| SEC06 | F04 | ALL | UI断开、审批超时、policy服务失联均不执行；已排队许可撤销后再次校验 |
| SEC07 | F07/C02/C07 | ALL | OS库真实读写成功；库失效不落明文；配置/日志/诊断包对sentinel secret命中0 |
| SEC08 | F04 | ALL | 不信任项目的.pi/.agents/祖先扩展、MCP、!command不执行marker；无法阻止时启动被拒 |

### 配置、存储与运行

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| CFG01 | F07/C01 | HOST | 五层合并/数组按ID/显式禁用正确且可查询来源，项目不能扩大权限或换凭证 |
| CFG02 | F07/C01 | HOST | 同revision两个writer只有一个成功，外部编辑产生ConfigConflict，失败不覆盖源文件 |
| CFG03 | F04/C03 | ALL | 运行快照真实限制资源加载，项目自动发现不能绕过禁用/信任，旧run与新run revision可见 |
| CFG04 | F07 | HOST | 原子写入中断后仍可恢复有效配置，独立agentDir不改原Pi目录 |
| DB01 | F07 | HOST | 初建/迁移重跑/升级schema正确，SQLite事务失败无半迁移 |
| DB02 | F07 | ALL | 锁定驱动真实支持FTS5 trigram；数据库锁/权限错误可诊断，不偷偷换内存DB |
| DB03 | F07 | HOST | JSON/DB/Pi transcript权威职责不冲突，重建索引不改原配置/会话内容 |
| RUN01 | F05/C06 | ALL | accepted不completed，compaction/retry不误终止，最终idle才completed，错误/取消区分 |
| RUN02 | F05 | HOST | 关键事件seq递增；重连快照+游标无丢失/重复，50ms文本合并不丢工具结果 |
| RUN03 | F05 | HOST | 仅2个活跃worker，第三个排队；排队取消不启动，slot释放后按FIFO启动 |
| RUN04 | F05/C06 | ALL | stop≤2秒可见；5秒后升级终止、10秒后明确清理结果；取消后不发新写请求 |
| RUN05 | F05 | ALL | 工具已写后崩溃，恢复计数仍1；未知副作用结果不自动重放（真实模型下的复核归 E2E05） |
| RUN06 | F05/C07 | HOST | 第101个工具调用被拒、30分钟后不发下一请求；usage未知显示unavailable，不显示伪0 |
| RUN07 | F05 | ALL | 重启可查Pi transcript和产品索引，单会话无并发writer，不常驻历史worker |

### 安装与资源

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| INS01 | F06 | ALL | 无Pi界面可一键下载已认证OS/arch包，hash与manifest一致，probe后Ready |
| INS02 | F06 | HOST | hash错误/不可信重定向/错架构不激活；active旧版hash不变 |
| INS03 | F06 | HOST | ../、绝对路径、symlink/hardlink越界包拒绝；根目录外marker不存在 |
| INS04 | F06 | ALL | 下载取消、断网、空间/权限不足、解压失败可重试，active不变，无半成品被发现Ready |
| INS05 | F06 | ALL | 激活中断可恢复，保留1个旧版可回滚；活跃worker继续使用原路径 |
| INS06 | F06 | ALL | 外部Pi不可被客户端卸载/覆盖；默认不改系统PATH、不要求系统目录写权限 |
| INS07 | F06 | ALL | 无Node真实独立包可用，基础包未藏Node；如果不成立整项FAIL/BLOCKED而非换口径 |
| PI_INS01 | F06 | ALL | 受信清单、HTTPS allowlist、重定向、size/hash/signature、归档边界和目标架构全部验证后才进入 staging probe |
| PI_INS02 | F06 | ALL | 缺失 Pi 一键安装到 Anvil 托管目录并通过 F08 探测；不需要管理员、不改 PATH、不写外部 Pi |
| PI_INS03 | F06 | ALL | 取消、断网、校验/解压/probe 失败时 active 与旧版本 hash 不变，半成品不被发现为可用 |
| PI_UPG01 | F06 | ALL | 空闲时升级原子切换 active，活跃 worker 继续使用原可执行路径，新 run 才使用新版本 |
| PI_UPG02 | F06 | ALL | 候选 probe 或切换失败自动回滚；保留一个已验证旧版并可由 UI 执行回滚 |
| PI_UPG03 | F06 | ALL | 外部 Pi 从不覆盖/卸载；升级默认创建托管版本，回滚不改变外部安装 hash |
| PI_PKG01 | F06/X03 | ALL | Anvil 基础包内容扫描不存在 Pi/Node/npm/模型/独立 Chromium，Pi 只从独立受信包安装 |
| PI_PKG02 | F06/X03 | ALL | 六目标无 Pi/Node 的干净机器从安装后的 Anvil 完成一键安装、probe、重启后复用 |
| MOD01 | C02 | HOST | model schema、ID、API、context/output校验准确，秘密只有credentialRef |
| MOD02 | C02 | ALL | 临时Pi连接测试实际调用fixture provider；401/429/超时/有效响应分类准确 |
| MOD03 | C02 | ALL | 旧Pi auth无读取/修改，客户端key显式输入OS库；未知模型能力标unknown |
| MOD04 | C02 | ALL | model切换在新run或明确支持的安全点生效，记录真实provider/model及配置revision |
| RES01 | C03 | HOST | 本地skill导入校验、编辑、删除、启停真实落盘；不改源目录、不执行导入脚本 |
| RES02 | C03 | ALL | 纯托管skill禁用后新run不加载；同名自动发现副本按2026-11-04用户指令同步（禁用不保证消失、启用也不保证覆盖）；运行记录不得把托管清单冒充实际加载清单；旧run保持快照（ADR 0003） |
| RES03 | C03 | HOST | skill依赖缺失显示原因；恶意路径/损坏frontmatter被拒；不自动安装外部工具 |
| RES04 | C03/C04 | ALL | 系统policy扩展不可关闭；工具启停影响实际执行，不只是菜单可见性 |
| RES05 | U04 | ALL | Skill 链接导入只解析清单不执行脚本，非 HTTPS/跨域重定向/超大/归档越界被拒且托管目录不变；隔离试运行用临时 agentDir 与只读工具，用户 agentDir hash 不变 |
| MCP01 | C04 | ALL | stdio/HTTP两类经真实Pi发现与调用fixture工具，各一个成功 |
| MCP02 | C04 | ALL | 断开/超时/鉴权失败/重连状态准确；未授权副作用计数0 |
| MCP03 | C04 | ALL | server工具列表变化与暴露策略生效；禁用后不可调用，旧run变化策略明确 |
| MCP04 | C04 | HOST | command/args分开、秘密env/header引用、拒绝SSE或无效配置；保存不执行未知脚本 |
| MCP05 | C04 | ALL | 只由Pi管理外部MCP连接，退出清理其owned进程；OAuth未支持时不显示connected |
| CONN01 | C08 | ALL | 六类 connector 各有真实 authenticated health 与最小读取动作；缺 OAuth/测试账户为 BLOCKED，不显示 connected |
| CONN02 | C08 | ALL | 六类写入/外发动作各自经过统一 policy；拒绝/超时前 fixture 或专用账户副作用计数为0，允许一次为1 |
| CONN03 | C08 | ALL | connector 配置、日志、导出只含 credentialRef，OS库失效时不落明文 sentinel |
| CONN04 | C08 | ALL | action catalog 的风险、资源范围、capability、schema、幂等性准确，driver 不能自行降级风险 |
| CONN05 | C08 | ALL | 禁用/撤权后 app-owned MCP 工具从新 run 消失且实际不能调用，旧 run 策略明确 |
| CONN06 | C08 | ALL | 发送后断线/超时返回 OutcomeUnknown；非幂等动作不自动重放，幂等键可从外部 oracle 核对 |
| CONN07 | C08/U01 | NATIVE | Connectors 页面列出真实定义、权限、健康、错误和修复动作；刷新/重启后与 API/凭证库一致 |
| CONN08 | C08 | ALL | 邮箱附件与各平台允许资源范围不可越权；伪造 session/workspace/origin 到不了 adapter handler |
| SCH01 | C09 | HOST | cron 与 IANA 时区校验正确；DST 春跳/秋回、系统时钟前后跳不漏跑或重复同一 occurrence |
| SCH02 | C09 | HOST | 定义 CAS、暂停/恢复/删除/立即运行和重启恢复持久化；唯一 occurrence 键防重复 |
| SCH03 | C09 | ALL | 触发只创建普通 RunService session/run，遵守全局并发2和 FIFO，不存在第二套 Pi runtime |
| SCH04 | C09 | ALL | 同任务禁止重入；漏跑策略只补规定次数；连续两次失败自动暂停并记录原因 |
| SCH05 | C09 | ALL | 无人值守写入/外发仍 waiting_approval；拒绝/过期后副作用为0，未知结果不重放 |
| SCH06 | C09/U01 | NATIVE | Schedules 页面真实显示 next/last/run/session/status；编辑与暂停经 API/DB/重启复核 |
| SCH07 | C09 | ALL | 输出目标、workspace/Profile/权限引用越界被拒；删除任务不删除已产生的 run、会话或产物 |

### 记忆、界面和产物

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| MEM01 | C05 | HOST | 中文trigram检索命中预置约定，少于3字符返回QueryTooShort |
| MEM02 | C05 | HOST | workspaceA/B隔离、user可共享；candidate/deleted/expired不被注入 |
| MEM03 | C05 | ALL | 显式保存/候选确认真实落库；默认无后台抽取模型调用 |
| MEM04 | C05 | HOST | ≤5条且≤6000 UTF8 bytes，裁剪不损UTF8，来源/session可追踪 |
| MEM05 | C05 | ALL | 编辑/删除/关闭作用于下一run，导出可读；不宣称删除已发模型历史 |
| UI01 | C01 | NATIVE | 五入口真实绑定Go，空/错/加载状态、1024×720、键盘导航可用，缺Pi仍可打开设置 |
| UI02 | C06 | NATIVE | streaming/工具/审批/停止/历史与真实后端一致；重放无重复，Markdown不执行注入HTML |
| UI03 | B07 | NATIVE | 右侧URL/tabId与agent工具相同；用户修改网页后agent能观察到同一修改 |
| UI04 | B07 | NATIVE | dialog/命令面板/审批层不被native页遮挡；切换面板不销毁tab，紧急停止始终可达 |
| UI05 | U01 | NATIVE | Anvil 壳层九个生产路由、历史、搜索和工作台均读真实 Go API；无原型固定数字或假 connected |
| UI06 | U02 | NATIVE | Chat 的消息/工具/审批/停止/恢复、附件和右栏四分区由后端事件推进，点击不直接标成功；内容澄清与工具审批分离，回答问题不授予任何权限 |
| UI07 | U03 | NATIVE | 单 Agent/Profile 的模型、prompt、思考级别、Skills、工具、权限和记忆真实保存，且 system prompt 与思考级别实际进入 Pi 请求（fixture provider 记录）；无子Agent/多运行时入口 |
| UI08 | U04 | NATIVE | Skills 创建/导入/编辑/启停/依赖/隔离试运行及 MCP stdio/HTTP 状态与真实 API/runtime 一致 |
| UI09 | U05 | NATIVE | 资源库从文件/SQLite 核对 path/hash/size/source，记忆独立分区且删除只影响后续注入 |
| UI10 | U06 | NATIVE | 设置显示全部 Pi 候选、安装升级回滚和应用更新真实状态；无 Codex/Claude 伪运行时选项 |
| UI11 | U06 | NATIVE | 观测显示真实 usage/工具/审批/失败/run 链；缺 usage/价格为 unavailable，不显示伪0 |
| UI12 | U07 | NATIVE | 880×600 至 1920×1080、Windows 100/150/200%、Retina、Linux X11/Wayland 无关键操作遮挡 |
| UI13 | U07 | NATIVE | 键盘、焦点、accessible name、风险提示和 WCAG AA 对比度通过；built Wails 真机证据绑定 API/DB oracle |
| SES01 | C06 | HOST | 会话目录改名/置顶/归档/删除走 CAS，并发写只有一个成功；分页稳定，重启后一致；删除不删产物；普通会话在 scratch 工作区且无用户目录访问 |
| SES02 | C06 | ALL | 附件由 Go 解析真实路径；工作区外、symlink/junction 逃逸和 policy 拒绝的项逐项返回原因，run 上的引用不可变 |
| SES03 | C06 | ALL | HTML/Markdown/JSON 导出与 Pi transcript 一致；HTML 转义且不含脚本；导出中 sentinel 秘密命中 0 |
| SRCH01 | U01 | HOST | 全局搜索索引覆盖会话、Agent/Profile、Skill、产物，重启可重建；少于 3 字符返回 QueryTooShort，未建索引返回 unavailable；已删除和其它工作区的结果不出现 |
| I18N01 | U00 | HOST | zh-CN/en-US key 集合完全一致，缺 key、废弃 key和生产组件硬编码产品文案使静态检查失败 |
| I18N02 | U00/U07 | NATIVE | 切换语言即时更新静态/动态客户端文案并持久化；用户/模型/工具原文不被翻译 |
| I18N03 | U00/U07 | NATIVE | 错误 code+参数、日期/时间/数字/文件大小/费用按 locale 正确格式化，不匹配 Go 中文文本 |
| I18N04 | U07 | NATIVE | 九页中英文在规定窗口/缩放下无截断、重叠或不可达操作；每页有两语言截图与交互证据 |
| THEME01 | U00 | HOST | 生产组件只用语义 token；直接主题色、缺状态 token 或 React 渲染后才应用主题使静态检查失败 |
| THEME02 | U00/U07 | NATIVE | Light/Dark/跟随系统选择正确且持久化，无启动闪烁；跟随系统不是第三套视觉主题 |
| THEME03 | U07 | NATIVE | 两主题 default/hover/focus/active/disabled/loading/empty/error/success/审批风险均满足对比度和可见性 |
| THEME04 | U00/B07 | NATIVE | 切换主题不重建 browser tab、不丢会话/表单/下载、不改变人工接管或运行状态；远端网页不注入主题 |
| THEME05 | U07 | NATIVE | 九页在构建后 Wails 中提供两主题截图与外部状态 oracle；浏览器页面截图不能替代 native 证据 |
| BRAND01 | U00 | HOST | `ANVIL_*` 优先于 `MYAGENT_*`；只设旧变量时生效并记录弃用警告与配置来源；两者冲突时取 `ANVIL_*` |
| BRAND02 | U00 | ALL | 首次启动从旧 MyAgent 目录迁移非敏感配置与数据库：staging → 校验 → 原子切换，原目录保留，重复启动幂等；注入失败时源目录不变且无半写入的新目录 |
| BRAND03 | U00 | ALL | 迁移后已有 credentialRef 仍可解析；秘密不被复制到新目录、配置或日志（sentinel 命中 0） |
| ART01 | C07 | HOST+ALL路径集成 | artifact只登记已授权路径，越界/丢失文件明确拒绝，无任意本机文件打开后门 |
| ART02 | C07 | ALL | 产物hash/大小/来源run可核对，重启后可查，文件变化显示变化而不是旧hash有效 |
| ART03 | C07 | ALL | 上下文来源/压缩/usage/费用呈现真实或明确unknown；诊断包可定位故障且脱敏 |

### 原生浏览器

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| BR00 | B00 | HOST | 两origin fixture独立，counter可核对，trusted-only负例确实拒非trusted输入 |
| BR01 | B01 | Windows两架构 | 同一窗口右侧真实WebView2、页面可手动操作、不是浮窗/iframe/画面流 |
| BR02 | B02 | macOS两架构 | 同一窗口右侧真实WKWebView，公开API，页面可手动操作 |
| BR03 | B03 | LNX | 同一窗口右侧真实WebKitGTK，GTK容器正确；X11和Wayland均有运行证据 |
| BR04 | B01/B02/B03 | NATIVE | 缩放100/150/200%或平台等价分辨率下，点击坐标误差≤2 CSS px；分栏20轮不漂移 |
| BR05 | B01/B02/B03 | NATIVE | 中文IME、focus、clipboard、dialog层级、最小化恢复可用；UI线程无死锁 |
| BR06 | B01/B02/B03 | NATIVE | 创建销毁50轮无崩溃，关闭后native tab/handle无遗留；callbacks不访问释放对象 |
| BR07 | B04 | NATIVE | 标准form按钮/input/select/textarea/checkbox可观察/填写/点击，server结果与预期一致 |
| BR08 | B04 | NATIVE | 同URL导航/SPA断开节点后旧ref拒绝且counter0，不能重新编号误点其他元素 |
| BR09 | B04 | NATIVE | 密码不泄漏、元素/文本/JSON限额受控，截图完整且≤4MiB并有正确scale |
| BR10 | B04 | NATIVE | cross-origin/closed-shadow未覆盖部分明确Unsupported；trusted-only的后置条件失败返回VerificationFailed/NeedsHuman，Delivered不能冒充Verified，无隐式绕过 |
| BR11 | B05 | NATIVE | 接管使queued动作失效；Human显示后无agent输入；恢复必须重新observe |
| BR12 | B05 | NATIVE | sessionB不能操作A的tab；4/session、12/total限额真实生效 |
| BR13 | B05 | NATIVE | browser工具经MCP/策略服务；交互许可拒绝/过期/改origin后counter0 |
| BR14 | B05 | NATIVE | 导航/关闭/超时/撤销后不新派发或重放写操作；已派发结果未知标OutcomeUnknown，无法确认静止保持Pausing/ControlUncertain；late callback不能触发后续写操作 |
| BR15 | B06 | NATIVE | workspace cookie隔离，重启同profile可保留，清除生效；不访问系统个人浏览器配置 |
| BR16 | B06 | NATIVE | window.open继承session和tab限额，不创建未受控窗口或跨session归属 |
| BR17 | B06 | NATIVE | 下载需目标确认、取消无最终文件、文件hash正确且有artifact；上传未支持不伪装完成 |

### 端到端、安装与发行

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| E2E01 | X01/C06 | NATIVE | 实际Wails+Pi+fixture模型完成文件旅程，授权与文件hash可验证，重启历史可查 |
| E2E02 | X01/C07 | NATIVE | 实际Wails+Pi调用fixture MCP，结果/产物可核查，禁用后counter不再增长 |
| E2E03 | X01/B07 | NATIVE | 右侧真实form由Pi操作、用户接管、恢复观察到人工修改，server提交次数准确 |
| E2E04 | X02 | NATIVE | 真实模型完成最小工具闭环；每目标有真实请求/工具烟测；无key不改为fixture PASS |
| E2E05 | X02 | NATIVE | worker/MCP/UI/provider/DB/凭证故障演练状态准确，无未知写重放，日志可定位；在真实模型下复核 RPC05 的进程树清理和 RUN05 的未知写不重放 |
| E2E06 | X01/C08 | NATIVE | 实际 Wails+Pi 通过 fixture 连接器完成读取→审批→写入，fixture 写计数精确为 1，拒绝时为 0；重启后定义仍在，秘密只在 OS 凭证库 |
| E2E07 | X01/C09 | NATIVE | 实际 Wails 创建定时任务，可控时钟触发普通 RunService 会话并产出产物；重启后 next/last 与 occurrence 一致，无重复执行 |
| PKG01 | X03 | ALL | 包名/arch/hash/revision/版本锁一致，解包无Pi/Node/npm/模型/独立Chromium，runtime依赖另报 |
| PKG02 | X03 | ALL | 无开发工具环境安装/启动，不访问源码路径，缺Pi可一键安装后使用 |
| PKG03 | X03 | NATIVE | 包内启动网页/模型/文件/MCP烟测均成功，浏览器不是开发模式专有功能 |
| PKG04 | X03 | ALL | 中文用户/空格路径、非管理员、离线启动、下载受限/WebView缺失有正确修复界面 |
| PKG05 | X03 | ALL | 安装包/解包/安装后尺寸报告齐全；预算Win/Mac60MiB、deb80MiB、AppImage220MiB，不含后下载Pi |
| PKG06 | X04 | ALL | 从真实旧installer升级保留非空配置/记忆/会话/cookie/凭证引用，迁移失败有可验证恢复 |
| PKG07 | X04 | ALL | 卸载默认保留数据/外部Pi；托管清理不越界；不兼容schema降级明确拒绝或恢复 |
| UPD01 | F09/X04 | ALL | 只选择匹配 stable channel、target、arch 且高于当前版本的清单；未知 schema/普通降级拒绝 |
| UPD02 | F09/X04 | ALL | 清单和完整包分别验签并核对 size/SHA-256；任一失败不进入 ready，旧程序不变 |
| UPD03 | F09/X04 | ALL | 非官方源及跨 allowlist 重定向拒绝；TLS/代理错误可诊断，不执行网页脚本 |
| UPD04 | F09/X04 | ALL | 断点续传只在 ETag/validator 匹配时继续；不匹配时丢弃 partial，最终 hash 唯一 |
| UPD05 | F09/X04 | ALL | 后台下载和 ready 不主动重启，当前 PID/版本保持；下次正常启动前才应用 |
| UPD06 | F09/X04 | ALL | 从旧真实二进制经 updater 启动新真实二进制，进程/API 版本和 hash 均发生预期变化 |
| UPD07 | F09/X04 | ALL | 活跃 run/审批/Pi安装/接管/未保存编辑/清理失败时退出更新被阻止，并列出真实 blocker |
| UPD08 | F09/X04 | ALL | 新程序启动或健康检查失败恢复旧程序；active 指针、版本和 hash 与更新前一致 |
| UPD09 | F09/X04 | ALL | 非空 workspace/Profile/session/memory/artifact/connector/schedule 数据升级后保持且关联一致 |
| UPD10 | F09/X04 | ALL | 数据库迁移失败恢复程序和数据备份；不兼容降级拒绝，不以空库重建冒充恢复 |
| UPD11 | F09/X04 | ALL | 在每个交换事务阶段模拟中断后只留下一个完整 active 版本；下次启动可确定恢复 |
| UPD12 | F09/X04 | ALL | 应用更新前后外部与托管 Pi 路径/version/hash 均不变；更新包不含 Pi/Node/npm/模型/Chromium |
| UPD13 | X04 | ALL | 六目标用真实旧/新签名候选包完成升级；Linux deb 无权限时保留包并明确 BLOCKED/授权流程 |
| UPD14 | U06/U07/X04 | NATIVE | 更新检查/下载/验证/ready/apply/rollback/blocked 在中英文×浅深主题中显示真实状态和修复动作 |
| PERF01 | X04 | NATIVE | 参考环境记录完整；10次冷启动p95≤5s，到主界面可交互，不包含模型时间 |
| PERF02 | X04 | NATIVE | 100events/s×60s，后端接收至UI显示p95≤200ms，关键事件序号无丢失 |
| PERF03 | X04 | NATIVE | 预热5轮后开关tab50轮，等30秒owned进程总RSS增长≤100MiB；统计包括网页子进程 |
| PERF04 | X04 | ALL | 清理10秒后ownedworker/MCP为0，晚到回调不创建新资源，无关用户进程不受影响 |
| REL01 | X05 | Windows两架构 | 正式包签名链与时间戳通过OS工具验证 |
| REL02 | X05 | macOS两架构 | app及内嵌代码签名、公证、staple、Gatekeeper检查通过，非关闭保护后运行 |
| REL03 | X05 | Linux两架构 | 包hash与签名清单可验证，具体渠道验证方式有记录 |
| REL04 | X05 | ALL | 独立审查、支持矩阵、已知限制、安装/恢复指南齐全；发布包与验收包同hash，公开上传有用户授权 |

## 3. Gate 的必需用例集合

- G0：ENV01–03、GOV01–05、PI01–04、PI_DISC01–02、PI_COMP01–04、RPC01–05、SEC01–06/SEC08 的 runtime 子case、CFG03.isolation。版本范围只允许探测，不能自动判兼容。
- G1：BR00–BR07（BR07先用原生基础操作，B04再做完整工具语义）、UI04的最小dialog验证；六目标/LNX双display齐全。
- G2：CFG01–04、DB01–03、RUN01–07、PI_INS01–03、PI_UPG01–03、MOD01–04、RES01–04、MCP01–05、MEM01–05、CONN01–06/CONN08、SCH01–05/SCH07、SEC01–08、BR08–BR17。旧 INS01–07 保留历史坐标，但不能替代新增原子 case。
- G3：UI01–13、CONN07、SCH06、SES01–03、SRCH01、RES05、BRAND01–03、I18N01–04、THEME01–05、ART01–03、E2E01–07，并保留相关 G0–G2 通过证据。每个生产页至少有 zh-CN/en-US × Light/Dark，Playwright 网页截图不能替代 built Wails 证据。
- G4：PKG01–07、PI_PKG01–02、UPD01–14、PERF01–04、INS01–07，外加当前候选包自身 PKG03 的 NATIVE 结果；必须使用真实旧/新包和干净目标环境，fixture 更新不替代包更新。宣称 G4 通过时 `tests/acceptance/oracle-allowlist.json` 必须为空，即 G0–G3 的全部 case 都有实现边界之外的 oracle。
- G5：REL01–04 及当前 revision 完整 G0–G4。

### 避免阶段依赖循环

G1 的普通表单和dialog检查是B01–B03 spike中的原生验收，runner记录 BR07.native 和 UI04.native 子case。G3 再执行BR07.tool/E2E03、UI04.workbench，不能用spike通过替代成品集成。G0的SEC03/SEC04只要求F04已有runtime执行通道；G2补SEC03.browser/SEC04.browser。CFG03.isolation归F04/G0，CFG03.resources归C03/G2。RUN04.service归F05/G2，RUN04.ui归C06/G3；RUN06.service归F05/G2，RUN06.ui归C07/G3。RUN01.service归F05/G2，RUN01.ui归C06/G3；SEC07.store归F07/C02/G2，SEC07.diagnostics归C07/G3（2026-11-05 增加；此前 RUN01/SEC07 归 G2，但其 UI/诊断部分的 owner 要等 G2 过闸，G2 永远等不到它们）。F01清单必须明确子case、owner、阶段和不同artifact，不得把同一个ID的部分成功冒充全部通过。

## 4. 证据目录和格式

```text
docs/evidence/runs/<source-revision>/<gate>/<targetId>/<caseId>/   # gitignore，不入库
  result.json
  command.txt
  stdout.log
  stderr.log
  environment.json
  oracle.json
  screenshot.png / recording.mp4   # native/人工步骤要求
```

带 display 的 case（Linux 双会话、原生用例）必须把 display 写进路径，否则同一目标
的 X11 与 Wayland 两份证据会互相覆盖：

```text
docs/evidence/runs/<source-revision>/<gate>/<targetId>/<display>/<caseId>/
```

该根由 `internal/devtools/integration.go` 的 `evidenceRoot` 写入，历史证据文档与交接中的复现命令均指向它；改根必须同时改产生方、测试与全部复现命令。

oracle 分两类，由 `tests/acceptance/cases.json` 的 `oracleKind` 声明：

- `external`（实现边界之外）：`oracle.json` 必须为 `kind=external-observation`，含合法 `source`（`fixture-counter` / `http-server` / `database` / `filesystem` / `process-tree` / `os-signature` / `manual-review`）、非空 `observation` 和至少一个目录内非空 artifact（文件 hash、服务器 counter、DB 查询、进程树、坐标/时间/RSS 原始样本）。`test-process` 在此类 case 上被判死。
- 缺省/`process`：断言本身就是子进程结果，允许 `kind=test-process`（命令、退出码、stdout/stderr 哈希由 gate 重算）。

2026-11-05 新增的 61 条 case（Pi 生命周期、连接器、调度、UI、i18n、主题、更新）全部声明 `external`。加固前已存在且已有 runner 的 case 暂列入 `tests/acceptance/oracle-allowlist.json`，作为可见欠账；该表只允许缩小，`TestOracleKindIsDeclaredOrGrandfathered` 会拒绝“新 case 不声明 external”和“已声明 external 却仍留在 allowlist”。改造这些旧 runner 时必须改用外部观察。

结构校验只能证明证据自洽与未被篡改；artifact 是否真由外部系统产生，仍由 case 断言和独立审查保证。原始证据可保留在 CI artifact 或用户指定本地目录，仓库只提交索引与脱敏摘要；禁止提交真实凭证、个人网页/cookies、模型私密对话。对外分享诊断前由用户决定。

result.json字段见F01 GateEvidence schema；每个CaseResult含id/subcase/status/expected/actual/oraclePath/startedAt/endedAt；GateEvidence另含targetId、display（Linux: x11/wayland；Windows/macOS: desktop 或空）、worktreeDirty（必须为 false）、sourceRevision、versionsLockHash、reportHash、reviewer。截图只能证明视觉状态，不能独自证明后台调用被拒或不存在数据泄漏。

签名/公证/人工审查涉及人或外部凭证；脚本不能自行生成“已签名”“已审查”字段当证据。结构校验通过不是事实审查通过。

## 5. 回归和验收纪律

- 修复bug后先重跑拥有该case的focused测试；修改RPC/policy/contracts/storage时重跑所有依赖integration；修改native host时重跑相应平台native以及共享browser契约；最终包重跑package。
- 不强制为了数字写大量低价值测试；以本矩阵的可观测行为和风险负例为准，coverage仅作为诊断，不以覆盖率百分比替代case通过。
- 在稳定输入下每个关键权限/取消/引用失效case连续执行10次不得失败；首次失败保留报告，重跑不能覆盖原始失败。其他性能采样按case固定样本数。
- gate失败不能通过调大超时、增大体积/RSS预算、删fixture/平台、移除断言来解决。必要修改须ADR说明原因、影响和新验收，并按基线变更规则处理。
- 最终报告逐目标给出PASS/FAIL/BLOCKED/NOT_RUN、产物路径/hash、实测限制。禁止使用“应该可用”“理论上支持”“CI编译过”代替安装验收。
