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
- **Sidebar history/projects/personal sections are deferred** to R0-05
  (chat page) because they are session data, not shell chrome.

## Follow-up tasks

- R0-05: chat session header, history/project sidebar, chat page body.
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
  emit a sample-mode notice, never a fake success.
- Layout follows the prototype module order: header, 4 stats, activity
  heatmap, continue chats, recent artifacts, my agents, today's schedules,
  run status, global search, header actions. The split is two-column at wide
  sizes and one column at ≤1180px; stats go 4 → 2 → 1 at 900/620px.
- The greeting is time-based in the normal entry and fixed to the prototype's
  afternoon greeting in the sample entry (which intentionally keeps the
  prototype's 陈羽 fixture name). Normal never shows a person name.
- Global search is a real shell dialog (page results, sample agents/artifacts
  when sample mode is active); Escape restores focus to the toolbar trigger.
- The 820×1180 spot check has the same Windows 1080 work-area clamp documented
  in R0-01; horizontal overflow remains false.
