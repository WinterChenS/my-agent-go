# Anvil i18n catalogue

Source baseline: prototype `assets/studio.js` `I18N` at commit `112fcae854b324d2e75fdf5c890083ef9c1d3d6e`, extended with R0-01..R0-04 shell/workbench copy. Both catalogues must share every key. User text, session content, file paths and user names are never translated.

| Key | zh-CN | en-US |
| --- | --- | --- |
| `nav.group.start` | 开始 | Start |
| `nav.tools` | 工具与自动化 | Tools & Automation |
| `nav.workbench` | 工作台 | Dashboard |
| `nav.chat` | 对话 | Chats |
| `nav.agents` | Agent | Agents |
| `nav.library` | 资源库 | Library |
| `nav.skills` | Skill | Skills |
| `nav.mcp` | MCP | MCP |
| `nav.connectors` | 连接器 | Connectors |
| `nav.schedules` | 定时任务 | Schedules |
| `nav.settings` | 设置 | Settings |
| `page.workbench.title` | 工作台 | Dashboard |
| `page.chat.title` | 对话 | Chat |
| `page.agents.title` | Agent | Agents |
| `page.library.title` | 资源库 | Library |
| `page.skills.title` | Skill | Skills |
| `page.mcp.title` | MCP | MCP |
| `page.connectors.title` | 连接器 | Connectors |
| `page.schedules.title` | 定时任务 | Schedules |
| `page.settings.title` | 设置 | Settings |
| `crumb.workspace` | 工作区 | Workspace |
| `crumb.agents` | 智能体 | Agents |
| `crumb.automation` | 自动化 | Automation |
| `crumb.system` | 系统 | System |
| `crumb.chat` |  |  |
| `main.content` | 主内容 | Main content |
| `brand.subtitle` | 本地智能体客户端 · v0.9 | Local agent client · v0.9 |
| `runtime.title` | 本地运行时 | Local runtime |
| `runtime.status.unknown` | 未检测 | Not detected |
| `user.name` | 本地工作区 | Local workspace |
| `user.plan` | 未连接账号 | No account connected |
| `topbar.search` | 搜索 | Search |
| `topbar.help` | 帮助 | Help |
| `topbar.notice.search` | 搜索将在后续任务接入 | Search will be connected in a later task |
| `topbar.notice.help` | 帮助将在后续任务接入 | Help will be connected in a later task |
| `topbar.runtime.notConnected` | 运行时未接入 | Runtime not connected |
| `document.titleFormat` | {title} · Anvil | {title} · Anvil |
| `placeholder.badge` | R0-01 · shell | R0-01 · shell |
| `placeholder.description` | 共用壳已锁定 {path} 路由。页面正文由 {task} 按原型 {source} 实现，R0-01 只交付壳层。 | The shared shell owns the {path} route. The page body will be implemented by {task} from prototype {source}; R0-01 only delivers the shell. |
| `placeholder.card.title` | 路由占位 | Route placeholder |
| `placeholder.card.sub` | {task} 实现页面内容 | {task} implements the page content |
| `placeholder.body` | 当前地址 {path} 已由共用壳承载；侧栏、顶栏、滚动容器、焦点与导航行为可用。 | The shared shell hosts {path}; sidebar, topbar, scroll container, focus and navigation are available. |
| `placeholder.note` | 业务内容、弹窗与数据状态不在本任务范围。 | Business content, dialogs and data states are outside this task. |
| `placeholder.fallback` | 未找到路由 {path} | Unknown route {path} |
| `settings.theme.title` | 主题 | Theme |
| `settings.theme.subtitle` | 浅色、深色或跟随 Windows 外观 | Light, dark, or follow the Windows appearance |
| `settings.theme.system` | 跟随系统 | System |
| `settings.theme.system.hint` | 跟随 Windows 外观 | Follow the Windows appearance |
| `settings.theme.light` | 浅色 | Light |
| `settings.theme.light.hint` | 始终使用浅色 | Always use light |
| `settings.theme.dark` | 深色 | Dark |
| `settings.theme.dark.hint` | 始终使用深色 | Always use dark |
| `settings.theme.status` | 当前主题：{resolved}（{mode}） | Current theme: {resolved} ({mode}) |
| `settings.theme.resolved.light` | 浅色 | Light |
| `settings.theme.resolved.dark` | 深色 | Dark |
| `settings.theme.notice.invalid` | 主题设置无效，已回退到跟随系统 | Invalid theme setting; reverted to System |
| `settings.theme.notice.readFailed` | 主题设置读取失败，已回退到跟随系统 | Could not read the theme setting; reverted to System |
| `settings.theme.notice.saveFailed` | 主题设置保存失败，本次选择仍然生效 | Could not save the theme setting; this choice is still active |
| `settings.language.title` | 语言 | Language |
| `settings.language.subtitle` | 界面语言 | Interface language |
| `settings.language.zh` | 简体中文 | Chinese |
| `settings.language.en` | English | English |
| `settings.language.status` | 当前语言：{language} | Current language: {language} |
| `settings.language.notice.invalid` | 语言设置无效，已回退到简体中文 | Invalid language setting; reverted to Chinese |
| `settings.language.notice.readFailed` | 语言设置读取失败，已回退到简体中文 | Could not read the language setting; reverted to Chinese |
| `settings.language.notice.saveFailed` | 语言设置保存失败，本次选择仍然生效 | Could not save the language setting; this choice is still active |
| `language.quickSwitch` | 语言 | Language |
| `workbench.greeting.morning` | 早上好 | Good morning |
| `workbench.greeting.afternoon` | 下午好 | Good afternoon |
| `workbench.greeting.evening` | 晚上好 | Good evening |
| `workbench.subtitle.empty` | 尚未接入运行时；完成 G1/G2 后会显示真实会话、任务与产出。 | Runtime not connected; real chats, schedules and artifacts arrive with G1/G2. |
| `workbench.subtitle.sample` | 4 个会话仍在进行，2 个定时任务将在今天运行。上次离开时，「API 迁移评估」正在等待你确认一条写文件操作。 | 4 chats are active and 2 schedules run today. The API migration review was waiting for a file-write approval. |
| `workbench.action.configureAgent` | 配置 Agent | Configure agent |
| `workbench.action.newChat` | 新建对话 | New chat |
| `workbench.action.allChats` | 全部会话 | All chats |
| `workbench.action.continue` | 继续 | Continue |
| `workbench.action.manage` | 管理 | Manage |
| `workbench.action.all` | 全部 | All |
| `workbench.action.unavailable` | 未接入（G1/G2） | Not connected (G1/G2) |
| `workbench.action.sampleNotice` | 样例模式：业务操作未接入，不会产生真实结果。 | Sample mode: business actions are not connected and produce no real result. |
| `workbench.sampleBanner` | 原型样例数据 · 仅用于设计对照，不是真实状态 | Prototype sample data · design comparison only, not real state |
| `workbench.stats.title` | 概览 | Overview |
| `workbench.stats.sessions` | 本周会话 | Sessions this week |
| `workbench.stats.toolCalls` | 工具调用 | Tool calls |
| `workbench.stats.tokens` | Token 消耗 | Token usage |
| `workbench.stats.pending` | 待确认操作 | Pending approvals |
| `workbench.stats.emptySub` | 尚未接入运行时 | No runtime connected |
| `workbench.stats.sessionsDelta` | 较上周 +6 | +6 vs last week |
| `workbench.stats.tokensSub` | 输入 4.8M · 输出 1.4M | Input 4.8M · output 1.4M |
| `workbench.stats.pendingSub` | 1 中风险 · 1 高风险 | 1 medium · 1 high risk |
| `workbench.stats.successRate` | 成功率 97.4% | 97.4% success |
| `workbench.activity.title` | Agent 工作活跃度 | Agent activity |
| `workbench.activity.subtitle` | 每天完成的会话、工具调用与定时任务 | Sessions, tool calls and schedules completed each day |
| `workbench.activity.range12` | 最近 12 周 | Last 12 weeks |
| `workbench.activity.range26` | 最近 26 周 | Last 26 weeks |
| `workbench.activity.range53` | 最近一年 | Last year |
| `workbench.activity.summaryYear` | 过去一年共完成 {count} 次 Agent 工作 | {count} Agent jobs completed in the past year |
| `workbench.activity.summaryWeeks` | 最近 {weeks} 周共完成 {count} 次 Agent 工作 | {count} Agent jobs completed in the last {weeks} weeks |
| `workbench.activity.activeDays` | 活跃天数 | Active days |
| `workbench.activity.completed` | 完成工作 | Completed |
| `workbench.activity.streak` | 最长连续天数 | Longest streak |
| `workbench.activity.foot` | 统计保存在本机，仅计算已完成的工作 | Statistics stay on this machine and count completed work only |
| `workbench.activity.less` | 少 | Less |
| `workbench.activity.more` | 多 | More |
| `workbench.activity.empty` | 尚未接入会话活动 | No session activity yet |
| `workbench.activity.cell` | {date}，完成 {count} 次 Agent 工作 | {date}, {count} Agent jobs completed |
| `workbench.activity.mon` | 一 | Mon |
| `workbench.activity.wed` | 三 | Wed |
| `workbench.activity.fri` | 五 | Fri |
| `workbench.chats.title` | 继续对话 | Continue conversations |
| `workbench.chats.subtitle` | 按最近活动排序 | Sorted by recent activity |
| `workbench.chats.empty` | 尚未接入会话 | No chats connected yet |
| `workbench.artifacts.title` | 最近产出 | Recent artifacts |
| `workbench.artifacts.subtitle` | 来自 3 个会话 | From 3 chats |
| `workbench.artifacts.empty` | 尚未接入产出物 | No artifacts yet |
| `workbench.agents.title` | 我的 Agent | My agents |
| `workbench.agents.empty` | 尚未接入 Agent | No agents connected yet |
| `workbench.agents.ready` | 就绪 | Ready |
| `workbench.schedules.title` | 今日定时任务 | Today’s schedules |
| `workbench.schedules.empty` | 尚未接入定时任务 | No schedules connected yet |
| `workbench.status.title` | 运行状态 | Run status |
| `workbench.status.empty` | 运行时未接入 | Runtime not connected |
| `workbench.status.runtime` | 本地运行时 | Local runtime |
| `workbench.status.mcp` | MCP 服务 | MCP services |
| `workbench.status.connectors` | 连接器 | Connectors |
| `workbench.status.quota` | 下次配额重置 | Next quota reset |
| `workbench.status.pending` | 待确认 | Pending |
| `workbench.status.draft` | 草稿 | Draft |
| `workbench.status.published` | 已发布 | Published |
| `workbench.search.title` | 全局搜索 | Global search |
| `workbench.search.subtitle` | 跨会话、Agent、Skill 与产出物检索。 | Search chats, agents, skills and artifacts. |
| `workbench.search.placeholder` | 输入关键词，例如「迁移」「周报」「MCP」 | Type a keyword, e.g. migration, report or MCP |
| `workbench.search.hint` | 按 {shortcut} 可在任意页面唤起。 | Press {shortcut} on any page. |
| `workbench.search.close` | 关闭 | Close |
| `workbench.search.pages` | 页面 | Pages |
| `workbench.search.agents` | Agent | Agents |
| `workbench.search.artifacts` | 输出物 | Artifacts |
| `workbench.search.empty` | 没有匹配结果 | No matches |
| `workbench.search.open` | 打开全局搜索 | Open global search |
| `workbench.palette.label` | 快速跳转 | Quick jump |
| `workbench.palette.placeholder` | 搜索页面、Agent、任务… | Search pages, agents, and tasks… |
| `workbench.palette.empty` | 没有匹配的页面或操作 | No matching pages or actions |
| `workbench.palette.sub.page` | 页面 | Page |
| `workbench.palette.sub.automation` | 自动化 | Automation |
| `workbench.palette.action.newChat` | 新建对话 | New chat |
| `workbench.palette.action.newChatSub` | 操作 · 对话 | Action · Chats |
| `workbench.palette.action.newSchedule` | 新建定时任务 | New schedule |
| `workbench.palette.action.newScheduleSub` | 操作 · 自动化 | Action · Automation |
| `workbench.palette.action.addMcp` | 添加 MCP 服务 | Add MCP server |
| `workbench.palette.action.addMcpSub` | 操作 · MCP | Action · MCP |
| `workbench.palette.action.manageRuntime` | 管理本地运行时 | Manage local runtimes |
| `workbench.palette.action.manageRuntimeSub` | 设置 | Settings |
| `workbench.palette.action.tokenAudit` | Token 用量审计 | Token usage audit |
| `workbench.palette.action.tokenAuditSub` | 设置 | Settings |
| `workbench.palette.hint.move` | ↑ ↓ 移动 | ↑ ↓ Move |
| `workbench.palette.hint.open` | ↵ 打开 | ↵ Open |
| `workbench.palette.hint.summon` | ⌘ K 呼出 | ⌘ K Summon |
| `workbench.palette.hint.escape` | ESC | ESC |
| `workbench.palette.unavailable` | 未接入：该操作将在后续任务开放 | Not connected: this action arrives in a later task |
