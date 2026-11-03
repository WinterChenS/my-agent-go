# Integration and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用真实Pi、真实原生网页、真实目标安装包验证V1，而非只交付开发模式演示。

**Architecture:** 本地受控fixture保证可重复；真实模型单独验收；平台runner采集原始证据；发布门禁汇总六目标而不隐去缺项。

**Tech Stack:** 基线锁定版本、各平台native SDK与打包/签名工具。

**Spec:** V1执行基线、master plan、`docs/acceptance/v1-gates.md`。

## Global Constraints

必须区分 G4内部候选与G5公开正式发行；签名凭证和目标机器缺失为BLOCKED。用户未授权公开发布时只构建/验证产物，不上传发布。此计划要求的代码审查不授权给外部账号发送消息。

## Review Focus

开发目录能启动而安装包缺assets→X03；仅mock provider闭环→X02；应用升级覆盖用户Pi或记忆→X04；混用旧revision测试报告→X03/X05；退出残留子进程/浏览器视图泄漏→X02/X04。

## X01：三条 deterministic 完整使用旅程

**依赖：** C01–C07/B07、G2。**Files：** `tests/e2e/{filesystem,mcp,browser}_journey.*`、`tests/e2e/fixtures/`、`internal/devtools/e2e.go`、`docs/evidence/e2e/`。

**Interfaces：** `devtools verify --suite=e2e --target=<targetId>` 从构建后Wails程序启动，传隔离appData、fixture provider/MCP URLs、fixture workspace；提供 test-only结构化状态探针，release必须移除/禁用探针。不能开放生产任意调试/写数据库接口。

- [ ] 编写三个初始失败旅程：本地文件读取→授权修改→产物核对；MCP发现→调用→结果→禁用再调用被拒；右侧网页观察→授权填写/提交→服务端counter→接管→恢复。
- [ ] 为每条旅程建立独立oracle：文件hash/内容、MCP server调用记录、网页server数据库/计数；UI文本不是唯一证据。
- [ ] 连接真实Pi执行受控provider脚本，完成旅程；每条必须包含重启客户端后可查历史、配置revision和产物来源，不能依赖内存假数据。
- [ ] 三端目标跑built app，UI自动化可用原生test driver/辅助功能，native区域步骤允许人工录制但必须逐步记录；React组件测试不覆盖此项。
- [ ] 归档来源revision、版本/fixture hash、命令、计数、录屏、人工review，suite退出码一致后提交。

**验收：** E2E01–E2E03。

## X02：真实模型验收与故障演练

**依赖：** X01/F03/F04/F05。**Files：** `tests/integration/live_model_test.go`、`tests/e2e/failure_matrix.*`、`docs/evidence/live-model/`；默认本地unit不自动发起真实云调用。

**Interfaces：** `devtools verify --suite=live --target=<targetId> --credential-ref=<ref>`；F01 runner须为该suite明确注册，运行前显示将调用哪一个provider/model。凭证从OS库读取，命令行只传引用，报告不存secret。整个G3至少一个已支持provider的真实模型闭环，各平台均有模型调用与工具执行烟测；不强制同一模型多次重测以堆证据。

- [ ] 真实Pi+真实模型完成三条E2E旅程的最小版；浏览器写操作只访问本地fixture，不给真实网站发帖/购买。模型结果不确定时保留失败，不能换成回放文件声称通过。
- [ ] 故障演练：kill worker、MCP退出、模型401/429/超时、UI断线、应用异常退出、安装下载断网、数据库被锁、凭证库不可用；每项状态/恢复路径与基线一致。
- [ ] `TestInterruptedRunDoesNotReplayUnknownWrite`从fixture外部确认计数保持1；重试逻辑只针对明确幂等/未发送阶段，不能对所有tool error一律retry。
- [ ] 进程树退出后10秒内owned workers/MCP/fixture计数=0；外部用户Pi/浏览器保持存活。不能按进程名批量杀所有node/pi。
- [ ] 执行e2e+live suites、归档数据和费用usage可用性，提交。模型key/网络缺失记BLOCKED，不能改成本地fixture通过live。

**验收：** E2E04/E2E05、RUN05、RPC05。

## X03：六目标安装包与干净机器

**依赖：** G3。**Files：** `packaging/{windows,macos,linux}/`、`.github/workflows/{verify,package}.yml` 或等价所选CI、`internal/devtools/{package,packageverify}.go`、`tests/package/`、`docs/evidence/packages/`。

**Interfaces：** master package/verify命令；产物命名 `MyAgent-<version>-<targetId>.<ext>`，manifest字段version/sourceRevision/versionsLockHash/target/hash/sizeBytes/files/runtimeDependencies/channel。版本初始0.1.0-internal.1，后续按实际release记录递增；不可上传未经用户授权的公开release。

