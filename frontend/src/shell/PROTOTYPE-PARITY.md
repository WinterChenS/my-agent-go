# R0-01 prototype parity review

Prototype source: `docs/ui/Agent-客户端产品原型` at commit
`112fcae854b324d2e75fdf5c890083ef9c1d3d6e`. Same-condition screenshots are
produced by `frontend/scripts/capture-prototype.mjs` for `index` and `settings`
at 1440×900 and 390×844 and stored next to the R0-01 evidence.

## Aligned

- Nine locked routes and labels: 工作台, 对话, Agent, 资源库, Skill, MCP,
  连接器, 定时任务, 设置.
- Sidebar geometry, brand mark, primary nav + 「工具与自动化」 flyout,
  topbar height, page scroll container and 900/620 breakpoints.
- Breadcrumb parents per screen (工作区 / 智能体 / 自动化 / 系统), driven by
  `routes.json`.
- Topbar actions per screen: help only on 工作台; search on every shell route;
  settings has a runtime badge + search.
- Frozen tokens from the prototype (`--bg`, `--surface`, `--fg`, `--border`,
  `--accent`, radii, shadows, fonts, motion); shell CSS references tokens only.
- Keyboard focus order (sidebar → topbar → main), `aria-current`, tool menu
  open via menu button, Arrow keys, Escape returns focus, `Space`/`Enter`
  activate menu items.

## Intentional deviations (with reason)

- **No persistent back button.** The prototype only has a session-specific
  「返回工作台」 control in `chat.html`, which belongs to R0-05. The shell relies
  on browser history (`Alt+Left` / `Cmd+[`); native acceptance injects
  `Alt+Left` and asserts the previous route marker.
- **Chat route has no common topbar.** `chat.html` owns a session header with
  its own actions; R0-05/R2-01 will render it. The shell route still exists and
  is navigable.
- **No fixture data in the normal entry.** Prototype counts, user identity and
  runtime state are design fixtures. Normal R0-01 shows neutral state
  (no count badges, 「本地运行时 / 未检测」, 「本地工作区 / 未连接账号」).
  Fixture-filled states will be reproduced only through isolated sample entry
  points in later tasks.
- **Settings runtime badge is honest.** The prototype shows `服务正常`; without
  a runtime probe (G1) the shell shows `运行时未接入` with a warning tone. Once
  R1-08 connects runtime discovery, the same badge can show the real status.
- **390px prototype overflow is not copied.** The manifest requires
  `mustAvoidHorizontalScroll`; the implementation collapses the sidebar to
  60px and keeps content within the viewport instead of reproducing the
  prototype’s own horizontal overflow at 390px. The visible breadcrumb keeps
  both `parent / title` at every breakpoint (no degraded parent state): the
  narrow native evidence asserts the parent `statictext` has non-zero bounds
  at 360×800 and 390×844.
- Sidebar history/projects/personal sections are rendered only on the chat
  route; they are session data, not shell chrome, and arrived with R0-05.

## Follow-up tasks

- R1-08: runtime status badge fed by real discovery.
- R0-04..R0-12: page bodies landed between `98a419d` and `22f69ae`; each page keeps
  the honest empty state on its normal entry and reproduces the prototype fixtures
  only behind `/__sample/<page>`.

## Manual review conclusion

Shell geometry, navigation, breadcrumbs, topbar actions, token values and
responsive behavior were compared against the prototype screenshots and the
prototype CSS. The deviations above are deliberate and scope-driven; no layout
or interaction difference beyond them is accepted. The prototype fixture
identity/counts are the only content intentionally absent from the normal
entry; they must not be reintroduced outside isolated sample fixtures.

## R0-04 workbench

- Normal `/` is the real entry and now reads one host command,
  `workbench_overview`. The four stats, the activity heatmap, continue-chats,
  recent outputs, my agents and run status all come from committed ledger rows,
  the persona catalog and the files sessions wrote inside their projects.
  A day is busy only when a run completed or a tool returned; a missing token
  counter is shown as "not reported", never as a measured zero. Dates are
  bucketed on the host using the viewer's UTC offset.
