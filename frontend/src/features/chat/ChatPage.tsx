// 对话页（U02 / UI06）。
//
// 页面只做三件事：读这个会话的后端事实、把事件状态渲染出来、把用户动作转发给后端。
// 它不保存第二份业务真相：
//   - 消息、工具、审批、澄清的状态在 runState（由后端事件推进）；
//   - 会话标题/置顶/归档/revision 在 C06 会话目录（改完重新读）；
//   - 产物与执行事实在 C07（右栏读），导出由后端写文件。
//
// 几个刻意的选择：
//   - 重命名/置顶/归档/删除一律带 expectedRevision，冲突显示 CONFIG_CONFLICT 并给重新读取，
//     不做“看起来保存成功”的乐观更新；
//   - 导出只显示后端写到哪里（path/bytes/sha256/脱敏数），不在前端生成内容；
//   - 上下文面板在这里装配，浏览器分区挂的是 B07 的 BrowserPanel：面板状态
//     只来自 `BrowserState()`（tab/控制态/可见性），几何上报走 `UpdateBrowserSlot`；
//     宽窄切换只改布局，不重建这些节点——原生视图是宿主 DOM 的子窗口，重建
//     就等于丢掉用户的页面、滚动位置和登录态。
//   - 面板自己**不**读 Wails runtime 的私有状态：绑定缺失时（没有原生宿主）它
//     如实显示 unavailable 与原因，而不是画一个假的标签页。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatBytes, formatError, t } from "../../i18n";
import type { CallError, RunState } from "../../types";
import type { ClarificationItem, UiApproval } from "../../runState";
import type { ApprovalChoice } from "../../types";
import { useShell } from "../../app/shell";
import type { PageProps } from "../../app/navigation";
import { toCallError } from "../../app/useBackend";
import { historyApi, type HistorySession } from "../history/api";
import { settingsApi, type PiDiscoveryResult } from "../settings/api";
import { Composer, type PermissionSummary } from "./Composer";
import { ReadinessPanel } from "./ReadinessPanel";
import { evaluateReadiness, fixtureSignal, type ReadinessReport } from "./readiness";
import { BrowserPanel } from "../browser/BrowserPanel";
import { ContextPanel, ContextPanelToggle, useNarrowLayout } from "./ContextPanel";
import { MessageList } from "./MessageList";
import { useBrowserApprovals, type BrowserApprovalBackend } from "./browserApproval";
import { chatApi, type ChatClarificationResponse, type ChatExportFormat, type ChatExportResult } from "./api";
import type { UiState } from "../../runState";

/** 已知的 run 状态集合：未知状态按 unknown 显示，不猜。 */
const RUN_STATES: readonly string[] = [
  "idle",
  "queued",
  "starting",
  "running",
  "waiting_approval",
  "stopping",
  "completed",
  "failed",
  "cancelled",
  "interrupted",
];

function runStateBadge(state: string) {
  return RUN_STATES.includes(state) ? (state as RunState) : "unknown";
}

/** 一次会话目录变更返回的东西（成功给新行，失败给结构化错误）。 */
type SessionMutationOutcome = { item?: HistorySession; error?: CallError };

/**
 * 会话目录变更的动作签名。
 *
 * 写成调用签名接口而不是箭头函数类型：`() => Promise<X>` 里的 `>Promise<`
 * 会被 UI 文案扫描器当成一段 JSX 文本，误报成“catalog 之外的用户文案”。
 */
interface SessionMutation {
  (): Promise<SessionMutationOutcome>;
}

/**
 * 把界面状态里的“运行痕迹”摘掉，只保留本会话自己的消息。
 *
 * 另一个会话正在跑时，本页面不能显示它的消息、审批与澄清；但本地已发生的
 * 对话内容仍然要显示。所以把运行相关字段清空，而不是整页空白。
 */
