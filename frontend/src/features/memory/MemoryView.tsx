// 记忆（C05）。
//
// 这一页要同时说清三件容易混淆的事：
//   1. 条目的状态：active 会参与检索/注入；candidate 只是候选，界面必须显式标"未注入"；
//   2. 作用范围：user 跨项目共享，workspace 只属于当前项目，按 scope 可关闭注入；
//   3. 删除的边界：删除只影响"之后运行的注入"，已经发给模型的历史消息无法撤回。
//
// 短查询（少于 3 个 Unicode 字符）显示提示，而不是一个空列表——空列表会让人
// 以为记忆里真的没有相关内容，而原因其实是查询太短。

import { useCallback, useEffect, useMemo, useState } from "react";
import { BrainCircuit, Check, Download, Plus, Search, Trash2 } from "lucide-react";
import { api } from "../../api";
import type { CallError, MemoryItem, MemoryScope, MemoryScopeState } from "../../types";
import { toCallError } from "../../app/useBackend";
import { EmptyState, ErrorState, LoadingState } from "../../components/StateViews";

/** 与 Go 侧 memory.MinQueryRunes 一致（按 Unicode 码点算，不是字节）。 */
const MIN_QUERY_CHARS = 3;
const SHORT_QUERY_HINT = `请输入至少 ${MIN_QUERY_CHARS} 个字符`;

const SCOPE_LABEL: Record<MemoryScope, string> = {
  user: "用户级（跨项目）",
  workspace: "项目级（仅当前项目）",
};

const KIND_LABEL: Record<string, string> = {
  fact: "事实",
  preference: "偏好",
  convention: "约定",
  note: "备注",
};

const KIND_OPTIONS = ["convention", "preference", "fact", "note"];

function charCount(text: string): number {
  return [...text.trim()].length;
}

function isExpired(m: MemoryItem): boolean {
  if (!m.expiresAt) return false;
  const t = Date.parse(m.expiresAt);
  return Number.isFinite(t) && t <= Date.now();
}

