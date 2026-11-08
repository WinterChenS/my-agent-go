// 应用外壳（U01 / UI05 / D1）：顶栏 + 侧栏（品牌、导航、历史会话、运行时与工作区）+ 主区 + 右栏。
//
// 结构来自规格 §5.1；**几何来自原型**（docs/ui/Agent-客户端产品原型/）：`--anvil-sidebar-w`
// 网格、品牌头、主导航 + 「工具与自动化」组、按项目分组的历史（由 history 渲染）、
// 底部 Pi 状态与工作区、52px 顶栏。对应 studio.css 的
// .shell / .sidebar / .brand / .nav-* / .sidebar-foot / .topbar / .page。
//
// 为什么把顶栏放在 DOM 的最前面（原型里侧栏在最前）：Tab 顺序必须"顶栏搜索 → 导航 →
// 历史 → 主内容"，视口里的左右位置由 grid-template-areas 决定，两者可以分开。
//
// 本组件只渲染壳，不判断业务状态：搜索、历史、页脚都从上层传入。
//
// 顶栏是"一条 52px 的带子"（D2c）：路由需要自己的页头（对话页的标题/副行/动作）时，
// 用 `useTopbarSlot()` 把内容 portal 进 `[data-topbar-slot]` 并 claim 占用，壳层随即
// 让出自己的 crumb / 搜索 / 状态区；没路由占用时保持原样（外壳自己的标题）。

import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import {
  Activity,
  Bot,
  Brain,
  Clock,
  FolderOpen,
  Layers,
  LayoutGrid,
  Link2,
  MessageSquare,
  PanelRightClose,
  PanelRightOpen,
  Play,
  Plug,
  Puzzle,
  Settings as SettingsIcon,
} from "lucide-react";
import { navGroups, type RouteId } from "./navigation";
import { TopbarSlotProvider, type TopbarSlot } from "./topbarSlot";
import type { Status } from "../types";
import { t } from "../i18n";

export interface LayoutProps {
  status: Status | null;
  /** 当前页面 id，用于高亮导航项。 */
  current: RouteId;
  onNavigate: (path: string) => void;
  /** 顶栏的全局搜索（来自 features/search）。 */
  search: ReactNode;
  /** 左侧栏下半部：会话历史。 */
  history: ReactNode;
  /** 左侧栏底部：Pi 状态与本地工作区。 */
  footer: ReactNode;
  right: ReactNode;
  rightOpen: boolean;
  onToggleRight: () => void;
  /** run 阶段，用于顶栏状态点。没有活跃 run 时为 null。 */
  phaseLabel?: { label: string; tone: string } | null;
  children: ReactNode;
}

/**
 * 导航图标。原型每个入口都有自己的图标（grid/message/bot/folder/puzzle/plug/
 * link/clock/settings），全部是 15px 描边图标；这里保持同一套隐喻，
 * 让"设置"和"定时任务"在侧栏里一眼可分。
 */
const NAV_ICONS: Record<string, typeof Layers> = {
  workbench: LayoutGrid,
  chat: MessageSquare,
  agents: Bot,
  library: FolderOpen,
  memory: Brain,
  skills: Puzzle,
  mcp: Plug,
  connectors: Link2,
  schedules: Clock,
  settings: SettingsIcon,
  runs: Play,
  observability: Activity,
};

