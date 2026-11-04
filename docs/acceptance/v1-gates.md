# V1 验收矩阵与门禁

日期：2026-11-03。当前状态：全部 NOT_RUN，产品和门禁runner均未实现。本文件是待开发测试的规范，不是测试报告。

## 1. 判定规则

状态仅 PASS/FAIL/BLOCKED/NOT_RUN；用例可额外标 UNSUPPORTED_EXPECTED，但必须是本文件明确规定的负例，且断言“无副作用+明确错误”通过后才能归为PASS。required用例不得SKIP。缺设备、网络、key、签名或依赖=BLOCKED；实现错误/断言不符=FAIL；二者都不能通过所属gate。

执行顺序G0→G1→G2→G3→G4→G5，但独立任务可先完成。前置失败不应浪费时间运行必然失败的依赖用例，也不能标通过。gate针对同一产品revision、版本锁hash、fixture版本和平台矩阵；代码变更后按影响映射重跑相关suite，重编后的最终包必须重新跑package suite。保留未改模块报告时，需说明对应源码tree hash不变，不能简单把旧revision字符串改成当前值。

平台范围缩写：ALL=六目标；LNX=两个Linux目标各X11/Wayland；HOST=当前测试宿主的纯逻辑测试，仍由六目标完整构建检查补充；NATIVE=ALL且LNX双会话。公开版本需G5，内部版本至少G4。

## 2. 用例与断言

### 环境和门禁自身

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| ENV01 | F00 | ALL | Go/Node/npm/Wails及nativeSDK来源、精确版本、命令路径入锁文件，支持该目标构建；旧Node路径检测报错 |
| ENV02 | F00 | ALL | 矩阵中每目标有可访问的真实运行环境和架构证据；Linux两个display有检查资源；资源缺失保持BLOCKED |
| ENV03 | F00 | HOST | frontend lock/go.sum/versions.lock一致，依赖来源可追溯；重复安装构建不漂移版本 |
| GOV01 | F01 | HOST | 必需case缺失/空suite/unknown target返回2，不能PASS |
| GOV02 | F01 | HOST | 旧版本锁/错误revision/报告hash篡改被拒；人工证据缺真实机器信息或review字段被拒 |
| GOV03 | F01 | HOST | 命令断言失败返回1、配置缺失返回2、成功0；子进程失败不会被日志成功覆盖 |
| GOV04 | F01 | HOST | 所有本矩阵case都在机器索引且有owner/runner；未实现runner非零；仅SKIP的native报告不能通过 |

### Pi、RPC、权限

