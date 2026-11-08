// 右栏上下文面板（U02 / D2b）：浏览器、输出物、引用、执行四个分区。
//
// 几何来自原型 `assets/studio.css` 1157–1200（`.rail` / `.rail-head` 42px /
// `.rail-tab` 26px / `.rail-body`）+ `assets/pages.css`（`.rail-foot` /
// `.file-row` / `.exec-row` / `.rail-empty`）。宽窗是 400px 的第二列
// （列宽由 `.chat-layout` 的 grid 给，不在这里写死），窄窗变成底部抽屉。
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
// 3. **窄窗关闭时是隐藏，不是卸载。** `hidden`/`visibility` 只影响可见性；卸载会销毁原生视图。
//
// 分区的切换是**可见性**切换（`data-active` + CSS），不是条件渲染：四个分区始终在
// DOM 里，因此“切到别的 tab 会不会把浏览器视图摘掉”这个问题在结构上就不存在。

import { useCallback, useEffect, useState, type ReactNode } from "react";
import {
  FileText,
  Globe,
  Link2,
  PanelRight,
  PanelRightClose,
  RefreshCw,
  ScanLine,
  Signal,
  Terminal,
  X,
} from "lucide-react";
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

/** 窄窗判定：与 chat.css 里右栏抽屉的断点一致（≤1100px 是抽屉）。 */
export const NARROW_QUERY = "(max-width: 1100px)";

/** 四个分区（顺序即 tabs 顺序，与原型的 rail-head 一致）。 */
const RAIL_TABS: { id: string; icon: typeof Globe; labelKey: string }[] = [
  { id: "browser", icon: Globe, labelKey: "chat.context.browser" },
  { id: "artifacts", icon: FileText, labelKey: "chat.context.artifacts" },
  { id: "citations", icon: Link2, labelKey: "chat.context.citations" },
  { id: "execution", icon: Terminal, labelKey: "chat.context.execution" },
];

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

