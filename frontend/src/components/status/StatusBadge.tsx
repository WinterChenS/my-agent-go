// 领域状态徽标（U00）。
//
// 唯一入口是**类型化的领域状态**。这里刻意不接受布尔值、也不接受
// "刚点过连接按钮"这类事件信息：原型里那种"点击后徽标变绿 = connected"
// 是 V1 明确禁止的假状态（规格 §5.1：不得从点击推断连接成功）。
//
// 文案来自 catalog（`status.*`），颜色来自语义 token，因此中英文与深浅主题
// 都不需要各写一套。

import { t } from "../../i18n";
import type { RunState } from "../../types";

/**
 * 可展示的领域状态。
 *
 * 前半是 run 状态（与 contracts.RunState 对齐），后半是资源/连接状态。
 * 新增状态必须同时补 `status.*` 消息与 tone，否则 typecheck 会失败。
 */
export type DomainStatus =
  /** 还没有任何运行（前端 phase 的初始值，不是后端 run 状态）。 */
  | "idle"
  | RunState
  | "connected"
  | "disconnected"
  | "degraded"
  | "unknown"
  | "unavailable"
  | "not_implemented";

export type StatusTone = "neutral" | "info" | "success" | "warning" | "danger";

const TONE_BY_STATUS: Record<DomainStatus, StatusTone> = {
  idle: "neutral",
  queued: "info",
  starting: "info",
  running: "info",
  waiting_approval: "warning",
  stopping: "warning",
  completed: "success",
  failed: "danger",
  cancelled: "neutral",
  interrupted: "danger",
  connected: "success",
  disconnected: "neutral",
  degraded: "warning",
  unknown: "neutral",
  unavailable: "neutral",
  not_implemented: "neutral",
};

const TONE_CLASS: Record<StatusTone, string> = {
  neutral:
    "border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]",
  info: "border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] text-[color:var(--anvil-accent)]",
  success:
    "border-[color:var(--anvil-success-border)] bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
  warning:
    "border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
  danger:
    "border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
};

export function statusTone(status: DomainStatus): StatusTone {
  return TONE_BY_STATUS[status] ?? "neutral";
}

export interface StatusBadgeProps {
  status: DomainStatus;
  /** 覆盖 tone 只用于"同一状态在不同上下文里强调不同"；不改变文案。 */
  tone?: StatusTone;
  className?: string;
}

/**
 * 状态徽标。
 *
 * 传进来的必须是后端/事件给出的状态；界面不得自行推导
 * （例如"按钮点过了" ⇒ connected）。
 */
export function StatusBadge({ status, tone, className }: StatusBadgeProps) {
  const resolved = tone ?? statusTone(status);
  return (
    <span
      data-status={status}
      data-tone={resolved}
      role="status"
      className={[
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] leading-none",
        TONE_CLASS[resolved],
        className ?? "",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      {t(`status.${status}`)}
    </span>
  );
}
