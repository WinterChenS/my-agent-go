// 内容澄清卡（U02 / D2b）。
//
// 澄清与工具审批是**两条通道**，不能长成同一张卡，也不能共用一个提交动作：
//   - 审批：放不放行某个工具调用 → ResolveApproval → approval.resolved（警示色、`.confirm` 形态）；
//   - 澄清：模型向用户问一个内容问题 → AnswerClarification → clarification.resolved
//     （强调色、`.inline-thil` 形态：图标 + kicker + 问题 + 说明 + 选项）。
//
// 两张卡的几何刻意不同：用户要靠形状分辨“这是授权，还是只是问我一句话”。
// 回答一个问题不会授予任何工具、站点或会话权限。这不是“小心一点”的问题：
// 一旦两者共用 ResolveApproval，用户回答“用 staging”就会顺手放行一条 shell 命令。
//
// 与审批一样：点击只表示已提交，卡片留到后端 clarification.resolved 到达。

import { useState } from "react";
import { MessageSquare, ShieldCheck } from "lucide-react";
import { t } from "../../i18n";
import { useBrowserOcclusionLayer } from "../../app/BrowserOcclusionCoordinator";
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

  // 与审批卡同一个理由：澄清卡也是盖在原生网页之上的应用浮层，必须让位。
  // 来源名固定，多张卡同时存在由协调器按来源计数。
  useBrowserOcclusionLayer("clarification-card", true);

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
      className="achat-thil"
    >
      <div className="achat-thil-mark" aria-hidden>
        <MessageSquare size={15} />
      </div>

      <div className="achat-thil-main">
        <div className="achat-thil-kicker">{t("chat.clarification.kicker")}</div>
        <h3 id={`clarification-${item.requestId}`} className="achat-thil-question" data-clarification-question="">
          {item.question}
        </h3>
        <p>{t("chat.clarification.pendingHint")}</p>

        {submitted ? (
          <p data-clarification-submitted="true" className="achat-thil-note">
            {t("chat.clarification.submitted")}
          </p>
        ) : null}

        {kind === "select" ? (
          <div className="achat-thil-options">
            {item.options.map((option) => (
              <button
                key={option}
                type="button"
                disabled={locked}
                onClick={() => onSubmit(item, { value: { text: option } })}
                className="achat-btn achat-btn-sm"
              >
                {option}
              </button>
            ))}
          </div>
        ) : null}

        {kind === "confirm" ? (
          <div className="achat-thil-options">
            <button
              type="button"
              disabled={locked}
              onClick={() => onSubmit(item, { confirmed: true })}
              className="achat-btn achat-btn-sm achat-btn-primary"
            >
              {t("chat.clarification.confirmYes")}
            </button>
            <button
              type="button"
              disabled={locked}
              onClick={() => onSubmit(item, { confirmed: false })}
              className="achat-btn achat-btn-sm"
            >
              {t("chat.clarification.confirmNo")}
            </button>
          </div>
        ) : null}

        {kind === "input" || kind === "editor" ? (
          <div className="achat-thil-answer">
            <label htmlFor={`clarification-input-${item.requestId}`} className="achat-hint">
              {t("chat.clarification.answerLabel")}
            </label>
            <textarea
              id={`clarification-input-${item.requestId}`}
              value={text}
              disabled={locked}
              onChange={(e) => setText(e.target.value)}
              placeholder={t("chat.clarification.placeholder")}
              rows={kind === "editor" ? 5 : 2}
              className="achat-thil-textarea"
            />
            <button
              type="button"
              disabled={locked || text.trim() === ""}
              onClick={submitValue}
              className="achat-btn achat-btn-sm achat-btn-primary"
              style={{ alignSelf: "flex-start" }}
            >
              {t("chat.clarification.submit")}
            </button>
          </div>
        ) : null}

        <div className="achat-thil-options">
          <button
            type="button"
            disabled={locked}
            onClick={() => onSubmit(item, { cancelled: true })}
            className="achat-btn achat-btn-sm achat-btn-ghost"
          >
            {t("chat.clarification.cancel")}
          </button>
        </div>

        <div className="achat-thil-note">
          <ShieldCheck size={11} aria-hidden />
          {t("chat.clarification.note")}
        </div>
      </div>
    </section>
  );
}