| ID | Owner | Scope | 不可替代的通过条件 |
|---|---|---|---|
| PI01 | F02 | ALL | 精确路径优先、中文/空格/npm shim正确、多Pi不静默切换、失效路径报NotFound |
| PI02 | F02 | ALL | probe用隔离agentDir，不访问真实provider、不启动用户MCP，用户配置marker和hash不变 |
| PI03 | F02 | ALL | 真实Pi认证版本完成RPC/MCP/extension探测，行为与release契约一致；原始流可追溯，mock不得替代 |
| PI04 | F02 | ALL | 无Node环境的官方独立Pi包通过探测；未知版本unverified不自动运行；npm安装形式单独记录 |
| RPC01 | F03 | HOST | LF随机分块/CRLF/U+2028/UTF8/128KiB记录完整，16MiB允许、超1字节失败，不截断JSON |
| RPC02 | F03 | HOST | 并发乱序response按id匹配，重复/未知id不误归属，取消Call能结束 |
| RPC03 | F03 | ALL | stderr持续洪泛不堵stdout，长输出/写背压不死锁，日志缓冲不无限增长 |
| RPC04 | F03 | HOST | malformed/未知事件有明确诊断不崩溃，未知最终完成语义不报completed |
| RPC05 | F03/X02 | ALL | 退出10秒内owned子孙均清理，无关Pi/Node进程仍在；CleanupFailed不会被显示成清理成功 |
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
| RUN05 | F05/X02 | ALL | 工具已写后崩溃，恢复计数仍1；未知副作用结果不自动重放 |
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
| MOD01 | C02 | HOST | model schema、ID、API、context/output校验准确，秘密只有credentialRef |
| MOD02 | C02 | ALL | 临时Pi连接测试实际调用fixture provider；401/429/超时/有效响应分类准确 |
| MOD03 | C02 | ALL | 旧Pi auth无读取/修改，客户端key显式输入OS库；未知模型能力标unknown |
| MOD04 | C02 | ALL | model切换在新run或明确支持的安全点生效，记录真实provider/model及配置revision |
| RES01 | C03 | HOST | 本地skill导入校验、编辑、删除、启停真实落盘；不改源目录、不执行导入脚本 |
| RES02 | C03 | ALL | 禁用skill后新run不加载；同名自动发现副本不能绕过；旧run保持快照 |
| RES03 | C03 | HOST | skill依赖缺失显示原因；恶意路径/损坏frontmatter被拒；不自动安装外部工具 |
| RES04 | C03/C04 | ALL | 系统policy扩展不可关闭；工具启停影响实际执行，不只是菜单可见性 |
| MCP01 | C04 | ALL | stdio/HTTP两类经真实Pi发现与调用fixture工具，各一个成功 |
| MCP02 | C04 | ALL | 断开/超时/鉴权失败/重连状态准确；未授权副作用计数0 |
| MCP03 | C04 | ALL | server工具列表变化与暴露策略生效；禁用后不可调用，旧run变化策略明确 |
| MCP04 | C04 | HOST | command/args分开、秘密env/header引用、拒绝SSE或无效配置；保存不执行未知脚本 |
| MCP05 | C04 | ALL | 只由Pi管理外部MCP连接，退出清理其owned进程；OAuth未支持时不显示connected |

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
| E2E05 | X02 | NATIVE | worker/MCP/UI/provider/DB/凭证故障演练状态准确，无未知写重放，日志可定位 |
| PKG01 | X03 | ALL | 包名/arch/hash/revision/版本锁一致，解包无Pi/Node/npm/模型/独立Chromium，runtime依赖另报 |
| PKG02 | X03 | ALL | 无开发工具环境安装/启动，不访问源码路径，缺Pi可一键安装后使用 |
| PKG03 | X03 | NATIVE | 包内启动网页/模型/文件/MCP烟测均成功，浏览器不是开发模式专有功能 |
| PKG04 | X03 | ALL | 中文用户/空格路径、非管理员、离线启动、下载受限/WebView缺失有正确修复界面 |
| PKG05 | X03 | ALL | 安装包/解包/安装后尺寸报告齐全；预算Win/Mac60MiB、deb80MiB、AppImage220MiB，不含后下载Pi |
| PKG06 | X04 | ALL | 从真实旧installer升级保留非空配置/记忆/会话/cookie/凭证引用，迁移失败有可验证恢复 |
| PKG07 | X04 | ALL | 卸载默认保留数据/外部Pi；托管清理不越界；不兼容schema降级明确拒绝或恢复 |
| PERF01 | X04 | NATIVE | 参考环境记录完整；10次冷启动p95≤5s，到主界面可交互，不包含模型时间 |
| PERF02 | X04 | NATIVE | 100events/s×60s，后端接收至UI显示p95≤200ms，关键事件序号无丢失 |
| PERF03 | X04 | NATIVE | 预热5轮后开关tab50轮，等30秒owned进程总RSS增长≤100MiB；统计包括网页子进程 |
| PERF04 | X04 | ALL | 清理10秒后ownedworker/MCP为0，晚到回调不创建新资源，无关用户进程不受影响 |
| REL01 | X05 | Windows两架构 | 正式包签名链与时间戳通过OS工具验证 |
| REL02 | X05 | macOS两架构 | app及内嵌代码签名、公证、staple、Gatekeeper检查通过，非关闭保护后运行 |
| REL03 | X05 | Linux两架构 | 包hash与签名清单可验证，具体渠道验证方式有记录 |
| REL04 | X05 | ALL | 独立审查、支持矩阵、已知限制、安装/恢复指南齐全；发布包与验收包同hash，公开上传有用户授权 |