function shellValueWithoutRun(ui: UiState): UiState {
  return {
    ...ui,
    approvals: [],
    clarifications: [],
    settled: null,
    stopRequested: false,
    lastApprovalResolution: null,
    lastClarificationResolution: null,
  };
}

const EXPORT_FORMATS: { format: ChatExportFormat; labelKey: string }[] = [
  { format: "html", labelKey: "chat.export.html" },
  { format: "markdown", labelKey: "chat.export.markdown" },
  { format: "json", labelKey: "chat.export.json" },
];

export interface ChatPageProps extends PageProps {
  /**
   * 浏览器审批通道（M1）。产品里用默认的绑定；测试注入替身。
   * 抽成参数而不是模块级 mock：测的是组件行为，不是我对 Wails runtime 的伪造。
   */
  browserApprovals?: BrowserApprovalBackend;
  /** 浏览器审批的轮询间隔（毫秒）；<=0 关闭轮询（测试手动驱动）。 */
  browserApprovalPollMs?: number;
  /**
   * 发送前提的探测通道（UX1）：产品里用 DiscoverPi；测试注入替身。
   *
   * 与浏览器审批同一个理由：探一次 Pi 是跨进程动作，测试不该依赖 Wails runtime。
   */
  readiness?: ReadinessBackend;
}

/** 发送前提检查需要的一个只读能力（只探测，不改变任何设置）。 */
export interface ReadinessBackend {
  /**
   * 只读探测：返回后端候选（不改设置、不改 PATH）。
   *
   * 写成方法签名而不是箭头函数类型：`() => Promise<X>` 里的 `>Promise<`
   * 会被 UI 文案扫描器当成一段 JSX 文本。
   */
  discoverPi(): Promise<PiDiscoveryResult | null>;
}

/**
 * 产品里用的真实探测。
 *
 * 用**快速发现**而不是 DiscoverPi：完整发现会对每个可用候选跑六项契约探测
 * （spawn 真实 Pi，本机实测单个候选 10s+），挂在页面挂载上就是“每次启动都
 * 卡在 Pi 连接”。快速发现只探测版本与指纹，返回同样的 DiscoveryResult，
 * 并用 verification=version_only 说明“契约未验证”。
 * 要验证契约由设置页的显式动作触发（见 PiSettings 的“验证契约”）。
 */
const defaultReadinessBackend: ReadinessBackend = {
  discoverPi: () => settingsApi.discoverPiFast(),
};

