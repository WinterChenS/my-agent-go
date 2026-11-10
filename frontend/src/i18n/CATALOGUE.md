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
| `workbench.palette.hint.move` | 移动 | Move |
| `workbench.palette.hint.open` | 打开 | Open |
| `workbench.palette.hint.summon` | 呼出 | Summon |
| `workbench.palette.hint.escape` | ESC | ESC |
| `workbench.palette.unavailable` | 未接入：该操作将在后续任务开放 | Not connected: this action arrives in a later task |
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
