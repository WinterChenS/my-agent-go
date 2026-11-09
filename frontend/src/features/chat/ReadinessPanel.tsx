// 发送前提面板（UX1）。
//
// 为什么是面板而不是红 toast：toast 会自己消失，而这里的每一条都是用户必须
// 动手解决的前提。面板列出**全部**未满足项（不是只报第一条），每一条都说清
// 当前值、要做什么、以及跳到哪个设置分区；全部满足后整块消失。
//
// 工作区未信任这一条是个例外：它是**可以就地解决**的（信任只覆盖"执行"），
// 让用户自己跑去设置里找那把锁，是把界面自己的阻塞丢给用户去翻译
// （2026-11-08 用户实测：对话页只给"去设置 · 权限（工作区）"，人被赶走）。
// 所以这条给主按钮「信任此工作区」（可访问名里带被信任的绝对路径），
// 设置链接降为次级入口——留着只是给想看完整权限对照的人。
//
// 文案全部来自 catalog：这一层只写 key 与后端事实（路径/版本/provider/模型）。

import { useCallback, useState } from "react";
import { t } from "../../i18n";
import { formatError } from "../../i18n";
import type { CallError } from "../../types";
import { toCallError } from "../../app/useBackend";
import type { ReadinessBlocker, ReadinessReport } from "./readiness";

export interface ReadinessPanelProps {
  report: ReadinessReport;
  /** 正在重新探测：重试按钮禁用，但没有阻塞项时不渲染这块。 */
  checking: boolean;
  onNavigate: (route: string) => void;
  onRecheck: () => void;
  /**
   * 就地授权：把某个工作区标成已信任。
   *
   * 这里只转发动作，不改本地结论——成功之后由壳层重新读工作区事实，
   * 页面据此自动重算就绪（不刷新页面，也不"点了就算信任了"）。
   *
   * 写成方法签名而不是箭头函数类型：`=> Promise<...>` 里的 `>Promise<`
   * 会被 UI 文案扫描器当成一段 JSX 文本（与 ShellValue 里同一处理）。
   */
  onTrustWorkspace?(workspaceId: string): Promise<void>;
}

/** 后端字段可能为空（版本读不出来）；空值显示 catalog 里的"未知"，不显示空白。 */
function orUnknown(value: string | undefined): string {
  const trimmed = (value ?? "").trim();
  return trimmed === "" ? t("common.unknown") : trimmed;
}

function itemText(blocker: ReadinessBlocker, field: "title" | "detail" | "next"): string {
  return t(`chat.readiness.codes.${blocker.code}.${field}`, {
    ...blocker.params,
    version: orUnknown(blocker.params.version),
    state: orUnknown(blocker.params.state),
  });
}

export function ReadinessPanel({
  report,
  checking,
  onNavigate,
  onRecheck,
  onTrustWorkspace,
}: ReadinessPanelProps) {
  // 正在信任的那一条（按 blocker code 记）：只为禁掉重复点击，不是"已信任"状态。
  const [trusting, setTrusting] = useState<string | null>(null);
  const [trustError, setTrustError] = useState<{ code: string; error: CallError } | null>(null);

  const trust = useCallback(
    async (code: string, workspaceId: string) => {
      if (!onTrustWorkspace) return;
      setTrusting(code);
      setTrustError(null);
      try {
        await onTrustWorkspace(workspaceId);
      } catch (e) {
        // 失败要说清是哪一条失败，不能只留一句"操作失败"。
        setTrustError({ code, error: toCallError(e) });
      } finally {
        setTrusting(null);
      }
    },
    [onTrustWorkspace]
  );

  if (report.blockers.length === 0) return null;

  return (
    <section
      data-readiness-panel=""
      aria-labelledby="chat-readiness-title"
      className="space-y-2 border-t border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-3 py-2.5"
    >
      {/* 卡片自己的标题行不是文档级的 <header>：chat 路由的 header 只能是壳层顶栏那一条（D2c）。 */}
      <div className="space-y-0.5">
        <h2 id="chat-readiness-title" className="text-[12px] font-medium text-[color:var(--anvil-warning)]">
          {t("chat.readiness.title", { count: report.blockers.length })}
        </h2>
        <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("chat.readiness.hint")}
        </p>
      </div>

      <ul className="space-y-1.5">
        {report.blockers.map((blocker) => {
          const action = blocker.action;
          const busy = trusting === blocker.code;
          return (
            <li
              key={blocker.code}
              data-readiness-blocker={blocker.code}
              data-target={blocker.target.route}
              className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2.5 py-2"
            >
              <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
                {itemText(blocker, "title")}
              </p>
              <p data-readiness-value="" className="mt-0.5 break-all font-mono text-[10px] text-[color:var(--anvil-muted)]">
                {itemText(blocker, "detail")}
              </p>
              <p className="mt-0.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
                {itemText(blocker, "next")}
              </p>

              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {/* 就地授权：主按钮，只在后端真有这个动作时出现。 */}
                {action && action.kind === "trust_workspace" && onTrustWorkspace ? (
                  <button
                    type="button"
                    data-readiness-trust={action.workspaceId}
                    disabled={busy}
                    // 可访问名里带绝对路径：同一个"信任"按钮出现在两个目录下时，
                    // 光看名字分不出自己在授权哪一个。
                    aria-label={t("chat.readiness.trustWorkspaceAria", { path: action.path })}
                    title={t("chat.readiness.trustWorkspaceAria", { path: action.path })}
                    onClick={() => void trust(blocker.code, action.workspaceId)}
                    className="rounded-md bg-[color:var(--anvil-accent)] px-2 py-1 text-[10px] font-medium text-[color:var(--anvil-background)] hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  >
                    {busy ? t("chat.readiness.trusting") : t("chat.readiness.trustWorkspace")}
                  </button>
                ) : null}

                {/* 次级入口：想看完整权限对照的人从这里去设置。 */}
                <button
                  type="button"
                  data-readiness-jump={blocker.code}
                  onClick={() => onNavigate(blocker.target.route)}
                  className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                >
                  {t("chat.readiness.jump", {
                    section: t(`chat.readiness.section.${blocker.target.section}`),
                  })}
                </button>
              </div>

              {trustError && trustError.code === blocker.code ? (
                <p role="alert" data-readiness-trust-error="" className="mt-1 text-[10px] text-[color:var(--anvil-danger)]">
                  {t("chat.readiness.trustFailed", {
                    code: trustError.error.code,
                    message: formatError(trustError.error),
                  })}
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>

      <div className="flex items-center gap-2">
        <button
          type="button"
          data-readiness-recheck=""
          onClick={onRecheck}
          disabled={checking}
          className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:cursor-not-allowed disabled:opacity-50"
        >
          {checking ? t("chat.readiness.checking") : t("chat.readiness.recheck")}
        </button>
      </div>
    </section>
  );
}
