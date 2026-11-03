# 给实施 Agent 的任务指令

以下内容供用户在决定开始开发后交给执行agent；本文件本身不是启动开发或公开发布的动作。

```text
在 D:\project\my-agent-go 实施V1客户端。

先读现场AGENTS.md，然后依次阅读：
docs/superpowers/specs/2026-11-03-v1-execution-baseline.md
docs/superpowers/plans/2026-11-03-agent-client-master.md
docs/acceptance/v1-gates.md
docs/acceptance/v1-plan-index.json
再读当前任务所属模块计划。

先检查现场，不能假定仓库仍只有文档。检查.codegraph；存在时按AGENTS先用CodeGraph。
以F00开始，按依赖推进，每次只认领一个Task ID。
在实现前列出：当前任务、已满足依赖、输入版本、将修改文件、失败用例与验收命令。
使用计划执行技能；用户未要求并行时不要擅自启动其他agent。

遵守Go/Wails/React/本地Pi选型；基础包不带Pi/Node/Chromium。
右侧浏览器必须是真实同窗口视图；不可以iframe、浮窗或截图流替代。
G0不通过不得开始依赖它的工作台集成；G1不通过不得宣称浏览器跨平台完成。
某目标没有测试机器时状态BLOCKED，继续可独立推进任务，不删除目标或改成PASS。

每个任务完成失败用例、最小实现、focused验证、原始证据和独立可审查提交。
所有测试在隔离appData/Pi agentDir/workspace执行，不改用户真实配置和登录。
权限和停止用实际文件/进程/HTTP计数验收，不只看UI文字。
真实Pi、fixture provider和真实模型结果分开记录，不能互相替代。
缺key/设备/签名/网络时保留BLOCKED及修复步骤，不能编造证据。

遇到选型不可行、计划冲突或必须改变需求时，先写ADR说明证据和影响。
不自行换桌面框架、删平台、加后置功能、抬门禁阈值、关闭隔离或迁移用户凭证。
计划未覆盖但不改变产品范围的小实现选择自行解决并记录，不逐个请求确认。

最终报告逐Task/目标给PASS/FAIL/BLOCKED/NOT_RUN、实际命令退出码、产物路径/hash和限制。
未通过G4不称V1可交付，未通过G5不称正式发行完成。
构建安装包不等于获得公开上传授权；只有用户明确授权才发布。
```
