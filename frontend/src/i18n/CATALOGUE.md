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
| `workbench.palette.hint.move` | 移动 | Move |
| `workbench.palette.hint.open` | 打开 | Open |
| `workbench.palette.hint.summon` | 呼出 | Summon |
| `workbench.palette.hint.escape` | ESC | ESC |
| `workbench.palette.unavailable` | 未接入：该操作将在后续任务开放 | Not connected: this action arrives in a later task |
| `workbench.subtitle.live` | 近 7 天 {sessions} 个会话、{tools} 次工具调用，{active} 个会话进行中。数据来自本机会话账本。 | {sessions} sessions and {tools} tool calls in the last 7 days; {active} chats are running. All figures come from the local session ledger. |
| `workbench.loading` | 正在读取本机会话账本… | Reading the local session ledger… |
| `workbench.failed` | 工作台数据读取失败：{detail} | Could not read workbench data: {detail} |
| `workbench.refreshing` | 正在更新统计范围… | Updating the range… |
| `workbench.stats.sessionsDeltaLive` | 较上周同期 {delta} | {delta} vs the same days last week |
| `workbench.stats.successRateLive` | 成功率 {rate}% | {rate}% success |
| `workbench.stats.successRateMissing` | 工具未回报执行结果 | Tools did not report an outcome |
| `workbench.stats.tokensSubLive` | 输入 {input} · 输出 {output} | Input {input} · output {output} |
| `workbench.stats.tokensMissing` | 运行时未回报 Token 用量 | The runtime did not report token usage |
| `workbench.stats.pendingSubLive` | 账本中未解决的审批请求 | Unresolved approval requests in the ledger |
| `workbench.activity.cellBreakdown` | 完成回合 {runs} · 工具执行 {tools} | {runs} completed runs · {tools} tool results |
| `workbench.activity.footLive` | 来自本机会话账本，只计已完成的回合与返回结果的工具调用 | From the local session ledger; completed runs and returned tool calls only |
| `workbench.activity.emptyLive` | 账本中还没有完成的回合或工具调用 | No completed runs or tool results in the ledger yet |
| `workbench.chats.meta` | {agent} · {time} · {turns} 轮 | {agent} · {time} · {turns} turns |
| `workbench.chats.running` | 进行中 | Running |
| `workbench.chats.untitled` | 未命名会话 | Untitled chat |
| `workbench.chats.emptyLive` | 还没有会话，新建一个开始 | No chats yet — start a new one |
| `workbench.artifacts.meta` | {session} · {size} · {time} | {session} · {size} · {time} |
| `workbench.artifacts.subtitleLive` | 来自 {count} 个本地文件 | From {count} local files |
| `workbench.artifacts.emptyLive` | 最近的会话还没有写出文件 | Recent chats have not written any files |
| `workbench.agents.meta` | {model} · {skills} 个 Skill · {runs} 次运行 | {model} · {skills} skills · {runs} runs |
| `workbench.agents.disabled` | 已停用 | Disabled |
| `workbench.agents.emptyLive` | 尚未配置 Agent | No agents configured |
| `workbench.schedules.unavailable` | 本机未接入定时任务调度器，暂无今日任务 | No scheduler is connected to this machine, so there are no tasks today |
| `workbench.status.unavailable` | 未接入 | Not connected |
| `workbench.status.runtimeValue` | {label} · {ready}/{total} 可用 | {label} · {ready}/{total} ready |
| `workbench.status.noRuntime` | 未检测到已就绪的运行时 | No ready runtime detected |
| `workbench.status.mcpValue` | {connected}/{total} 已连接 | {connected}/{total} connected |
| `workbench.status.quotaUnavailable` | 未配置 | Not configured |
| `workbench.status.localFile` | 本地文件 | Local file |
| `workbench.sources.note` | 数据来源：会话账本、资源目录与项目文件；定时任务调度、连接器与配额服务在本版本未接入。 | Sources: the session ledger, the resource catalog and project files. A scheduler, connectors and a quota service are not available in this build. |
| `workbench.sources.title` | 数据说明 | About this data |
| `chat.back` | 返回工作台 | Back to dashboard |
| `chat.normal.title` | 尚未接入会话 | No chat connected |
| `chat.normal.subtitle` | 完成 G1/G2 后会在这里显示真实会话。 | Real chats will appear here after G1/G2. |
| `chat.empty.title` | 尚未接入运行时/会话 | No runtime or chat connected |
| `chat.empty.body` | 当前没有可显示的对话。接入运行时后，消息、思考、工具调用和产出会出现在这里。 | There is no conversation to show. Messages, thinking, tool calls, and artifacts will appear here after a runtime is connected. |
| `chat.sampleBanner` | 原型样例数据 · 仅用于设计对照，不是真实状态 | Prototype sample data · design reference only, not a real state |
| `chat.badge.clarification` | 1 项待澄清 | 1 clarification |
| `chat.badge.pendingAuth` | 1 项待授权 | 1 approval |
| `chat.badge.confirmed` | 已确认 | Confirmed |
| `chat.export` | 导出 | Export |
| `chat.more` | 更多操作 | More actions |
| `chat.action.unavailable` | 未接入（G1/G2） | Not connected (G1/G2) |
| `chat.sample.notice` | 样例模式：业务操作未接入，不会产生真实结果。 | Sample mode: business actions are not connected and produce no real result. |
| `chat.notice.send` | 样例模式：消息发送未接入，不会生成回复。 | Sample mode: sending is not connected; no reply will be generated. |
| `chat.notice.stop` | 样例模式：停止生成未接入。 | Sample mode: stop is not connected. |
| `chat.notice.export` | 样例模式：导出未接入。 | Sample mode: export is not connected. |
| `chat.notice.project` | 样例模式：项目切换未接入。 | Sample mode: project switching is not connected. |
| `chat.notice.history` | 样例模式：历史会话操作未接入。 | Sample mode: history actions are not connected. |
| `chat.notice.attachment` | 样例模式：附件仅作展示。 | Sample mode: attachments are display-only. |
| `chat.notice.clarification` | 样例模式：澄清提交未接入。 | Sample mode: clarification submit is not connected. |
| `chat.notice.approval` | 样例模式：审批操作未接入。 | Sample mode: approval is not connected. |
| `chat.notice.rail` | 样例模式：面板操作未接入。 | Sample mode: panel actions are not connected. |
| `chat.notice.mention` | 样例模式：引用仅作展示。 | Sample mode: mentions are display-only. |
| `chat.notice.copy` | 样例模式：复制未接入。 | Sample mode: copy is not connected. |
| `chat.status.generating` | 生成中 | Generating |
| `chat.status.paused` | 已暂停，等待你的确认 · 已生成 {tokens} tokens | Paused pending your approval · {tokens} tokens generated |
| `chat.status.done` | 已完成 | Done |
| `chat.status.tokens` | 完成 · 本轮 1,284 tokens · 9.6 秒 | Done · 1,284 tokens · 9.6s |
| `chat.message.copy` | 复制 | Copy |
| `chat.message.regenerate` | 重新生成 | Regenerate |
| `chat.message.artifacts` | 本轮输出物 · {count} | Turn artifacts · {count} |
| `chat.message.openArtifacts` | 输出物 | Artifacts |
| `chat.message.cite` | 引用 {num} | Source {num} |
| `chat.thinking.toggle` | 展开或收起思考 | Toggle thinking |
| `chat.tool.toggle` | 展开或收起工具结果 | Toggle tool output |
| `chat.tool.pending` | 待确认 | Pending |
| `chat.tool.failed` | 工具失败 | Tool failed |
| `chat.clarification.kicker` | THIL · 内容确认 | THIL · Content decision |
| `chat.clarification.view` | 查看 3 个方案 | View 3 options |
| `chat.confirm.title` | 需要确认：写入 1 个文件 | Approval needed: write 1 file |
| `chat.confirm.risk` | 中风险 | Medium risk |
| `chat.confirm.desc` | Agent 想要在仓库中创建 docs/migration/v1-to-v2-plan.md。该目录当前为空，不会覆盖已有内容；操作可通过 git checkout 撤销。 | The agent wants to create docs/migration/v1-to-v2-plan.md. The folder is empty, so nothing is overwritten; the change can be undone with git checkout. |
| `chat.confirm.deny` | 拒绝 | Deny |
| `chat.confirm.always` | 总是允许 | Always allow |
| `chat.confirm.once` | 允许本次 | Allow once |
| `chat.confirm.hint` | 本次会话内有效 · 可撤销 | Valid for this chat · reversible |
| `chat.state.detail` | 查看详情 | View details |
| `chat.state.login` | 重新登录 | Sign in again |
| `chat.state.compress` | 压缩并继续 | Compress and continue |
| `chat.state.branch` | 新建分支 | Branch |
| `chat.state.continue` | 继续 | Continue |
| `chat.state.retry` | 重新检测 | Retry |
| `chat.history.title` | 历史会话 | History |
| `chat.history.new` | 新建对话 | New chat |
| `chat.history.search` | 搜索会话或项目… | Search chats or projects… |
| `chat.history.projects` | 项目会话 | Project chats |
| `chat.history.personal` | 普通会话 | Personal chats |
| `chat.history.loadMore` | 继续滚动加载 | Scroll to load more |
| `chat.history.loadedAll` | 已加载全部会话 | All chats loaded |
| `chat.history.noMatches` | 没有匹配的会话 | No matching chats |
| `chat.history.empty` | 尚未接入会话 | No chats connected yet |
| `chat.history.sessions` | {count} 个会话 | {count} chats |
| `chat.history.actions` | 会话操作 | Chat actions |
| `chat.history.pin` | 置顶 / 取消置顶 | Pin / Unpin |
| `chat.history.rename` | 重命名 | Rename |
| `chat.history.archive` | 归档 | Archive |
| `chat.history.delete` | 删除 | Delete |
| `chat.history.open` | 打开会话 | Open chat |
| `chat.composer.placeholder` | 继续追问，或 @ 引用文件与 Agent… | Ask a follow-up, or @ mention files and agents… |
| `chat.composer.normalPlaceholder` | 接入运行时后可发送消息 | Chat is unavailable until a runtime is connected |
| `chat.composer.project.current` | 当前项目 | Current project |
| `chat.composer.project.none` | 无项目 | No project |
| `chat.composer.project.change` | 更换项目目录 | Change project folder |
| `chat.composer.project.changeDesc` | 选择现有项目或新的本地目录 | Choose an existing project or a local folder |
| `chat.composer.project.detach` | 取消项目 | Detach project |
| `chat.composer.project.detachDesc` | 会话保留并移入普通会话 | Keep the chat and move it to personal chats |
| `chat.composer.model` | 模型 | Model |
| `chat.composer.thinking` | 思考深度 | Thinking depth |
| `chat.composer.permission` | 权限模式 | Permission mode |
| `chat.composer.thinking.off` | 思考 · 关 | Thinking · Off |
| `chat.composer.thinking.low` | 思考 · 低 | Thinking · Low |
| `chat.composer.thinking.medium` | 思考 · 中 | Thinking · Medium |
| `chat.composer.thinking.high` | 思考 · 高 | Thinking · High |
| `chat.composer.permission.readonly` | 只读 | Read only |
| `chat.composer.permission.medium` | 中风险确认 | Confirm medium risk |
| `chat.composer.permission.high` | 高风险确认 | Confirm high risk |
| `chat.composer.permission.full` | 完全控制 | Full control |
| `chat.composer.context` | 上下文 {count} | Context {count} |
| `chat.composer.add` | 添加文件、目录或 Skill | Add files, folders, or Skills |
| `chat.composer.addContext` | 添加上下文 | Add context |
| `chat.composer.files` | 文件或目录 | Files or folders |
| `chat.composer.filesDesc` | 添加本机上下文 | Add local context |
| `chat.composer.chooseFiles` | 选择文件 | Choose files |
| `chat.composer.chooseFilesDesc` | 支持多选 | Multiple selection |
| `chat.composer.chooseDir` | 选择目录 | Choose folder |
| `chat.composer.chooseDirDesc` | 添加整个文件夹 | Add an entire folder |
| `chat.composer.skill` | Skill | Skill |
| `chat.composer.skillDesc` | 调用已安装能力 | Use an installed capability |
| `chat.composer.stop` | 停止 | Stop |
| `chat.composer.send` | 发送 | Send |
| `chat.composer.sendHint` | 发送 | Send |
| `chat.composer.newlineHint` | 换行 | New line |
| `chat.composer.pending` | 1 项操作等待确认。允许后 Agent 会继续生成。 | 1 action awaits approval. The agent continues after approval. |
| `chat.composer.normalHint` | 接入运行时后可发送消息 | Connect a runtime to send messages |
| `chat.composer.remove` | 移除 {name} | Remove {name} |
| `chat.composer.mention` | 引用文件或 Agent | Mention files or agents |
| `chat.composer.mentionFiles` | 文件 | Files |
| `chat.composer.mentionAgents` | Agent | Agents |
| `chat.composer.mentionEmpty` | 没有匹配的文件或 Agent | No matching files or agents |
| `chat.rail.label` | 会话详情面板 | Chat details panel |
| `chat.rail.collapse` | 折叠右侧面板 | Collapse details panel |
| `chat.rail.expand` | 展开右侧面板 | Expand details panel |
| `chat.rail.close` | 关闭面板 | Close panel |
| `chat.rail.closeScrim` | 关闭输出物面板 | Close details overlay |
| `chat.rail.toggle` | 右侧面板 | Details panel |
| `chat.rail.browser` | 浏览器 | Browser |
| `chat.rail.artifacts` | 输出物 {count} | Artifacts {count} |
| `chat.rail.sources` | 引用 {count} | Sources {count} |
| `chat.rail.exec` | 执行 | Run |
| `chat.rail.empty` | 尚未接入会话数据 | No chat data connected yet |
| `chat.rail.browser.open` | 打开 | Open |
| `chat.rail.browser.copy` | 复制路径 | Copy path |
| `chat.rail.browser.rendered` | 渲染于 {time} | Rendered at {time} |
| `chat.rail.browser.preview` | 预览由 Agent 在本地产出，尚未写入仓库。 | Preview produced locally by the agent; not yet written. |
| `chat.rail.artifacts.heading` | 本次会话产出 · {count} | Artifacts from this chat · {count} |
| `chat.rail.artifacts.return` | 返回原始轮次 | Return to turn |
| `chat.rail.artifacts.saved` | 已保存 | Saved |
| `chat.rail.artifacts.pending` | 待确认 | Pending |
| `chat.rail.artifacts.foot` | 产出会同步到资源库 | Artifacts sync to the library |
| `chat.rail.artifacts.library` | 资源库 | Library |
| `chat.rail.artifacts.notice` | 样例模式：输出物操作未接入。 | Sample mode: artifact actions are not connected. |
| `chat.rail.sources.heading` | 引用来源 · {count} | Sources · {count} |
| `chat.rail.sources.foot` | 引用来自 {count} 个来源 | Citations from {count} sources |
| `chat.rail.sources.export` | 导出 | Export |
| `chat.rail.exec.heading` | 本轮执行 · {count} 步 | This run · {count} steps |
| `chat.rail.exec.usage` | 本会话用量 | Chat usage |
| `chat.rail.exec.input` | 输入 | Input |
| `chat.rail.exec.output` | 输出 | Output |
| `chat.rail.exec.cache` | 缓存命中 | Cache hits |
| `chat.rail.exec.cost` | 预估成本 | Estimated cost |
| `chat.rail.exec.done` | 完成 | Done |
| `chat.rail.exec.pending` | 待确认 | Pending |
| `chat.rail.exec.paused` | 已暂停 | Paused |
| `chat.project.title` | 为当前对话选择项目 | Choose a project for this chat |
| `chat.project.desc` | 项目会绑定本地目录，并将会话归入侧栏中的项目分组。 | A project binds a local folder and groups related chats in the sidebar. |
| `chat.project.current` | 当前项目 | Current project |
| `chat.project.opened` | 已打开 | Open |
| `chat.project.recent` | 最近项目 | Recent projects |
| `chat.project.openLocal` | 打开本地项目 | Open local project |
| `chat.project.openLocalDesc` | 选择已有目录并开始对话 | Choose an existing folder and start chatting |
| `chat.project.new` | 新建项目 | New project |
| `chat.project.newDesc` | 选择目录并以文件夹名创建 | Choose a folder and create from its name |
| `chat.project.security` | 选择目录只建立本地引用；Agent 仍受当前会话的读取与写入权限约束。 | Choosing a folder creates a local reference only. Agent read and write permissions still apply. |
| `chat.project.cancel` | 取消 | Cancel |
| `chat.project.continue` | 在此项目中继续 | Continue in project |
| `chat.project.sessions` | {count} 个会话 | {count} chats |
| `chat.project.notice` | 样例模式：项目选择未接入。 | Sample mode: project selection is not connected. |
| `chat.export.title` | 导出会话 | Export chat |
| `chat.export.desc` | 默认在本机生成快照，不上传会话、文件路径或工具输出。 | Create a local snapshot by default. No chat, file path, or tool output is uploaded. |
| `chat.export.format` | 导出格式 | Export format |
| `chat.export.html` | HTML · 可交互快照 | HTML · interactive snapshot |
| `chat.export.markdown` | Markdown · 纯文本 | Markdown · plain text |
| `chat.export.json` | JSON · 完整记录 | JSON · full record |
| `chat.export.includeThinking` | 包含思考摘要 | Include thinking summaries |
| `chat.export.includeThinkingDesc` | 仅导出折叠后的摘要，不包含隐藏推理 | Exports collapsed summaries only, never hidden reasoning |
| `chat.export.includeToolOutput` | 包含工具原始输出 | Include raw tool output |
| `chat.export.includeToolOutputDesc` | 可能包含本地路径、命令参数和内部链接 | May contain local paths, command arguments, and internal links |
| `chat.export.cloudShare` | 创建云端只读链接 | Create a cloud read-only link |
| `chat.export.optional` | 可选 | Optional |
| `chat.export.cloudShareDesc` | 启用后会将经过脱敏的会话副本上传到分享服务。默认 7 天失效，可随时撤销。 | Uploads a redacted copy to the sharing service. It expires after 7 days by default and can be revoked. |
| `chat.export.confirmUpload` | 我确认上传脱敏后的会话副本 | I confirm uploading a redacted chat copy |
| `chat.export.createLink` | 创建链接 | Create link |
| `chat.export.cancel` | 取消 | Cancel |
| `chat.export.local` | 导出到本机 | Export locally |
| `chat.export.notice` | 样例模式：导出未接入。 | Sample mode: export is not connected. |
| `chat.thil.title` | 灰度方案需要你做一个业务判断 | Choose a rollout approach |
| `chat.thil.desc` | Agent 已完成技术分析，但仓库中没有流量分组规则。请选择迁移计划采用的灰度口径。 | The technical analysis is complete, but the repository has no traffic segmentation rules. Choose how the migration plan should roll out. |
| `chat.thil.choose` | 选择一个方案 | Choose an option |
| `chat.thil.option.tenant` | 按租户白名单 | Tenant allowlist |
| `chat.thil.option.tenantDesc` | 先选择 3–5 个内部租户，最容易回滚和对账 | Start with 3–5 internal tenants for the easiest rollback and reconciliation |
| `chat.thil.option.traffic` | 按 10% 随机流量 | 10% random traffic |
| `chat.thil.option.trafficDesc` | 覆盖更广，但需要稳定的用户分桶与指标看板 | Broader coverage, but requires stable user bucketing and dashboards |
| `chat.thil.option.environment` | 先仅在预发布环境验证 | Staging only |
| `chat.thil.option.environmentDesc` | 不进入生产流量，计划中保留生产灰度为待定项 | No production traffic; production rollout stays pending in the plan |
| `chat.thil.note` | 补充约束（可选） | Additional constraints (optional) |
| `chat.thil.placeholder` | 例如：支付链路不能进入第一批；错误率超过 0.5% 自动回滚 | Example: exclude payments from the first cohort; roll back above 0.5% errors |
| `chat.thil.scope` | 这只确认任务内容，不会授予文件、终端或外部连接器权限；相关操作仍会单独请求授权。 | This confirms task content only. File, terminal, and connector permissions remain separate. |
| `chat.thil.later` | 稍后处理 | Later |
| `chat.thil.continue` | 确认并继续 | Confirm and continue |
| `chat.thil.radioLabel` | 灰度方案 | Rollout options |
| `chat.thil.notice` | 样例模式：澄清提交未接入。 | Sample mode: clarification submit is not connected. |
| `chat.thread.title` | 会话操作 | Chat actions |
| `chat.thread.branch` | 从这条消息分叉新会话 | Branch from this message |
| `chat.thread.archive` | 归档到资源库 | Archive to library |
| `chat.thread.export` | 导出为 Markdown | Export as Markdown |
| `chat.thread.delete` | 删除会话 | Delete chat |
| `chat.thread.close` | 关闭 | Close |
| `chat.dialog.close` | 关闭 | Close |
| `chat.dialog.cancel` | 取消 | Cancel |
| `sample.nav.chat` | 对话 {count} | Chats {count} |
| `sample.nav.libraryMeta` | {count} 个输出物 | {count} artifacts |
| `sample.nav.skillsMeta` | {count} 个已安装 | {count} installed |
| `sample.nav.mcpMeta` | {count} 个服务 | {count} services |
| `sample.nav.connectorsMeta` | {count} 个连接 | {count} connections |
| `sample.nav.schedulesMeta` | {count} 个任务 | {count} tasks |
| `agents.title` | Agent 管理 | Agent management |
| `agents.description` | 配置主 Agent 与子 Agent 的模型、system prompt、可用工具与权限边界。子 Agent 继承主 Agent 的权限上限。 | Configure models, system prompts, tools, and permission boundaries for main and sub-agents. Sub-agents inherit the main agent's permission ceiling. |
| `agents.import` | 导入 | Import |
| `agents.new` | 新建 Agent | New Agent |
| `agents.newSub` | 新建子 Agent | New sub-agent |
| `agents.addSub` | 添加子 Agent | Add sub-agent |
| `agents.searchPlaceholder` | 搜索 Agent… | Search agents… |
| `agents.searchLabel` | 搜索 Agent | Search agents |
| `agents.emptyList` | 尚未接入 Agent | No agents connected yet |
| `agents.emptyListBody` | 运行时接入后会在这里显示主 Agent 与子 Agent。 | Main and sub-agents will appear here after a runtime is connected. |
| `agents.emptyDetail` | 尚未接入 Agent 详情 | No agent details connected yet |
| `agents.emptyDetailBody` | 接入运行时并选择 Agent 后，可在这里查看配置。 | Connect a runtime and select an agent to inspect its configuration. |
| `agents.sampleBanner` | 原型样例数据 · 仅用于设计对照，不是真实状态 | Prototype sample data · design reference only, not a real state |
| `agents.action.unavailable` | 未接入（G1） | Not connected (G1) |
| `agents.notice.save` | 样例模式：保存未接入。 | Sample mode: save is not connected. |
| `agents.notice.chat` | 样例模式：发起对话未接入。 | Sample mode: starting a chat is not connected. |
| `agents.notice.create` | 样例模式：创建未接入。 | Sample mode: creation is not connected. |
| `agents.notice.import` | 样例模式：导入未接入。 | Sample mode: import is not connected. |
| `agents.notice.toggle` | 样例模式：设置切换未接入。 | Sample mode: toggling settings is not connected. |
| `agents.notice.variable` | 样例模式：变量插入未接入。 | Sample mode: variable insertion is not connected. |
| `agents.group.main` | 主 Agent · {count} | Main agents · {count} |
| `agents.group.sub` | 子 Agent · {count} | Sub-agents · {count} |
| `agents.status.ready` | 就绪 | Ready |
| `agents.subBadge` | 子 | Sub |
| `agents.subReadonly` | 只读 | Read only |
| `agents.subDelegated` | 委派自 {name} | Delegated by {name} |
| `agents.skills` | {count} skills | {count} skills |
| `agents.listMeta` | {model} · {count} skills | {model} · {count} skills |
| `agents.listNoMatch` | 没有匹配的 Agent | No matching agents |
| `agents.tab.basic` | 基础配置 | Basic configuration |
| `agents.tab.prompt` | 指令与工具 | Instructions & tools |
| `agents.tab.sub` | 子 Agent {count} | Sub-agents {count} |
| `agents.tab.perm` | 权限边界 | Permission boundaries |
| `agents.tab.runs` | 运行记录 | Run history |
| `agents.startChat` | 发起对话 | Start chat |
| `agents.save` | 保存 | Save |
| `agents.backToList` | 返回 Agent 列表 | Back to agent list |
| `agents.permissionChip` | 权限 · {level} | Permission · {level} |
| `agents.form.name` | 名称 | Name |
| `agents.form.slug` | 标识 | Identifier |
| `agents.form.description` | 简介 | Description |
| `agents.form.runtime` | Agent 运行时 | Agent runtime |
| `agents.form.model` | 运行时模型 | Runtime model |
| `agents.form.thinking` | 思考深度 | Thinking depth |
| `agents.form.context` | 上下文窗口 | Context window |
| `agents.form.memory` | 跨会话记忆 | Cross-chat memory |
| `agents.form.memoryDesc` | 在新会话中自动带入此 Agent 的历史结论摘要。 | Carry this agent's prior conclusions into new chats. |
| `agents.form.autoCompact` | 自动压缩上下文 | Auto-compact context |
| `agents.form.autoCompactDesc` | 超过 80% 上下文窗口时，自动摘要早期轮次。 | Summarize early turns automatically above 80% of the context window. |
| `agents.form.runtimeHint` | 运行时决定可用工具协议、模型与子 Agent 能力。 | The runtime determines tool protocols, models, and sub-agent capabilities. |
| `agents.form.modelHint` | 显示 {runtime} 当前可用的模型。 | Shows models currently available from {runtime}. |
| `agents.form.thinkingHint` | 深度思考会增加延迟与 token 消耗。 | Deeper thinking increases latency and token use. |
| `agents.form.contextHint` | 上限同时受运行时与模型能力约束。 | The limit is constrained by both the runtime and the model. |
| `agents.form.unavailable` | 未接入 | Not connected |
| `agents.form.modelUnavailableHint` | 接入运行时后显示可用模型。 | Available models appear after a runtime is connected. |
| `agents.thinking.off` | 关闭 | Off |
| `agents.thinking.low` | 低 | Low |
| `agents.thinking.medium` | 中 | Medium |
| `agents.thinking.high` | 高 | High |
| `agents.runtime.pi` | Pi Agent | Pi Agent |
| `agents.runtime.codex` | Codex CLI | Codex CLI |
| `agents.runtime.claude` | Claude Code | Claude Code |
| `agents.runtime.piOption` | Pi Agent · 默认 | Pi Agent · default |
| `agents.runtime.codexOption` | Codex CLI | Codex CLI |
| `agents.runtime.claudeOption` | Claude Code | Claude Code |
| `agents.validation.nameRequired` | 请填写 Agent 名称 | Enter an Agent name |
| `agents.validation.slugRequired` | 请填写标识 | Enter an identifier |
| `agents.validation.slugInvalid` | 标识只能包含小写字母、数字和连字符 | Use only lowercase letters, numbers, and hyphens |
| `agents.prompt.system` | System prompt | System prompt |
| `agents.prompt.variables` | 变量 | Variables |
| `agents.prompt.tools` | 可用工具 | Available tools |
| `agents.prompt.tool` | 工具 | Tool |
| `agents.prompt.desc` | 说明 | Description |
| `agents.prompt.risk` | 风险 | Risk |
| `agents.prompt.enabled` | 启用 | Enabled |
| `agents.prompt.skillCount` | 挂载 Skill · {count} | Mounted Skills · {count} |
| `agents.risk.low` | 低 | Low |
| `agents.risk.medium` | 中 | Medium |
| `agents.risk.high` | 高 | High |
| `agents.tool.read.desc` | 读取仓库文件 | Read repository files |
| `agents.tool.search.desc` | 按模式检索调用点 | Find call sites by pattern |
| `agents.tool.write.desc` | 创建或修改文件 | Create or change files |
| `agents.tool.shell.desc` | 执行构建与测试命令 | Run build and test commands |
| `agents.tool.push.desc` | 推送到远端分支 | Push to a remote branch |
| `agents.skill.review` | 代码审查 | Code review |
| `agents.skill.reviewDesc` | 按 diff 出具风险分级 | Grade risks from the diff |
| `agents.skill.deps` | 依赖扫描 | Dependency scan |
| `agents.skill.depsDesc` | 识别已知漏洞与许可冲突 | Find known vulnerabilities and licence conflicts |
| `agents.skill.docs` | 文档写作 | Documentation |
| `agents.skill.docsDesc` | 统一标题层级与语气 | Unify heading levels and tone |
| `agents.skill.research` | 竞品调研 | Competitor research |
| `agents.skill.researchDesc` | 需要浏览器权限 | Requires browser permission |
| `agents.sub.notice` | 子 Agent 继承主 Agent 的权限上限：即使子 Agent 配置了「完全控制」，实际仍受主 Agent 的中风险确认约束。 | Sub-agents inherit the main agent's permission ceiling: even with full control configured, the main agent's medium-risk confirmation still applies. |
| `agents.sub.table.agent` | 子 Agent | Sub-agent |
| `agents.sub.table.runtime` | 运行时 | Runtime |
| `agents.sub.table.model` | 模型 | Model |
| `agents.sub.table.condition` | 委派条件 | Delegation condition |
| `agents.sub.table.concurrency` | 并发 | Concurrency |
| `agents.sub.table.enabled` | 启用 | Enabled |
| `agents.sub.condition.audit` | diff > 200 行或涉及鉴权 | diff > 200 lines or authentication changes |
| `agents.sub.condition.tests` | 改动命中测试覆盖目录 | Changes touch test-covered directories |
| `agents.sub.policy` | 委派策略 | Delegation policy |
| `agents.sub.policyHint` | 自动委派会并行消耗 token，可在观测页查看分支用量。 | Automatic delegation consumes tokens in parallel; branch usage is visible in observability. |
| `agents.sub.policy.auto` | 按条件自动委派 | Delegate automatically by condition |
| `agents.sub.policy.explicit` | 仅在我明确要求时 | Only when I ask explicitly |
| `agents.sub.policy.never` | 禁止委派 | Never delegate |
| `agents.sub.concurrency` | 全局并发上限 | Global concurrency limit |
| `agents.sub.concurrencyHint` | 超过上限时，委派任务排队执行。 | Delegated work queues above this limit. |
| `agents.sub.new` | 新建子 Agent | New sub-agent |
| `agents.perm.mode` | 权限模式 | Permission mode |
| `agents.perm.readonly` | 只读 | Read only |
| `agents.perm.readonlyDesc` | 仅读取与检索，不产生任何写入 | Read and search only; never write |
| `agents.perm.medium` | 中风险确认 | Confirm medium risk |
| `agents.perm.mediumDesc` | 写入、执行命令需逐次确认（当前） | Writes and commands require confirmation each time (current) |
| `agents.perm.high` | 高风险确认 | Confirm high risk |
| `agents.perm.highDesc` | 仅推送、删除、外发数据时确认 | Confirm only pushes, deletions, and external sends |
| `agents.perm.full` | 完全控制 | Full control |
| `agents.perm.fullDesc` | 不再确认，权限等同当前登录用户 | No more confirmations; equal to the signed-in user |
| `agents.perm.writable` | 可写目录 | Writable folders |
| `agents.perm.writableHint` | 留空表示整个工作区可写。支持 glob。 | Leave empty to allow the whole workspace. Glob patterns are supported. |
| `agents.perm.allowlist` | 命令白名单 | Command allowlist |
| `agents.perm.network` | 允许访问网络 | Allow network access |
| `agents.perm.networkDesc` | 关闭后，http_fetch 与浏览器工具将被禁用。 | When off, http_fetch and browser tools are disabled. |
| `agents.perm.confirm` | 高危操作二次确认 | Confirm high-risk actions |
| `agents.perm.confirmDesc` | 推送、删除分支、外发邮件时，强制要求输入确认短语。 | Require a confirmation phrase for pushes, branch deletion, and external email. |
| `agents.runs.time` | 时间 | Time |
| `agents.runs.chat` | 会话 | Chat |
| `agents.runs.turns` | 轮次 | Turns |
| `agents.runs.tools` | 工具调用 | Tool calls |
| `agents.runs.tokens` | Token | Tokens |
| `agents.runs.status` | 状态 | Status |
| `agents.runs.pending` | 待确认 | Pending |
| `agents.runs.done` | 完成 | Done |
| `agents.runs.archived` | 已归档 | Archived |
| `agents.dialog.newTitle` | 新建 Agent | New Agent |
| `agents.dialog.newDesc` | 创建后可继续配置工具、Skill 与子 Agent。 | Configure tools, Skills, and sub-agents after creation. |
| `agents.dialog.newType` | 类型 | Type |
| `agents.dialog.newTypeMain` | 主 Agent | Main agent |
| `agents.dialog.newTypeSub` | 子 Agent（由主 Agent 委派） | Sub-agent (delegated by a main agent) |
| `agents.dialog.newRuntime` | Agent 运行时 | Agent runtime |
| `agents.dialog.newModel` | 运行时模型 | Runtime model |
| `agents.dialog.newNamePlaceholder` | 例如：发布检查员 | For example: Release checker |
| `agents.dialog.newDescPlaceholder` | 一句话说明这个 Agent 负责什么 | Describe this agent in one sentence |
| `agents.dialog.cancel` | 取消 | Cancel |
| `agents.dialog.create` | 创建 | Create |
| `agents.dialog.importTitle` | 导入 Agent | Import Agent |
| `agents.dialog.importDesc` | 支持 Anvil 导出的 JSON，或包含 agent.yaml 的目录。 | Supports Anvil JSON exports or a folder containing agent.yaml. |
| `agents.dialog.chooseFile` | 选择文件 | Choose file |
| `agents.dialog.importNotice` | 导入会覆盖同名 Agent 的工具配置，但不会改动权限边界。 | Import overwrites tool configuration for same-named agents but never permission boundaries. |
| `agents.dialog.importAction` | 导入 | Import |
| `agents.dialog.close` | 关闭 | Close |
| `agents.validation.fileRequired` | 请选择要导入的文件 | Choose a file to import |
| `skills.title` | Skill | Skills |
| `skills.description` | Skill 是可复用的能力包，包含指令、触发条件与可选脚本。Agent 在命中触发条件时自动调用，也可以被对话显式唤起。 | Skills are reusable capability packages containing instructions, triggers, and optional scripts. Agents call them when a trigger matches, or a chat can invoke them explicitly. |
| `skills.import` | 导入 Skill | Import Skill |
| `skills.new` | 新建 Skill | New Skill |
| `skills.sampleBanner` | 原型样例数据 · 仅用于设计对照，不是真实状态 | Prototype sample data · design reference only, not a real state |
| `skills.tab.installed` | 已安装 | Installed |
| `skills.tab.installedCount` | 已安装 {count} | Installed {count} |
| `skills.tab.market` | 市场 | Market |
| `skills.tab.drafts` | 草稿 | Drafts |
| `skills.tab.draftsCount` | 草稿 {count} | Drafts {count} |
| `skills.searchPlaceholder` | 按名称或触发条件搜索 | Search by name or trigger |
| `skills.filter.source` | 全部来源 | All sources |
| `skills.filter.agent` | 全部 Agent | All agents |
| `skills.toolbar.summary` | {count} 个已安装 · 上次同步 2 小时前 | {count} installed · synced 2 hours ago |
| `skills.emptyList` | 尚未接入 Skill | No Skills connected yet |
| `skills.emptyListBody` | Skill 数据源接入后会在这里显示。 | Skills will appear here after a data source is connected. |
| `skills.emptyDetail` | 尚未接入 Skill 详情 | No Skill details connected yet |
| `skills.emptyDetailBody` | 接入并选择一个 Skill 后，可在这里查看概览、工作流与测试。 | Connect a data source and select a Skill to inspect its overview, workflow, and tests. |
| `skills.listNoMatch` | 没有匹配的 Skill | No matching Skills |
| `skills.source.builtin` | 内置 | Built-in |
| `skills.source.local` | 本地 | Local |
| `skills.source.market` | 市场 | Market |
| `skills.source.official` | 官方 | Official |
| `skills.source.community` | 社区 | Community |
| `skills.meta` | {version} · {source} | {version} · {source} |
| `skills.calls` | 调用 {count} 次 | {count} calls |
| `skills.updated` | {time} | {time} |
| `skills.installs` | 安装 {count} | {count} installs |
| `skills.status.draft` | 草稿 | Draft |
| `skills.status.unsaved` | 未保存 | Unsaved |
| `skills.status.needsTest` | 待测试 | Needs test |
| `skills.action.unavailable` | 未接入（G1） | Not connected (G1) |
| `skills.notice.save` | 样例模式：保存未接入。 | Sample mode: save is not connected. |
| `skills.notice.enable` | 样例模式：启用未接入。 | Sample mode: enabling is not connected. |
| `skills.notice.disable` | 样例模式：停用未接入。 | Sample mode: disabling is not connected. |
| `skills.notice.test` | 样例模式：试运行未接入。 | Sample mode: test runs are not connected. |
| `skills.notice.publish` | 样例模式：发布未接入。 | Sample mode: publishing is not connected. |
| `skills.notice.install` | 样例模式：安装未接入。 | Sample mode: install is not connected. |
| `skills.notice.import` | 样例模式：导入未接入。 | Sample mode: import is not connected. |
| `skills.notice.edit` | 样例模式：编辑未接入。 | Sample mode: editing is not connected. |
| `skills.notice.toggle` | 样例模式：设置切换未接入。 | Sample mode: toggling settings is not connected. |
| `skills.notice.continue` | 样例模式：继续编辑未接入。 | Sample mode: continue editing is not connected. |
| `skills.notice.auth` | 样例模式：连接器授权未接入。 | Sample mode: connector authorization is not connected. |
| `skills.detail.save` | 保存 | Save |
| `skills.detail.backToList` | 返回 Skill 列表 | Back to Skill list |
| `skills.more` | 更多操作 | More actions |
| `skills.detail.overview` | 概览 | Overview |
| `skills.detail.version` | 版本 {version} | Version {version} |
| `skills.detail.successRate` | 成功率 | Success rate |
| `skills.detail.recentRun` | 最近运行 | Last run |
| `skills.detail.enable` | 启用 Skill | Enable Skill |
| `skills.workflow.title` | 工作流步骤 | Workflow steps |
| `skills.workflow.stepCount` | {count} 步 | {count} steps |
| `skills.workflow.done` | 已完成 | Done |
| `skills.workflow.ready` | 可执行 | Ready |
| `skills.workflow.waiting` | 等待确认 | Waiting |
| `skills.workflow.tools` | 所需工具/资源 | Tools/resources |
| `skills.workflow.toggle` | 展开或收起步骤 | Toggle step |
| `skills.trigger.title` | 触发与资源 | Triggers & resources |
| `skills.trigger.conditions` | 触发条件 | Trigger conditions |
| `skills.trigger.schedule` | 定时触发 | Scheduled trigger |
| `skills.trigger.resources` | 依赖与资源 | Dependencies & resources |
| `skills.resource.sentry` | Sentry 连接器 | Sentry connector |
| `skills.resource.sentryDesc` | 读取今日告警与处理状态 | Read today's alerts and resolution state |
| `skills.resource.dingtalk` | 钉钉连接器 | DingTalk connector |
| `skills.resource.dingtalkDesc` | 读取值班群处理记录 | Read on-call group resolution records |
| `skills.resource.library` | 资源库 | Library |
| `skills.resource.libraryDesc` | 写入 Markdown 交接单 | Write the Markdown handoff |
| `skills.resource.terminal` | 终端脚本 | Terminal script |
| `skills.resource.terminalDesc` | 当前流程不需要 | Not needed for this flow |
| `skills.test.title` | 测试与发布 | Test & publish |
| `skills.test.description` | Agent 会在隔离会话中跑一遍示例输入，不会发送消息或写入正式资源库。 | The agent runs the sample input in an isolated chat; no message is sent and no production resource is written. |
| `skills.test.input` | 试运行输入 | Test input |
| `skills.test.run` | 运行测试 | Run test |
| `skills.test.fail` | 模拟失败 | Simulate failure |
| `skills.test.checks` | 检查项 | Checks |
| `skills.test.structure` | SKILL.md 结构完整 | SKILL.md structure is valid |
| `skills.test.triggerable` | 触发条件可判定 | Trigger conditions are decidable |
| `skills.test.dependencies` | 依赖均已授权 | Dependencies are authorized |
| `skills.test.awaiting` | 等待试运行输出 | Awaiting test output |
| `skills.test.running` | 运行中… | Running… |
| `skills.test.success` | 试运行成功 | Test succeeded |
| `skills.test.failure` | 试运行失败 | Test failed |
| `skills.test.enableAfter` | 试运行后启用 | Enable after test |
| `skills.test.defaultAgent` | 发布后默认只对「文档助手」可用，可在 Agent 配置中挂载到其它 Agent。 | After publishing, this Skill is available only to Documentation agent by default; mount it on other agents from their configuration. |
| `skills.import.title` | 导入 Skill | Import Skill |
| `skills.import.description` | 粘贴仓库或 SKILL.md 链接，Pi Agent 会拉取、检查依赖并在本地安装；也可从文件导入。 | Paste a repository or SKILL.md URL; Pi Agent fetches it, checks dependencies, and installs locally. You can also import a file. |
| `skills.import.urlTab` | 从链接 | From URL |
| `skills.import.fileTab` | 从文件 | From file |
| `skills.import.url` | Skill 链接 | Skill URL |
| `skills.import.urlPlaceholder` | https://github.com/owner/repo/tree/main/my-skill | https://github.com/owner/repo/tree/main/my-skill |
| `skills.import.analyze` | 交给 Agent | Hand to agent |
| `skills.import.urlHint` | 支持 GitHub/GitLab 仓库、目录链接、原始 SKILL.md URL 与 git 地址。 | Supports GitHub/GitLab repositories, directory links, raw SKILL.md URLs, and git addresses. |
| `skills.import.file` | 选择文件 | Choose file |
| `skills.import.fileHint` | 支持 SKILL.md、ZIP 或包含 assets/ 与 scripts/ 的目录包。 | Supports SKILL.md, ZIP, or directories containing assets/ and scripts/. |
| `skills.import.notice` | 链接内容视为不可信输入。Agent 只解析 Skill 清单，不会执行仓库中的安装说明或脚本，直到你确认依赖与权限。 | Link contents are untrusted. The agent only parses the Skill manifest and does not run repository instructions or scripts until dependencies and permissions are confirmed. |
| `skills.import.cancel` | 取消 | Cancel |
| `skills.import.action` | 导入 | Import |
| `skills.import.close` | 关闭 | Close |
| `skills.validation.urlRequired` | 请填写 Skill 链接 | Enter a Skill URL |
| `skills.validation.urlInvalid` | 请输入有效的 http/https 链接 | Enter a valid http/https URL |
| `skills.validation.fileRequired` | 请选择要导入的文件 | Choose a file to import |
| `skills.builder.title` | 创建可复用能力 | Create reusable capability |
| `skills.builder.description` | 先描述目标，Agent 会生成结构；只有在需要时才展开触发、工具与测试配置。 | Describe the goal first; the agent generates structure and only asks for triggers, tools, and tests when needed. |
| `skills.builder.kicker` | 标准 Skill 创建器 | Standard Skill builder |
| `skills.builder.step.basics` | 基础信息 | Basic information |
| `skills.builder.step.basicsDesc` | 名称与说明 | Name and description |
| `skills.builder.step.triggers` | 触发与资源 | Triggers & resources |
| `skills.builder.step.triggersDesc` | 按需展开 | Configure as needed |
| `skills.builder.step.test` | 测试与发布 | Test & publish |
| `skills.builder.step.testDesc` | 试运行后启用 | Enable after test |
| `skills.builder.progress` | 第 {current} / {total} 步 | Step {current} / {total} |
| `skills.builder.name` | 名称 | Name |
| `skills.builder.slug` | 标识 | Identifier |
| `skills.builder.version` | 版本 | Version |
| `skills.builder.descriptionLabel` | 用途说明 | Description |
| `skills.builder.triggerLabel` | 触发条件 | Trigger conditions |
| `skills.builder.triggerPlaceholder` | 例如：用户提到「交接」或每日 18:00 | For example: the user mentions handoff, or daily at 18:00 |
| `skills.builder.semantic` | 语义条件 | Semantic conditions |
| `skills.builder.schedule` | 定时触发 | Scheduled trigger |
| `skills.builder.next` | 继续 | Continue |
| `skills.builder.prev` | 上一步 | Previous |
| `skills.builder.publish` | 发布 Skill | Publish Skill |
| `skills.builder.cancel` | 取消 | Cancel |
| `skills.builder.saveDraft` | 存为草稿 | Save draft |
| `skills.validation.nameRequired` | 请填写 Skill 名称 | Enter a Skill name |
| `skills.validation.slugRequired` | 请填写标识 | Enter an identifier |
| `skills.validation.slugInvalid` | 标识只能包含小写字母、数字和连字符 | Use only lowercase letters, numbers, and hyphens |
| `skills.validation.versionRequired` | 请填写版本号 | Enter a version |
| `skills.validation.versionInvalid` | 版本格式应为 v1.2.3 | Use a version like v1.2.3 |
| `skills.validation.triggerRequired` | 请填写至少一个触发条件 | Enter at least one trigger condition |
| `skills.card.open` | 打开 {name} | Open {name} |
| `skills.card.menu` | 更多操作：{name} | More actions: {name} |
| `skills.card.install` | 获取 | Get |
| `skills.card.continue` | 继续编辑 | Continue editing |
| `skills.footnote` | Skill 在本机沙箱内执行，脚本只能访问被授权的目录与网络域名。修改系统级 Skill 需具备管理员角色。 | Skills run in a local sandbox; scripts can only reach authorized folders and network domains. Changing system Skills requires an administrator role. |
| `skills.builder.step.intent` | 描述意图 | Describe intent |
| `skills.builder.step.intentDesc` | 与 AI 一起定义 | Define with the AI |
| `skills.builder.intentTitle` | 你希望 Agent 学会什么？ | What should the agent learn? |
| `skills.builder.intentDesc` | 用自然语言描述真实任务，Agent 会追问缺失条件并生成 Skill 草稿。 | Describe a real task in natural language; the agent asks for missing details and creates a draft Skill. |
| `skills.builder.intentLabel` | 任务描述 | Task description |
| `skills.builder.intentPlaceholder` | 例如：每天交接前，把告警、处理动作和遗留问题整理成交接单。 | Example: before each shift handoff, collect alerts, actions, and open issues into a handoff report. |
| `skills.builder.workflow` | 工作流步骤 | Workflow steps |
| `mcp.title` | MCP 服务 | MCP services |
| `mcp.description` | 通过 Model Context Protocol 把本地文件、数据库与浏览器接进 Agent。服务声明的每个工具都会出现在 Agent 的工具列表里，调用前按权限策略校验。 | Connect local files, databases, and browsers to agents through the Model Context Protocol. Every tool a server exposes appears in the agent tool list and is checked against the permission policy before use. |
| `mcp.reconnect` | 重连全部 | Reconnect all |
| `mcp.add` | 添加服务 | Add server |
| `mcp.sampleBanner` | 原型样例数据 · 仅用于设计对照，不是真实状态 | Prototype sample data · design reference only, not a real state |
| `mcp.action.unavailable` | 未接入（G1） | Not connected (G1) |
| `mcp.stats.connected` | 已连接服务 | Connected servers |
| `mcp.stats.connectedSub` | 1 个未连接 | 1 disconnected |
| `mcp.stats.tools` | 暴露工具 | Exposed tools |
| `mcp.stats.toolsSub` | 本周新增 4 | 4 added this week |
| `mcp.stats.calls` | 今日调用 | Calls today |
| `mcp.stats.callsSub` | 失败 3 · 成功率 98.6% | 3 failed · 98.6% success |
| `mcp.stats.latency` | 平均延迟 | Average latency |
| `mcp.stats.latencySub` | p95 480ms | p95 480ms |
| `mcp.stats.empty` | 尚未接入 | Not connected |
| `mcp.searchPlaceholder` | 按服务名或工具名搜索 | Search by server or tool name |
| `mcp.filter.transport` | 全部传输方式 | All transports |
| `mcp.filter.transportLabel` | 传输方式 | Transport |
| `mcp.filter.status` | 全部状态 | All statuses |
| `mcp.filter.statusLabel` | 状态 | Status |
| `mcp.transport.stdio` | stdio | stdio |
| `mcp.transport.http` | HTTP / SSE | HTTP / SSE |
| `mcp.transport.websocket` | WebSocket | WebSocket |
| `mcp.status.connected` | 已连接 | Connected |
| `mcp.status.needsAuth` | 需授权 | Authorization needed |
| `mcp.status.disconnected` | 未连接 | Disconnected |
| `mcp.status.disabled` | 已停用 | Disabled |
| `mcp.configPath` | 配置文件 | Config file |
| `mcp.toolsCount` | {count} 个工具 | {count} tools |
| `mcp.toolsLabel` | 工具清单 | Tools |
| `mcp.tag.stdio` | stdio | stdio |
| `mcp.tag.readonly` | 只读 | Read only |
| `mcp.tag.http` | HTTP / SSE | HTTP / SSE |
| `mcp.tag.rowLimit` | 行级限制 5,000 | Row limit 5,000 |
| `mcp.tag.writesWeb` | 可写外部网站 | Writes external sites |
| `mcp.tag.highRisk` | 高风险 | High risk |
| `mcp.tag.oauthPending` | OAuth 待完成 | OAuth pending |
| `mcp.callsToday` | 今日 {count} 次调用 | {count} calls today |
| `mcp.lastError` | 上次错误 {time} | Last error {time} |
| `mcp.average` | 平均 {latency} | Average {latency} |
| `mcp.waitingAuth` | 等待你在对话中确认授权 | Waiting for you to approve access in chat |
| `mcp.authorize` | 授权 | Authorize |
| `mcp.goConfirm` | 去确认 | Confirm |
| `mcp.notionAuth` | 授权流程在浏览器里完成，返回后会自动拉取可用工具。 | Authorization completes in the browser; tools are fetched automatically when you return. |
| `mcp.tryFailed` | 上次尝试 · {time} 失败 | Last attempt · failed at {time} |
| `mcp.more` | 更多设置 | More settings |
| `mcp.noResults` | 没有匹配的 MCP 服务 | No matching MCP servers |
| `mcp.empty` | 尚未接入 MCP 服务 | No MCP servers connected yet |
| `mcp.emptyBody` | 接入 MCP 配置后会在这里显示服务、工具与连接状态。 | Servers, tools, and connection states will appear here after MCP configuration is connected. |
| `mcp.footnote` | stdio 服务以当前用户身份在本机运行，权限等同于你的终端。带写操作的工具默认走中风险确认；高风险工具每次调用都会重新确认。 | stdio servers run locally as the current user with your terminal's permissions. Write tools require medium-risk approval by default; high-risk tools require approval on every call. |
| `mcp.logs.title` | 连接日志 | Connection log |
| `mcp.logs.recent` | 最近 {count} 条 | Last {count} entries |
| `mcp.logs.refresh` | 刷新 | Refresh |
| `mcp.logs.empty` | 尚未接入连接日志 | No connection log yet |
| `mcp.logs.notice` | 样例模式：日志刷新未接入。 | Sample mode: log refresh is not connected. |
| `mcp.notice.reconnect` | 样例模式：重连未接入。 | Sample mode: reconnect is not connected. |
| `mcp.notice.enable` | 样例模式：启用未接入。 | Sample mode: enabling is not connected. |
| `mcp.notice.disable` | 样例模式：停用未接入。 | Sample mode: disabling is not connected. |
| `mcp.notice.settings` | 样例模式：服务设置未接入。 | Sample mode: server settings are not connected. |
| `mcp.notice.auth` | 样例模式：授权未接入。 | Sample mode: authorization is not connected. |
| `mcp.notice.test` | 样例模式：连接测试未接入。 | Sample mode: connection testing is not connected. |
| `mcp.notice.add` | 样例模式：添加服务未接入。 | Sample mode: adding a server is not connected. |
| `mcp.service.filesystemDesc` | 本地文件系统 | Local filesystem |
| `mcp.service.postgresDesc` | 只读副本 · 报表库 | Read-only replica · reporting |
| `mcp.service.playwrightDesc` | 浏览器自动化 | Browser automation |
| `mcp.service.notionDesc` | 远端托管服务 | Remote hosted service |
| `mcp.tool.readFile` | 读取工作目录内的文件，单次最多 200 KB。 | Read files inside the working folder, up to 200 KB per call. |
| `mcp.tool.listDirectory` | 列目录，隐藏 .git 与 node_modules。 | List a folder while hiding .git and node_modules. |
| `mcp.tool.searchFiles` | 按文件名与内容正则检索，返回命中行号。 | Search names and contents by regular expression and return matching lines. |
| `mcp.tool.writeFile` | 写入文件，需要中风险确认。 | Write a file; requires medium-risk confirmation. |
| `mcp.tool.listTables` | 列出 schema 与表，附带行数估计。 | List schemas and tables with estimated row counts. |
| `mcp.tool.describeTable` | 返回字段、类型、索引与注释。 | Return fields, types, indexes, and comments. |
| `mcp.tool.runQuery` | 执行只读 SQL，超时 30 秒，禁用写语句。 | Run read-only SQL with a 30-second timeout; writes are disabled. |
| `mcp.tool.navigate` | 打开 URL 并等待网络空闲。 | Open a URL and wait for network idle. |
| `mcp.tool.screenshot` | 截取视口，写入会话产出物。 | Capture the viewport into chat artifacts. |
| `mcp.tool.click` | 按选择器点击，高风险操作。 | Click by selector; high-risk operation. |
| `mcp.dialog.title` | 添加 MCP 服务 | Add MCP server |
| `mcp.dialog.description` | 填写启动命令或远端地址，Anvil 会先握手并拉取工具清单。 | Enter a launch command or remote address; Anvil handshakes first and fetches the tool list. |
| `mcp.form.name` | 服务名称 | Server name |
| `mcp.form.namePlaceholder` | 例如 gitlab | For example: gitlab |
| `mcp.form.transport` | 传输方式 | Transport |
| `mcp.form.command` | 启动命令或地址 | Launch command or address |
| `mcp.form.commandPlaceholder` | npx -y @scope/server-gitlab | npx -y @scope/server-gitlab |
| `mcp.form.commandHint.stdio` | stdio 使用命令，远端服务填写 https://…/sse。 | stdio uses a command; remote servers use https://…/sse. |
| `mcp.form.commandHint.remote` | 填写远端 MCP 服务的 http/https 地址。 | Enter the http/https address of the remote MCP server. |
| `mcp.form.env` | 环境变量（每行一条） | Environment variables (one per line) |
| `mcp.form.envPlaceholder` | GITLAB_TOKEN=… | GITLAB_TOKEN=… |
| `mcp.form.envHint` | 提交时读取；预览值已脱敏。 | Read on submit; preview values are masked. |
| `mcp.form.envPreview` | 环境变量预览 | Environment preview |
| `mcp.form.envMasked` | {key}=•••• | {key}=•••• |
| `mcp.form.permission` | 默认权限 | Default permission |
| `mcp.form.permission.readonly` | 只读（拒绝写工具） | Read only (deny write tools) |
| `mcp.form.permission.medium` | 中风险确认 | Confirm medium risk |
| `mcp.form.permission.full` | 完全控制 | Full control |
| `mcp.form.agents` | 可用 Agent | Available agents |
| `mcp.form.allAgents` | 全部 Agent | All agents |
| `mcp.form.readonly24` | 添加后先以只读模式运行 24 小时，观察工具调用记录后再放开写权限。 | Run in read-only mode for the first 24 hours, then grant write access after reviewing tool calls. |
| `mcp.form.cancel` | 取消 | Cancel |
| `mcp.form.add` | 添加 | Add |
| `mcp.form.test` | 测试连接 | Test connection |
| `mcp.form.close` | 关闭 | Close |
| `mcp.validation.nameRequired` | 请填写服务名称 | Enter a server name |
| `mcp.validation.addressRequired` | 请填写启动命令或地址 | Enter a launch command or address |
| `mcp.validation.urlInvalid` | 远端服务地址必须是有效的 http/https URL | Remote server address must be a valid http/https URL |
| `mcp.validation.envInvalid` | 环境变量格式应为 KEY=VALUE | Environment variables must use KEY=VALUE |
