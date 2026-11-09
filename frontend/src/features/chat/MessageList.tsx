// 消息流（U02 / D2b）。
//
// 渲染顺序就是事件顺序：用户气泡 → 助手块（头 + 流式文本 + 工具卡按到达顺序挂在
// 所属气泡下）→ 待审批卡 → 澄清卡。审批和澄清之所以排在消息之后而不是塞进
// 某条气泡里，是因为它们属于“此刻需要人做的事”，不是历史内容。
//
// 几何（原型 `assets/studio.css` 873–910 / `pages.css` 417–470）：
//   .thread-inner gap:26px  ← 轮次之间
//   .msg          gap:8px   ← 一轮之内（头部/思考/工具/正文/元信息）
//   .msg-user .bubble 背景 surface-2、r-md、padding 9px 13px、13.5px/1.65、max-width 88%
//   .agent-head  badge 22×22 r6 + name 12.5/560 + model 11 mono
// 这些数字由 chat-diff.mjs 实测，不在这里"顺手调一下"。
//
// 这里不做任何“聪明”的事：
//   - 不把工具结果当成“这一轮完成”（结论只来自终局 run.state）；
//   - 不把 assistant 文本里的问句猜成澄清（澄清只来自 clarification.requested）；
//   - 不把失败画成成功（错误卡 + 终局结论分开显示）。

