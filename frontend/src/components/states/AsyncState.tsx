// 异步区域的状态呈现（U00）。
//
// 存在的理由只有一条：**加载中绝不显示上一次的内容**。
// 分页/刷新时把旧列表留在屏幕上，会让用户对着已经过期的数据操作
// （典型事故：以为会话还在，点进去已经被删了）。
// 所以 pending 时只渲染加载态，children 根本不进 DOM。
//
// 六种状态各自可分辨：loading / empty / error / blocked / permission-denied / success。
// "被阻塞"和"没权限"与"失败"分开：前者要用户先做一件事，后者是这次操作错了。

import type { ReactNode } from "react";
import { AlertTriangle, Inbox, Loader2, ShieldAlert, Square } from "lucide-react";
import { formatError, t, type ErrorLike } from "../../i18n";

export type AsyncPhase =
  | "loading"
  | "success"
  | "empty"
  | "error"
  | "blocked"
  | "permission-denied";

export interface AsyncStateProps {
  phase: AsyncPhase;
  /** phase=error 时的结构化错误；code 决定文案（不显示 Go 的 message）。 */
  error?: ErrorLike | null;
  /** 覆盖加载文案（例如"正在读取会话…"）。 */
  loadingLabel?: string;
  /** 空态标题（默认来自 catalog）。 */
  emptyTitle?: string;
  emptyHint?: string;
  /** blocked / permission-denied 的原因：必须由调用方给出真实原因。 */
  reason?: string;
  /** 空态/阻塞态附带的一个动作按钮（可选）。 */
  action?: ReactNode;
  /** 失败或阻塞时的重试回调；没有它就不显示重试按钮。 */
  onRetry?: () => void;
  children?: ReactNode;
}

function RetryButton({ onRetry }: { onRetry: () => void }) {
  return (
    <button
      type="button"
      onClick={onRetry}
      className="mt-2.5 rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
    >
      {t("common.retry")}
    </button>
  );
}

/**
 * 渲染一个区域的状态。
 *
 * 约定：调用方只描述"现在是什么状态"（来自后端/请求结果），
 * 不从点击动作推断成功；失败必须带 code。
 */
export function AsyncState({
  phase,
  error,
  loadingLabel,
  emptyTitle,
  emptyHint,
  reason,
  action,
  onRetry,
  children,
}: AsyncStateProps) {
  switch (phase) {
    case "loading":
      // 只渲染加载态：旧内容不进 DOM（见文件头注释）。
      return (
        <div
          data-phase="loading"
          role="status"
          aria-live="polite"
          className="flex flex-col items-center justify-center gap-2 py-10 text-[color:var(--anvil-muted)]"
        >
          <Loader2 className="h-5 w-5 animate-spin" aria-hidden />
          <p className="text-xs">{loadingLabel ?? t("state.loading")}</p>
        </div>
      );

    case "error":
      return (
        <div
          data-phase="error"
          role="alert"
          className="rounded-xl border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] p-4"
        >
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--anvil-danger)]" aria-hidden />
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">
                {t("state.errorTitle")}
                {error?.code ? (
                  <span className="ml-2 rounded bg-[color:var(--anvil-overlay)] px-1.5 py-0.5 font-mono text-[10px]">
                    {error.code}
                  </span>
                ) : null}
              </p>
              <p className="mt-1 break-words text-xs leading-relaxed">{formatError(error)}</p>
              {onRetry ? <RetryButton onRetry={onRetry} /> : null}
            </div>
          </div>
        </div>
      );

    case "empty":
      return (
        <div
          data-phase="empty"
          className="flex flex-col items-center justify-center gap-2 px-6 py-10 text-center"
        >
          <div className="mb-1 flex h-9 w-9 items-center justify-center rounded-xl bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]">
            <Inbox className="h-4 w-4" aria-hidden />
          </div>
          <p className="text-sm text-[color:var(--anvil-foreground)]">
            {emptyTitle ?? t("state.emptyTitle")}
          </p>
          {emptyHint ? (
            <p className="max-w-md text-xs leading-relaxed text-[color:var(--anvil-muted)]">{emptyHint}</p>
          ) : null}
          {action ? <div className="mt-2">{action}</div> : null}
        </div>
      );

    case "blocked":
    case "permission-denied":
      return (
        <div
          data-phase={phase}
          role="alert"
          className="rounded-xl border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] p-4"
        >
          <div className="flex items-start gap-2">
            {phase === "blocked" ? (
              <Square className="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--anvil-warning)]" aria-hidden />
            ) : (
              <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--anvil-warning)]" aria-hidden />
            )}
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium">
                {phase === "blocked" ? t("state.blockedTitle") : t("state.permissionDeniedTitle")}
              </p>
              <p className="mt-1 break-words text-xs leading-relaxed">
                {reason ?? t(phase === "blocked" ? "state.blockedHint" : "state.permissionDeniedHint", {})}
              </p>
              {action ? <div className="mt-2.5">{action}</div> : null}
              {onRetry ? <RetryButton onRetry={onRetry} /> : null}
            </div>
          </div>
        </div>
      );

    case "success":
      return <div data-phase="success">{children ?? null}</div>;
  }
}
