# 开发文档入口

当前只有设计与任务书，没有产品实现，全部开发门禁为 NOT_RUN。

## 执行必读

1. [V1执行基线](superpowers/specs/2026-11-03-v1-execution-baseline.md)：本轮收敛的范围、默认决策、精确数值与排除项。
2. [总控计划](superpowers/plans/2026-11-03-agent-client-master.md)：任务依赖、阶段门禁、变更纪律。
3. [验收矩阵](acceptance/v1-gates.md)：用例ID、owner、目标平台、外部可观察断言。
4. [机器索引](acceptance/v1-plan-index.json)：28个任务、6个gate、平台和R01–R10追踪。
5. 对应模块计划：
   - [基础与Pi：F00–F07](superpowers/plans/2026-11-03-foundation-runtime.md)
   - [原生浏览器：B00–B07](superpowers/plans/2026-11-03-native-browser.md)
   - [资源与工作台：C01–C07](superpowers/plans/2026-11-03-resources-workbench.md)
   - [集成与发行：X01–X05](superpowers/plans/2026-11-03-integration-release.md)

## 运行配置

[模型与运行配置的环境变量](model-config-env.md)：`ANVIL_PROVIDER / ANVIL_MODEL / ANVIL_BASE_URL /
ANVIL_MODEL_API_KEY` 等变量的语义、优先级（设置 > 环境变量 > 默认值）、不落盘与密钥引用规则。

[给实施agent的任务指令](agent-handoff.md)可复制到执行会话。当前编写计划不等于已经授权公开发布，也不等于本轮开始实施。

## 历史设计与调研

[原方案](agent-client-proposal.md)、[技术决策讨论记录](technical-decisions.md)、[Wails浏览器设计](wails-browser-design.md)、[GooeyPi源码调研](gooeypi-browser-review.md)。

与V1基线冲突时，以用户最新指令和V1基线为准；历史候选方案不能被实现agent任意选取来降低交付范围。
