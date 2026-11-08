// 工具审批卡（U02 / D2b）。
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
// 几何来自原型 `assets/studio.css` 的 `.confirm`（警示头 + 正文 + 代码块 +
// 底部动作条）：头和底都是 `--surface-2` 面，正文在 `--surface` 上。原型的
// 三个动作是「拒绝 / 总是允许 / 允许本次」，与我们的 deny / session_scope / once
// 一一对应（同一组按钮，只是文案跟着 catalog 走）。

import { Shield } from "lucide-react";
import { formatDate, t } from "../../i18n";
import { useBrowserOcclusionLayer } from "../../app/BrowserOcclusionCoordinator";
import type { UiApproval } from "../../runState";
import type { ApprovalChoice } from "../../types";

/** 原型 .confirm-foot 的动作顺序：拒绝在左，主操作在右。 */
const CHOICES: { choice: ApprovalChoice; labelKey: string; primary?: boolean }[] = [
  { choice: "deny", labelKey: "chat.approval.deny" },
  { choice: "session_scope", labelKey: "chat.approval.session" },
  { choice: "once", labelKey: "chat.approval.once", primary: true },
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

  // 遮挡登记：原生网页是盖在右栏 slot 上的子窗口，**不会**被 DOM 的 z-index 挡住。
  // 卡片在挂载期间持有这一层（按来源引用计数），原生视图让位；卡片消失即恢复。
  // 这只是“看不见”，不接管控制权、不停止 run（隐藏面板 ≠ 暂停 agent）。
  useBrowserOcclusionLayer("approval-card", true);

  return (
    <section
      data-approval-card={item.approvalId}
      data-submitted={submitted ? "true" : "false"}
      aria-labelledby={`approval-${item.approvalId}`}
      className="achat-confirm"
    >
      <div className="achat-confirm-head">
        <Shield size={14} aria-hidden />
        <h3 id={`approval-${item.approvalId}`} className="achat-confirm-head-title">
          {t("chat.approval.title")}
        </h3>
        <span className="achat-badge achat-badge-warn" style={{ marginLeft: "auto" }}>
          {t("chat.approval.riskBadge")}
        </span>
      </div>

      <div className="achat-confirm-body">
        <p className="achat-confirm-desc">
          {t("chat.approval.requestedTool")}
          {" · "}
          <span className="achat-mono" style={{ color: "var(--anvil-foreground)" }}>
            {item.toolName}
          </span>
        </p>
        <p className="achat-confirm-desc">{t("chat.approval.pendingHint")}</p>
        {item.expiresAtUnix > 0 ? (
          <p data-approval-expires="" className="achat-hint">
            {t("chat.approval.expiresAt")}
            {" · "}
            {formatDate(new Date(item.expiresAtUnix * 1000).toISOString())}
          </p>
        ) : null}
        <pre className="achat-confirm-code" data-approval-args="">
          {item.args}
        </pre>
      </div>

      <div className="achat-confirm-foot">
        <span className="achat-confirm-foot-hint">
          {submitted ? t("chat.approval.submitted") : t("chat.approval.footHint")}
        </span>
        {submitted ? (
          <span data-approval-submitted="true" className="achat-badge">
            {t("chat.approval.submitted")}
          </span>
        ) : null}
        {CHOICES.map(({ choice, labelKey, primary }) => (
          <button
            key={choice}
            type="button"
            disabled={locked}
            onClick={() => onDecide(item, choice)}
            className={[
              "achat-btn",
              "achat-btn-sm",
              primary ? "achat-btn-primary" : choice === "deny" ? "achat-btn-danger" : "",
            ]
              .filter(Boolean)
              .join(" ")}
          >
            {t(labelKey)}
          </button>
        ))}
      </div>
    </section>
  );
}
