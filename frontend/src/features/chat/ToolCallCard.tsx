// 工具调用卡（U02）。
//
// 一张卡只说两件事：**这个工具被调用了**，以及**结果是什么状态**。
// 状态是四态而不是两态：完成 / 失败 / 执行中 / 结果未知。
// “未知”这一态是必须的——停止或中断之后，有些调用既没有结果也不会再有结果，
// 把它画成“成功”就是在编造成果，画成“失败”同样是在编造。
//
// 参数默认收起：工具参数里经常有很长的命令与路径，全展开会把消息流淹掉。
// 颜色只用语义 token（`--anvil-*`），主题切换不需要第二套样式。

import { useState } from "react";
import { t } from "../../i18n";
import { toolOutcomeOf, type ToolOutcome, type UiToolCard } from "../../runState";

const OUTCOME_LABEL: Record<ToolOutcome, string> = {
  pending: "chat.tool.running",
  ok: "chat.tool.ok",
  error: "chat.tool.error",
  unknown: "chat.tool.unknown",
};

const OUTCOME_CLASS: Record<ToolOutcome, string> = {
  pending:
    "border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] text-[color:var(--anvil-accent)]",
  ok: "border-[color:var(--anvil-success-border)] bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
  error:
    "border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
  unknown:
    "border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
};

export function ToolCallCard({ card }: { card: UiToolCard }) {
  const [open, setOpen] = useState(false);
  const outcome = toolOutcomeOf(card);

  return (
    <article
      data-tool-card={card.toolCallId}
      data-run-id={card.runId}
      data-tool-outcome={outcome}
      className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
    >
      <header className="flex flex-wrap items-center gap-2">
        <span className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
          {t("chat.tool.title")}
        </span>
        <span className="font-mono text-[11px] text-[color:var(--anvil-foreground)]">{card.toolName}</span>
        <span
          data-tool-state={outcome}
          className={[
            "inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] leading-none",
            OUTCOME_CLASS[outcome],
          ].join(" ")}
        >
          {t(OUTCOME_LABEL[outcome])}
        </span>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="ml-auto rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-background)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          {open ? t("chat.tool.hideArgs") : t("chat.tool.showArgs")}
        </button>
      </header>

      {open ? (
        <div className="mt-2 space-y-1">
          <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {t("chat.tool.args")}
          </p>
          <pre className="max-h-40 overflow-auto rounded bg-[color:var(--anvil-background)] p-2 font-mono text-[10px] leading-relaxed text-[color:var(--anvil-foreground)]">
            {card.args}
          </pre>
        </div>
      ) : null}

      {outcome === "pending" || card.result === undefined || card.result === "" ? null : (
        <div className="mt-2 space-y-1">
          <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {t("chat.tool.result")}
          </p>
          <pre
            data-tool-result=""
            className="max-h-40 overflow-auto whitespace-pre-wrap rounded bg-[color:var(--anvil-background)] p-2 font-mono text-[10px] leading-relaxed text-[color:var(--anvil-foreground)]"
          >
            {card.result}
          </pre>
        </div>
      )}

      {outcome === "unknown" ? (
        <p className="mt-2 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("chat.tool.unknownHint")}
        </p>
      ) : null}
    </article>
  );
}
