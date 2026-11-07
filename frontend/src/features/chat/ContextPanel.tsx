// 右栏上下文面板（U02 / UI06）：浏览器、产物、引用、执行四个分区。
//
// 三条纪律决定了这个文件的形状：
//
// 1. **四个分区都读后端状态。** 产物来自 ListRunArtifacts（磁盘核对走 GetArtifact），
//    执行/引用来自 RunDetails；读不到就显示 unavailable + 原因，不显示空数组冒充“没有”。
//    浏览器分区需要 B07 的原生绑定，当前后端没有暴露浏览器状态接口——所以它显示
//    不可用并说明原因，而不是画一个假标签页。
//
// 2. **窄窗变抽屉时不重建浏览器分区。** 真机里浏览器是原生子视图，宿主 DOM 节点被
//    重建就等于把用户的页面、滚动位置和登录态一起丢掉。因此这里**只有一个**容器元素：
//    宽窗时是右侧 aside，窄窗时是覆盖层抽屉，二者只是 class/aria 不同；
//    分区的树结构与位置完全不改，React 不会卸载任何子节点（测试用挂载计数断言）。
//
// 3. **窄窗关闭时是隐藏，不是卸载。** `hidden` 只影响可见性；卸载会销毁原生视图。

import { useCallback, useEffect, useId, useState, type ReactNode } from "react";
import { formatBytes, formatDate, t } from "../../i18n";
import type { CallError } from "../../types";
import {
  chatApi,
  type ChatArtifact,
  type ChatRunDetails,
  type ChatVerification,
} from "./api";

/** 浏览器分区不可用的原因码：后端没有浏览器状态接口（B07 尚未提供绑定）。 */
const BROWSER_UNAVAILABLE_CODE = "BROWSER_API_MISSING";

/** 引用分区不可用的原因码：RunDetails 里没有 citations 字段。 */
const CITATIONS_UNSUPPORTED_CODE = "CITATIONS_UNSUPPORTED";

/** 窄窗判定：与规格 §7 的布局矩阵一致（< 1024px 视为窄窗）。 */
export const NARROW_QUERY = "(max-width: 1023px)";

/**
 * 订阅一条媒体查询。
 *
 * 没有 matchMedia（jsdom / 极端环境）时按“宽窗”处理：宽窗是默认布局，
 * 猜成窄窗会让抽屉在桌面上一闪而过。
 */
export function useNarrowLayout(query: string = NARROW_QUERY): boolean {
  const [narrow, setNarrow] = useState<boolean>(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
    return window.matchMedia(query).matches;
  });

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(query);
    const onChange = () => setNarrow(mq.matches);
    onChange();
    if (typeof mq.addEventListener === "function") {
      mq.addEventListener("change", onChange);
      return () => mq.removeEventListener("change", onChange);
    }
    // 老 WebView 只有 addListener；两种都注册不冲突。
    mq.addListener?.(onChange);
    return () => mq.removeListener?.(onChange);
  }, [query]);

  return narrow;
}

/**
 * 不可用的分区。
 *
 * reason 必须由调用方给出真实原因（缺接口 / 后端报错码），不能写“暂无数据”：
 * “暂无”和“读不到”是两件事，用户要做的下一步完全不同。
 */
function UnavailableSection({ id, title, reason }: { id: string; title: string; reason: string }) {
  return (
    <SectionShell id={id} title={title}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span
          data-section-state="unavailable"
          className="rounded-full border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
        >
          {t("status.unavailable")}
        </span>
        <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">{reason}</span>
      </div>
      <p className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">{t("chat.context.unavailable")}</p>
    </SectionShell>
  );
}

function SectionShell({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: ReactNode;
}) {
  const headingId = `context-section-${id}-heading`;
  return (
    <section data-context-section={id} aria-labelledby={headingId} className="space-y-1.5">
      <h3 id={headingId} className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
        {title}
      </h3>
      {children}
    </section>
  );
}

/** 浏览器分区的默认内容：后端没有浏览器状态接口时如实说明。 */
export function BrowserUnavailable() {
  return (
    <div data-browser-section="unavailable" className="space-y-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <span
          data-section-state="unavailable"
          className="rounded-full border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
        >
          {t("status.unavailable")}
        </span>
        <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
          {BROWSER_UNAVAILABLE_CODE}
        </span>
      </div>
      <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("chat.context.browserMissing")}
      </p>
    </div>
  );
}