export function MemoryView() {
  const [items, setItems] = useState<MemoryItem[]>([]);
  const [scopes, setScopes] = useState<MemoryScopeState[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [query, setQuery] = useState("");
  const [shortQuery, setShortQuery] = useState<string | null>(null);
  const [results, setResults] = useState<MemoryItem[] | null>(null);
  const [searching, setSearching] = useState(false);

  const [content, setContent] = useState("");
  const [kind, setKind] = useState("convention");
  const [scope, setScope] = useState<MemoryScope>("workspace");
  const [saving, setSaving] = useState(false);

  const [busyId, setBusyId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [exported, setExported] = useState<{ json: string; count: number } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await api.memories();
      if (res.error) {
        setError(res.error);
        setItems([]);
        setScopes([]);
      } else {
        setItems(res.items ?? []);
        setScopes(res.scopes ?? []);
      }
    } catch (e) {
      setError(toCallError(e));
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const visible = results ?? items;
  const activeCount = useMemo(
    () => visible.filter((m) => m.status === "active" && !isExpired(m)).length,
    [visible]
  );

  async function runSearch() {
    setError(null);
    setNotice(null);
    setShortQuery(null);
    if (charCount(query) < MIN_QUERY_CHARS) {
      // 后端也会返回 QUERY_TOO_SHORT；先在前端拦住，避免"查了个空"。
      setShortQuery(SHORT_QUERY_HINT);
      setResults(null);
      return;
    }
    setSearching(true);
    try {
      const res = await api.searchMemories(query.trim());
      if (res.error) {
        if (res.error.code === "QUERY_TOO_SHORT") {
          setShortQuery(SHORT_QUERY_HINT);
        } else {
          setError(res.error);
        }
        setResults(null);
        return;
      }
      setResults(res.items ?? []);
    } catch (e) {
      setError(toCallError(e));
      setResults(null);
    } finally {
      setSearching(false);
    }
  }

  function clearSearch() {
    setQuery("");
    setResults(null);
    setShortQuery(null);
  }

  function replaceItem(next: MemoryItem) {
    setItems((prev) => prev.map((m) => (m.id === next.id ? next : m)));
    setResults((prev) => (prev ? prev.map((m) => (m.id === next.id ? next : m)) : prev));
  }

  async function saveMemory() {
    setError(null);
    setNotice(null);
    if (!content.trim()) {
      setNotice("请先写下要记住的内容。");
      return;
    }
    setSaving(true);
    try {
      const res = await api.saveMemory(content.trim(), kind, scope);
      if (res.error) {
        setError(res.error);
        return;
      }
      if (res.item) {
        setItems((prev) => [res.item as MemoryItem, ...prev]);
        setResults(null);
      }
      setContent("");
      setNotice("已保存。它会在之后运行的检索中作为参考数据出现，不会被当成系统指令。");
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setSaving(false);
    }
  }

  async function confirm(id: string) {
    setError(null);
    setNotice(null);
    setBusyId(id);
    try {
      const res = await api.confirmMemory(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      if (res.item) replaceItem(res.item);
      setNotice("已确认。该条目从下一次运行起参与注入。");
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  async function remove(id: string) {
    setError(null);
    setNotice(null);
    setBusyId(id);
    try {
      const res = await api.deleteMemory(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setItems((prev) => prev.filter((m) => m.id !== id));
      setResults((prev) => (prev ? prev.filter((m) => m.id !== id) : prev));
      setDeletingId(null);
      setNotice("已删除。之后的运行不会再注入它；已经发给模型的历史消息无法撤回。");
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  async function toggleScope(state: MemoryScopeState) {
    setError(null);
    setNotice(null);
    setBusyId(`scope:${state.scope}`);
    try {
      const res = await api.setMemoryScopeEnabled(state.scope, !state.enabled);
      if (res.error) {
        setError(res.error);
        return;
      }
      setScopes((prev) =>
        prev.map((s) => (s.scope === res.state.scope ? res.state : s))
      );
      setNotice(
        res.state.enabled
          ? `${SCOPE_LABEL[res.state.scope]} 的注入已开启（下一次运行生效）。`
          : `${SCOPE_LABEL[res.state.scope]} 的注入已关闭；条目仍在，只是不再参与检索与注入。`
      );
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  async function exportMemories() {
    setError(null);
    setNotice(null);
    try {
      const res = await api.exportMemories();
      if (res.error) {
        setError(res.error);
        return;
      }
      setExported({ json: res.json, count: res.itemCount });
      setNotice("导出只在本地显示，不会上传到任何服务。");
    } catch (e) {
      setError(toCallError(e));
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-4 py-5">
      <header className="mb-3">
        <h2 className="text-sm font-medium text-slate-200">记忆</h2>
        <p className="mt-0.5 text-[11px] text-slate-500">
          跨会话保留的上下文。项目之间互相隔离，不会把 A 项目的记忆带进 B 项目；
          用户级条目在所有项目可见。
        </p>
      </header>

      {error && (
        <div className="mb-3">
          <ErrorState error={error} onRetry={() => void load()} />
        </div>
      )}

      {loading ? (
        <LoadingState label="读取记忆…" />
      ) : (
        <div className="space-y-4">
          {/* 检索 */}
          <section className="rounded-xl border border-white/10 bg-white/[0.02] p-3">
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void runSearch();
              }}
              className="flex items-end gap-2"
            >
              <label className="min-w-0 flex-1">
                <span className="mb-1 block text-[11px] text-slate-400">搜索记忆</span>
                <input
                  aria-label="搜索记忆"
                  value={query}
                  onChange={(e) => {
                    setQuery(e.target.value);
                    if (shortQuery) setShortQuery(null);
                  }}
                  placeholder="例如：数据库连接池"
                  className="w-full rounded-md border border-white/10 bg-slate-950/60 px-2 py-1.5 text-xs text-slate-200 placeholder:text-slate-600 focus:border-sky-400/60 focus:outline-none"
                />
              </label>
              <button
                type="submit"
                disabled={searching}
                className="inline-flex items-center gap-1 rounded-md border border-white/10 px-2.5 py-1.5 text-xs text-slate-200 hover:bg-white/5 disabled:opacity-50"
              >
                <Search className="h-3.5 w-3.5" aria-hidden />
                {searching ? "检索中…" : "搜索"}
              </button>
              {results && (
                <button
                  type="button"
                  onClick={clearSearch}
                  className="rounded-md border border-white/10 px-2.5 py-1.5 text-xs text-slate-400 hover:bg-white/5"
                >
                  清空检索
                </button>
              )}
            </form>
            {shortQuery && (
              <p role="alert" className="mt-2 text-xs text-amber-300">
                {shortQuery}
              </p>
            )}
            {results && !shortQuery && (
              <p className="mt-2 text-[11px] text-slate-500">
                检索命中 {results.length} 条（只含已生效且未过期；候选与已删除不参与）。
              </p>
            )}
          </section>

          {/* 范围开关 */}
          <section aria-label="注入范围" className="rounded-xl border border-white/10 bg-white/[0.02] p-3">
            <h3 className="mb-2 text-[11px] font-medium text-slate-300">注入范围</h3>
            <ul className="space-y-1.5">
              {scopes.map((s) => (
                <li key={s.scope} className="flex items-center justify-between gap-2 text-xs">
                  <span className="text-slate-300">
                    {SCOPE_LABEL[s.scope]}{" "}
                    <span className={s.enabled ? "text-emerald-300" : "text-slate-500"}>
                      {s.enabled ? "注入开启" : "注入已关闭"}
                    </span>
                  </span>
                  <button
                    type="button"
                    disabled={busyId === `scope:${s.scope}`}
                    onClick={() => void toggleScope(s)}
                    className="rounded-md border border-white/10 px-2 py-1 text-[11px] text-slate-300 hover:bg-white/5 disabled:opacity-50"
                  >
                    {s.enabled ? "关闭注入" : "开启注入"}
                  </button>
                </li>
              ))}
            </ul>
          </section>

          {/* 列表 */}
          <section aria-label="记忆列表">
            <div className="mb-1.5 flex items-center justify-between">
              <h3 className="text-[11px] font-medium text-slate-300">
                {results ? "检索结果" : "全部记忆"}
                <span className="ml-1.5 text-slate-500">
                  {visible.length} 条，可用 {activeCount} 条
                </span>
              </h3>
              <button
                type="button"
                onClick={() => void exportMemories()}
                className="inline-flex items-center gap-1 rounded-md border border-white/10 px-2 py-1 text-[11px] text-slate-300 hover:bg-white/5"
              >
                <Download className="h-3 w-3" aria-hidden />
                导出
              </button>
            </div>

            {visible.length === 0 ? (
              <EmptyState
                title={results ? "没有命中的记忆" : "还没有保存任何记忆"}
                hint={
                  results
                    ? "换一个更具体的关键词（至少 3 个字符）。候选条目与已过期条目不参与检索。"
                    : "在下面写一条，或让模型通过 memory_save 工具显式保存。V1 不会在后台自动抽取记忆。"
                }
                icon={<BrainCircuit className="h-4 w-4" aria-hidden />}
              />
            ) : (
              <ul className="space-y-1.5">
                {visible.map((m) => (
                  <MemoryRow
                    key={m.id}
                    item={m}
                    busy={busyId === m.id}
                    confirmingDelete={deletingId === m.id}
                    onConfirm={() => void confirm(m.id)}
                    onAskDelete={() => setDeletingId(m.id)}
                    onCancelDelete={() => setDeletingId(null)}
                    onDelete={() => void remove(m.id)}
                  />
                ))}
              </ul>
            )}
          </section>

          {/* 新建 */}
          <section className="rounded-xl border border-white/10 bg-white/[0.02] p-3">
            <h3 className="mb-2 text-[11px] font-medium text-slate-300">新建记忆</h3>
            <textarea
              aria-label="记忆内容"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              rows={3}
              placeholder="例如：数据库连接池上限为二十"
              className="w-full rounded-md border border-white/10 bg-slate-950/60 px-2 py-1.5 text-xs text-slate-200 placeholder:text-slate-600 focus:border-sky-400/60 focus:outline-none"
            />
            <div className="mt-2 flex flex-wrap items-end gap-2">
              <label className="text-[11px] text-slate-400">
                类型
                <select
                  aria-label="记忆类型"
                  value={kind}
                  onChange={(e) => setKind(e.target.value)}
                  className="ml-1 rounded-md border border-white/10 bg-slate-950/60 px-1.5 py-1 text-xs text-slate-200"
                >
                  {KIND_OPTIONS.map((k) => (
                    <option key={k} value={k}>
                      {KIND_LABEL[k] ?? k}
                    </option>
                  ))}
                </select>
              </label>
              <label className="text-[11px] text-slate-400">
                范围
                <select
                  aria-label="记忆范围"
                  value={scope}
                  onChange={(e) => setScope(e.target.value as MemoryScope)}
                  className="ml-1 rounded-md border border-white/10 bg-slate-950/60 px-1.5 py-1 text-xs text-slate-200"
                >
                  <option value="workspace">项目级（仅当前项目）</option>
                  <option value="user">用户级（跨项目）</option>
                </select>
              </label>
              <button
                type="button"
                disabled={saving}
                onClick={() => void saveMemory()}
                className="inline-flex items-center gap-1 rounded-md bg-sky-500/90 px-2.5 py-1.5 text-xs font-medium text-slate-950 hover:bg-sky-400 disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" aria-hidden />
                {saving ? "保存中…" : "保存记忆"}
              </button>
            </div>
            <p className="mt-2 text-[11px] text-slate-500">
              显式保存直接生效。密钥类文本会被拒绝：请把凭证放进凭证库，不要把工具日志原文存成记忆。
            </p>
          </section>

          {notice && (
            <p role="status" className="rounded-md border border-white/10 bg-white/[0.03] px-2.5 py-1.5 text-[11px] text-slate-300">
              {notice}
            </p>
          )}

          {exported && (
            <section className="rounded-xl border border-white/10 bg-slate-950/40 p-3">
              <div className="mb-1.5 flex items-center justify-between">
                <h3 className="text-[11px] font-medium text-slate-300">
                  导出内容（{exported.count} 条，仅本地）
                </h3>
                <button
                  type="button"
                  onClick={() => setExported(null)}
                  className="rounded-md border border-white/10 px-2 py-0.5 text-[11px] text-slate-400 hover:bg-white/5"
                >
                  收起
                </button>
              </div>
              <pre
                aria-label="记忆导出 JSON"
                className="max-h-64 overflow-auto rounded-md bg-black/40 p-2 text-[11px] leading-relaxed text-slate-300"
              >
                {exported.json}
              </pre>
            </section>
          )}
        </div>
      )}
    </div>
  );
}

function MemoryRow({
  item,
  busy,
  confirmingDelete,
  onConfirm,
  onAskDelete,
  onCancelDelete,
  onDelete,
}: {
  item: MemoryItem;
  busy: boolean;
  confirmingDelete: boolean;
  onConfirm: () => void;
  onAskDelete: () => void;
  onCancelDelete: () => void;
  onDelete: () => void;
}) {
  const expired = isExpired(item);
  const candidate = item.status === "candidate";
  return (
    <li className="rounded-lg border border-white/10 bg-white/[0.02] px-2.5 py-2">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <p className="break-words text-xs text-slate-200">{item.content}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px]">
            <span className="rounded bg-white/5 px-1.5 py-0.5 text-slate-400">
              {SCOPE_LABEL[item.scope]}
            </span>
            <span className="rounded bg-white/5 px-1.5 py-0.5 text-slate-400">
              {KIND_LABEL[item.kind] ?? item.kind}
            </span>
            {candidate && (
              <span className="rounded bg-amber-400/10 px-1.5 py-0.5 text-amber-300">
                候选 · 未注入
              </span>
            )}
            {item.status === "active" && !expired && (
              <span className="rounded bg-emerald-400/10 px-1.5 py-0.5 text-emerald-300">
                已生效
              </span>
            )}
            {expired && (
              <span className="rounded bg-rose-400/10 px-1.5 py-0.5 text-rose-300">
                已过期 · 不注入
              </span>
            )}
            <span className="text-slate-500">
              来源会话：{item.sourceSessionId || "未记录"}
            </span>
            {item.expiresAt && (
              <span className="text-slate-500">过期时间：{item.expiresAt}</span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {candidate && (
            <button
              type="button"
              disabled={busy}
              onClick={onConfirm}
              className="inline-flex items-center gap-1 rounded-md border border-emerald-400/30 px-1.5 py-1 text-[11px] text-emerald-200 hover:bg-emerald-400/10 disabled:opacity-50"
            >
              <Check className="h-3 w-3" aria-hidden />
              确认
            </button>
          )}
          {confirmingDelete ? (
            <>
              <button
                type="button"
                disabled={busy}
                onClick={onDelete}
                className="rounded-md bg-rose-500/90 px-1.5 py-1 text-[11px] font-medium text-slate-950 hover:bg-rose-400 disabled:opacity-50"
              >
                确认删除
              </button>
              <button
                type="button"
                onClick={onCancelDelete}
                className="rounded-md border border-white/10 px-1.5 py-1 text-[11px] text-slate-400 hover:bg-white/5"
              >
                取消
              </button>
            </>
          ) : (
            <button
              type="button"
              aria-label={`删除记忆 ${item.id}`}
              onClick={onAskDelete}
              className="inline-flex items-center gap-1 rounded-md border border-white/10 px-1.5 py-1 text-[11px] text-slate-400 hover:bg-white/5"
            >
              <Trash2 className="h-3 w-3" aria-hidden />
              删除
            </button>
          )}
        </div>
      </div>
    </li>
  );
}
