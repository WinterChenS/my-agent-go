// 历史侧栏（U01 / UI05）：C06 会话目录的分页列表 + 改名/置顶/归档/删除。
//
// 三条纪律：
//   1. **后端说了算**。改名/置顶/归档/删除之后一律重新 list，界面显示的是
//      重新读到的行，不是本地乐观更新的值。服务端会归一化输入（例如去空白），
//      乐观更新会把用户输入当成结果，看起来"改成功了"，实际服务端存的是别的。
//   2. **失败要说清楚**。CONFIG_CONFLICT 意味着别人先改过：显示本地化文案并
//      重新读取，用户基于最新版本重试即可。错误码在界面上可见（不吞掉）。
//   3. **分页用游标**。C06 用 keyset，翻页期间新会话插入不会让已翻过的页错位；
//      前端只是把 nextCursor 原样带回去。
//
// 分组：按项目名分组；workspaceId 不在当前工作区列表里时单列为"普通会话"——
// 那是无法命名的项目（例如旧数据），把它藏起来或塞进现有项目都是错的。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Archive, Pin, PinOff, Plus, Trash2 } from "lucide-react";
import { AsyncState } from "../../components/states/AsyncState";
import type { CallError, Workspace } from "../../types";
import { formatError, t } from "../../i18n";
import {
  HISTORY_PAGE_SIZE,
  historyApi,
  type HistoryMutationResult,
  type HistoryPageResult,
  type HistorySession,
} from "./api";

export { HISTORY_PAGE_SIZE };

export interface HistorySidebarProps {
  workspaces: Workspace[];
  /** "" 表示全部项目。 */
  workspaceFilter: string;
  onFilterChange: (workspaceId: string) => void;
  activeSessionId?: string | null;
  onOpenSession: (id: string) => void;
  onNewSession: () => void;
}

interface SidebarState {
  sessions: HistorySession[];
  nextCursor?: string;
  hasMore: boolean;
}

const emptyState: SidebarState = { sessions: [], hasMore: false };

/** 变更结果：会话行或结构化错误（删除只回错误）。 */
type MutationOutcome = HistoryMutationResult | { error?: CallError };

/** 变更调用签名。写成 call signature 而不是箭头类型只是风格取舍。 */
interface MutationCallable {
  (): Promise<MutationOutcome>;
}

/** 把一页结果摊平成有序列表（分组只是展示用，顺序由后端决定）。 */
function flatten(page: HistoryPageResult): HistorySession[] {
  const out: HistorySession[] = [];
  for (const group of page.groups ?? []) {
    for (const session of group.sessions ?? []) out.push(session);
  }
  return out;
}

