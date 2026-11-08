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
//
// 版式（D1）来自原型 docs/ui/Agent-客户端产品原型 的 `.sidebar-history*` /
// `.history-*`：项目是可折叠的头（图标 + 名称 + 会话数/路径 + 折角），会话行是
// 图标 + 标题 + 次要信息，行尾操作在悬停/聚焦时出现。结构和几何都在 index.css
// 的 `.anvil-*` 里，组件这里只表达语义。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Archive, ChevronRight, FolderOpen, MessageSquare, Pin, PinOff, Plus, SlidersHorizontal, Trash2 } from "lucide-react";
import type { CallError, Workspace } from "../../types";
import { formatDate, formatError, t } from "../../i18n";
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

interface SessionGroup {
  id: string;
  label: string;
  sessions: HistorySession[];
  ordinary: boolean;
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
  // 折叠的项目 id。原型里项目默认展开（点一下折起来），这里保持同一默认：
  // 会话是侧栏的主要用途，默认折起来等于把内容藏掉。
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
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

  const workspacePath = useMemo(() => {
    const map = new Map<string, string>();
    for (const ws of workspaces) map.set(ws.id, ws.canonicalPath);
    return map;
  }, [workspaces]);

  const groups = useMemo<SessionGroup[]>(() => {
    // 分组只影响展示：同一个项目的会话连在一起，未知项目落到"普通会话"。
    const known: SessionGroup[] = [];
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

  const projectGroups = groups.filter((g) => !g.ordinary);
  const ordinaryGroup = groups.find((g) => g.ordinary);

  const hasAny = state.sessions.length > 0;

  const toggleProject = (id: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  /** 一行会话。项目组与普通会话段共用同一套行（原型里也是这样）。 */
  const renderSession = (session: HistorySession) => {
    const title = session.title || t("shell.untitledSession");
    const active = session.id === activeSessionId;
    // 标签先算好再传给属性：属性里直接放模板串会让"文案是否走 catalog"
    // 无法被静态扫描证明（也读不出它其实是 t() 拼出来的）。
    const renameLabel = `${t("shell.rename")} ${title}`;
    const pinLabel = `${session.pinned ? t("shell.unpin") : t("shell.pin")} ${title}`;
    const archiveLabel = `${t("shell.archive")} ${title}`;
    const deleteLabel = `${t("shell.delete")} ${title}`;
    return (
      <li key={session.id}>
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
              className="min-w-0 flex-1 rounded-[var(--anvil-r-xs)] border border-[color:var(--anvil-border-strong)] bg-[color:var(--anvil-surface-raised)] px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-foreground)]"
            />
            <button
              type="submit"
              disabled={busyId === session.id || renaming.draft.trim() === ""}
              className="rounded-[var(--anvil-r-xs)] px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-accent)] disabled:opacity-40"
            >
              {t("common.save")}
            </button>
            <button
              type="button"
              onClick={() => setRenaming(null)}
              className="rounded-[var(--anvil-r-xs)] px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-muted)]"
            >
              {t("common.cancel")}
            </button>
          </form>
        ) : (
          <div className="anvil-history-row" data-pinned={session.pinned ? "true" : "false"}>
            <button
              type="button"
              onClick={() => onOpenSession(session.id)}
              aria-current={active ? "page" : undefined}
              title={title}
              className="anvil-history-item"
            >
              <span className="anvil-history-icon" aria-hidden>
                {session.pinned ? (
                  <Pin className="h-[11px] w-[11px]" />
                ) : (
                  <MessageSquare className="h-[11px] w-[11px]" />
                )}
              </span>
              <span className="anvil-history-copy">
                <span className="anvil-history-title" data-testid="history-session-title">
                    {title}
                  </span>
                <span className="anvil-history-meta">
                  {formatDate(session.updatedAt, { dateStyle: "medium" })}
                </span>
              </span>
            </button>
            <span className="anvil-history-actions">
              <button
                type="button"
                aria-label={renameLabel}
                title={t("shell.rename")}
                onClick={() => setRenaming({ id: session.id, draft: title })}
                className="anvil-history-action"
              >
                <SlidersHorizontal className="h-3 w-3" aria-hidden />
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
                className="anvil-history-action"
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
                className="anvil-history-action"
              >
                <Archive className="h-3 w-3" aria-hidden />
              </button>
              <button
                type="button"
                aria-label={deleteLabel}
                title={t("shell.delete")}
                onClick={() => setDeleting(session.id)}
                className="anvil-history-action"
                data-danger="true"
              >
                <Trash2 className="h-3 w-3" aria-hidden />
              </button>
            </span>
          </div>
        )}

        {deleting === session.id ? (
          <div className="mt-0.5 mb-1 flex items-center justify-between gap-2 rounded-[var(--anvil-r-xs)] border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1">
            <span className="text-[10px] text-[color:var(--anvil-foreground)]">
              {t("shell.confirmDelete")}
            </span>
            <span className="flex items-center gap-1">
              <button
                type="button"
                onClick={() => setDeleting(null)}
                className="rounded-[var(--anvil-r-xs)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
              >
                {t("common.cancel")}
              </button>
              <button
                type="button"
                disabled={busyId === session.id}
                onClick={() =>
                  void mutate(session.id, () => historyApi.remove(session.id, session.revision))
                }
                className="rounded-[var(--anvil-r-xs)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-danger)] disabled:opacity-40"
              >
                {t("shell.confirm")}
              </button>
            </span>
          </div>
        ) : null}
      </li>
    );
  };

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="history-sidebar">
      <div className="anvil-history-head">
        <h2 className="text-[10.5px] font-medium uppercase tracking-[0.06em] text-[color:var(--anvil-muted)]">
          {t("shell.historyTitle")}
        </h2>
        <button
          type="button"
          onClick={onNewSession}
          aria-label={t("shell.newSession")}
          title={t("shell.newSession")}
          className="anvil-history-action"
        >
          <Plus className="h-3.5 w-3.5" aria-hidden />
        </button>
      </div>

      <label className="anvil-history-search">
        <SlidersHorizontal className="h-3 w-3 shrink-0" aria-hidden />
        <select
          aria-label={t("shell.filterLabel")}
          value={workspaceFilter}
          onChange={(e) => onFilterChange(e.target.value)}
        >
          <option value="">{t("shell.filterAll")}</option>
          {workspaces.map((ws) => (
            <option key={ws.id} value={ws.id}>
              {ws.name}
            </option>
          ))}
        </select>
      </label>

      {notice ? (
        <div
          role="alert"
          className="mx-1 mb-2 rounded-[var(--anvil-r-sm)] border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1.5 text-[10.5px] text-[color:var(--anvil-foreground)]"
        >
          <span className="mr-1 font-mono text-[10px] text-[color:var(--anvil-fg-2)]">{notice.code}</span>
          {formatError(notice)}
        </div>
      ) : null}

      <div className="anvil-history-scroll">
        {loading && !hasAny ? (
          <p role="status" className="px-2 py-3 text-[10.5px] text-[color:var(--anvil-muted)]">
            {t("shell.loadingHistory")}
          </p>
        ) : null}

        {/* 侧栏只有 236px 宽、且高度被导航挤得很紧：这里用紧凑态而不是
            页面级的大卡片（大卡片会溢出滚区，把按钮压到页脚上——那是可达性缺陷，
            不只是难看）。 */}
        {!loading && error ? (
          <div
            role="alert"
            className="mx-1 mb-1 flex items-start gap-1.5 rounded-[var(--anvil-r-sm)] border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1.5 text-[10.5px] text-[color:var(--anvil-foreground)]"
          >
            <span className="min-w-0 flex-1">
              <span className="mr-1 font-mono text-[10px] text-[color:var(--anvil-fg-2)]">
                {error.code}
              </span>
              {formatError(error)}
            </span>
            <button
              type="button"
              onClick={() => void reload()}
              className="shrink-0 rounded-[var(--anvil-r-xs)] border border-[color:var(--anvil-danger-border)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-soft)]"
            >
              {t("shell.retryHistory")}
            </button>
          </div>
        ) : null}

        {!loading && !error && !hasAny ? (
          <p className="px-2 py-1.5 text-[10.5px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("shell.emptyHistory")}
            {" · "}
            {workspaceFilter ? t("shell.emptyHistoryHintFiltered") : t("shell.emptyHistoryHint")}
          </p>
        ) : null}

        {projectGroups.length > 0 ? (
          <div className="anvil-history-section-label">{t("shell.projectSessions")}</div>
        ) : null}

        {projectGroups.map((group) => {
          const open = !collapsed.has(group.id);
          const path = workspacePath.get(group.id) ?? "";
          return (
            <section key={group.id} data-testid="history-group" className="anvil-history-project">
              <button
                type="button"
                className="anvil-project-head"
                aria-expanded={open}
                onClick={() => toggleProject(group.id)}
              >
                <span className="anvil-project-icon" aria-hidden>
                  <FolderOpen className="h-3 w-3" />
                </span>
                <span className="min-w-0">
                  <span className="anvil-project-name">{group.label}</span>
                  <span className="anvil-project-meta">
                    {t("shell.projectMeta", { count: group.sessions.length, path })}
                  </span>
                </span>
                <span className="anvil-project-caret" aria-hidden>
                  <ChevronRight className="h-[10px] w-[10px]" />
                </span>
              </button>
              {open ? (
                <ul className="anvil-project-sessions">{group.sessions.map(renderSession)}</ul>
              ) : null}
            </section>
          );
        })}

        {ordinaryGroup ? (
          <section data-testid="history-group">
            <div className="anvil-history-section-label anvil-history-personal-label">
              {ordinaryGroup.label}
            </div>
            <ul className="anvil-history-list">{ordinaryGroup.sessions.map(renderSession)}</ul>
          </section>
        ) : null}

        {state.hasMore ? (
          <div className="px-1 py-1.5">
            <button
              type="button"
              disabled={loading}
              onClick={() => void load(state.nextCursor)}
              className="anvil-history-more"
            >
              {t("shell.loadMore")}
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}