/** 分区里“读不到”的提示：必须带原因码，不能写“暂无数据”。 */
function UnavailableNote({ code, hintKey }: { code: string; hintKey: string }) {
  return (
    <div style={{ padding: "12px 14px", display: "flex", flexDirection: "column", gap: 5 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
        <span data-section-state="unavailable" className="achat-badge">
          {t("status.unavailable")}
        </span>
        <span className="achat-mono" style={{ fontSize: 10.5, color: "var(--anvil-muted)" }}>
          {code}
        </span>
      </div>
      <p className="achat-hint">{t(hintKey)}</p>
    </div>
  );
}

/** 浏览器分区的默认内容：后端没有浏览器状态接口时如实说明。 */
export function BrowserUnavailable() {
  return (
    <div data-browser-section="unavailable">
      <UnavailableNote code={BROWSER_UNAVAILABLE_CODE} hintKey="chat.context.browserMissing" />
    </div>
  );
}

/** 产物核对结论 → 文案与 tone。 */
function verificationView(v: ChatVerification): { key: string; tone: string } {
  switch (v.status) {
    case "verified":
      return { key: "chat.context.hashOk", tone: "var(--anvil-success)" };
    case "drifted":
    case "changed":
      return { key: "chat.context.hashDrifted", tone: "var(--anvil-warning)" };
    case "missing":
      return { key: "chat.context.hashMissing", tone: "var(--anvil-danger)" };
    default:
      return { key: "chat.context.verifying", tone: "var(--anvil-muted)" };
  }
}

/** 文件扩展名的 3 字母缩写（原型 .file-ext 里的那个小方块）。 */
function extOf(name: string): string {
  const parts = name.split(".");
  if (parts.length < 2) return "·";
  return parts[parts.length - 1].slice(0, 3);
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

  const [tab, setTab] = useState<string>("browser");
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

  const paneProps = (id: string) => ({
    id: `context-pane-${id}`,
    role: "tabpanel" as const,
    "aria-labelledby": `context-tab-${id}`,
    "data-context-section": id,
    "data-active": tab === id ? "true" : "false",
    className: "achat-rail-pane",
  });

  return (
    <aside
      data-context-panel=""
      data-mode={narrow ? "drawer" : "aside"}
      data-open={open ? "true" : "false"}
      aria-label={t("chat.context.title")}
      className="achat-rail"
    >
      {/* 抽屉抓手：只有窄窗可见（原型 .rail-drawer-handle） */}
      <div className="achat-rail-handle" aria-hidden>
        <span />
      </div>

      {/* rail-head 42px：四个分区 tab + 关闭 */}
      <div className="achat-rail-head" role="tablist" aria-label={t("chat.context.tabs")}>
        {RAIL_TABS.map(({ id, icon: Icon, labelKey }) => (
          <button
            key={id}
            type="button"
            role="tab"
            id={`context-tab-${id}`}
            aria-selected={tab === id}
            aria-controls={`context-pane-${id}`}
            data-rail-tab={id}
            className="achat-rail-tab"
            onClick={() => setTab(id)}
          >
            <Icon size={13} aria-hidden />
            <span>{t(labelKey)}</span>
          </button>
        ))}
        <button
          type="button"
          className="achat-iconbtn"
          style={{ marginLeft: "auto" }}
          onClick={onToggle}
          aria-label={t("chat.context.closePanel")}
          title={t("chat.context.closePanel")}
        >
          <X size={14} aria-hidden />
        </button>
      </div>

      <div className="achat-rail-body">
        {/* 1) 浏览器：宿主注入的节点位置固定（不随宽窄/切换重建）。 */}
        <section {...paneProps("browser")}>
          {browserSlot ?? <BrowserUnavailable />}
        </section>

        {/* 2) 输出物：索引 + 逐条磁盘核对 + 回到来源轮次。 */}
        <section {...paneProps("artifacts")}>
          <div className="achat-section-label">{t("chat.context.artifacts")}</div>
          {!runId ? (
            <p className="achat-rail-empty">{t("chat.context.noRun")}</p>
          ) : artifactLoading ? (
            <p className="achat-rail-empty">{t("common.loading")}</p>
          ) : artifactError ? (
            <UnavailableNote
              code={artifactError.code}
              hintKey="chat.context.unavailable"
            />
          ) : artifacts.length === 0 ? (
            <p className="achat-rail-empty">{t("chat.context.noArtifacts")}</p>
          ) : (
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {artifacts.map((artifact) => {
                const check = verified[artifact.id];
                const isError = !!check && "code" in check;
                const view = check && !isError ? verificationView(check) : null;
                return (
                  <li key={artifact.id} data-artifact-id={artifact.id}>
                    <div className="achat-file-row">
                      <span className="achat-file-ext" aria-hidden>
                        {extOf(artifact.displayName)}
                      </span>
                      <div className="achat-file-row-main">
                        <div className="achat-file-name">{artifact.displayName}</div>
                        <div data-artifact-path="" className="achat-file-sub">
                          {artifact.canonicalPath}
                        </div>
                        <div className="achat-file-sub">
                          {formatBytes(artifact.size)}
                          {" · "}
                          <span data-artifact-hash="">{artifact.sha256}</span>
                        </div>
                        {check && !isError ? (
                          <div className="achat-file-sub">
                            {t("chat.context.artifactObservedHash")}
                            {" · "}
                            <span data-artifact-observed="">{check.observedSha256 || "—"}</span>
                          </div>
                        ) : null}
                        {view ? (
                          <p
                            data-artifact-verification={check && !isError ? check.status : ""}
                            style={{ fontSize: 10.5, color: view.tone, margin: "2px 0 0" }}
                          >
                            {t(view.key)}
                          </p>
                        ) : null}
                        {isError ? (
                          <p style={{ fontSize: 10.5, color: "var(--anvil-danger)", margin: "2px 0 0" }}>
                            {t("chat.context.unavailableReason", { reason: (check as CallError).code })}
                          </p>
                        ) : null}
                      </div>
                      <div className="achat-file-actions">
                        <button
                          type="button"
                          className="achat-iconbtn"
                          onClick={() => void verifyArtifact(artifact.id)}
                          aria-label={t("chat.context.verify")}
                          title={t("chat.context.verify")}
                        >
                          <ScanLine size={14} aria-hidden />
                        </button>
                        {onLocateTurn ? (
                          <button
                            type="button"
                            className="achat-iconbtn"
                            onClick={() => onLocateTurn(artifact.runId ?? runId)}
                            aria-label={t("chat.context.locateTurn")}
                            title={t("chat.context.locateTurn")}
                          >
                            <Signal size={14} aria-hidden />
                          </button>
                        ) : null}
                      </div>
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </section>

        {/* 3) 引用：后端 RunDetails 里没有 citations 字段——只能如实报缺。 */}
        <section {...paneProps("citations")}>
          <div className="achat-section-label">{t("chat.context.citations")}</div>
          {!runId ? (
            <p className="achat-rail-empty">{t("chat.context.noRun")}</p>
          ) : detailsLoading ? (
            <p className="achat-rail-empty">{t("common.loading")}</p>
          ) : detailsError ? (
            <UnavailableNote code={detailsError.code} hintKey="chat.context.unavailable" />
          ) : (
            <UnavailableNote code={CITATIONS_UNSUPPORTED_CODE} hintKey="chat.context.citationsMissing" />
          )}
        </section>

        {/* 4) 执行：run 记录、用量（缺就说不缺）、工具、审批、错误、事件数。 */}
        <section {...paneProps("execution")}>
          <div className="achat-section-label">{t("chat.context.execution")}</div>
          {!runId ? (
            <p className="achat-rail-empty">{t("chat.context.noRun")}</p>
          ) : detailsLoading ? (
            <p className="achat-rail-empty">{t("common.loading")}</p>
          ) : detailsError ? (
            <UnavailableNote code={detailsError.code} hintKey="chat.context.unavailable" />
          ) : !details ? (
            <p className="achat-rail-empty">{t("chat.context.empty")}</p>
          ) : (
            <div>
              <div className="achat-exec-row">
                <span className="achat-exec-label">{t("chat.context.state")}</span>
                <span data-execution-state="" className="achat-exec-name">
                  {details.record?.state ?? t("common.unknown")}
                </span>
                <span className="achat-exec-t">{details.eventCount}</span>
              </div>
              <div className="achat-exec-row">
                <span className="achat-exec-label">{t("chat.context.provider")}</span>
                <span className="achat-exec-sub" style={{ marginTop: 0 }}>
                  {details.record?.provider || t("common.unknown")} /{" "}
                  {details.record?.model || t("common.unknown")}
                </span>
                <span className="achat-exec-t">
                  {details.record?.startedAt ? formatDate(details.record.startedAt) : ""}
                </span>
              </div>
              <div className="achat-exec-row">
                <span className="achat-exec-label">{t("chat.context.usage")}</span>
                {details.usage.availability === "unavailable" ? (
                  <span className="achat-exec-sub" data-usage="unavailable" style={{ marginTop: 0 }}>
                    {t("chat.context.usageUnavailable")}
                  </span>
                ) : (
                  <span
                    className="achat-exec-name"
                    data-usage="reported"
                    style={{ fontFamily: "var(--anvil-font-mono)" }}
                  >
                    {details.usage.tokens.totalTokens ?? t("common.unknown")}
                  </span>
                )}
                <span className="achat-exec-t" />
              </div>
              {details.record?.budget ? (
                <div className="achat-exec-row">
                  <span className="achat-exec-label">{t("chat.context.budget")}</span>
                  <span className="achat-exec-sub" style={{ marginTop: 0 }}>
                    {t("chat.context.budgetUsed", {
                      used: details.record.budget.toolCalls,
                      max: details.record.budget.maxToolCalls,
                    })}
                    {details.record.budget.exceeded
                      ? ` · ${details.record.budget.exceededReason || t("common.unknown")}`
                      : ""}
                  </span>
                  <span className="achat-exec-t" />
                </div>
              ) : null}

              <div className="achat-section-label">{t("chat.context.tools")}</div>
              {details.tools.length === 0 ? (
                <p className="achat-rail-empty" style={{ padding: "12px 14px" }}>
                  {t("chat.context.empty")}
                </p>
              ) : (
                <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                  {details.tools.map((tool) => (
                    <li key={`${tool.eventId}`} className="achat-exec-row">
                      <span className="achat-exec-label">{tool.type}</span>
                      <span className="achat-exec-name">{tool.toolName || t("common.unknown")}</span>
                      <span className="achat-exec-t">
                        {tool.isError === undefined
                          ? ""
                          : tool.isError
                            ? t("chat.tool.error")
                            : t("chat.tool.ok")}
                      </span>
                    </li>
                  ))}
                </ul>
              )}

              <div className="achat-section-label">{t("chat.context.approvals")}</div>
              {details.approvals.length === 0 ? (
                <p className="achat-rail-empty" style={{ padding: "12px 14px" }}>
                  {t("chat.context.empty")}
                </p>
              ) : (
                <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                  {details.approvals.map((a) => (
                    <li key={a.eventId} className="achat-exec-row">
                      <span className="achat-exec-label">{a.type}</span>
                      <span className="achat-exec-name">
                        {a.toolName || a.approvalId || t("common.unknown")}
                      </span>
                      <span className="achat-exec-t">{a.choice || ""}</span>
                    </li>
                  ))}
                </ul>
              )}

              {details.diagnostics.length > 0 ? (
                <>
                  <div className="achat-section-label">{t("chat.context.diagnostics")}</div>
                  <ul style={{ listStyle: "none", margin: 0, padding: "0 14px 10px" }}>
                    {details.diagnostics.map((d) => (
                      <li key={d.eventId} className="achat-hint">
                        {d.kind ? `${d.kind}: ` : ""}
                        {d.message || d.type}
                      </li>
                    ))}
                  </ul>
                </>
              ) : null}

              {details.errors.length > 0 ? (
                <>
                  <div className="achat-section-label">{t("chat.context.errors")}</div>
                  <ul style={{ listStyle: "none", margin: 0, padding: "0 14px 10px" }}>
                    {details.errors.map((e) => (
                      <li key={e.eventId} className="achat-hint achat-hint-danger">
                        {e.kind ? `${e.kind}: ` : ""}
                        {e.message || e.type}
                      </li>
                    ))}
                  </ul>
                </>
              ) : null}

              {/* 后端明确说“这一段落我答不出来”的，原样显示。 */}
              {details.unavailable.length > 0 ? (
                <div data-execution-unavailable="">
                  <div className="achat-section-label">{t("status.unavailable")}</div>
                  <ul style={{ listStyle: "none", margin: 0, padding: "0 14px 10px" }}>
                    {details.unavailable.map((u) => (
                      <li key={u.section} className="achat-hint">
                        {u.section}
                        {" · "}
                        {u.reason}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </div>
          )}
        </section>
      </div>

      {/* rail-foot：本轮 run + 重新读取（读不到的时候至少给一条重新读的路） */}
      <div className="achat-rail-foot">
        <span className="achat-mono" style={{ fontSize: 10.5 }}>
          {runId ? `${t("chat.context.runLabel")} · ${runId}` : t("chat.context.railFootIdle")}
        </span>
        <span className="achat-rail-foot-spacer" />
        <button
          type="button"
          className="achat-iconbtn"
          onClick={() => {
            void loadArtifacts();
            void loadDetails();
          }}
          disabled={!runId}
          aria-label={t("common.retry")}
          title={t("common.retry")}
        >
          <RefreshCw size={13} aria-hidden />
        </button>
      </div>

      {/* 抽屉模式下需要一个关闭覆盖层：它不是分区的一部分，位置在列表之后。 */}
      {narrow ? (
        <button
          type="button"
          aria-label={t("chat.context.closePanel")}
          onClick={onToggle}
          data-rail-scrim=""
          data-open={open ? "true" : "false"}
          className="achat-rail-scrim"
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
      className="achat-iconbtn"
      aria-label={open ? t("chat.context.closePanel") : t("chat.context.openPanel")}
      title={open ? t("chat.context.closePanel") : t("chat.context.openPanel")}
    >
      {open ? <PanelRightClose size={16} aria-hidden /> : <PanelRight size={16} aria-hidden />}
      {/* 窄窗时标明是抽屉，用户知道它会盖住内容。 */}
      {narrow && open ? <span className="sr-only">{t("chat.context.drawer")}</span> : null}
    </button>
  );
}