- Outside the desktop host (jsdom, plain browser) there is no ledger, so the
  same page keeps the honest empty state: zeroed stats, "not connected" cards
  and disabled actions with the "not connected (G1/G2)" tooltip. A read failure
  shows an alert instead of falling back to fixtures. No prototype fixture
  numbers, names or identities are rendered in either real state.
- The heatmap keeps the prototype's 12/26/53-week select, month labels, level
  legend and hover tooltip. Each range re-reads the ledger; the tooltip adds the
  real split of completed runs and tool results for that day.
- Three cards have no source in this build and say so explicitly: today's
  schedules (no scheduler), connectors (no registry) and next quota reset (no
  quota service). The status card carries a "data sources" note listing this
  boundary. They are not filled with placeholder zeros.
- Buttons navigate for real: configure/manage agents → `/agents`, new chat → a
  cleared `/chat`, all chats → `/chat`, continue → that exact session id in
  `/chat`, all outputs → `/library`, all schedules → `/schedules`.
- The prototype-filled state is isolated at `/?sample=workbench` and the
  path alias `/__sample/workbench` (used by the native case; not in navigation).
  A visible sample banner marks it as design data, and its action buttons only
  emit a sample-mode notice, never a fake success. In this sample mode the
  shell also replays the prototype navigation counts (Chat 4 and the five tool
  menu metadata strings); the normal entry keeps them absent.
- Layout follows the prototype module order: header, 4 stats, activity
  heatmap, continue chats, recent artifacts, my agents, today's schedules,
  run status, global search, header actions. The split is two-column at wide
  sizes and one column at ≤1180px; stats go 4 → 2 → 1 at 900/620px.