## 3. Gate 的必需用例集合

- G0：ENV01–03、GOV01–04、PI01–04、RPC01–05、SEC01–06/SEC08的runtime子case、CFG03.isolation（F04项目执行隔离）。SEC07/配置存储可独立完成，但不能用未实现的明文fallback暂过门禁。
- G1：BR00–BR07（BR07先用原生基础操作，B04再做完整工具语义）、UI04的最小dialog验证；六目标/LNX双display齐全。
- G2：CFG01–04、DB01–03、RUN01–07、MOD01–04、RES01–04、MCP01–05、MEM01–05、SEC01–08、BR08–BR17。（INS01–07 于 2026-11-04 移到 G4，见 ADR 0002；G2 对 Pi 来源只要求记录外部已装版本与hash，不要求自研安装器。）
- G3：UI01–04、ART01–03、E2E01–05，并保留相关G0–G2通过证据。
- G4：PKG01–07、PERF01–04、INS01–07，外加当前候选包自身PKG03的NATIVE结果；不能仅沿用开发二进制G3证据。INS 是 F06 安装器的验收，与安装包在同一阶段：安装器会改写 active Pi 指针并清理旧版，不在功能开发期执行（ADR 0002）。
- G5：REL01–04及当前revision完整G0–G4。

### 避免阶段依赖循环

G1 的普通表单和dialog检查是B01–B03 spike中的原生验收，runner记录 BR07.native 和 UI04.native 子case。G3 再执行BR07.tool/E2E03、UI04.workbench，不能用spike通过替代成品集成。G0的SEC03/SEC04只要求F04已有runtime执行通道；G2补SEC03.browser/SEC04.browser。CFG03.isolation归F04/G0，CFG03.resources归C03/G2。RUN04.service归F05/G2，RUN04.ui归C06/G3；RUN06.service归F05/G2，RUN06.ui归C07/G3。F01清单必须明确子case、owner、阶段和不同artifact，不得把同一个ID的部分成功冒充全部通过。

## 4. 证据目录和格式

```text
artifacts/evidence/<source-revision>/<gate>/<targetId>/<caseId>/
  result.json
  command.txt
  stdout.log
  stderr.log
  environment.json
  oracle.json
  screenshot.png / recording.mp4   # native/人工步骤要求
```

oracle按case包含文件hash、服务器counter、DB查询、进程树、坐标/时间/RSS原始样本。原始证据可保留在CI artifact或用户指定本地目录，仓库只提交索引与脱敏摘要；禁止提交真实凭证、个人网页/cookies、模型私密对话。对外分享诊断前由用户决定。

result.json字段见F01 GateEvidence schema；每个CaseResult含id/subcase/status/expected/actual/oraclePath/startedAt/endedAt。截图只能证明视觉状态，不能独自证明后台调用被拒或不存在数据泄漏。

签名/公证/人工审查涉及人或外部凭证；脚本不能自行生成“已签名”“已审查”字段当证据。结构校验通过不是事实审查通过。

## 5. 回归和验收纪律

- 修复bug后先重跑拥有该case的focused测试；修改RPC/policy/contracts/storage时重跑所有依赖integration；修改native host时重跑相应平台native以及共享browser契约；最终包重跑package。
- 不强制为了数字写大量低价值测试；以本矩阵的可观测行为和风险负例为准，coverage仅作为诊断，不以覆盖率百分比替代case通过。
- 在稳定输入下每个关键权限/取消/引用失效case连续执行10次不得失败；首次失败保留报告，重跑不能覆盖原始失败。其他性能采样按case固定样本数。
- gate失败不能通过调大超时、增大体积/RSS预算、删fixture/平台、移除断言来解决。必要修改须ADR说明原因、影响和新验收，并按基线变更规则处理。
- 最终报告逐目标给出PASS/FAIL/BLOCKED/NOT_RUN、产物路径/hash、实测限制。禁止使用“应该可用”“理论上支持”“CI编译过”代替安装验收。
