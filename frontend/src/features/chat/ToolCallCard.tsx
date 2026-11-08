// 工具调用卡（U02 / D2b）。
//
// 一张卡只说两件事：**这个工具被调用了**，以及**结果是什么状态**。
// 状态是四态而不是两态：完成 / 失败 / 执行中 / 结果未知。
// “未知”这一态是必须的——停止或中断之后，有些调用既没有结果也不会再有结果，
// 把它画成“成功”就是在编造成果，画成“失败”同样是在编造。
//
// 几何来自原型 `assets/studio.css` 的 `.tool` / `.tool-row` / `.tool-ico` /
// `.tool-name` / `.tool-arg` / `.tool-body`：一行 38px 高的可点行（图标 20×20、
// 名字与参数都是 11.5px 等宽），展开后才出现参数与输出。参数里经常有很长的
// 命令与路径，默认展开会把消息流淹掉，所以默认收起。
//
// 原型的那一行右侧是耗时；我们没有可靠的耗时事实（后端事件里没有时间差），
// 所以这个位置放**结果状态**——它是后端给的，不是我们编的。

import { useState } from "react";
import { ChevronRight, TerminalSquare } from "lucide-react";
import { t } from "../../i18n";
import { toolOutcomeOf, type ToolOutcome, type UiToolCard } from "../../runState";

const OUTCOME_LABEL: Record<ToolOutcome, string> = {
  pending: "chat.tool.running",
  ok: "chat.tool.ok",
  error: "chat.tool.error",
  unknown: "chat.tool.unknown",
};

const OUTCOME_BADGE: Record<ToolOutcome, string> = {
  pending: "achat-badge achat-badge-accent",
  ok: "achat-badge achat-badge-success",
  error: "achat-badge achat-badge-danger",
  unknown: "achat-badge achat-badge-warn",
};

/**
 * 参数预览：后端给的 args 是 JSON 文本。
 *
 * 原型那一行显示 `pattern="api/v1/**" · 命中 3 个调用点`。我们没有“命中数”这种
 * 结果摘要，但参数本来就是事实，所以把前两个顶层字段压成 `k=v · k=v`；
 * 解不开 JSON 就退回第一行（是纯文本命令的情况）。太长由 CSS 省略号收口。
 */
function argPreview(args: string): string {
  const raw = args.trim();
  if (!raw) return "";
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const pairs = Object.entries(parsed as Record<string, unknown>)
        .slice(0, 2)
        .map(([key, value]) => {
          const text = typeof value === "string" ? value : JSON.stringify(value);
          const clipped = text.length > 64 ? `${text.slice(0, 64)}…` : text;
          return `${key}=${clipped}`;
        });
      if (pairs.length > 0) return pairs.join(" · ");
    }
  } catch {
    // 不是 JSON：退回第一行。
  }
  return raw.split("\n").map((line) => line.trim()).find((line) => line !== "") ?? "";
}

export function ToolCallCard({ card }: { card: UiToolCard }) {
  const [open, setOpen] = useState(false);
  const outcome = toolOutcomeOf(card);
  const preview = argPreview(card.args);
  const hasResult = card.result !== undefined && card.result !== "";

  return (
    <article
      data-tool-card={card.toolCallId}
      data-run-id={card.runId}
      data-tool-outcome={outcome}
      data-open={open ? "true" : "false"}
      className="achat-tool"
    >
      <button
        type="button"
        className="achat-tool-row"
        data-tool-toggle=""
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="achat-tool-ico" aria-hidden>
          <TerminalSquare size={12} />
        </span>
        <span className="achat-tool-name">{card.toolName}</span>
        {preview ? <span className="achat-tool-arg">{preview}</span> : null}
        <span data-tool-state={outcome} className={OUTCOME_BADGE[outcome]}>
          {t(OUTCOME_LABEL[outcome])}
        </span>
        <span className="achat-tool-caret" aria-hidden>
          <ChevronRight size={12} />
        </span>
      </button>

      {open ? (
        <pre className="achat-tool-body" data-tool-args="">
          {card.args}
          {hasResult ? `\n\n${t("chat.tool.result")}\n${card.result}` : ""}
          {hasResult ? null : `\n\n${t("chat.tool.noResult")}`}
        </pre>
      ) : null}

      {outcome === "unknown" ? (
        <p className="achat-hint" style={{ padding: "0 12px 9px" }}>
          {t("chat.tool.unknownHint")}
        </p>
      ) : null}
    </article>
  );
}
