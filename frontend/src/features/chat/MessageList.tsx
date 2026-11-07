// 消息流（U02）。
//
// 渲染顺序就是事件顺序：用户气泡 → 助手气泡（流式文本 + 工具卡按到达顺序挂在
// 所属气泡下）→ 待审批卡 → 澄清卡。审批和澄清之所以排在消息之后而不是塞进
// 某条气泡里，是因为它们属于“此刻需要人做的事”，不是历史内容。
//
// 这里不做任何“聪明”的事：
//   - 不把工具结果当成“这一轮完成”（结论只来自终局 run.state）；
//   - 不把 assistant 文本里的问句猜成澄清（澄清只来自 clarification.requested）；
//   - 不把失败画成成功（失败横幅 + 终局结论分开显示）。

import { t } from "../../i18n";
import {
  clarificationsOf,
  settledOf,
  type ApprovalResolution,
  type ClarificationResolution,
  type UiApproval,
  type UiState,
} from "../../runState";
import type { ApprovalChoice } from "../../types";
import { ApprovalCard } from "./ApprovalCard";
import { ClarificationCard } from "./ClarificationCard";
import type { ChatClarificationResponse } from "./api";
import type { ClarificationItem } from "../../runState";
import { ToolCallCard } from "./ToolCallCard";

export interface MessageListProps {
  ui: UiState;
  onDecide: (item: UiApproval, choice: ApprovalChoice) => void;
  onClarify: (item: ClarificationItem, response: ChatClarificationResponse) => void;
  /** 有活跃 run 才能提交审批/澄清。 */
  canRespond: boolean;
  /** 上一轮失败后重试（重发最后一条用户消息）。没有可重试内容时为 undefined。 */
  onRetry?: () => void;
  onClearError?: () => void;
}

/** 错误码 → 下一步提示。未知错误码只说“按错误码排查”，不猜原因。 */
function errorHintKey(code: string | null | undefined): string {
  switch (code) {
    case "BUDGET_EXCEEDED":
      return "chat.error.hintBudget";
    case "COMPATIBILITY":
      return "chat.error.hintCompatibility";
    case "PROVIDER_ERROR":
    case "PROVIDER_UNREACHABLE":
      return "chat.error.hintProvider";
    default:
      return "chat.error.hintGeneric";
  }
}

/**
 * 错误横幅。
 *
 * 只显示后端给的 code 与 message：把 message 翻译成“发送失败”会掩盖
 * “工作区未信任”和“Pi 不存在”需要完全不同的下一步动作。
 */
export function RunErrorBanner({
  error,
  code,
  onRetry,
  onClear,
  retryable = true,
}: {
  error: string;
  code?: string | null;
  onRetry?: () => void;
  onClear?: () => void;
  retryable?: boolean;
}) {
  return (
    <div
      data-run-error=""
      role="alert"
      className="rounded-md border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2.5 py-2"
    >
      <p className="text-[11px] font-medium text-[color:var(--anvil-danger)]">{t("chat.error.banner")}</p>
      {code ? (
        <p className="mt-1 font-mono text-[10px] text-[color:var(--anvil-foreground)]">
          {t("chat.error.code")}
          {" · "}
          {code}
        </p>
      ) : null}
      <p className="mt-1 break-words text-[11px] leading-relaxed text-[color:var(--anvil-foreground)]">
        {error}
      </p>
      <p className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t(errorHintKey(code))}
      </p>
      <div className="mt-2 flex flex-wrap gap-2">
        {retryable && onRetry ? (
          <button
            type="button"
            onClick={onRetry}
            className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            {t("chat.error.retry")}
          </button>
        ) : null}
        {onClear ? (
          <button
            type="button"
            onClick={onClear}
            className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            {t("chat.error.clear")}
          </button>
        ) : null}
      </div>
    </div>
  );
}

/**
 * 后端对一条审批的结论文案。
 *
 * 卡片消失必须伴随一句“为什么”：后端拒绝时往往带理由（例如这一轮已经结束），
 * 默默消失会让用户以为授权成功了。
 */