export function HistorySidebar({
  workspaces,
  workspaceFilter,
  onFilterChange,
  activeSessionId,
  onOpenSession,
  onNewSession,
}: HistorySidebarProps) {
  const [state, setState] = useState<SidebarState>(emptyState);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<CallError | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<{ id: string; draft: string } | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  // 每次重新读取都换一个序号：并发的旧请求回来时不能覆盖新结果。
  const requestSeq = useRef(0);

  const load = useCallback(
    async (cursor?: string) => {
      const seq = ++requestSeq.current;
      setLoading(true);
      setError(null);
      try {
        const page = await historyApi.list({
          workspaceId: workspaceFilter,
          limit: HISTORY_PAGE_SIZE,
          cursor,
        });
        if (seq !== requestSeq.current) return;
        if (page.error) {
          setError(page.error);
          setState(emptyState);
          return;
        }
        const items = flatten(page);
        setState((prev) =>
          cursor
            ? { sessions: [...prev.sessions, ...items], nextCursor: page.nextCursor, hasMore: page.hasMore }
            : { sessions: items, nextCursor: page.nextCursor, hasMore: page.hasMore }
        );
      } catch (e) {
        if (seq !== requestSeq.current) return;
        setError({ code: "INTERNAL_ERROR", message: String(e), retryable: true });
        setState(emptyState);
      } finally {
        if (seq === requestSeq.current) setLoading(false);
      }
    },
    [workspaceFilter]
  );

  // 首次加载与筛选变化都重新从第一页读：换项目后旧的游标没有意义。
  useEffect(() => {
    void load();
  }, [load]);

  const reload = useCallback(async () => {
    await load();
  }, [load]);

  /** 执行一次变更：先看错误码，成功后重新读取，绝不用返回值拼列表。 */
  const mutate = useCallback(
    async (id: string, fn: MutationCallable) => {
      setBusyId(id);
      setNotice(null);
      try {
        const res = await fn();
        if (res.error) {
          setNotice(res.error);
          // 冲突/过期：以服务端为准重读，用户基于最新版本重试。
          await reload();
          return;
        }
        await reload();
        setRenaming(null);
        setDeleting(null);
      } catch (e) {
        setNotice({ code: "INTERNAL_ERROR", message: String(e), retryable: true });
        await reload();
      } finally {
        setBusyId(null);
      }
    },
    [reload]
  );

  const workspaceName = useMemo(() => {
    const map = new Map<string, string>();
    for (const ws of workspaces) map.set(ws.id, ws.name);
    return map;
  }, [workspaces]);

  const groups = useMemo(() => {
    // 分组只影响展示：同一个项目的会话连在一起，未知项目落到"普通会话"。
    const known: { id: string; label: string; sessions: HistorySession[]; ordinary: boolean }[] = [];
    const byId = new Map<string, number>();
    const ordinary: HistorySession[] = [];
    for (const session of state.sessions) {
      const name = workspaceName.get(session.workspaceId);
      if (!name) {
        ordinary.push(session);
        continue;
      }
      const existing = byId.get(session.workspaceId);
      if (existing === undefined) {
        byId.set(session.workspaceId, known.length);
        known.push({ id: session.workspaceId, label: name, sessions: [session], ordinary: false });
      } else {
        known[existing].sessions.push(session);
      }
    }
    if (ordinary.length > 0) {
      known.push({ id: "__ordinary__", label: t("shell.ordinarySessions"), sessions: ordinary, ordinary: true });
    }
    return known;
  }, [state.sessions, workspaceName]);

  const hasAny = state.sessions.length > 0;

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="history-sidebar">
      <div className="flex shrink-0 items-center justify-between px-3 py-2">
        <h2 className="text-[11px] font-medium uppercase tracking-wide text-[color:var(--anvil-muted)]">
          {t("shell.historyTitle")}
        </h2>
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={onNewSession}
            aria-label={t("shell.newSession")}
            title={t("shell.newSession")}
            className="rounded p-1 text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] hover:text-[color:var(--anvil-foreground)]"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden />
          </button>
        </div>
      </div>

      <div className="shrink-0 px-2 pb-2">
        <select
          aria-label={t("shell.filterLabel")}
          value={workspaceFilter}
          onChange={(e) => onFilterChange(e.target.value)}
          className="w-full rounded border border-[color:var(--anvil-border)] bg-transparent px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)]"
        >
          <option value="">{t("shell.filterAll")}</option>
          {workspaces.map((ws) => (
            <option key={ws.id} value={ws.id}>
              {ws.name}
            </option>
          ))}
        </select>
      </div>

      {notice ? (
        <div
          role="alert"
          className="mx-2 mb-2 rounded border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1.5 text-[11px]"
        >
          <span className="mr-1 font-mono text-[10px] text-[color:var(--anvil-muted)]">{notice.code}</span>
          {formatError(notice)}
        </div>
      ) : null}

      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {loading && !hasAny ? (
          <p role="status" className="px-2 py-3 text-[11px] text-[color:var(--anvil-muted)]">
            {t("shell.loadingHistory")}
          </p>
        ) : null}

        {!loading && error ? (
          <div className="px-1 py-2">
            <AsyncState phase="error" error={error} onRetry={() => void reload()} />
          </div>
        ) : null}

        {!loading && !error && !hasAny ? (
          <AsyncState
            phase="empty"
            emptyTitle={t("shell.emptyHistory")}
            emptyHint={
              workspaceFilter ? t("shell.emptyHistoryHintFiltered") : t("shell.emptyHistoryHint")
            }
          />
        ) : null}

        {groups.map((group) => (
          <section key={group.id} data-testid="history-group" className="mb-1">
            <h3 className="px-2 py-1 text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {group.label}
            </h3>
            <ul className="space-y-0.5">
              {group.sessions.map((session) => {
                const title = session.title || t("shell.untitledSession");
                const active = session.id === activeSessionId;
                // 标签先算好再传给属性：属性里直接放模板串会让"文案是否走 catalog"
                // 无法被静态扫描证明（也读不出它其实是 t() 拼出来的）。
                const renameLabel = `${t("shell.rename")} ${title}`;
                const pinLabel = `${session.pinned ? t("shell.unpin") : t("shell.pin")} ${title}`;
                const archiveLabel = `${t("shell.archive")} ${title}`;
                const deleteLabel = `${t("shell.delete")} ${title}`;
                return (
                  <li key={session.id} className="rounded">
                    {renaming && renaming.id === session.id ? (
                      <form
                        className="flex items-center gap-1 px-1.5 py-1"
                        onSubmit={(e) => {
                          e.preventDefault();
                          void mutate(session.id, () =>
                            historyApi.rename(session.id, session.revision, renaming.draft)
                          );
                        }}
                      >
                        <input
                          aria-label={t("shell.renameInput")}
                          value={renaming.draft}
                          autoFocus
                          onChange={(e) => setRenaming({ id: session.id, draft: e.target.value })}
                          className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-transparent px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-foreground)]"
                        />
                        <button
                          type="submit"
                          disabled={busyId === session.id || renaming.draft.trim() === ""}
                          className="rounded px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-accent)] disabled:opacity-40"
                        >
                          {t("common.save")}
                        </button>
                        <button
                          type="button"
                          onClick={() => setRenaming(null)}
                          className="rounded px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-muted)]"
                        >
                          {t("common.cancel")}
                        </button>
                      </form>
                    ) : (
                      <div
                        className={
                          "group flex items-center gap-1 rounded px-1.5 py-1 " +
                          (active ? "bg-[color:var(--anvil-accent-surface)]" : "hover:bg-[color:var(--anvil-surface)]")
                        }
                      >
                        <button
                          type="button"
                          onClick={() => onOpenSession(session.id)}
                          data-testid="history-session-title"
                          aria-current={active ? "page" : undefined}
                          className="min-w-0 flex-1 truncate text-left text-[11px] text-[color:var(--anvil-foreground)]"
                        >
                          {title}
                        </button>
                        <button
                          type="button"
                          aria-label={renameLabel}
                          title={t("shell.rename")}
                          onClick={() => setRenaming({ id: session.id, draft: title })}
                          className="shrink-0 rounded px-1 text-[10px] text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
                        >
                          {t("shell.rename")}
                        </button>
                        <button
                          type="button"
                          aria-label={pinLabel}
                          title={session.pinned ? t("shell.unpin") : t("shell.pin")}
                          onClick={() =>
                            void mutate(session.id, () =>
                              historyApi.setPinned(session.id, session.revision, !session.pinned)
                            )
                          }
                          className="shrink-0 rounded p-0.5 text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
                        >
                          {session.pinned ? (
                            <PinOff className="h-3 w-3" aria-hidden />
                          ) : (
                            <Pin className="h-3 w-3" aria-hidden />
                          )}
                        </button>
                        <button
                          type="button"
                          aria-label={archiveLabel}
                          title={t("shell.archive")}
                          onClick={() =>
                            void mutate(session.id, () =>
                              historyApi.setArchived(session.id, session.revision, true)
                            )
                          }
                          className="shrink-0 rounded p-0.5 text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
                        >
                          <Archive className="h-3 w-3" aria-hidden />
                        </button>
                        <button
                          type="button"
                          aria-label={deleteLabel}
                          title={t("shell.delete")}
                          onClick={() => setDeleting(session.id)}
                          className="shrink-0 rounded p-0.5 text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-danger)]"
                        >
                          <Trash2 className="h-3 w-3" aria-hidden />
                        </button>
                      </div>
                    )}

                    {deleting === session.id ? (
                      <div className="mt-0.5 flex items-center justify-between gap-2 rounded border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1">
                        <span className="text-[10px] text-[color:var(--anvil-foreground)]">
                          {t("shell.confirmDelete")}
                        </span>
                        <span className="flex items-center gap-1">
                          <button
                            type="button"
                            onClick={() => setDeleting(null)}
                            className="rounded px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                          >
                            {t("common.cancel")}
                          </button>
                          <button
                            type="button"
                            disabled={busyId === session.id}
                            onClick={() =>
                              void mutate(session.id, () =>
                                historyApi.remove(session.id, session.revision)
                              )
                            }
                            className="rounded px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-danger)] disabled:opacity-40"
                          >
                            {t("shell.confirm")}
                          </button>
                        </span>
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          </section>
        ))}

        {state.hasMore ? (
          <div className="px-2 py-1.5">
            <button
              type="button"
              disabled={loading}
              onClick={() => void load(state.nextCursor)}
              className="w-full rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40"
            >
              {t("shell.loadMore")}
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}