export function Layout({
  status,
  current,
  onNavigate,
  search,
  history,
  footer,
  right,
  rightOpen,
  onToggleRight,
  phaseLabel,
  children,
}: LayoutProps) {
  const groups = navGroups();
  const [toolsOpen, setToolsOpen] = useState(true);
  const mainRef = useRef<HTMLElement | null>(null);

  // 顶栏插槽（D2c）：路由可以把自己的页头渲进这条 52px 的带子里，而不是在页面里
  // 再画一条。占用期间壳层让出自己的 crumb / 搜索 / 状态区——一个头里不能装两套内容。
  const [topbarElement, setTopbarElement] = useState<HTMLDivElement | null>(null);
  const [topbarClaimedBy, setTopbarClaimedBy] = useState<object | null>(null);
  const claimTopbar = useCallback((token: object, claimed: boolean) => {
    setTopbarClaimedBy((current) => (claimed ? token : current === token ? null : current));
  }, []);
  const topbarSlot = useMemo<TopbarSlot>(
    () => ({ element: topbarElement, claim: claimTopbar }),
    [topbarElement, claimTopbar]
  );
  const topbarClaimed = topbarClaimedBy !== null;

  const renderNavItem = (item: { id: RouteId; path: string; labelKey: string; hintKey: string }) => {
    const active = item.id === current;
    const Icon = NAV_ICONS[item.id] ?? Layers;
    return (
      <li key={item.id}>
        <button
          type="button"
          onClick={() => onNavigate(item.path)}
          title={t(item.hintKey)}
          aria-current={active ? "page" : undefined}
          className="anvil-nav-item"
        >
          <Icon className="h-[15px] w-[15px]" aria-hidden />
          <span>{t(item.labelKey)}</span>
        </button>
      </li>
    );
  };

  const currentLabel = groups.flatMap((g) => g.items).find((i) => i.id === current);
  const pageTitle = currentLabel ? t(currentLabel.labelKey) : t("app.name");

  /**
   * 右栏开关。对话页占用顶栏后仍然要能关掉已经打开的详情栏——
   * 那是壳层唯一能收起它的控件，藏起来等于让人关不掉。
   */
  const detailToggle = (
    <button
      type="button"
      onClick={onToggleRight}
      className="anvil-iconbtn"
      aria-pressed={rightOpen}
      aria-label={rightOpen ? t("shell.hideDetails") : t("shell.showDetails")}
      title={rightOpen ? t("shell.hideDetails") : t("shell.showDetails")}
    >
      {rightOpen ? (
        <PanelRightClose className="h-4 w-4" aria-hidden />
      ) : (
        <PanelRightOpen className="h-4 w-4" aria-hidden />
      )}
    </button>
  );

  return (
    <div
      className="anvil-shell"
      data-detail={rightOpen ? "open" : "closed"}
      data-shell-region="shell"
      data-topbar-claimed={topbarClaimed ? "true" : "false"}
    >
      <header
        className="anvil-shell-topbar"
        role="banner"
        data-shell-region="topbar"
        data-topbar-claimed={topbarClaimed ? "true" : "false"}
      >
        {topbarClaimed ? null : (
          <>
            <div className="anvil-topbar-crumb">
              <span>{t("shell.workspaceRoot")}</span>
              <span className="sep" aria-hidden>
                /
              </span>
              <strong>{pageTitle}</strong>
            </div>

            <div className="anvil-topbar-search">{search}</div>

            <div className="anvil-topbar-spacer" />

            <div className="flex shrink-0 items-center gap-3 text-[11px] text-[color:var(--anvil-muted)]">
              {phaseLabel && (
                <span className="flex items-center gap-1.5" aria-live="polite">
                  <span className={`h-1.5 w-1.5 rounded-full ${phaseLabel.tone}`} aria-hidden />
                  {phaseLabel.label}
                </span>
              )}
              {/* 只有在真的读到后端事实时才显示；读不到就什么都不显示——
                  顶栏排三个"未知"既不提供信息，也和原型的顶栏不一样。 */}
              {status?.platform ? <span className="hidden truncate lg:inline">{status.platform}</span> : null}
              {status?.provider || status?.model ? (
                <span className="hidden truncate xl:inline">
                  {status.provider ?? t("common.unknown")} / {status.model ?? t("common.unknown")}
                </span>
              ) : null}
              {detailToggle}
            </div>
          </>
        )}

        <div className="anvil-topbar-slot" data-topbar-slot="" ref={setTopbarElement} />

        {/* 占领期间的详情栏开关：见 detailToggle 的注释。 */}
        {topbarClaimed && rightOpen ? detailToggle : null}
      </header>

      <aside className="anvil-sidebar" aria-label={t("nav.primaryLabel")} data-shell-region="sidebar">
        <button
          type="button"
          onClick={() => onNavigate("/")}
          className="anvil-brand"
          aria-label={t("app.name")}
          title={t("app.name")}
        >
          <span className="anvil-brand-mark" aria-hidden>
            <Layers className="h-[15px] w-[15px]" />
          </span>
          <span className="anvil-brand-copy">
            <span className="anvil-brand-name">{t("app.name")}</span>
            <span className="anvil-brand-sub">{t("shell.brandSub")}</span>
          </span>
        </button>

        <nav className="anvil-nav" aria-label={t("nav.primaryLabel")}>
          <div className="anvil-nav-group">
            <span className="anvil-nav-label">{t("nav.startLabel")}</span>
            <ul className="anvil-nav-list">{groups[0].items.map(renderNavItem)}</ul>
          </div>

          <div className="anvil-nav-group">
            <button
              type="button"
              onClick={() => setToolsOpen((v) => !v)}
              aria-expanded={toolsOpen}
              title={t("shell.toggleTools")}
              className="anvil-nav-label anvil-nav-toggle flex items-center justify-between"
            >
              <span>{t("nav.toolsLabel")}</span>
              <span className="anvil-nav-caret" aria-hidden>
                <svg
                  width="12"
                  height="12"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.6"
                  strokeLinecap="round"
                >
                  <path d="M9 6l6 6-6 6" />
                </svg>
              </span>
            </button>
            {toolsOpen ? (
              <ul className="anvil-nav-list" aria-label={t("nav.toolsLabel")}>
                {groups[1].items.map(renderNavItem)}
              </ul>
            ) : null}
          </div>
        </nav>

        <div className="anvil-sidebar-history" data-shell-region="history">
          {history}
        </div>

        <div className="anvil-sidebar-foot" data-shell-region="footer">
          {footer}
        </div>
      </aside>

      <main ref={mainRef} className="anvil-page" aria-label={pageTitle} data-shell-region="page">
        {/* 路由在页面里取顶栏插槽（D2c）：插槽节点本身在顶栏里，Provider 只包页面。 */}
        <TopbarSlotProvider value={topbarSlot}>{children}</TopbarSlotProvider>
      </main>

      {rightOpen && (
        <aside className="anvil-detail" aria-label={t("common.details")} data-shell-region="detail">
          {right}
        </aside>
      )}
    </div>
  );
}