function approvalResolutionText(resolution: ApprovalResolution): string {
  if (resolution.choice === "deny") {
    return resolution.reason
      ? t("chat.approval.resolvedWithReason", { reason: resolution.reason })
      : t("chat.approval.resolvedDeny");
  }
  if (resolution.choice === "session") return t("chat.approval.resolvedSession");
  if (resolution.choice === "once") return t("chat.approval.resolvedOnce");
  return t("chat.approval.resolvedDeny");
}

/**
 * 后端对一条澄清的收口文案。
 *
 * 超时是很常见的一种收口：不说清楚，用户只会看到卡片“自己消失了”。
 */
function clarificationResolutionText(resolution: ClarificationResolution): string {
  if (resolution.reason === "timeout") return t("chat.clarification.expired");
  return resolution.outcome;
}

/** 终局结论条：只有 settledOf() 非空才渲染（即确实收到过终局 run.state）。 */
export function SettledBanner({ settled }: { settled: NonNullable<UiState["settled"]> }) {
  const tone =
    settled === "completed"
      ? "border-[color:var(--anvil-success-border)] bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]"
      : settled === "cancelled"
        ? "border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]"
        : "border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]";
  return (
    <div data-settled={settled} className={["rounded-md border px-2.5 py-1.5", tone].join(" ")}>
      <p className="text-[11px] font-medium">
        {t("chat.settled")}
        {" · "}
        {t(`status.${settled}`)}
      </p>
      <p className="mt-0.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("chat.settledHint")}
      </p>
    </div>
  );
}

export function MessageList({
  ui,
  onDecide,
  onClarify,
  canRespond,
  onRetry,
  onClearError,
}: MessageListProps) {
  const approvals = ui.approvals;
  const clarifications = clarificationsOf(ui);
  const settled = settledOf(ui);

  if (ui.messages.length === 0 && approvals.length === 0 && clarifications.length === 0 && !ui.error) {
    return (
      <div className="flex h-full items-center justify-center px-4 py-10 text-center">
        <div>
          <p className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
            {t("chat.emptyTitle")}
          </p>
          <p className="mt-1 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("chat.emptyHint")}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-3" data-message-list="">
      {ui.messages.map((message) => (
        <div
          key={message.id}
          data-message-role={message.role}
          data-run-id={message.runId ?? undefined}
          className="space-y-2"
        >
          <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {message.role === "user" ? t("chat.role.user") : t("chat.role.assistant")}
          </p>
          {message.text ? (
            <p
              data-message-text=""
              className="whitespace-pre-wrap break-words text-[12px] leading-relaxed text-[color:var(--anvil-foreground)]"
            >
              {message.text}
            </p>
          ) : null}
          {message.streaming ? (
            <p data-streaming="true" className="text-[10px] text-[color:var(--anvil-muted)]">
              {t("chat.streaming")}
            </p>
          ) : null}
          {message.tools.map((card) => (
            <ToolCallCard key={`${card.runId}:${card.toolCallId}`} card={card} />
          ))}
        </div>
      ))}

      {/* 审批与澄清排在这里：它们属于“此刻要人做的事”，顺序与事件到达一致。 */}
      {approvals.map((item) => (
        <ApprovalCard key={item.approvalId} item={item} onDecide={onDecide} disabled={!canRespond} />
      ))}
      {clarifications.map((item) => (
        <ClarificationCard key={item.requestId} item={item} onSubmit={onClarify} disabled={!canRespond} />
      ))}

      {ui.lastApprovalResolution ? (
        <p data-approval-resolution={ui.lastApprovalResolution.choice} className="text-[10px] text-[color:var(--anvil-muted)]">
          {approvalResolutionText(ui.lastApprovalResolution)}
        </p>
      ) : null}

      {ui.lastClarificationResolution ? (
        <p
          data-clarification-resolution={ui.lastClarificationResolution.outcome}
          className="text-[10px] text-[color:var(--anvil-muted)]"
        >
          {clarificationResolutionText(ui.lastClarificationResolution)}
        </p>
      ) : null}

      {ui.error ? (
        <RunErrorBanner
          error={ui.error}
          code={ui.errorCode ?? null}
          onRetry={onRetry}
          onClear={onClearError}
          retryable={ui.errorCode !== "COMPATIBILITY"}
        />
      ) : null}

      {settled ? <SettledBanner settled={settled} /> : null}
    </div>
  );
}