import { AlertTriangle, CheckCircle2, CircleSlash } from "lucide-react";
import { formatError, t } from "../../i18n";
import {
  clarificationsOf,
  settledOf,
  type ApprovalResolution,
  type ClarificationResolution,
  type UiApproval,
  type UiState,
} from "../../runState";
import type { ApprovalChoice } from "../../types";
import type { UiMessage } from "../../runState";
import { ApprovalCard } from "./ApprovalCard";
import { BrowserApprovalCard } from "./BrowserApprovalCard";
import type { BrowserApprovalHandle, BrowserApprovalNote } from "./browserApproval";
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
  /**
   * 浏览器写动作的待审批（M1）。
   *
   * 它是**另一条通道**：来源是 PendingBrowserApprovals 的读取结果，结论走
   * ResolveBrowserApproval，与 run 的 approval.request/resolved 不共用。
   * 页面不属于当前 run 时由 ChatPage 传入 undefined（不显示别的会话的审批）。
   */
  browserApprovals?: BrowserApprovalHandle;
  /** 这一轮实际使用的模型（启动快照）；空 = 未知，显示“未知”而不是编一个。 */
  modelLabel?: string;
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
 * 错误卡（原型 `.conversation-state` 的虚线形态 + 危险语气）。
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
    <div data-run-error="" role="alert" className="achat-error-card">
      <span className="achat-error-icon" aria-hidden>
        <AlertTriangle size={14} />
      </span>
      <div className="achat-error-card-body">
        <b>{t("chat.error.banner")}</b>
        {code ? <p className="achat-error-code">{code}</p> : null}
        <p>{error}</p>
        <p>{t(errorHintKey(code))}</p>
        {retryable && onRetry ? (
          <div className="achat-thil-options">
            <button type="button" onClick={onRetry} className="achat-btn achat-btn-sm">
              {t("chat.error.retry")}
            </button>
            {onClear ? (
              <button type="button" onClick={onClear} className="achat-btn achat-btn-sm achat-btn-ghost">
                {t("chat.error.clear")}
              </button>
            ) : null}
          </div>
        ) : onClear ? (
          <div className="achat-thil-options">
            <button type="button" onClick={onClear} className="achat-btn achat-btn-sm achat-btn-ghost">
              {t("chat.error.clear")}
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/**
 * 一条浏览器审批“为什么没了”的文案。
 *
 * 卡片消失必须带原因：过期/撤销是“没有批准、没有派发”，而拒绝是用户的结论。
 * 让卡片默默消失就等于用“什么都没发生”表达一个安全结论。
 */
function browserApprovalNoteText(note: BrowserApprovalNote): string {
  const params = { tool: note.toolName, origin: note.origin };
  switch (note.kind) {
    case "approved":
      return note.choice === "session_scope"
        ? t("chat.browserApproval.noteApprovedSession", params)
        : t("chat.browserApproval.noteApproved", params);
    case "denied":
      return t("chat.browserApproval.noteDenied", params);
    case "expired":
      return t("chat.browserApproval.noteExpired", params);
    default:
      return t("chat.browserApproval.noteRevoked", params);
  }
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
  if (resolution.choice === "session_scope" || resolution.choice === "session") {
    return t("chat.approval.resolvedSession");
  }
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
  const Icon =
    settled === "completed" ? CheckCircle2 : settled === "cancelled" ? CircleSlash : AlertTriangle;
  return (
    <div data-settled={settled} data-role="system" className="achat-state-row">
      <span className="achat-state-icon" aria-hidden>
        <Icon size={14} />
      </span>
      <div>
        <b>
          {t("chat.settled")}
          {" · "}
          {t(`status.${settled}`)}
        </b>
        <span>{t("chat.settledHint")}</span>
      </div>
    </div>
  );
}

// messageLabel 为每条消息造一个可访问名。
//
// 只写"用户/助手"不够：读屏时连续几条同名节点分不清是哪一条；带上正文摘要
// （前 60 字符）既能定位，也不会把整段长文塞进 aria-label。
function messageLabel(message: UiMessage, isUser: boolean): string {
  const prefix = t(isUser ? "chat.message.byUser" : "chat.message.byAssistant");
  const text = (message.text ?? "").replace(/\s+/g, " ").trim();
  if (!text) return prefix;
  const brief = text.length > 60 ? text.slice(0, 60) + "…" : text;
  return prefix + "：" + brief;
}

export function MessageList({
  ui,
  onDecide,
  onClarify,
  canRespond,
  onRetry,
  onClearError,
  browserApprovals,
  modelLabel,
}: MessageListProps) {
  const approvals = ui.approvals;
  const clarifications = clarificationsOf(ui);
  const settled = settledOf(ui);
  const browserItems = browserApprovals?.items ?? [];
  const browserNotes = browserApprovals?.notes ?? [];

  if (
    ui.messages.length === 0 &&
    approvals.length === 0 &&
    clarifications.length === 0 &&
    browserItems.length === 0 &&
    browserNotes.length === 0 &&
    !ui.error
  ) {
    return (
      <div className="achat-empty" data-message-list="">
        <b>{t("chat.emptyTitle")}</b>
        <p>{t("chat.emptyHint")}</p>
      </div>
    );
  }

  return (
    <div className="achat-msg-list" data-message-list="" role="log" aria-label={t("chat.transcript")} aria-live="polite">
      {ui.messages.map((message) => {
        const isUser = message.role === "user";
        return (
          <div
            key={message.id}
            // 消息必须有可访问结构：只靠视觉样式时，读屏软件和 UI 自动化都读不到
            // 对话内容（实测：UIA 里主区只有输入框/发送，整段转录不可寻址）。
            // role=article + 可访问名（谁说的 + 摘要）把每条消息变成一个可定位的单元。
            role="article"
            aria-label={messageLabel(message, isUser)}
            aria-roledescription={t("chat.message.role")}
            data-message-role={message.role}
            data-role={message.role}
            data-run-id={message.runId ?? undefined}
            className="achat-msg"
          >
            {isUser ? (
              <>
                {message.text ? (
                  <div data-message-text="" className="achat-bubble">
                    {message.text}
                  </div>
                ) : null}
                <div className="achat-msg-meta">
                  <span>{t("chat.role.user")}</span>
                </div>
              </>
            ) : (
              <>
                <div className="achat-agent-head">
                  <span className="achat-agent-badge" aria-hidden>
                    {t("chat.agent.badge")}
                  </span>
                  <span className="achat-agent-name">{t("chat.role.assistant")}</span>
                  {modelLabel ? <span className="achat-agent-model">{modelLabel}</span> : null}
                  {message.streaming ? (
                    <span className="achat-badge achat-badge-success">
                      <i className="achat-dot" aria-hidden />
                      {t("chat.streamingBadge")}
                    </span>
                  ) : null}
                </div>

                {message.text ? (
                  <div data-message-text="" className="achat-prose">
                    {message.text}
                  </div>
                ) : null}

                {message.tools.map((card) => (
                  <ToolCallCard key={`${card.runId}:${card.toolCallId}`} card={card} />
                ))}

                {message.streaming ? (
                  <div data-streaming="true" className="achat-stream-status" aria-live="polite">
                    <span className="achat-live-dot" aria-hidden />
                    <span>{t("chat.streaming")}</span>
                  </div>
                ) : null}
              </>
            )}
          </div>
        );
      })}

      {/* 审批与澄清排在这里：它们属于“此刻要人做的事”，顺序与事件到达一致。 */}
      {approvals.map((item) => (
        <ApprovalCard key={item.approvalId} item={item} onDecide={onDecide} disabled={!canRespond} />
      ))}
      {/* 浏览器写动作：另一条通道（读取 + ResolveBrowserApproval），所以单独一组。
          卡片只在后端读取结果里不再有这条时消失，原因写在下面的结论行里。 */}
      {browserItems.length > 0 ? (
        <div data-browser-approvals="" className="achat-msg-list">
          {browserItems.map((item) => (
            <BrowserApprovalCard
              key={item.approvalId}
              item={item}
              submitted={browserApprovals?.submitted[item.approvalId]}
              error={browserApprovals?.errors[item.approvalId] ?? null}
              onDecide={(target, choice) => browserApprovals?.decide(target, choice)}
            />
          ))}
        </div>
      ) : null}
      {browserNotes.map((note) => (
        <p
          key={`${note.approvalId}:${note.kind}`}
          data-browser-approval-note={note.kind}
          className="achat-hint"
        >
          {browserApprovalNoteText(note)}
        </p>
      ))}
      {browserApprovals?.hint ? (
        <p
          data-browser-approval-read-failed={browserApprovals.hint.code}
          role="alert"
          className="achat-hint achat-hint-danger"
        >
          {t("chat.browserApproval.readFailed", { reason: formatError(browserApprovals.hint) })}
        </p>
      ) : null}
      {clarifications.map((item) => (
        <ClarificationCard key={item.requestId} item={item} onSubmit={onClarify} disabled={!canRespond} />
      ))}

      {ui.lastApprovalResolution ? (
        <p data-approval-resolution={ui.lastApprovalResolution.choice} className="achat-hint">
          {approvalResolutionText(ui.lastApprovalResolution)}
        </p>
      ) : null}

      {ui.lastClarificationResolution ? (
        <p
          data-clarification-resolution={ui.lastClarificationResolution.outcome}
          className="achat-hint"
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
