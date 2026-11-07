// 应用外壳（U01 / UI05）：顶栏 + 左侧栏（导航 + 会话历史 + 状态）+ 主区 + 右栏。
//
// 左侧栏的结构来自规格 §5.1：
//   上半部是三个主入口（工作台/对话/Agent）与"工具与自动化"聚合入口；
//   下半部是按项目分组的会话与普通会话（HistorySidebar）；
//   底部显示 Pi 状态与本地工作区。
//
// 工具分组默认展开但可收起：默认展开是因为里面的设置页是修复"工作区未信任"的
// 唯一入口，藏起来会让人点不到。
//
// 本组件只渲染壳，不判断业务状态：搜索、历史、状态都从上层传入。

import { useRef, useState, type ReactNode } from "react";
import { PanelRightClose, PanelRightOpen, ShieldCheck } from "lucide-react";
import { navGroups, type RouteId } from "./navigation";
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

  const renderNavItem = (item: { id: RouteId; path: string; labelKey: string; hintKey: string }) => {
    const active = item.id === current;
    return (
      <li key={item.id}>
        <button
          type="button"
          onClick={() => onNavigate(item.path)}
          title={t(item.hintKey)}
          aria-current={active ? "page" : undefined}
          className={
            "flex w-full items-center rounded px-2 py-1 text-left text-[12px] transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)] " +
            (active
              ? "bg-[color:var(--anvil-accent-surface)] text-[color:var(--anvil-accent)]"
              : "text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] hover:text-[color:var(--anvil-foreground)]")
          }
        >
          {t(item.labelKey)}
        </button>
      </li>
    );
  };

  const currentLabel = groups
    .flatMap((g) => g.items)
    .find((i) => i.id === current);

  return (
    <div className="flex h-screen flex-col bg-[color:var(--anvil-background)] text-[color:var(--anvil-foreground)]">
      <header className="flex h-12 shrink-0 items-center gap-3 border-b border-[color:var(--anvil-border)] px-3">
        <div className="flex min-w-0 shrink-0 items-center gap-2">
          <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-[color:var(--anvil-accent-surface)]">
            <ShieldCheck className="h-4 w-4 text-[color:var(--anvil-accent)]" aria-hidden />
          </div>
          <span className="text-sm font-semibold tracking-wide">{t("app.name")}</span>
          <span className="hidden shrink-0 rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-muted)] sm:inline">
            {status?.platform ?? t("common.unknown")}
          </span>
        </div>

        <div className="min-w-0 flex-1">{search}</div>

        <div className="flex shrink-0 items-center gap-3 text-xs text-[color:var(--anvil-muted)]">
          {phaseLabel && (
            <span className="flex items-center gap-1.5" aria-live="polite">
              <span className={`h-1.5 w-1.5 rounded-full ${phaseLabel.tone}`} aria-hidden />
              {phaseLabel.label}
            </span>
          )}
          <span className="hidden truncate md:inline">
            {status?.provider ?? t("common.unknown")} / {status?.model ?? t("common.unknown")}
          </span>
          <button
            type="button"
            onClick={onToggleRight}
            className="rounded p-1.5 hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
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
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        <aside
          className="hidden w-60 shrink-0 flex-col overflow-hidden border-r border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] sm:flex"
          aria-label={t("nav.primaryLabel")}
        >
          <nav className="shrink-0 space-y-2 px-2 py-2">
            <ul className="space-y-0.5" aria-label={t("nav.primaryLabel")}>
              {groups[0].items.map(renderNavItem)}
            </ul>

            <div>
              <button
                type="button"
                onClick={() => setToolsOpen((v) => !v)}
                aria-expanded={toolsOpen}
                title={t("shell.toggleTools")}
                className="flex w-full items-center justify-between rounded px-2 py-1 text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
              >
                <span>{t("nav.toolsLabel")}</span>
                <span aria-hidden>{toolsOpen ? "–" : "+"}</span>
              </button>
              {toolsOpen ? (
                <ul className="space-y-0.5" aria-label={t("nav.toolsLabel")}>
                  {groups[1].items.map(renderNavItem)}
                </ul>
              ) : null}
            </div>
          </nav>

          <div className="min-h-0 flex-1 border-t border-[color:var(--anvil-border)]">
            {history}
          </div>

          <div className="shrink-0 border-t border-[color:var(--anvil-border)] px-3 py-2">
            {footer}
          </div>
        </aside>

        <main
          ref={mainRef}
          className="min-w-0 flex-1 overflow-y-auto"
          aria-label={currentLabel ? t(currentLabel.labelKey) : t("app.name")}
        >
          {children}
        </main>

        {rightOpen && (
          <aside
            className="hidden w-72 shrink-0 overflow-y-auto border-l border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] lg:block"
            aria-label={t("common.details")}
          >
            {right}
          </aside>
        )}
      </div>
    </div>
  );
}
