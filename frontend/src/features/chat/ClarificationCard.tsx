// 内容澄清卡（U02 / UI06）。
//
// 澄清与工具审批是**两条通道**，不能长成同一张卡，也不能共用一个提交动作：
//   - 审批：放不放行某个工具调用 → ResolveApproval → approval.resolved；
//   - 澄清：模型向用户问一个内容问题 → AnswerClarification → clarification.resolved。
//
// 回答一个问题不会授予任何工具、站点或会话权限。这不是“小心一点”的问题：
// 一旦两者共用 ResolveApproval，用户回答“用 staging”就会顺手放行一条 shell 命令。
//
// 与审批一样：点击只表示已提交，卡片留到后端 clarification.resolved 到达。

import { useState } from "react";
import { t } from "../../i18n";
import type { ClarificationItem } from "../../runState";
import type { ChatClarificationResponse } from "./api";

export interface ClarificationCardProps {
  item: ClarificationItem;
  /** 提交答复（只走 AnswerClarification）。 */
  onSubmit: (item: ClarificationItem, response: ChatClarificationResponse) => void;
  disabled?: boolean;
}

export function ClarificationCard({ item, onSubmit, disabled = false }: ClarificationCardProps) {
  const [text, setText] = useState("");
  const submitted = item.submitted;
  const locked = submitted || disabled;
  const kind = item.kind;

  const submitValue = () => {
    const value = text.trim();
    if (!value) return;
    onSubmit(item, { value: { text: value } });
  };

  return (
    <section
      data-clarification-card={item.requestId}
      data-clarification-kind={kind}
      data-submitted={submitted ? "true" : "false"}
      aria-labelledby={`clarification-${item.requestId}`}
      className="rounded-md border border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] px-2.5 py-2"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h3
          id={`clarification-${item.requestId}`}
          className="text-[11px] font-medium text-[color:var(--anvil-foreground)]"
        >
          {t("chat.clarification.title")}
        </h3>
        {submitted ? (
          <span
            data-clarification-submitted="true"
            className="rounded-full border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
          >
            {t("chat.clarification.submitted")}
          </span>
        ) : null}
      </header>

      <p className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("chat.clarification.pendingHint")}
      </p>

      <p className="mt-2 text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
        {t("chat.clarification.question")}
      </p>
      <p data-clarification-question="" className="mt-0.5 text-[12px] leading-relaxed text-[color:var(--anvil-foreground)]">
        {item.question}
      </p>

      {kind === "select" ? (
        <div className="mt-2 space-y-1">
          <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {t("chat.clarification.chooseOption")}
          </p>
          <div className="flex flex-wrap gap-2">
            {item.options.map((option) => (
              <button
                key={option}
                type="button"
                disabled={locked}
                onClick={() => onSubmit(item, { value: { text: option } })}
                className={[
                  "rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]",
                  locked ? "cursor-not-allowed opacity-50" : "hover:bg-[color:var(--anvil-surface)]",
                ].join(" ")}
              >
                {option}
              </button>
            ))}
          </div>
        </div>
      ) : null}

      {kind === "confirm" ? (
        <div className="mt-2 flex flex-wrap gap-2">
          <button
            type="button"
            disabled={locked}
            onClick={() => onSubmit(item, { confirmed: true })}
            className={[
              "rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]",
              locked ? "cursor-not-allowed opacity-50" : "hover:bg-[color:var(--anvil-surface)]",
            ].join(" ")}
          >
            {t("chat.clarification.confirmYes")}
          </button>
          <button
            type="button"
            disabled={locked}
            onClick={() => onSubmit(item, { confirmed: false })}
            className={[
              "rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]",
              locked ? "cursor-not-allowed opacity-50" : "hover:bg-[color:var(--anvil-surface)]",
            ].join(" ")}
          >
            {t("chat.clarification.confirmNo")}
          </button>
        </div>
      ) : null}

      {kind === "input" || kind === "editor" ? (
        <div className="mt-2 space-y-1.5">
          <label
            htmlFor={`clarification-input-${item.requestId}`}
            className="block text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]"
          >
            {t("chat.clarification.answerLabel")}
          </label>
          <textarea
            id={`clarification-input-${item.requestId}`}
            value={text}
            disabled={locked}
            onChange={(e) => setText(e.target.value)}
            placeholder={t("chat.clarification.placeholder")}
            rows={kind === "editor" ? 5 : 2}
            className="w-full rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 text-xs text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          />
          <button
            type="button"
            disabled={locked || text.trim() === ""}
            onClick={submitValue}
            className={[
              "rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]",
              locked || text.trim() === "" ? "cursor-not-allowed opacity-50" : "hover:bg-[color:var(--anvil-surface)]",
            ].join(" ")}
          >
            {t("chat.clarification.submit")}
          </button>
        </div>
      ) : null}

      <div className="mt-2">
        <button
          type="button"
          disabled={locked}
          onClick={() => onSubmit(item, { cancelled: true })}
          className={[
            "rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-muted)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]",
            locked ? "cursor-not-allowed opacity-50" : "hover:bg-[color:var(--anvil-surface)]",
          ].join(" ")}
        >
          {t("chat.clarification.cancel")}
        </button>
      </div>
    </section>
  );
}
