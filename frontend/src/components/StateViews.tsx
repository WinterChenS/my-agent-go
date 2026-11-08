// 四类状态的统一呈现：空 / 加载 / 失败 / 无权限。
//
// UI01 要求这些状态可用且可分辨。做成统一组件而不是各视图各写一套，
// 是为了让"加载中"和"没有数据"永远长得不一样——
// 一个转圈和一句"暂无内容"如果同一个样子，用户会把失败当成空。

import type { ReactNode } from "react";
import { AlertTriangle, Inbox, Loader2, ShieldAlert } from "lucide-react";
import type { CallError } from "../types";
import { t } from "../i18n";

// 默认文案来自 catalog（`state.*`）：这几个状态是壳层共用组件，
// 写死中文会让 en-US 下的任何一页在出错时直接漏出中文（I18N04）。

export function LoadingState({ label }: { label?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-12 text-[color:var(--anvil-muted)]">
      <Loader2 className="h-5 w-5 animate-spin" aria-hidden />
      <p className="text-xs" role="status" aria-live="polite">
        {label ?? t("state.loading")}
      </p>
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  action,
  icon,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-12 text-center">
      <div className="mb-1 flex h-9 w-9 items-center justify-center rounded-xl bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]">
        {icon ?? <Inbox className="h-4 w-4" aria-hidden />}
      </div>
      <p className="text-sm text-[color:var(--anvil-foreground)]">{title}</p>
      {hint && <p className="max-w-md text-xs leading-relaxed text-[color:var(--anvil-muted)]">{hint}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

/**
 * 失败态。
 *
 * 必须同时显示 code 与 message：code 是给排查用的稳定标识，
 * message 是给人看的解释。只显示其一都会让下一次排查从零开始。
 */
export function ErrorState({
  error,
  onRetry,
  title,
}: {
  error: CallError | { code: string; message: string };
  onRetry?: () => void;
  title?: string;
}) {
  return (
    <div
      className="rounded-xl border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] p-4"
      role="alert"
    >
      <div className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--anvil-danger)]" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-[color:var(--anvil-danger)]">
            {title ?? t("state.errorTitle")}
            <span className="ml-2 rounded bg-[color:var(--anvil-danger-surface)] px-1.5 py-0.5 font-mono text-[10px] text-[color:var(--anvil-danger)]">
              {error.code}
            </span>
          </p>
          <p className="mt-1 break-words text-xs leading-relaxed text-[color:var(--anvil-danger)]">
            {error.message}
          </p>
          {onRetry && (
            <button
              onClick={onRetry}
              className="mt-2.5 rounded-md border border-[color:var(--anvil-danger-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
            >
              {t("common.retry")}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * 无权限态。
 *
 * 与失败态分开：失败意味着"做错了什么"，无权限意味着"需要你先做一件事"。
 * 后者一定要带可执行的动作，否则用户只能去翻文档。
 */
export function NoPermissionState({
  message,
  action,
}: {
  message: string;
  action?: ReactNode;
}) {
  return (
    <div className="rounded-xl border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] p-4" role="alert">
      <div className="flex items-start gap-2">
        <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-[color:var(--anvil-warning)]" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-[color:var(--anvil-warning)]">
            {t("state.permissionDeniedTitle")}
          </p>
          <p className="mt-1 break-words text-xs leading-relaxed text-[color:var(--anvil-warning)]">{message}</p>
          {action && <div className="mt-2.5">{action}</div>}
        </div>
      </div>
    </div>
  );
}
