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
- R0-04..R0-12: page bodies and their empty/sample states.

## Manual review conclusion

Shell geometry, navigation, breadcrumbs, topbar actions, token values and
responsive behavior were compared against the prototype screenshots and the
prototype CSS. The deviations above are deliberate and scope-driven; no layout
or interaction difference beyond them is accepted. The prototype fixture
identity/counts are the only content intentionally absent from the normal
entry; they must not be reintroduced outside isolated sample fixtures.

## R0-04 workbench

- Normal `/` is the real entry: stats show `0`, lists show empty states, and
  「配置 Agent」「新建对话」「全部会话」「继续」「管理」「全部」 are disabled
  with an explicit "not connected (G1/G2)" tooltip. No prototype fixture
  numbers, names or identities are rendered.
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
- Page options navigate to their route. The five action options are not wired
  yet (new chat → R0-05/G2, schedule → R0-10, MCP/runtime/token audit → later
  settings tasks), so activating one keeps the palette open and shows the
  localized 「未接入」 notice. It never navigates, mutates state, or reports a
  fake success.
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

## R0-07 skills

- Normal `/skills` is the real entry: the page header, three tabs (without
  fixed counts), search/filter toolbar, empty list, empty detail and disabled
  导入 Skill / 新建 Skill / 保存 / 启用 / 运行测试 actions. No Skill names,
  versions, success rates, call counts or identifiers are prefilled.
- The populated prototype state is isolated at `/?sample=skills` and
  `/__sample/skills`. A sample banner marks it as design data. The installed
  tab shows nine Skills with `已安装 9`, the market tab six packs, and drafts
  two (`草稿 2`); the selected detail exposes version/source/calls/success
  rate, enable/save, workflow, triggers/resources, checks and test results.
- The list is a searchable master pane (name/description/tags) with local
  source/Agent selects and 「没有匹配的 Skill」. Wide layout is list + detail;
  ≤1180px narrows the list, ≤900px switches between detail and list with a
  back control, and ≤620px folds the hero/sections. The prototype's card grid
  is represented as the required list + detail surface; no fixture is lost.
- Four workflow steps render as an expandable timeline with status, description
  and tool/resource chips. Trigger conditions, cron schedule, resource
  checkboxes and the sandbox note are present.
- The test panel provides the prototype input, running/success/failure states,
  the four checks, and 「试运行后启用」 which is enabled only after success.
  Every action (save, enable, toggles, workflow resources, test, enable-after,
  install/continue-draft) only emits a localized sample-mode notice; no file,
  setting or real Skill command is written.
- 新建 Skill opens a three-stage builder (基础信息 → 触发与资源 → 测试与发布)
  with 上一步/继续/发布 Skill, progress labels and validation for name, slug,
  version (`v1.2.3` shape) and trigger. 导入 Skill has 从链接 / 从文件 tabs,
  URL required + http/https validation, file-required validation, ESC/backdrop
  close and focus return.
- All chrome, labels, hints, checks, table/tab text, validation and aria names
  come from the `skills.*` zh/en catalogue; key sets stay equal and
  `CATALOGUE.md` lists every key. Skill names, descriptions, prompt/test
  content and user-entered values keep their original language.
- All nine manifest viewports were probed without page-level horizontal
  overflow (timeline/list/local tables may scroll internally); 820×1180 keeps
  the existing Windows work-area clamp limitation.
- Sample navigation reuses `sample.nav.*` (Skill 9 个已安装 in the tool menu);
  normal `/skills` renders no fixed counts or metadata.