- [ ] 编写 `MissingNativeEvidenceFailsGate`、`PackageRuntimeScanFindsPiNode`：缺目标报告或包里含Pi/Node/runtime必须失败。扫描解包内容/文件清单，不只文件名搜字面字符串；验证构建脚本未下载并捆绑这些runtime。
- [ ] 原生runner生成win两架构NSIS、mac两架构DMG、Linux两架构deb/AppImage；保留构建工具版本与依赖清单。交叉编译允许作为构建方式，运行验收仍在对应目标上执行。
- [ ] 在无Go/Node/Pi开发工具的干净环境安装并启动；无Pi页面能一键安装到用户目录、probe通过，然后最小模型/本地文件/MCP/browser旅程可用。安装后不从开发目录读assets或绝对源码路径。
- [ ] 中文用户名/空格workspace、离线启动、WebView缺失、下载受限、默认非管理员用户分别检查；缺Pi离线时仍能管理设置/历史，不能卡空白窗口。
- [ ] 包体预算：Windows/macOS压缩产物≤60MiB，Linux deb≤80MiB、AppImage≤220MiB；实际解包与安装后字节另报，Pi后下载占用另报。超预算写体积归因与ADR，不删除required资源或临时抬预算。
- [ ] 执行package suite、六目标manifest及hash汇总；缺任何安装运行证据G4=BLOCKED，不能把“已生成文件”当安装通过。提交。

**验收：** PKG01–PKG05；CI账号未配置可先产本地证据，不虚构绿色CI链接。

## X04：升级、数据保留、回滚与性能

**依赖：** X03/F06/F07。**Files：** `tests/package/{upgrade,rollback,uninstall,performance}.*`、`docs/evidence/performance/`、必要迁移修复；不在此任务增加应用在线自动更新。

**Interfaces：** 升级用两份真实installer（baseline与candidate），app data schema版本、Pi active指针、memory/artifact/session索引hash作为前后对照。升级前fixture记录明确预期，不用空数据库验收迁移。

- [ ] 建立上一内部版本数据：两个workspace、两个Profile、active/candidate记忆、会话、浏览器cookie、凭证引用和外部Pi路径。升级后字段和关联保持，安装目录改变不改变数据归属。
- [ ] 模拟DB迁移中断和Pi激活失败：数据库事务回滚/备份恢复可用；降级到不兼容schema必须明确拒绝或走已验证恢复路径，不能直接启动后丢表。
- [ ] 卸载默认保留用户数据/外部Pi；选择清理托管Pi不删除外部安装或超出目标目录；所有递归删除先解析并验证预期产品根目录。
- [ ] 性能采用统一fixture：至少4核/8GB/SSD，记录实际CPU/RAM/OS/引擎和样本数；不含模型响应时间。冷启动到可交互10次p95≤5秒；100事件/秒持续60秒，Go事件接收到UI显示p95≤200ms；收集状态序号保证关键事件不丢。
- [ ] 预热5轮后开关native tab50次，关闭后等待30秒，产品拥有的进程总RSS相对预热基线增长≤100MiB；native tab/handle计数归零，10秒后owned子进程为0。RSS统计含浏览器子进程并说明共享页口径。失败报告不以“框架缓存”直接豁免。
- [ ] package/performance suites及迁移验证通过，产出G4证据，提交。

**验收：** PKG06/PKG07、PERF01–PERF04。

## X05：正式发行门禁和最终交接

**依赖：** G4，签名/公证资源及公开发行授权。**Files：** `packaging/signing/`、`build/release-manifest.json`、`docs/release/{checklist,known-limitations,user-guide}.md`、`docs/evidence/release/`。

**Interfaces：** `devtools gate --id=G5 --evidence=<root>`；release manifest只引用同revision已验收的包；签名密码和私钥来自runner安全存储，不能写仓库/日志。

- [ ] Windows签名链/时间戳验证；macOS应用及内嵌native可执行签名、公证、staple与Gatekeeper验证；Linux发布校验和与签名清单并验证，实际发行渠道要求另记录。
- [ ] 校验签名后包若发生变化，hash更新且重新执行package安装验证；不能给未测旧包换名称冒充当前release。
- [ ] 文档列出精确支持矩阵、Pi版本/安装方式、浏览器Unsupported场景、凭证归属、安装/恢复/卸载步骤；不能宣称任意网站或全平台通用。
- [ ] 独立reviewer核对G0–G5原始证据及已知限制；没有独立review时G5不能由实现者自己填PASS。该要求适用于开发执行，不要求本轮计划编写启动子agent。
- [ ] 只在用户已授权发行时上传；否则交付已验证候选及说明，公开发布状态保持pending。列出每个目标下载/本地路径、hash、测试结果和剩余外部依赖。

**验收：** REL01–REL04。到此才可称“公开发行完成”。
