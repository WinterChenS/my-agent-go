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
//   - 上下文面板在这里装配，浏览器分区用固定占位节点（真实原生视图由 B07 挂进去），
//     宽窄切换只改布局，不重建节点。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatBytes, formatError, t } from "../../i18n";
import type { CallError, RunState } from "../../types";
import type { ClarificationItem, UiApproval } from "../../runState";
import type { ApprovalChoice } from "../../types";
import { useShell } from "../../app/shell";
import type { PageProps } from "../../app/navigation";
import { historyApi, type HistorySession } from "../history/api";
import { Composer, type PermissionSummary } from "./Composer";
import {
  BrowserUnavailable,
  ContextPanel,
  ContextPanelToggle,
  useNarrowLayout,
} from "./ContextPanel";
import { MessageList } from "./MessageList";
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

export function ChatPage({ onNavigate, route }: PageProps) {
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
          />
        </div>

        <Composer
          sessionId={sessionId}
          value={shell.input}
          onChange={shell.onInput}
          onSend={(text, attachmentIds) => shell.onSendToSession(sessionId, text, attachmentIds)}
          onStop={shell.onStop}
          stopRequested={shell.ui.stopRequested === true}
          busy={shell.busy}
          modelLabel={modelLabel}
          profile={null}
          skills={shell.skills}
          permission={permission}
          sendError={shell.actionError}
        />
      </div>

      {/* 右栏：宽窗常驻，窄窗为抽屉（同一棵树，不重建） */}
      <ContextPanel
        runId={shell.ui.runId ?? ""}
        browserSlot={<BrowserUnavailable />}
        onLocateTurn={locateTurn}
        open={panelOpen}
        onToggle={() => setPanelOpen((v) => !v)}
      />
    </div>
  );
}