export function ChatPage({
  onNavigate,
  route,
  browserApprovals,
  browserApprovalPollMs,
  readiness,
}: ChatPageProps) {
  const shell = useShell();
  const sessionId = route?.params.id ?? "";
  const narrow = useNarrowLayout();

  const [session, setSession] = useState<HistorySession | null>(null);
  const [sessionLoading, setSessionLoading] = useState(false);
  const [sessionError, setSessionError] = useState<CallError | null>(null);
  const [actionError, setActionError] = useState<CallError | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [draftTitle, setDraftTitle] = useState("");
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [exporting, setExporting] = useState<ChatExportFormat | null>(null);
  const [exportResult, setExportResult] = useState<ChatExportResult | null>(null);
  const [exportError, setExportError] = useState<CallError | null>(null);
  const [panelOpen, setPanelOpen] = useState(true);

  const messagesRef = useRef<HTMLDivElement | null>(null);

  /**
   * 当前这次运行是否属于本页面这个会话。
   *
   * 绑定层只有一个活跃 run，而页面是按会话的：不判断归属，A 会话的页面就会显示
   * B 会话的审批卡——用户会在看不见上下文的地方批准一条工具调用。
   * 归属未知（还没跑过任何一轮）时按“属于本会话”处理：那时也没有可显示的事件。
   */
  const runBelongsToPage =
    shell.ui.activeSessionId == null || shell.ui.activeSessionId === sessionId;

  /**
   * 浏览器写动作的待审批（M1）。
   *
   * 它是读取式通道（后端没有对应的产品事件），所以这里轮询；页面不属于当前 run 时
   * 不轮询也不展示：在别人的会话页上批准一条自己没看见的浏览器点击是最坏的一种错。
   *
   * 为什么不能只看 runId：浏览器审批的 sessionId 是运行时会话（runtimeSessionID），
   * 不是 C06 的会话 id，两者没有可信的映射，前端不能自己编一个。
   */
  const browserPending = useBrowserApprovals({
    api: browserApprovals,
    enabled: runBelongsToPage,
    pollIntervalMs: browserApprovalPollMs,
  });

  /**
   * 发送前提（UX1）：挂载时与每次发送前各检查一次。
   *
   * 为什么要重新探测而不是只信挂载时那份快照：Pi 被删、工作区被撤销信任、
   * 模型被换掉都发生在页面还开着的时候；拿旧结论放行，用户会看到又一次
   * "点了才知道发不出去"。
   *
   * 判定只读不写：这里不替用户选中任何外部二进制，也不后台改设置。
   */
  const readinessBackend = readiness ?? defaultReadinessBackend;
  const [readinessReport, setReadinessReport] = useState<ReadinessReport | null>(null);
  const [checkingReadiness, setCheckingReadiness] = useState(true);

  const runReadinessCheck = useCallback(async (quiet = false): Promise<ReadinessReport> => {
    // quiet：不亮"正在检查"指示。挂载后的重查（设置/工作区/模型变了）与发送前
    // 的复核都走静默模式——否则用户每发一条都会看到一次"正在检查发送前提"，
    // 而它实际上只是把同一份结论再算一遍（2026-11-08 用户反馈）。
    if (!quiet) setCheckingReadiness(true);
    let discovery: PiDiscoveryResult | null = null;
    let discoveryError: ReturnType<typeof toCallError> | null = null;
    try {
      discovery = (await readinessBackend.discoverPi()) ?? null;
    } catch (e) {
      discoveryError = toCallError(e);
    }
    const next = evaluateReadiness({
      discovery,
      discoveryError,
      currentWorkspace: shell.currentWorkspace,
      currentModel: shell.currentModel,
      settings: shell.settings,
    });
    setReadinessReport(next);
    setCheckingReadiness(false);
    return next;
  }, [readinessBackend, shell.currentWorkspace, shell.currentModel, shell.settings]);

  const readinessRef = useRef<ReadinessReport | null>(null);
  readinessRef.current = readinessReport;

  useEffect(() => {
    // 首次亮指示；之后状态变化引起的重查静默进行，旧结论继续显示。
    void runReadinessCheck(readinessRef.current !== null);
  }, [runReadinessCheck]);

  /**
   * 读取会话行（标题、是否置顶/归档、revision）。
   *
   * 用 C06 的会话目录而不是本地缓存：revision 是 CAS 的凭据，读旧了就会在保存时
   * 撞成 CONFIG_CONFLICT——那正是我们要暴露给用户的事实，不是要藏起来的噪音。
   */
  const loadSession = useCallback(async () => {
    if (!sessionId) return;
    setSessionLoading(true);
    setSessionError(null);
    try {
      const page = await historyApi.list({ includeArchived: true });
      if (page.error) {
        setSessionError(page.error);
        return;
      }
      const found = (page.groups ?? [])
        .flatMap((group) => group.sessions)
        .find((s) => s.id === sessionId);
      if (!found) {
        setSession(null);
        setSessionError({ code: "NOT_FOUND", message: sessionId, retryable: false });
        return;
      }
      setSession(found);
      setDraftTitle(found.title);
    } catch (e) {
      setSessionError({
        code: "INTERNAL_ERROR",
        message: e instanceof Error ? e.message : String(e),
        retryable: true,
      });
    } finally {
      setSessionLoading(false);
    }
  }, [sessionId]);

  useEffect(() => {
    void loadSession();
  }, [loadSession]);

  // 进入窄窗时收起抽屉：默认盖住消息流的抽屉比“多一次点击”更烦人。
  // 注意是“收起”而不是“卸载”——卸载会销毁原生浏览器视图。
  useEffect(() => {
    if (narrow) setPanelOpen(false);
  }, [narrow]);

  /** 会话目录变更：成功就重新读；CAS 冲突原样露出 code。 */
  const runMutation = useCallback(
    async (fn: SessionMutation) => {
      setActionError(null);
      try {
        const res = await fn();
        if (res.error) {
          setActionError(res.error);
          return;
        }
        await loadSession();
      } catch (e) {
        setActionError({
          code: "INTERNAL_ERROR",
          message: e instanceof Error ? e.message : String(e),
          retryable: true,
        });
      }
    },
    [loadSession]
  );

  const saveTitle = useCallback(async () => {
    if (!session) return;
    const title = draftTitle.trim();
    if (!title || title === session.title) {
      setRenaming(false);
      return;
    }
    await runMutation(() => historyApi.rename(session.id, session.revision, title));
    setRenaming(false);
  }, [session, draftTitle, runMutation]);

  const togglePin = useCallback(async () => {
    if (!session) return;
    await runMutation(() => historyApi.setPinned(session.id, session.revision, !session.pinned));
  }, [session, runMutation]);

  const toggleArchive = useCallback(async () => {
    if (!session) return;
    await runMutation(() => historyApi.setArchived(session.id, session.revision, !session.archived));
  }, [session, runMutation]);

  const removeSession = useCallback(async () => {
    if (!session) return;
    setConfirmingDelete(false);
    setActionError(null);
    try {
      const res = await historyApi.remove(session.id, session.revision);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      // 删掉的会话不该继续留在页面上：回到对话入口。
      onNavigate("/chat");
    } catch (e) {
      setActionError({
        code: "INTERNAL_ERROR",
        message: e instanceof Error ? e.message : String(e),
        retryable: true,
      });
    }
  }, [session, onNavigate]);

  /** 导出：由后端从 transcript 生成，前端只显示写到哪里。 */
  const exportSession = useCallback(
    async (format: ChatExportFormat) => {
      if (!sessionId) return;
      setExporting(format);
      setExportError(null);
      setExportResult(null);
      try {
        const res = await chatApi.exportSession(sessionId, format, "");
        if (res.error) {
          setExportError(res.error);
          return;
        }
        setExportResult(res);
      } catch (e) {
        setExportError({
          code: "INTERNAL_ERROR",
          message: e instanceof Error ? e.message : String(e),
          retryable: true,
        });
      } finally {
        setExporting(null);
      }
    },
    [sessionId]
  );

  /** 附件权限摘要：只从后端设置里能读到的字段推，读不到就是 null（界面显示未知）。 */
  const permission = useMemo<PermissionSummary>(() => {
    return {
      filesystem: null,
      network: null,
      shell: shell.settings ? shell.settings.shellAllowed : null,
    };
  }, [shell.settings]);

  /** 这一轮实际使用的模型：优先启动快照，其次当前模型，都没有就显示未知。 */
  const modelLabel = useMemo(() => {
    if (shell.ui.runMeta) {
      const { provider, model } = shell.ui.runMeta;
      return [provider, model].filter(Boolean).join(" / ");
    }
    if (shell.currentModel) {
      return [shell.currentModel.provider, shell.currentModel.model].filter(Boolean).join(" / ");
    }
    return "";
  }, [shell.ui.runMeta, shell.currentModel]);

  /** 重试：重发最后一条用户消息（那是唯一“用户确实说过的话”）。 */
  const lastUserText = useMemo(() => {
    for (let i = shell.ui.messages.length - 1; i >= 0; i--) {
      if (shell.ui.messages[i].role === "user") return shell.ui.messages[i].text;
    }
    return "";
  }, [shell.ui.messages]);

  const retry = useCallback(() => {
    if (!sessionId || !lastUserText) return;
    shell.onSendToSession(sessionId, lastUserText, []);
  }, [sessionId, lastUserText, shell]);

  /** 回到来源轮次：滚动到该 run 的第一条消息/工具卡。 */
  const locateTurn = useCallback((runId: string) => {
    const root = messagesRef.current;
    if (!root) return;
    const target = root.querySelector(`[data-run-id="${runId}"]`);
    if (target instanceof HTMLElement) {
      target.scrollIntoView({ block: "center" });
      target.setAttribute("data-located", "true");
    }
  }, []);

  const onDecide = useCallback(
    (item: UiApproval, choice: ApprovalChoice) => {
      shell.onDecide(item, choice);
    },
    [shell]
  );

  const onClarify = useCallback(
    (item: ClarificationItem, response: ChatClarificationResponse) => {
      shell.onAnswerClarification(item.requestId, response);
    },
    [shell]
  );

  /**
   * 发送：用**当前**结论，不再每次重新探测。
   *
   * 为什么不再重跑 DiscoverPi：那是要 spawn 探针的重活，每发一条消息跑一次
   * 既慢又会闪一次"正在检查发送前提"，而输入区在未就绪时本来就是禁用的。
   * 结论只在这几件事发生时更新（见上面的 effect）：Pi 候选/选择变了、工作区
   * 变了、模型变了，或用户点"重新检查"。真正的权威仍然是后端——它会在状态
   * 真正失效时返回结构化错误，界面照实显示。
   *
   * 兜底：如果没有结论（极端竞态），静默补一次再决定，不把消息丢给注定失败的调用。
   */
  const sendWhenReady = useCallback(
    async (text: string, attachmentIds: string[]) => {
      let report = readinessRef.current;
      if (!report) report = await runReadinessCheck(true);
      if (!report.ready) return;
      shell.onSendToSession(sessionId, text, attachmentIds);
    },
    [runReadinessCheck, shell, sessionId]
  );

  /** 内置 fixture 的标记独立于探测结果：探测失败时也要说清现在是什么模型。 */
  const fixture = useMemo(
    () => fixtureSignal(shell.currentModel, shell.settings),
    [shell.currentModel, shell.settings]
  );

  /**
   * 输入区为什么不能发送（null = 可以）。
   *
   * 只有**已得出否定结论**才阻塞：检查还在飞的时候不阻塞输入。
   * 理由（2026-11-08 用户实测）：挂载时把输入区锁住等探测，用户看到的就是
   * “页面卡住、什么都不能做”。检查本身是异步的，结论到达前用户完全可以先写。
   * “正在检查”仍会以一条轻量状态显示（checkingHint），不伪装成“缺东西”。
   */
  const blockedHint =
    readinessReport && !readinessReport.ready ? t("chat.composer.blocked") : null;
  const checkingHint =
    checkingReadiness && readinessReport === null ? t("chat.composer.checkingReadiness") : null;

  // 没有任何会话 id：沿用 U01 的空态（不自动建会话）。
  if (!sessionId) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-10">
        <AsyncState
          phase="empty"
          emptyTitle={t("shell.emptyHistory")}
          emptyHint={t("shell.emptyHistoryHint")}
          action={
            <button
              type="button"
              onClick={() => void shell.onNewSession()}
              className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
            >
              {t("shell.newSession")}
            </button>
          }
        />
      </div>
    );
  }

  if (sessionLoading && !session) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-6">
        <AsyncState phase="loading" loadingLabel={t("chat.loading")} />
      </div>
    );
  }

  if (sessionError && !session) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-6">
        <AsyncState phase="error" error={sessionError} onRetry={() => void loadSession()} />
      </div>
    );
  }

  const title = session?.title || t("chat.untitled");

  return (
    <div className="flex h-full min-h-0">
      {/* 主列：头部 + 消息流 + 输入区 */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <header className="shrink-0 space-y-1.5 border-b border-[color:var(--anvil-border)] px-3 py-2">
          <div className="flex flex-wrap items-center gap-2">
            <h1 data-session-title="" className="min-w-0 truncate text-[13px] font-medium text-[color:var(--anvil-foreground)]">
              {title}
            </h1>
            <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {t("chat.thinkingState")}
            </span>
            <StatusBadge status={runStateBadge(shell.ui.phase)} />
            {session?.pinned ? (
              <span className="rounded-full border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                {t("chat.sessionActions.pinnedBadge")}
              </span>
            ) : null}
            {session?.archived ? (
              <span className="rounded-full border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                {t("chat.sessionActions.archivedBadge")}
              </span>
            ) : null}
            <span className="ml-auto font-mono text-[10px] text-[color:var(--anvil-muted)]">
              {t("chat.sessionId")}
              {" · "}
              {sessionId}
            </span>
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            {renaming ? (
              <span className="flex flex-wrap items-center gap-1.5">
                <label htmlFor="session-title-input" className="sr-only">
                  {t("chat.sessionActions.renamePlaceholder")}
                </label>
                <input
                  id="session-title-input"
                  value={draftTitle}
                  onChange={(e) => setDraftTitle(e.target.value)}
                  placeholder={t("chat.sessionActions.renamePlaceholder")}
                  className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)]"
                />
                <button
                  type="button"
                  onClick={() => void saveTitle()}
                  className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                >
                  {t("chat.sessionActions.save")}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setRenaming(false);
                    setDraftTitle(session?.title ?? "");
                  }}
                  className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                >
                  {t("chat.sessionActions.cancel")}
                </button>
              </span>
            ) : (
              <button
                type="button"
                onClick={() => setRenaming(true)}
                className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
              >
                {t("chat.sessionActions.rename")}
              </button>
            )}

            <button
              type="button"
              onClick={() => void togglePin()}
              className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
            >
              {session?.pinned ? t("chat.sessionActions.unpin") : t("chat.sessionActions.pin")}
            </button>
            <button
              type="button"
              onClick={() => void toggleArchive()}
              className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
            >
              {session?.archived ? t("chat.sessionActions.unarchive") : t("chat.sessionActions.archive")}
            </button>
            {confirmingDelete ? (
              <span className="flex items-center gap-1.5">
                <span className="text-[10px] text-[color:var(--anvil-danger)]">
                  {t("chat.sessionActions.deleteConfirm")}
                </span>
                <button
                  type="button"
                  onClick={() => void removeSession()}
                  className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)]"
                >
                  {t("chat.sessionActions.confirm")}
                </button>
                <button
                  type="button"
                  onClick={() => setConfirmingDelete(false)}
                  className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-muted)]"
                >
                  {t("chat.sessionActions.cancel")}
                </button>
              </span>
            ) : (
              <button
                type="button"
                onClick={() => setConfirmingDelete(true)}
                className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
              >
                {t("chat.sessionActions.delete")}
              </button>
            )}

            {EXPORT_FORMATS.map(({ format, labelKey }) => (
              <button
                key={format}
                type="button"
                onClick={() => void exportSession(format)}
                disabled={exporting !== null}
                className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:cursor-not-allowed disabled:opacity-50"
              >
                {t("chat.export.label")}
                {" · "}
                {t(labelKey)}
              </button>
            ))}

            {shell.ui.runId ? (
              <button
                type="button"
                onClick={() =>
                  shell.onReplayRun(shell.ui.runId as string, shell.ui.lastSeqByRun[shell.ui.runId as string] ?? 0)
                }
                className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
              >
                {t("chat.replay")}
              </button>
            ) : null}

            <ContextPanelToggle open={panelOpen} narrow={narrow} onToggle={() => setPanelOpen((v) => !v)} />
          </div>

          {exporting ? (
            <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.export.writing")}</p>
          ) : null}
          {exportError ? (
            <p role="alert" className="text-[10px] text-[color:var(--anvil-danger)]">
              {t("chat.export.failed", { code: exportError.code })}
            </p>
          ) : null}
          {exportResult ? (
            <dl data-export-result="" className="space-y-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              <div>
                <dt className="inline">{t("chat.export.done")} · {t("chat.export.path")}：</dt>
                <dd data-export-path="" className="inline break-all font-mono text-[color:var(--anvil-foreground)]">
                  {exportResult.path}
                </dd>
              </div>
              <div>
                <dt className="inline">{t("chat.export.bytes")}：</dt>
                <dd className="inline">
                  {formatBytes(exportResult.bytes)}
                  {" · "}
                  {t("chat.export.redactions", { count: exportResult.redactions })}
                </dd>
              </div>
              <div>
                <dt className="inline">{t("chat.export.sha256")}：</dt>
                <dd className="inline break-all font-mono">{exportResult.sha256}</dd>
              </div>
            </dl>
          ) : null}
          {actionError ? (
            <div data-session-action-error="" role="alert" className="space-y-0.5">
              <p className="text-[10px] text-[color:var(--anvil-danger)]">{formatError(actionError)}</p>
              <p className="font-mono text-[10px] text-[color:var(--anvil-muted)]">{actionError.code}</p>
              {actionError.code === "CONFIG_CONFLICT" ? (
                <>
                  <p className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("chat.sessionActions.conflict")}
                  </p>
                  <p className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("chat.sessionActions.conflictHint")}
                  </p>
                  <button
                    type="button"
                    onClick={() => void loadSession()}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                  >
                    {t("chat.sessionActions.reload")}
                  </button>
                </>
              ) : null}
            </div>
          ) : null}
        </header>

        <div ref={messagesRef} className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
          {runBelongsToPage ? null : (
            <p data-other-session-run="" className="mb-2 text-[10px] text-[color:var(--anvil-warning)]">
              {t("chat.otherSessionRun")}
            </p>
          )}
          <MessageList
            ui={runBelongsToPage ? shell.ui : shellValueWithoutRun(shell.ui)}
            onDecide={onDecide}
            onClarify={onClarify}
            canRespond={shell.ui.runId !== null}
            onRetry={lastUserText ? retry : undefined}
            browserApprovals={runBelongsToPage ? browserPending : undefined}
          />
        </div>

        {readinessReport && readinessReport.blockers.length > 0 ? (
          <ReadinessPanel
            report={readinessReport}
            checking={checkingReadiness}
            onNavigate={onNavigate}
            onRecheck={() => void runReadinessCheck()}
          />
        ) : null}

        <Composer
          sessionId={sessionId}
          value={shell.input}
          onChange={shell.onInput}
          onSend={sendWhenReady}
          onStop={shell.onStop}
          stopRequested={shell.ui.stopRequested === true}
          busy={shell.busy}
          modelLabel={modelLabel}
          profile={null}
          skills={shell.skills}
          permission={permission}
          sendError={shell.actionError}
          blockedHint={blockedHint}
          checkingHint={checkingHint}
          fixture={fixture}
        />
      </div>

      {/* 右栏：宽窗常驻，窄窗为抽屉（同一棵树，不重建）。
          浏览器分区挂真实面板：宽窄切换只改 aside 的 class，面板与原生槽位节点
          位置不变，因此不会被卸载/重建（挂载计数断言在 ContextPanel.test 里）。 */}
      <ContextPanel
        runId={shell.ui.runId ?? ""}
        browserSlot={
          <BrowserPanel
            // 停止按钮只在“这一页确实有一轮在跑”时出现：别的会话的 run 不能
            // 从这个页面被停掉（那也是“点了就停”的假动作）。
            canStop={runBelongsToPage && shell.busy}
            onStop={shell.onStop}
          />
        }
        onLocateTurn={locateTurn}
        open={panelOpen}
        onToggle={() => setPanelOpen((v) => !v)}
      />
    </div>
  );
}
