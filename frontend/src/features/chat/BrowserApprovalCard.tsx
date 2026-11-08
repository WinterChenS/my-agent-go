// 浏览器写动作审批卡（M1 / BR07.tool · 界面侧）。
//
// 与工具审批卡（ApprovalCard）同一套纪律，但**通道不同**：
//   - 工具审批走 run 的 approval.request / approval.resolved（后端事件）；
//   - 浏览器审批走 PendingBrowserApprovals 的读取结果，结论走 ResolveBrowserApproval。
// 两条通道不能合并：合并就会让"允许一次 bash"顺手放行一次浏览器点击。
//
// 这张卡显示的四个事实都是用户判断"要不要批"所必需的：
//   工具名、目标 origin、动作摘要（kind/ref + 文档身份）、到期时间；
// 另加"范围"说明：once 只放行这一条，session_scope 是本会话（按工具，不按站点）。
//
// 卡片自己**不**决定移除：点击只是把结论提交给后端，卡片留在原地标"已提交"，
// 直到后端读取结果里不再有这条（见 browserApproval.ts 的归约器）。

import { formatDate, formatError, t } from "../../i18n";
import { useBrowserOcclusionLayer } from "../../app/BrowserOcclusionCoordinator";
import type { CallError } from "../../types";
import {
  actionSummary,
  type BrowserApprovalChoice,
  type BrowserApprovalItem,
} from "./browserApproval";

const CHOICES: { choice: BrowserApprovalChoice; labelKey: string }[] = [
  { choice: "once", labelKey: "chat.approval.once" },
  { choice: "session_scope", labelKey: "chat.approval.session" },
  { choice: "deny", labelKey: "chat.approval.deny" },
];

export interface BrowserApprovalCardProps {
  item: BrowserApprovalItem;
  /** 已提交的结论（只表示"请求已提交"，不是放行）。 */
  submitted?: BrowserApprovalChoice;
  /** 最近一次提交失败的原因（卡片不因此消失）。 */
  error?: CallError | null;
  onDecide: (item: BrowserApprovalItem, choice: BrowserApprovalChoice) => void;
  disabled?: boolean;
}

function expiresAtText(unix: number): string {
  if (unix <= 0) return "";
  const d = new Date(unix * 1000);
  if (Number.isNaN(d.getTime())) return "";
  return formatDate(d.toISOString());
}

export function BrowserApprovalCard({
  item,
  submitted,
  error = null,
  onDecide,
  disabled = false,
}: BrowserApprovalCardProps) {
  const locked = Boolean(submitted) || disabled;

  // 遮挡登记：原生网页盖在右栏 slot 上，DOM 的 z-index 挡不住它。
  // 卡片在挂载期间持有这一层；它只让网页让位，不接管控制权、不停止 run。
  useBrowserOcclusionLayer("browser-approval-card", true);

  const args = item.arguments ?? {};

  return (
    <section
      data-browser-approval-card={item.approvalId}
      data-submitted={submitted ? "true" : "false"}
      aria-labelledby={`browser-approval-${item.approvalId}`}
      className="rounded-md border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2.5 py-2"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h3
          id={`browser-approval-${item.approvalId}`}
          className="text-[11px] font-medium text-[color:var(--anvil-foreground)]"
        >
          {t("chat.browserApproval.title")}
        </h3>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">
          {t("chat.approval.tool")}
        </span>
        <span
          data-browser-approval-tool=""
          className="font-mono text-[11px] text-[color:var(--anvil-foreground)]"
        >
          {item.toolName}
        </span>
        {submitted ? (
          <span
            data-browser-approval-submitted="true"
            className="rounded-full border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
          >
            {t("chat.approval.submitted")}
          </span>
        ) : null}
      </header>

      <dl className="mt-1 space-y-0.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        <div>
          <dt className="inline">{t("chat.browserApproval.origin")}：</dt>
          <dd
            data-browser-approval-origin={item.origin}
            className="inline break-all font-mono text-[color:var(--anvil-foreground)]"
          >
            {item.origin || t("chat.browserApproval.originUnknown")}
          </dd>
        </div>
        <div>
          <dt className="inline">{t("chat.browserApproval.action")}：</dt>
          <dd
            data-browser-approval-summary=""
            className="inline break-all font-mono text-[color:var(--anvil-foreground)]"
          >
            {actionSummary(item)}
          </dd>
        </div>
        {item.url ? (
          <div>
            <dt className="inline">{t("chat.browserApproval.url")}：</dt>
            <dd data-browser-approval-url="" className="inline break-all font-mono">
              {item.url}
            </dd>
          </div>
        ) : null}
        <div>
          <dt className="inline">{t("chat.browserApproval.scope")}：</dt>
          <dd data-browser-approval-scope="" className="inline">
            {t("chat.browserApproval.scopeHint")}
          </dd>
        </div>
        {item.expiresAtUnix > 0 ? (
          <div>
            <dt className="inline">{t("chat.approval.expiresAt")}：</dt>
            <dd data-browser-approval-expires={String(item.expiresAtUnix)} className="inline">
              {expiresAtText(item.expiresAtUnix)}
            </dd>
          </div>
        ) : null}
      </dl>

      <p className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("chat.browserApproval.pendingHint")}
      </p>

      <div className="mt-2 space-y-1">
        <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
          {t("chat.browserApproval.arguments")}
        </p>
        <pre className="max-h-40 overflow-auto rounded bg-[color:var(--anvil-background)] p-2 font-mono text-[10px] leading-relaxed text-[color:var(--anvil-foreground)]">
          {JSON.stringify(args, null, 2)}
        </pre>
      </div>

      {error ? (
        <p
          data-browser-approval-error={error.code}
          role="alert"
          className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-danger)]"
        >
          {t("chat.browserApproval.resolveFailed", { reason: formatError(error) })}
        </p>
      ) : null}

      <div className="mt-2 flex flex-wrap gap-2">
        {CHOICES.map(({ choice, labelKey }) => (
          <button
            key={choice}
            type="button"
            disabled={locked}
            onClick={() => onDecide(item, choice)}
            className={[
              "rounded-md border px-2.5 py-1 text-xs focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]",
              choice === "deny"
                ? "border-[color:var(--anvil-danger-border)] text-[color:var(--anvil-danger)]"
                : "border-[color:var(--anvil-border)] text-[color:var(--anvil-foreground)]",
              locked ? "cursor-not-allowed opacity-50" : "hover:bg-[color:var(--anvil-surface)]",
            ].join(" ")}
          >
            {t(labelKey)}
          </button>
        ))}
      </div>
    </section>
  );
}
