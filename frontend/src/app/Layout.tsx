// 应用外壳：顶栏 + 五入口 + 三栏。
//
// 三栏是固定结构而不是各视图自己拼：左栏是"我在哪个项目"，
// 中栏是"现在做什么"，右栏是"这次运行的细节"。
// 切成资源库或设置时左栏仍在——项目是全局上下文，
// 换视图就把它收走会让人忘记自己正在哪个工作区里操作。

import { useRef, type ReactNode } from "react";
import { PanelRightClose, PanelRightOpen, ShieldCheck } from "lucide-react";
import { VIEWS, nextView, type ViewId } from "./Router";
import type { Status } from "../types";
import { t } from "../i18n";

export interface LayoutProps {
  status: Status | null;
  view: ViewId;
  onNavigate: (view: ViewId) => void;
  left: ReactNode;
  right: ReactNode;
  rightOpen: boolean;
  onToggleRight: () => void;
  children: ReactNode;
  /** run 阶段，用于顶栏状态点。没有活跃 run 时为 null。 */
  phaseLabel?: { label: string; tone: string } | null;
}

export function Layout({
  status,
  view,
  onNavigate,
  left,
  right,
  rightOpen,
  onToggleRight,
  children,
  phaseLabel,
}: LayoutProps) {
  const current = VIEWS.find((v) => v.id === view) ?? VIEWS[0];
  // 五个入口始终都在 DOM 里（只是 tabIndex 不同），所以移动焦点
  // 不需要等下一次渲染——直接 focus 目标元素即可。
  const tabRefs = useRef<Partial<Record<ViewId, HTMLButtonElement | null>>>({});

  return (
    <div className="flex h-screen flex-col bg-[#0f1218] text-slate-200">
      <header className="flex h-12 shrink-0 items-center justify-between border-b border-white/10 px-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-sky-500/15">
            <ShieldCheck className="h-4 w-4 text-sky-400" aria-hidden />
          </div>
          <span className="text-sm font-semibold tracking-wide">{t("app.name")}</span>
          <span className="hidden shrink-0 rounded border border-white/10 px-1.5 py-0.5 text-[10px] text-slate-400 sm:inline">
            {status?.platform ?? "—"}
          </span>
        </div>

        <div className="flex shrink-0 items-center gap-3 text-xs text-slate-400">
          {phaseLabel && (
            <span className="flex items-center gap-1.5" aria-live="polite">
              <span className={`h-1.5 w-1.5 rounded-full ${phaseLabel.tone}`} aria-hidden />
              {phaseLabel.label}
            </span>
          )}
          <span className="hidden truncate md:inline">
            {status?.provider ?? "—"} / {status?.model ?? "—"}
          </span>
          <button
            onClick={onToggleRight}
            className="rounded p-1.5 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
            title={rightOpen ? "收起详情栏" : "展开详情栏"}
            aria-pressed={rightOpen}
            aria-label={rightOpen ? "收起详情栏" : "展开详情栏"}
          >
            {rightOpen ? (
              <PanelRightClose className="h-4 w-4" aria-hidden />
            ) : (
              <PanelRightOpen className="h-4 w-4" aria-hidden />
            )}
          </button>
        </div>
      </header>

      <nav
        role="tablist"
        aria-label="主导航"
        className="flex h-10 shrink-0 items-stretch gap-0.5 overflow-x-auto border-b border-white/10 px-2"
        onKeyDown={(e) => {
          // 用左右箭头在入口之间移动（tablist 的标准键盘行为），
          // 而不是要求用户逐个 Tab——五个入口逐个 Tab 会把手动输入区推远。
          if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
          e.preventDefault();
          const target = nextView(view, e.key === "ArrowRight" ? 1 : -1);
          onNavigate(target);
          tabRefs.current[target]?.focus();
        }}
      >
        {VIEWS.map((spec) => {
          const active = spec.id === view;
          return (
            <button
              key={spec.id}
              ref={(el) => {
                tabRefs.current[spec.id] = el;
              }}
              role="tab"
              aria-selected={active}
              tabIndex={active ? 0 : -1}
              title={spec.hint}
              onClick={() => onNavigate(spec.id)}
              className={
                "relative shrink-0 px-3 text-xs transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-sky-400 " +
                (active ? "text-sky-300" : "text-slate-400 hover:text-slate-200")
              }
            >
              {spec.label}
              {active && (
                <span
                  className="absolute inset-x-2 bottom-0 h-0.5 rounded-t bg-sky-400"
                  aria-hidden
                />
              )}
            </button>
          );
        })}
      </nav>

      <div className="flex min-h-0 flex-1">
        <aside
          className="hidden w-56 shrink-0 overflow-y-auto border-r border-white/10 bg-[#11151d] sm:block"
          aria-label="项目与会话"
        >
          {left}
        </aside>

        <main className="min-w-0 flex-1 overflow-y-auto" aria-label={current.label}>
          {children}
        </main>

        {rightOpen && (
          <aside
            className="hidden w-72 shrink-0 overflow-y-auto border-l border-white/10 bg-[#11151d] lg:block"
            aria-label="详情"
          >
            {right}
          </aside>
        )}
      </div>
    </div>
  );
}
