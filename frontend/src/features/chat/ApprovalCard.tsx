// 工具审批卡（U02 / UI06）。
//
// 这一张卡是整个对话页最容易做错的地方，所以把纪律写在这里：
//
//   **点击 ≠ 已批准。**
//
// 点击只做一件事：把结论提交给后端（ResolveApproval）。提交成功后卡片标成
// “已提交，等待后端结论”并禁用按钮，但**继续留在原位**，直到后端
// approval.resolved 事件到达才移除（规格 §5.3、§11：乐观 UI 只能表示“请求已提交”）。
//
// 曾经的实现是点完就地移除，那会让界面在“后端其实拒绝了/根本没收到”时
// 显示成“授权成功”——用户以为工具已经能跑了，实际什么都没发生。
//
// 这个组件只接收一个已提交的本地标记（submitted），不做任何自行移除。

import { formatDate, t } from "../../i18n";
import type { UiApproval } from "../../runState";
import type { ApprovalChoice } from "../../types";

const CHOICES: { choice: ApprovalChoice; labelKey: string }[] = [
  { choice: "once", labelKey: "chat.approval.once" },
  { choice: "session", labelKey: "chat.approval.session" },
  { choice: "deny", labelKey: "chat.approval.deny" },
];

export interface ApprovalCardProps {
  item: UiApproval;
  /** 提交结论给后端；结果不由这里决定。 */
  onDecide: (item: UiApproval, choice: ApprovalChoice) => void;
  /** 没有 runId 时无法提交（例如会话还没开始跑）。 */
  disabled?: boolean;
}

export function ApprovalCard({ item, onDecide, disabled = false }: ApprovalCardProps) {
  const submitted = item.submitted === true;
  const locked = submitted || disabled;

  return (
    <section
      data-approval-card={item.approvalId}
      data-submitted={submitted ? "true" : "false"}
      aria-labelledby={`approval-${item.approvalId}`}
      className="rounded-md border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2.5 py-2"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h3
          id={`approval-${item.approvalId}`}
          className="text-[11px] font-medium text-[color:var(--anvil-foreground)]"
        >
          {t("chat.approval.title")}
        </h3>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.approval.tool")}</span>
        <span className="font-mono text-[11px] text-[color:var(--anvil-foreground)]">{item.toolName}</span>
        {submitted ? (
          <span
            data-approval-submitted="true"
            className="rounded-full border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
          >
            {t("chat.approval.submitted")}
          </span>
        ) : null}
      </header>

      <p className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("chat.approval.pendingHint")}
      </p>

      {item.expiresAtUnix > 0 ? (
        <p data-approval-expires="" className="mt-1 text-[10px] text-[color:var(--anvil-muted)]">
          {t("chat.approval.expiresAt")}
          {" · "}
          {formatDate(new Date(item.expiresAtUnix * 1000).toISOString())}
        </p>
      ) : null}

      <div className="mt-2 space-y-1">
        <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
          {t("chat.approval.arguments")}
        </p>
        <pre className="max-h-40 overflow-auto rounded bg-[color:var(--anvil-background)] p-2 font-mono text-[10px] leading-relaxed text-[color:var(--anvil-foreground)]">
          {item.args}
        </pre>
      </div>

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