- The greeting is time-based in the normal entry and fixed to the prototype's
  afternoon greeting in the sample entry (which intentionally keeps the
  prototype's 陈羽 fixture name). Normal never shows a person name.
- The ⌘K quick-jump palette follows the prototype: `role="dialog"` labelled
  「快速跳转」, a `role="listbox"` with the flat 14 items (9 pages + 5 actions),
  ↑/↓ changes the highlighted option, ↵ opens, ESC closes, and the footer shows
  the key hints with no close button. Focus returns to the toolbar trigger after
  closing; Tab stays in the search field.
- Page options navigate to their route. Since v64 the five action options open
  real screens: new chat clears the active session, new schedule → `/schedules`,
  add MCP → `/mcp`, manage runtime → `/settings#runtime`, token audit →
  `/settings#observability`. Activating one closes the palette; nothing reports
  a fake success.
- All palette copy comes from `workbench.palette.*` in both catalogues; the
  zh/en key sets stay identical and `CATALOGUE.md` lists every key.
- The 820×1180 spot check has the same Windows 1080 work-area clamp documented
  in R0-01; horizontal overflow remains false.

## R0-05 chat

- Normal `/chat` is the real entry: a runtime/session empty state, empty
  history/project/personal groups, disabled composer, send/stop, export, more,
  project picker and every unconnected action. No prototype titles, agent
  names, model IDs, token counts or file paths are rendered.
- The filled prototype state is isolated at `/?sample=chat` and the path alias
  `/__sample/chat` (used by the native case; not in navigation). The banner
  marks it as design data. Sidebar history, the conversation, exception cards,
  the four rail panes, composer draft/attachments and the dialogs come only
  from this sample entry.
- The chat header is its own 52px surface (no shared topbar): back-to-workbench,
  title/subline, clarification and approval badges, language quick switch,
  export, rail toggle and chat-actions trigger. On compact widths the subline,
  badges, language and secondary actions collapse; the history and rail toggles
  remain.
- Sidebar history follows the prototype: search filters projects and personal
  chats, project groups expand/collapse, the session action menu is present but
  every item is disabled, and personal chats load in pages ending in
  「已加载全部会话」. Sample session actions only show the localized
  sample-mode notice; the normal entry shows 「尚未接入会话」.
- Messages cover user bubbles, agent metadata, collapsible thinking duration
  and steps, collapsible tool cards with arguments/results, citation superscripts,
  turn artifacts, a stream status, an approval card and an inline THIL
  clarification. The sample additionally renders authentication failure,
  context-limit, stopped and recovery states with `status`/`alert` roles.
- The composer supports a multi-line draft (the prototype's sample draft is
  preserved), attachment/Skill chips with removal, `+` context menu, `@` mention
  listbox, project picker, model/thinking/permission selects, stop/send, and
  Ctrl/⌘+Enter. Every business action only emits the localized
  「样例模式…未接入」 notice; no message, approval or file operation is faked.
- The rail has 浏览器 / 输出物 / 引用 / 执行 tabs, browser preview, artifact rows,
  citations, run steps and usage. It collapses at any width and becomes a fixed
  bottom drawer at ≤900px. The compact history is a separate drawer. Both close
  from their close control or Escape and restore focus to their toggles.
- Project picker, export and chat-actions dialogs use `role="dialog"`,
  `aria-modal`, focus-in, Tab trapping, ESC close and focus return. THIL shares
  the same shell. Unwired actions inside them are disabled or show the
  sample-mode notice; export never claims a file was written.
- Chrome copy, status labels, aria labels and dynamic notices come from the
  zh/en `chat.*` catalogue; both key sets are identical and `CATALOGUE.md`
  lists every key. Conversation text, paths, file names and people names are
  user content and intentionally stay in their original language in English
  mode.
- The sample shell replays the prototype Navigation fixture: `对话 4` on the
  chat item and, when 工具与自动化 is expanded, 资源库 28 个输出物 / Skill
  9 个已安装 / MCP 3 个服务 / 连接器 5 个连接 / 定时任务 6 个任务. The
  values live in `shell/sampleNavigation.ts`, are formatted through the
  `sample.nav.*` zh/en keys, and are asserted in both languages. Normal
  `/chat` and `/` render no fixed count or metadata: the chat count, these five
  meta strings and their `nav-*` test hooks are absent.
- Responsive rules: 1180px narrows the rail to 340px, 900px turns both rail and
  history into drawers with a single thread column, and 620px compacts the
  header/composer. All nine manifest widths remain free of horizontal overflow;
  the 820×1180 Windows work-area clamp uses the R0-01 limitation.
- The sample composer keycap renders `Ctrl` on Windows and `⌘` where the host
  reports macOS; the prototype's mac keycap is otherwise unchanged. The
  prototype locale select is represented by the existing R0-03 quick-switch
  control.

## R0-06 agents

- Normal `/agents` is the real entry: page header and five tabs exist, the list
  and detail are explicit empty states, and the basic form shows disabled
  「未接入」 fields. 导入 / 新建 Agent / 保存 / 发起对话 are disabled. No agent
  names, runtime names, model IDs, Skill counts or identifiers are prefilled.
- The populated prototype state is isolated at `/?sample=agents` and
  `/__sample/agents`. A sample banner marks it as design data. It renders the
  four main agents and two sub-agents (只读 · 委派自 迁移工程师), the selected
  migration engineer detail, runtime/model/thinking/permission/Skill chips and
  all five panels: basic form, prompt/tools/Skills, sub-agent table and policy,
  permission modes/boundaries, and four run-history rows.
- The list filter is a local addition for the required filter interaction; it
  matches name, description, model and runtime and shows
  「没有匹配的 Agent」 when nothing matches. Selecting a row updates the hero,
  chips and basic form. Sub-agent rows are marked 子 and are read-only.
- Basic-form switches (memory/auto-compaction), every prompt/tool/Skill/sub
  switch, permission mode and dialog action only emit the localized sample-mode
  notice; nothing is persisted or sent. Save validates a name and a lowercase
  slug (`[a-z0-9-]+`). Start chat never creates a session.
- 新建 Agent and 导入 Agent dialogs share a focus-trapped `role="dialog"` shell:
  ESC/backdrop close, focus returns to the opener, create validates a required
  name, and import validates a chosen file before showing the sample notice.
- All chrome, field labels/hints, validation, tabs, table headers, risk labels
  and aria names come from the `agents.*` zh/en catalogue; key sets stay equal
  and `CATALOGUE.md` lists every key. Agent names, descriptions, system prompt
  and user-entered values are content and keep their original language.
- At wide sizes the layout is 268px list + detail; at ≤1180px the list narrows
  to 224px, at ≤900px it becomes one column with a detail/list switch and a
  back-to-list control, and at ≤620px the form/tables/hero fold. All nine
  manifest viewports were probed without horizontal overflow; 820×1180 keeps
  the existing Windows work-area clamp limitation.
- Sample navigation reuses the R0-05 shell fixture: `对话 4` and the five tool
  metadata strings are present only in sample mode. Normal `/agents` keeps them
  absent.
- **Sub agents are files, not rows.** Saving a sub agent writes
  `~/.pi/agent/agents/<slug>.md` — front matter `name`, `description`, `tools`,
  `model` (only when the id names a provider) and an `anvil: <id>` marker that
  pi ignores. That is the only interface pi's official `subagent` extension
  scans, so the row is what the runtime reads instead of a second registry.
  Files this app did not write are never touched: pi's own planner / reviewer /
  scout / worker stay, and a slug that collides with one is reported as
  「名称被占用」 instead of overwriting it.
- **The description is the selection input.** Every turn's appended system
  prompt carries a roster built from that same directory — this app's agents and
  pi's own on one list — plus the rule that a task with no matching agent is
  done by the parent itself. `description` is required for a sub agent for the
  same reason; without it pi drops the file and the parent has nothing to choose
  by.
- **The policy removes a tool.** `delegationPolicy: never` drops `subagent` from
  the allowlist and skips the roster; `explicit` keeps the tool and tells the
  parent not to delegate unless the user named an agent. An empty configured
  list becomes the built-ins minus `subagent`, never 「no tools」.
- `subagent_overview` reads the directory (no writes) so the table can say
  已生效 / 待保存 / 名称被占用 / 缺少描述 / 已停用 / 非 pi per row.
- Concurrency has no runtime switch in pi's extension; it reaches the parent as
  a line in the roster ("同时最多并行 N 个子任务"), which is an instruction, not a
  hard cap.

## R0-07 skills

- Normal `/skills` is the real entry: the page header, three tabs without
  fixed counts, search/filter toolbar and an empty card grid with an explicit
  empty-state explanation. 导入 Skill / 新建 Skill / 保存 / 启用 / 运行测试
  are unavailable. No Skill names, versions, success rates, call counts or
  identifiers are prefilled.
- The populated prototype state is isolated at `/?sample=skills` and
  `/__sample/skills`. A sample banner marks it as design data. Cards reproduce
  the prototype anatomy: icon, name, `version · source`, description, tag
  pills, call count, update time, enable switch and ellipsis menu. The
  installed tab shows nine cards with `已安装 9`, market six, drafts two
  (`草稿 2`); draft cards expose 「继续编辑」 and market cards 「获取」.
- The grid is three columns at wide sizes, two at ≤1180px and one at ≤900px;
  the page itself never overflows horizontally. There is no permanent detail
  column. Search (name/description/tags), source and Agent filters and the
  synchronization summary sit above the grid; 「没有匹配的 Skill」 is shown for
  an empty result. The card/menu both open the same editor, which is the one
  deliberate simplification of the prototype's separate menu surface.
- Clicking a card or its ellipsis opens 创建可复用能力 as a focus-trapped,
  prefilled four-stage builder (描述意图 → 基础信息 → 触发与资源 → 测试与发布).
  Escape/backdrop closes and returns focus to the card. The builder validates
  name, slug, version (`v1.2.3` shape) and trigger, and carries the prototype
  workflow timeline, trigger conditions, resources, test input, four checks,
  running/success/failure output and 「试运行后启用」 (enabled only after a
  successful run). 发布 Skill never writes a real Skill.
- 导入 Skill has 从链接 / 从文件 tabs, URL required + http/https validation,
  file-required validation, ESC/backdrop close and focus return. Every action
  (save, enable, switches, resources, test, publish, install, continue draft)
  only emits a localized sample-mode notice; no file, setting or command is
  written.
- All chrome, labels, hints, checks, status, validation and aria names come
  from the `skills.*` zh/en catalogue; key sets stay equal and `CATALOGUE.md`
  lists every key. Skill names, descriptions, workflow/test content and
  user-entered values keep their original language.
- All nine manifest viewports were probed without page-level horizontal
  overflow (internal timelines/tables may scroll); 820×1180 keeps the existing
  Windows work-area clamp limitation.
- Sample navigation reuses `sample.nav.*` (Skill 9 个已安装 in the tool menu);
  normal `/skills` renders no fixed counts or metadata.

- Live `/skills` writes too: 导入 Skill picks a folder and copies it into the
  runtime's skill root (`skill_import`), 新建 Skill writes a real `SKILL.md`
  with YAML frontmatter (`skill_create`), and both re-sync the catalog so the
  card appears without a restart. 草稿 lists skills whose scan state is not
  `valid` (missing frontmatter, or a link outside the root); 市场 states plainly
  that this build has no remote registry instead of showing fake listings.

## R0-08 MCP

- Normal `/mcp` is the real entry: the four statistics show 0 / 0 / 0 / — with
  「尚未接入」 subtext, the service grid is empty with an explicit empty state,
  the connection log is empty, and 重连全部 / 添加服务 / 刷新 are disabled. No
  server names, commands, tool counts, stats or log entries are prefilled.
- The prototype state is isolated at `/?sample=mcp` and `/__sample/mcp` with a
  sample banner. It renders the four prototype statistics (3 / 19 / 216 /
  142ms), four service cards in a two-column grid, connected/needs-auth/
  disconnected badges, command or URL, transport/permission/tool tags, tool
  descriptions, call/error or authorization footers and the switch.
- The toolbar searches service names and tool names, filters by transport and
  status, shows the config path, and reports 「没有匹配的 MCP 服务」. The log
  card shows six entries with time, service, result and detail; 刷新 only emits
  a localized sample notice.
- 添加 MCP 服务 is a focus-trapped dialog with name, transport, command/address,
  per-line env, permission radios, agent checkboxes and the read-only 24-hour
  note. Transport switches the command/remote hint and HTTP/SSE/WebSocket
  addresses validate as http/https. Env lines are parsed as KEY=VALUE and the
  preview renders masked `KEY=••••` values; no command is executed and no
  network request is made. ESC/backdrop closes and focus returns to the opener.
- Every card action (reconnect, switch, authorize, confirm, settings, install)
  and dialog action only shows a sample-mode notice. No MCP process starts, no
  config is written, and no secret value is rendered in text/screenshot preview.
- All chrome, statuses, tool descriptions, hints, validation and aria names use
  the zh/en `mcp.*` catalogue with equal key sets and `CATALOGUE.md` entries.
  Server names, commands, URLs, env keys and paths remain user content.
- Cards are two columns wide and one column at ≤900px; stats go 4 → 2 → 1.
  Tool lists have no fixed height or internal scroll: every declared tool stays
  visible and the page scrolls, matching the prototype. All nine manifest
  viewports were probed without page-level horizontal overflow; 820×1180 keeps
  the existing Windows clamp limitation.
- Sample navigation reuses `sample.nav.*` (MCP 3 个服务 in the tool menu);
  normal `/mcp` renders no fixed service/tool counts or metadata.
- Live `/mcp` reads the runtime's declared servers and their persisted probe
  state. 校验/测试连接 is a real connection: stdio servers are spawned and asked
  `initialize` + `tools/list`; remote servers are probed over **Streamable HTTP**
  (JSON-RPC `initialize`, `Mcp-Session-Id` carried on follow-ups, JSON or SSE
  replies) with a legacy HTTP+SSE handshake as a fallback. Header values are read
  from the runtime's own config for that one request — the catalogue only keeps
  header names — and the outcome (tool list or the server's error) is persisted.
- Settings are applied on change: there is no page-level 保存更改/检查配置, and
  the topbar no longer shows a 「运行时未接入」 badge on this page. The app's own
  right-click menu is removed in this build; the browser pane keeps its own.


## R0-09 connectors

- A connector is **not** an MCP server. MCP is a protocol a runtime speaks to a
  tool host; a connector is a service the user has an account on, with its own
  API and credentials. MCP servers live on the MCP page; this page only lists
  connectors, and the two no longer share a table or a command.
- Three real connector types: **邮件** (IMAP read + SMTP send), **Notion**
  (search / read page / create page, `Notion-Version: 2026-03-11`) and
  **钉钉文档** (enterprise app token + document search / block read). Each type
  declares its own fields, so the form is generated from the descriptor instead
  of a hardcoded dialog: text, password, number, select and tags controls, with
  required/secret flags driving validation and masking.
- Credentials are stored in the catalog database and **never** travel back to
  the UI: `connectors_overview` returns each config with secrets blanked plus a
  `secretsSet` list, and an edit that leaves a secret blank keeps the stored
  value. Nothing secret is written to the ledger, the audit export or a log.
- 校验 is a real request: IMAP `LIST` + SMTP `NOOP` for mail, `/v1/users/me` +
  `/v1/search` for Notion, `accessToken` + doc search for DingTalk. The outcome
  (message and steps) is what the service answered; the state and probe time are
  persisted, so the card reflects it after a reload.
- The policy panel lists connector · tool rows with the call and failure counts
  recorded in `connector_calls` — one row per tool call the agent actually made.
  The activity timeline is the last eight of those rows; the health panel shows
  probe recency per connector.
- The agent reaches connectors through `anvil-connector.mjs` (`list` / `call`),
  which posts to the loopback bridge. The host runs the call, records it, and
  returns the tool's own text; the tool list and parameters are printed by
  `list`, so the model never has to guess a name.
- Sample entry (`/?sample=connectors`) keeps the prototype's six connectors,
  policy table and credential-health meters; nothing there touches the host.

## R0-10 schedules

- Normal `/schedules` reads `schedule_overview`: schedules, their runs, stats
  and a seven-day trend, all from the ledger. A schedule is a row plus real
  occurrences — every fire opened a session, and the occurrence stores that
  session and run id.
- 新建任务 / 编辑配置 write through `schedule_save` (cron validated on the host,
  the agent resolved from the catalog), the switch uses `schedule_toggle`,
  立即运行 fires once through `schedule_run_now`, and 全部暂停 pauses every
  enabled row. Stats show 成功率 and 平均时长 as `—` when nothing finished, never
  as 0%.
- The host runs one tick thread; a due schedule is claimed exactly once
  (unique occurrence key), the session is opened as the saved agent and the
  prompt is sent, and the end state is recorded (`success`, `failed`, `skipped`
  for an aborted run, `unknown` when the app stopped mid-run).
- An agent can create one from a sentence: every session is told about
  `node ~/.anvil/bin/anvil-schedule.mjs create '{...}'`, which posts to the same
  loopback bridge and the same validation. `list` / `pause` / `resume` /
  `remove` are available too, so the model can check before adding.
- Sample entry (`/?sample=schedules`) keeps the six prototype tasks, their run
  history and the paused/partial badges.

## R0-11 library

- Normal `/library` scans what sessions actually wrote. For every recent
  session the host reads its `tool_call` events, runs the same
  `rail::artifact_files` the chat rail uses (same confinement, same scratch
  exclusions), deduplicates by canonical path and merges
  `<data dir>/library`, where uploads are copied.
- Stats are the scan's own numbers: file count, total bytes, files added in the
  last seven days, distinct source sessions and the newest modification time.
  There is no quota or auto-clean-up service in this build, so those two
  prototype stats are not printed as if they existed.
- Type tabs (文档/表格/网页/图表/代码) count by extension; the search box and the
  session filter narrow the same list. The detail dialog shows the real path,
  size, source and the first slice of a text file (`library_preview`), or says
  plainly that a binary file has no text preview.
- 上传 opens the native multi-select and copies into the library folder
  (`library_import`); 导出所选 copies the selection into a timestamped folder
  under `exports` (`library_export`); 在文件夹中显示 reveals the file. Nothing is
  deleted by this page.
- Sample entry (`/?sample=library`) keeps the prototype's eight assets and the
  fixture detail preview.

## R0-12 shell surface

- The main window has **no** right-click menu. `useContextMenuGuard` takes
  `contextmenu` in the capture phase on the document, so WebView2 never draws its
  own menu (返回/刷新/另存为/打印) over the app. The prototype's custom menu was
  removed in v67; leaving both gone is deliberate — a desktop surface should not
  put browser commands on the pointer.
- The built-in browser pane is a child webview with its own document and keeps
  its own menu (返回 / 前进 / 重新加载 / 复制页面地址), drawn inside the page so it
  sits above the native view.
- Consequence: right-click paste in the composer is gone; Ctrl+V still works.

## R0-13 runtime extensions

- 设置 → 运行时 → 运行时扩展 shows pi's `subagent` extension with one state and
  two actions. 检查更新 compares the content digest (FNV-1a over `index.ts` and
  `agents.ts`) of the installed copy with the copy inside the current pi
  package; a difference flips the badge to 可更新 and offers 更新.
- 更新 rewrites the extension's shipped sources only. Agent and prompt files the
  user edited are counted as skipped and left alone, and the README's
  "Installed by Anvil" note is appended once, not once per run.
- No package copy (pi installed somewhere the heuristic cannot see) means no
  update is offered at all — "cannot compare" is not reported as "up to date".


## R0-14 memory (设置 → 记忆)

- Two layers, stored as rows in the session ledger: **长期记忆** (`scope=user`)
  is flattenable text with a kind (偏好 / 事实 / 约束 / 习惯), a pin and an enable
  switch; **会话记忆** (`scope=session`) is one editable conclusion per
  conversation, written from the assistant's last answer.
- Only `user` rows reach the prompt, as a budgeted bullet list in the appended
  system prompt. A session conclusion is a record for the reader, never a
  hidden recall input; promoting it turns it into a user row and removes the
  session row.
- Every row shown in the page is the row that can be injected: disabling or
  deleting takes effect on the next open, with no cache to invalidate.
- Two switches: injection and recording. Turning recording off stops new
  conclusions without deleting old ones. A row the user edited (`source=manual`)
  is never overwritten by the next conversation's answer.
- The agent can write the same rows through `~/.anvil/bin/anvil-memory.mjs`
  (`list` / `add` / `forget`), so 「记住…」 is auditable in this page rather than
  disappearing into a hidden store.

## R0-15 agent run records

- One row used to be one session, selected by agent **id** while the ledger
  stores the agent **slug**. A saved agent's history therefore came back empty.
  The page now sends both; the built-in default also claims conversations opened
  from the model picker (whose stored agent id is empty).
- A delegated child has no session of its own — pi runs it inside the parent's
  process and reports it as a stream of `sub_agent` events. Those events are
  folded by `agentId` into one **子 Agent** row per call: the task as its title,
  `max()` of cumulative tool snapshots, the child's own `turns` and
  `input + output + cacheRead` tokens. The parent's row counts its own tool
  calls plus one per delegation, but never the child's tools or tokens.
- Status is taken from the child's terminal event (`completed` / `failed` /
  `interrupted`) rather than the parent's session status, and the table shows a
  来源 column so the two kinds of rows cannot be mistaken for each other.