/** 产物核对结论 → 文案与 tone。 */
function verificationView(v: ChatVerification): { key: string; tone: string } {
  switch (v.status) {
    case "verified":
      return { key: "chat.context.hashOk", tone: "text-[color:var(--anvil-success)]" };
    case "drifted":
    case "changed":
      return { key: "chat.context.hashDrifted", tone: "text-[color:var(--anvil-warning)]" };
    case "missing":
      return { key: "chat.context.hashMissing", tone: "text-[color:var(--anvil-danger)]" };
    default:
      return { key: "chat.context.verifying", tone: "text-[color:var(--anvil-muted)]" };
  }
}

export interface ContextPanelProps {
  /** 当前 run；空 = 还没有跑过（四个分区都显示“先发一条消息”）。 */
  runId: string;
  /**
   * 浏览器分区的内容。
   *
   * 由宿主注入（真机是 B07 的原生宿主占位节点，测试可以是挂载计数器）。
   * 关键点：这个节点在宽窗/抽屉两种形态下位置不变，因此不会被重建。
   */
  browserSlot?: ReactNode;
  /** 定位到产物来源轮次（滚动到对应消息），由 ChatPage 提供。 */
  onLocateTurn?: (runId: string) => void;
  /** 面板是否展开（窄窗时控制抽屉开关）。 */
  open: boolean;
  onToggle: () => void;
  /**
   * 显式覆盖窄窗判定。
   *
   * 默认走 useNarrowLayout()（真实视口）；宿主或测试可以在无法改变视口的环境里
   * 注入同一份判定，而不必伪造 window.matchMedia。
   */
  narrow?: boolean;
}

