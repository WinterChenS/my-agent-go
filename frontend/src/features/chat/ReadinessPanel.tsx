// 发送前提面板（UX1）。
//
// 为什么是面板而不是红 toast：toast 会自己消失，而这里的每一条都是用户必须
// 动手解决的前提。面板列出**全部**未满足项（不是只报第一条），每一条都说清
// 当前值、要做什么、以及跳到哪个设置分区；全部满足后整块消失。
//
// 文案全部来自 catalog：这一层只写 key 与后端事实（路径/版本/provider/模型）。

import { t } from "../../i18n";
import type { ReadinessBlocker, ReadinessReport } from "./readiness";

export interface ReadinessPanelProps {
  report: ReadinessReport;
  /** 正在重新探测：重试按钮禁用，但没有阻塞项时不渲染这块。 */
  checking: boolean;
  onNavigate: (route: string) => void;
  onRecheck: () => void;
}

/** 后端字段可能为空（版本读不出来）；空值显示 catalog 里的"未知"，不显示空白。 */
function orUnknown(value: string | undefined): string {
  const trimmed = (value ?? "").trim();
  return trimmed === "" ? t("common.unknown") : trimmed;
}

function itemText(blocker: ReadinessBlocker, field: "title" | "detail" | "next"): string {
  return t(`chat.readiness.codes.${blocker.code}.${field}`, {
    ...blocker.params,
    version: orUnknown(blocker.params.version),
    state: orUnknown(blocker.params.state),
  });
}

export function ReadinessPanel({ report, checking, onNavigate, onRecheck }: ReadinessPanelProps) {
  if (report.blockers.length === 0) return null;

  return (
    <section
      data-readiness-panel=""
      aria-labelledby="chat-readiness-title"
      className="space-y-2 border-t border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-3 py-2.5"
    >
      <header className="space-y-0.5">
        <h2 id="chat-readiness-title" className="text-[12px] font-medium text-[color:var(--anvil-warning)]">
          {t("chat.readiness.title", { count: report.blockers.length })}
        </h2>
        <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("chat.readiness.hint")}
        </p>
      </header>

      <ul className="space-y-1.5">
        {report.blockers.map((blocker) => (
          <li
            key={blocker.code}
            data-readiness-blocker={blocker.code}
            data-target={blocker.target.route}
            className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2.5 py-2"
          >
            <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
              {itemText(blocker, "title")}
            </p>
            <p data-readiness-value="" className="mt-0.5 break-all font-mono text-[10px] text-[color:var(--anvil-muted)]">
              {itemText(blocker, "detail")}
            </p>
            <p className="mt-0.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
              {itemText(blocker, "next")}
            </p>
            <button
              type="button"
              data-readiness-jump={blocker.code}
              onClick={() => onNavigate(blocker.target.route)}
              className="mt-1 rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
            >
              {t("chat.readiness.jump", {
                section: t(`chat.readiness.section.${blocker.target.section}`),
              })}
            </button>
          </li>
        ))}
      </ul>

      <div className="flex items-center gap-2">
        <button
          type="button"
          data-readiness-recheck=""
          onClick={onRecheck}
          disabled={checking}
          className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:cursor-not-allowed disabled:opacity-50"
        >
          {checking ? t("chat.readiness.checking") : t("chat.readiness.recheck")}
        </button>
      </div>
    </section>
  );
}