export function ContextPanel({
  runId,
  browserSlot,
  onLocateTurn,
  open,
  onToggle,
  narrow: narrowOverride,
}: ContextPanelProps) {
  const detectedNarrow = useNarrowLayout();
  const narrow = narrowOverride ?? detectedNarrow;

  const [artifacts, setArtifacts] = useState<ChatArtifact[]>([]);
  const [artifactError, setArtifactError] = useState<CallError | null>(null);
  const [artifactLoading, setArtifactLoading] = useState(false);
  const [verified, setVerified] = useState<Record<string, ChatVerification | CallError>>({});
  const [details, setDetails] = useState<ChatRunDetails | null>(null);
  const [detailsError, setDetailsError] = useState<CallError | null>(null);
  const [detailsLoading, setDetailsLoading] = useState(false);

  const loadArtifacts = useCallback(async () => {
    if (!runId) return;
    setArtifactLoading(true);
    setArtifactError(null);
    try {
      const res = await chatApi.runArtifacts(runId);
      if (res.error) {
        setArtifactError(res.error);
        setArtifacts([]);
        return;
      }
      setArtifacts(res.items ?? []);
    } catch (e) {
      setArtifactError({
        code: "INTERNAL_ERROR",
        message: e instanceof Error ? e.message : String(e),
        retryable: true,
      });
      setArtifacts([]);
    } finally {
      setArtifactLoading(false);
    }
  }, [runId]);

  const loadDetails = useCallback(async () => {
    if (!runId) return;
    setDetailsLoading(true);
    setDetailsError(null);
    try {
      const res = await chatApi.runDetails(runId);
      if (res.error) {
        setDetailsError(res.error);
        setDetails(null);
        return;
      }
      setDetails(res.details);
    } catch (e) {
      setDetailsError({
        code: "INTERNAL_ERROR",
        message: e instanceof Error ? e.message : String(e),
        retryable: true,
      });
      setDetails(null);
    } finally {
      setDetailsLoading(false);
    }
  }, [runId]);

  useEffect(() => {
    // 只在面板真的可见时加载：隐藏的抽屉不该一直打后端。
    if (!open || !runId) return;
    void loadArtifacts();
    void loadDetails();
  }, [open, runId, loadArtifacts, loadDetails]);

  /** 核对一个产物：只拿后端这次读到的 hash，不在前端算。 */
  const verifyArtifact = useCallback(async (id: string) => {
    try {
      const res = await chatApi.artifact(id);
      if (res.error) {
        setVerified((prev) => ({ ...prev, [id]: res.error as CallError }));
        return;
      }
      const v = res.item.verification;
      setVerified((prev) => ({
        ...prev,
        [id]: v ?? { status: "unknown", checkedAt: "" },
      }));
    } catch (e) {
      setVerified((prev) => ({
        ...prev,
        [id]: {
          code: "INTERNAL_ERROR",
          message: e instanceof Error ? e.message : String(e),
          retryable: true,
        },
      }));
    }
  }, []);

  const panelId = useId();
  const headingId = `context-panel-heading-${panelId}`;

  // 唯一容器：宽窗是右侧 aside，窄窗是覆盖抽屉。只有 class 与 aria 变，树结构不变。
  const containerClass = narrow
    ? [
        "fixed inset-y-0 right-0 z-40 w-[min(420px,90vw)] overflow-y-auto border-l border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] p-3 shadow-anvil",
        open ? "" : "hidden",
      ].join(" ")
    : [
        "w-[320px] shrink-0 overflow-y-auto border-l border-[color:var(--anvil-border)] p-3",
        open ? "" : "hidden",
      ].join(" ");

  return (
    <aside
      data-context-panel=""
      data-mode={narrow ? "drawer" : "aside"}
      data-open={open ? "true" : "false"}
      aria-labelledby={headingId}
      className={containerClass}
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h2 id={headingId} className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
          {narrow ? t("chat.context.drawer") : t("chat.context.title")}
        </h2>
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          {narrow ? t("chat.context.closePanel") : t("chat.context.closePanel")}
        </button>
      </header>

      <div className="mt-2 space-y-4">
        {/* 1) 浏览器：宿主注入的节点位置固定（不随宽窄重建）。 */}
        <SectionShell id="browser" title={t("chat.context.browser")}>
          {browserSlot ?? <BrowserUnavailable />}
        </SectionShell>

        {/* 2) 产物：索引 + 逐条磁盘核对 + 回到来源轮次。 */}
        <SectionShell id="artifacts" title={t("chat.context.artifacts")}>
          {!runId ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.context.noRun")}</p>
          ) : artifactLoading ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("common.loading")}</p>
          ) : artifactError ? (
            <div className="space-y-1">
              <span className="font-mono text-[10px] text-[color:var(--anvil-danger)]">
                {artifactError.code}
              </span>
              <p className="text-[10px] text-[color:var(--anvil-muted)]">
                {t("chat.context.unavailableReason", { reason: artifactError.code })}
              </p>
            </div>
          ) : artifacts.length === 0 ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.context.noArtifacts")}</p>
          ) : (
            <ul className="space-y-1.5">
              {artifacts.map((artifact) => {
                const check = verified[artifact.id];
                const isError = !!check && "code" in check;
                const view = check && !isError ? verificationView(check) : null;
                return (
                  <li
                    key={artifact.id}
                    data-artifact-id={artifact.id}
                    className="space-y-1 rounded border border-[color:var(--anvil-border)] px-2 py-1.5"
                  >
                    <p className="truncate text-[11px] text-[color:var(--anvil-foreground)]">
                      {artifact.displayName}
                    </p>
                    <dl className="space-y-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                      <div>
                        <dt className="inline">{t("chat.context.artifactPath")}：</dt>
                        <dd data-artifact-path="" className="inline break-all font-mono">
                          {artifact.canonicalPath}
                        </dd>
                      </div>
                      <div>
                        <dt className="inline">{t("chat.context.size")}：</dt>
                        <dd className="inline">{formatBytes(artifact.size)}</dd>
                      </div>
                      <div>
                        <dt className="inline">{t("chat.context.artifactHash")}：</dt>
                        <dd data-artifact-hash="" className="inline break-all font-mono">
                          {artifact.sha256}
                        </dd>
                      </div>
                      {check && !isError ? (
                        <div>
                          <dt className="inline">{t("chat.context.artifactObservedHash")}：</dt>
                          <dd data-artifact-observed="" className="inline break-all font-mono">
                            {check.observedSha256 || "—"}
                          </dd>
                        </div>
                      ) : null}
                    </dl>
                    {view ? (
                      <p data-artifact-verification={check && !isError ? check.status : ""} className={["text-[10px]", view.tone].join(" ")}>
                        {t(view.key)}
                      </p>
                    ) : null}
                    {isError ? (
                      <p className="text-[10px] text-[color:var(--anvil-danger)]">
                        {t("chat.context.unavailableReason", { reason: (check as CallError).code })}
                      </p>
                    ) : null}
                    <div className="flex flex-wrap gap-1.5">
                      <button
                        type="button"
                        onClick={() => void verifyArtifact(artifact.id)}
                        className="rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                      >
                        {t("chat.context.verify")}
                      </button>
                      {onLocateTurn ? (
                        <button
                          type="button"
                          onClick={() => onLocateTurn(artifact.runId ?? runId)}
                          className="rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                        >
                          {t("chat.context.locateTurn")}
                        </button>
                      ) : null}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </SectionShell>

        {/* 3) 引用：后端 RunDetails 里没有 citations 字段——只能如实报缺。 */}
        <SectionShell id="citations" title={t("chat.context.citations")}>
          {!runId ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.context.noRun")}</p>
          ) : detailsLoading ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("common.loading")}</p>
          ) : detailsError ? (
            <div className="space-y-1">
              <span className="font-mono text-[10px] text-[color:var(--anvil-danger)]">{detailsError.code}</span>
              <p className="text-[10px] text-[color:var(--anvil-muted)]">
                {t("chat.context.unavailableReason", { reason: detailsError.code })}
              </p>
            </div>
          ) : (
            <div className="space-y-1">
              <div className="flex flex-wrap items-center gap-1.5">
                <span
                  data-section-state="unavailable"
                  className="rounded-full border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                >
                  {t("status.unavailable")}
                </span>
                <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
                  {CITATIONS_UNSUPPORTED_CODE}
                </span>
              </div>
              <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
                {t("chat.context.citationsMissing")}
              </p>
            </div>
          )}
        </SectionShell>

        {/* 4) 执行：run 记录、用量（缺就说不缺）、工具、审批、错误、事件数。 */}
        <SectionShell id="execution" title={t("chat.context.execution")}>
          {!runId ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.context.noRun")}</p>
          ) : detailsLoading ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("common.loading")}</p>
          ) : detailsError ? (
            <div className="space-y-1">
              <span className="font-mono text-[10px] text-[color:var(--anvil-danger)]">{detailsError.code}</span>
              <p className="text-[10px] text-[color:var(--anvil-muted)]">
                {t("chat.context.unavailableReason", { reason: detailsError.code })}
              </p>
            </div>
          ) : !details ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.context.empty")}</p>
          ) : (
            <div className="space-y-2 text-[10px] text-[color:var(--anvil-muted)]">
              <dl className="space-y-0.5">
                <div>
                  <dt className="inline">{t("chat.context.state")}：</dt>
                  <dd data-execution-state="" className="inline text-[color:var(--anvil-foreground)]">
                    {details.record?.state ?? t("common.unknown")}
                  </dd>
                </div>
                <div>
                  <dt className="inline">{t("chat.context.provider")}：</dt>
                  <dd className="inline font-mono text-[color:var(--anvil-foreground)]">
                    {details.record?.provider || t("common.unknown")} / {details.record?.model || t("common.unknown")}
                  </dd>
                </div>
                <div>
                  <dt className="inline">{t("chat.context.events")}：</dt>
                  <dd className="inline">{details.eventCount}</dd>
                </div>
                {details.record?.startedAt ? (
                  <div>
                    <dt className="inline">{t("chat.context.state")}：</dt>
                    <dd className="inline">{formatDate(details.record.startedAt)}</dd>
                  </div>
                ) : null}
              </dl>

              <div>
                <p>{t("chat.context.usage")}</p>
                {details.usage.availability === "unavailable" ? (
                  <p data-usage="unavailable" className="text-[color:var(--anvil-muted)]">
                    {t("chat.context.usageUnavailable")}
                  </p>
                ) : (
                  <p data-usage="reported" className="font-mono text-[color:var(--anvil-foreground)]">
                    {details.usage.tokens.totalTokens ?? t("common.unknown")}
                  </p>
                )}
              </div>

              {details.record?.budget ? (
                <div>
                  <p>{t("chat.context.budget")}</p>
                  <p className="font-mono text-[color:var(--anvil-foreground)]">
                    {t("chat.context.budgetUsed", {
                      used: details.record.budget.toolCalls,
                      max: details.record.budget.maxToolCalls,
                    })}
                  </p>
                  {details.record.budget.exceeded ? (
                    <p className="text-[color:var(--anvil-warning)]">
                      {details.record.budget.exceededReason || t("common.unknown")}
                    </p>
                  ) : null}
                </div>
              ) : null}

              <div>
                <p>{t("chat.context.tools")}</p>
                {details.tools.length === 0 ? (
                  <p>{t("chat.context.empty")}</p>
                ) : (
                  <ul className="space-y-0.5">
                    {details.tools.map((tool) => (
                      <li key={`${tool.eventId}`} className="flex flex-wrap gap-1.5">
                        <span className="font-mono text-[color:var(--anvil-foreground)]">
                          {tool.toolName || t("common.unknown")}
                        </span>
                        <span>{tool.type}</span>
                        {tool.isError === undefined ? null : (
                          <span>{tool.isError ? t("chat.tool.error") : t("chat.tool.ok")}</span>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div>
                <p>{t("chat.context.approvals")}</p>
                {details.approvals.length === 0 ? (
                  <p>{t("chat.context.empty")}</p>
                ) : (
                  <ul className="space-y-0.5">
                    {details.approvals.map((a) => (
                      <li key={a.eventId} className="flex flex-wrap gap-1.5">
                        <span className="font-mono text-[color:var(--anvil-foreground)]">
                          {a.toolName || a.approvalId || t("common.unknown")}
                        </span>
                        <span>{a.choice || a.type}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              {details.diagnostics.length > 0 ? (
                <div>
                  <p>{t("chat.context.diagnostics")}</p>
                  <ul className="space-y-0.5">
                    {details.diagnostics.map((d) => (
                      <li key={d.eventId} className="break-words">
                        {d.kind ? `${d.kind}: ` : ""}
                        {d.message || d.type}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {details.errors.length > 0 ? (
                <div>
                  <p>{t("chat.context.errors")}</p>
                  <ul className="space-y-0.5">
                    {details.errors.map((e) => (
                      <li key={e.eventId} className="break-words">
                        {e.kind ? `${e.kind}: ` : ""}
                        {e.message || e.type}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {/* 后端明确说“这一段落我答不出来”的，原样显示。 */}
              {details.unavailable.length > 0 ? (
                <div data-execution-unavailable="">
                  <p>{t("status.unavailable")}</p>
                  <ul className="space-y-0.5">
                    {details.unavailable.map((u) => (
                      <li key={u.section} className="break-words">
                        {u.section}
                        {" · "}
                        {u.reason}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              <div className="flex flex-wrap gap-1.5">
                <button
                  type="button"
                  onClick={() => void loadDetails()}
                  className="rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                >
                  {t("common.retry")}
                </button>
              </div>
            </div>
          )}
        </SectionShell>
      </div>

      {/* 抽屉模式下需要一个关闭覆盖层：它不是分区的一部分，位置在列表之后。 */}
      {narrow && open ? (
        <button
          type="button"
          aria-label={t("chat.context.closePanel")}
          onClick={onToggle}
          className="fixed inset-0 -z-10 bg-[color:var(--anvil-overlay)]"
        />
      ) : null}
    </aside>
  );
}

/** 供页面在窄窗时给出“展开上下文”的按钮（与面板分开，避免面板未挂载就点不到）。 */
export function ContextPanelToggle({
  open,
  narrow,
  onToggle,
}: {
  open: boolean;
  narrow: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={open}
      className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
    >
      {open ? t("chat.context.closePanel") : t("chat.context.openPanel")}
      {/* 窄窗时标明是抽屉，用户知道它会盖住内容。 */}
      {narrow && open ? ` · ${t("chat.context.drawer")}` : ""}
    </button>
  );
}
