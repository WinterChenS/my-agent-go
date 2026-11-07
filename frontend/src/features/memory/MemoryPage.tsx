// 记忆页面（U05 / UI09）。
//
// 三条语义必须同时说清，否则界面会撒谎：
//   1. disabled ≠ deleted：按 scope 关闭注入只是"之后不再注入"，条目还在列表里，
//      并带"未注入"标识；删除才是真的从列表消失（且仍无法撤回已发出的历史消息）。
//   2. candidate ≠ active：候选要用户确认后才生效。
//   3. empty ≠ unavailable：没有条目是空态；库打不开是故障，显示原因码。
//
// 检索错误按 code 分支：QUERY_TOO_SHORT 提示"至少 3 个字符"，不能渲染成空结果。

import { useCallback, useEffect, useMemo, useState } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { formatError, t } from "../../i18n";
import type { CallError } from "../../types";
import {
  memoryApi,
  type MemoryAvailability,
  type MemoryExportResult,
  type MemoryItem,
  type MemoryScope,
  type MemoryScopeState,
} from "./api";

const MIN_QUERY_CHARS = 3;
const KINDS = ["convention", "preference", "fact", "note"] as const;
const SCOPES: MemoryScope[] = ["user", "workspace"];

export function MemoryPage() {
  const [items, setItems] = useState<MemoryItem[]>([]);
  const [scopes, setScopes] = useState<MemoryScopeState[]>([]);
  const [availability, setAvailability] = useState<MemoryAvailability>({ available: true });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);
  const [actionError, setActionError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [scopeFilter, setScopeFilter] = useState<"all" | MemoryScope>("all");
  const [query, setQuery] = useState("");
  const [searching, setSearching] = useState(false);
  const [results, setResults] = useState<MemoryItem[] | null>(null);
  const [shortQuery, setShortQuery] = useState(false);

  const [content, setContent] = useState("");
  const [kind, setKind] = useState<string>("convention");
  const [saveScope, setSaveScope] = useState<MemoryScope>("workspace");
  const [saving, setSaving] = useState(false);

  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [confirmedId, setConfirmedId] = useState<string | null>(null);
  const [exported, setExported] = useState<MemoryExportResult | null>(null);
  const [exporting, setExporting] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await memoryApi.list();
      setAvailability(res.availability ?? { available: true });
      if (res.error) {
        setError(res.error);
        setItems([]);
        setScopes([]);
        return;
      }
      setItems(res.items ?? []);
      setScopes(res.scopes ?? []);
    } catch (e) {
      setError(asCallError(e));
      setItems([]);
      setScopes([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const scopeEnabled = useCallback(
    (scope: MemoryScope): boolean => {
      const found = scopes.find((s) => s.scope === scope);
      // 没有开关记录时按"默认开启"显示，但只在列表行为里体现；
      // 这里返回 true 不会掩盖故障——故障由 availability/error 表达。
      return found ? found.enabled : true;
    },
    [scopes]
  );

  const visible = useMemo(() => {
    const source = results ?? items;
    if (scopeFilter === "all") return source;
    return source.filter((m) => m.scope === scopeFilter);
  }, [results, items, scopeFilter]);

  function charCount(text: string): number {
    return [...text.trim()].length;
  }

  function isExpired(item: MemoryItem): boolean {
    if (!item.expiresAt) return false;
    const ts = Date.parse(item.expiresAt);
    return Number.isFinite(ts) && ts <= Date.now();
  }

  async function runSearch() {
    setActionError(null);
    setNotice(null);
    setShortQuery(false);
    if (charCount(query) < MIN_QUERY_CHARS) {
      // 后端也会拒；前端先拦是为了不用等一次请求才知道"太短"。
      setShortQuery(true);
      setResults(null);
      return;
    }
    setSearching(true);
    try {
      const res = await memoryApi.search(query.trim());
      if (res.error) {
        if (res.error.code === "QUERY_TOO_SHORT") {
          setShortQuery(true);
        } else {
          setActionError(res.error);
        }
        setResults(null);
        return;
      }
      setResults(res.items ?? []);
    } catch (e) {
      setActionError(asCallError(e));
      setResults(null);
    } finally {
      setSearching(false);
    }
  }

  function clearSearch() {
    setQuery("");
    setResults(null);
    setShortQuery(false);
    setActionError(null);
  }

  async function saveMemory() {
    setActionError(null);
    setNotice(null);
    setSaving(true);
    try {
      const res = await memoryApi.save(content.trim(), kind, saveScope);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setContent("");
      setNotice(t("memory.notice.saved"));
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    } finally {
      setSaving(false);
    }
  }

  async function confirmCandidate(id: string) {
    setActionError(null);
    setNotice(null);
    try {
      const res = await memoryApi.confirm(id);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setConfirmedId(id);
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  async function remove(id: string) {
    setActionError(null);
    setNotice(null);
    setConfirmDelete(null);
    setDeletingId(id);
    try {
      const res = await memoryApi.remove(id);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setNotice(t("memory.notice.deleted"));
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    } finally {
      setDeletingId(null);
    }
  }

  async function toggleScope(scope: MemoryScope) {
    setActionError(null);
    setNotice(null);
    try {
      const next = !scopeEnabled(scope);
      const res = await memoryApi.setScopeEnabled(scope, next);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      // 用返回值更新开关：不从点击推断状态。
      setScopes((prev) =>
        prev.map((s) => (s.scope === scope ? { ...s, enabled: res.state?.enabled ?? next } : s))
      );
      setNotice(next ? t("memory.notice.scopeEnabled", { scope: t(`memory.scope.${scope}`) }) : t("memory.notice.scopeDisabled", { scope: t(`memory.scope.${scope}`) }));
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  async function exportJson() {
    setActionError(null);
    setExporting(true);
    try {
      const res = await memoryApi.exportJson();
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setExported(res);
    } catch (e) {
      setActionError(asCallError(e));
    } finally {
      setExporting(false);
    }
  }

  // ---- 状态判定：不可用 / 阻塞 / 空 / 有内容
  if (loading) {
    return (
      <div className="mx-auto max-w-3xl px-4 py-5">
        <AsyncState phase="loading" />
      </div>
    );
  }
  if (!availability.available) {
    return (
      <div className="mx-auto max-w-3xl space-y-3 px-4 py-5">
        <MemoryHeader />
        <AsyncState
          phase="blocked"
          reason={
            availability.reasonCode
              ? t("memory.unavailableHint", { code: availability.reasonCode })
              : t("memory.unavailableHintUnknown")
          }
          onRetry={() => void load()}
        />
      </div>
    );
  }
  if (error) {
    return (
      <div className="mx-auto max-w-3xl space-y-3 px-4 py-5">
        <MemoryHeader />
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      </div>
    );
  }

  const activeCount = items.filter((m) => m.status === "active" && !isExpired(m)).length;
  const empty = items.length === 0 && results === null;

  return (
    <div className="mx-auto max-w-3xl space-y-4 px-4 py-5">
      <MemoryHeader />

      {/* 作用域开关：关闭只影响注入，不删条目 */}
      <section className="space-y-1.5 rounded-lg border border-[color:var(--anvil-border)] p-2.5">
        <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
          {t("memory.scopeToggle.title")}
        </p>
        <ul className="space-y-1">
          {SCOPES.map((scope) => {
            const enabled = scopeEnabled(scope);
            return (
              <li key={scope} className="flex flex-wrap items-center gap-2 text-[11px]">
                <span className="text-[color:var(--anvil-foreground)]">{t(`memory.scope.${scope}`)}</span>
                <span
                  data-testid={`scope-state-${scope}`}
                  className={
                    enabled
                      ? "text-[color:var(--anvil-success)]"
                      : "text-[color:var(--anvil-warning)]"
                  }
                >
                  {enabled ? t("memory.scopeToggle.enabled") : t("memory.scopeToggle.disabled")}
                </span>
                <button
                  type="button"
                  onClick={() => void toggleScope(scope)}
                  className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                >
                  {enabled ? t("memory.scopeToggle.disable") : t("memory.scopeToggle.enable")}
                </button>
              </li>
            );
          })}
        </ul>
        <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("memory.scopeToggle.hint")}
        </p>
      </section>

      {actionError ? (
        <p role="alert" className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]">
          {formatError(actionError)}
        </p>
      ) : null}
      {notice ? (
        <p role="status" className="text-[11px] text-[color:var(--anvil-muted)]">
          {notice}
        </p>
      ) : null}

      {/* 检索与筛选 */}
      <section className="space-y-2">
        <div className="flex flex-wrap items-end gap-2">
          <label className="block flex-1 text-[10px] text-[color:var(--anvil-muted)]">
            {t("memory.searchLabel")}
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              aria-label={t("memory.searchLabel")}
              placeholder={t("memory.searchPlaceholder")}
              className="mt-1 w-full rounded-md border border-[color:var(--anvil-border)] bg-transparent px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)]"
            />
          </label>
          <button
            type="button"
            onClick={() => void runSearch()}
            disabled={searching}
            className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
          >
            {searching ? t("memory.searching") : t("memory.search")}
          </button>
          {results !== null ? (
            <button
              type="button"
              onClick={clearSearch}
              className="rounded-md px-2 py-1 text-[10px] text-[color:var(--anvil-muted)]"
            >
              {t("memory.clearSearch")}
            </button>
          ) : null}
        </div>
        <div className="flex flex-wrap items-center gap-2 text-[10px]">
          <span className="text-[color:var(--anvil-muted)]">{t("memory.filterLabel")}</span>
          {(["all", "user", "workspace"] as const).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={scopeFilter === value}
              onClick={() => setScopeFilter(value)}
              className={`rounded-md px-2 py-0.5 ${
                scopeFilter === value
                  ? "bg-[color:var(--anvil-surface)] text-[color:var(--anvil-foreground)]"
                  : "text-[color:var(--anvil-muted)]"
              }`}
            >
              {value === "all" ? t("memory.filterAll") : t(`memory.scope.${value}`)}
            </button>
          ))}
          <span className="text-[color:var(--anvil-muted)]">
            {t("memory.activeCount", { count: activeCount })}
          </span>
          <button
            type="button"
            onClick={() => void exportJson()}
            disabled={exporting}
            className="ml-auto rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
          >
            {exporting ? t("memory.exporting") : t("memory.export")}
          </button>
        </div>
        {shortQuery ? (
          <p role="alert" className="text-[11px] text-[color:var(--anvil-warning)]">
            {t("memory.tooShort", { count: MIN_QUERY_CHARS })}
          </p>
        ) : null}
      </section>

      {exported ? (
        <section className="space-y-1">
          <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
            {t("memory.exportTitle", { count: exported.itemCount })}
          </p>
          <pre
            data-testid="memory-export"
            className="max-h-56 overflow-auto rounded-md border border-[color:var(--anvil-border)] p-2 font-mono text-[10px] text-[color:var(--anvil-foreground)]"
          >
            {exported.json}
          </pre>
        </section>
      ) : null}

      {/* 新增一条记忆 */}
      <section className="space-y-1.5 rounded-lg border border-[color:var(--anvil-border)] p-2.5">
        <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">{t("memory.new.title")}</p>
        <textarea
          value={content}
          onChange={(e) => setContent(e.target.value)}
          aria-label={t("memory.new.contentLabel")}
          placeholder={t("memory.new.contentPlaceholder")}
          rows={2}
          className="w-full rounded-md border border-[color:var(--anvil-border)] bg-transparent p-2 text-[11px] text-[color:var(--anvil-foreground)]"
        />
        <div className="flex flex-wrap items-center gap-2 text-[10px]">
          <label className="flex items-center gap-1 text-[color:var(--anvil-muted)]">
            {t("memory.new.kindLabel")}
            <select
              value={kind}
              onChange={(e) => setKind(e.target.value)}
              aria-label={t("memory.new.kindLabel")}
              className="rounded-md border border-[color:var(--anvil-border)] bg-transparent px-1 py-0.5 text-[10px] text-[color:var(--anvil-foreground)]"
            >
              {KINDS.map((k) => (
                <option key={k} value={k}>
                  {t(`memory.kind.${k}`)}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1 text-[color:var(--anvil-muted)]">
            {t("memory.new.scopeLabel")}
            <select
              value={saveScope}
              onChange={(e) => setSaveScope(e.target.value as MemoryScope)}
              aria-label={t("memory.new.scopeLabel")}
              className="rounded-md border border-[color:var(--anvil-border)] bg-transparent px-1 py-0.5 text-[10px] text-[color:var(--anvil-foreground)]"
            >
              {SCOPES.map((s) => (
                <option key={s} value={s}>
                  {t(`memory.scope.${s}`)}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            onClick={() => void saveMemory()}
            disabled={saving || content.trim() === ""}
            className="ml-auto rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
          >
            {saving ? t("memory.new.saving") : t("memory.new.save")}
          </button>
        </div>
      </section>

      {/* 列表：empty 与 unavailable 已经分开；这里只剩"有内容/检索无结果" */}
      {empty ? (
        <AsyncState
          phase="empty"
          emptyTitle={t("memory.emptyTitle")}
          emptyHint={t("memory.emptyHint")}
        />
      ) : visible.length === 0 ? (
        <p className="text-[11px] text-[color:var(--anvil-muted)]">{t("memory.noResults")}</p>
      ) : (
        <ul className="space-y-2">
          {visible.map((item) => {
            const injected = item.status === "active" && !isExpired(item) && scopeEnabled(item.scope);
            const disabledScope = !scopeEnabled(item.scope);
            return (
              <li key={item.id} data-testid={`memory-${item.id}`}>
                <article className="space-y-1 rounded-lg border border-[color:var(--anvil-border)] p-2.5">
                  <div className="flex flex-wrap items-center gap-2 text-[10px]">
                    <span className="rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[color:var(--anvil-muted)]">
                      {t(`memory.scope.${item.scope}`)}
                    </span>
                    <span className="rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[color:var(--anvil-muted)]">
                      {t(`memory.kind.${item.kind}`)}
                    </span>
                    <span
                      className={
                        item.status === "active"
                          ? "text-[color:var(--anvil-success)]"
                          : "text-[color:var(--anvil-warning)]"
                      }
                    >
                      {t(`memory.status.${item.status}`)}
                    </span>
                    {disabledScope ? (
                      <span
                        data-testid={`memory-not-injecting-${item.id}`}
                        className="text-[color:var(--anvil-warning)]"
                      >
                        {t("memory.notInjecting")}
                      </span>
                    ) : null}
                    {item.status === "candidate" ? (
                      <span className="text-[color:var(--anvil-warning)]">{t("memory.candidateHint")}</span>
                    ) : null}
                    {isExpired(item) ? (
                      <span className="text-[color:var(--anvil-warning)]">{t("memory.expired")}</span>
                    ) : null}
                    {confirmedId === item.id ? (
                      <span className="text-[color:var(--anvil-success)]">{t("memory.notice.confirmed")}</span>
                    ) : null}
                  </div>
                  <p className="whitespace-pre-wrap text-[12px] text-[color:var(--anvil-foreground)]">
                    {item.content}
                  </p>
                  <p className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("memory.source")}: {item.sourceSessionId || t("memory.sourceUnknown")} ·{" "}
                    {t("memory.created")}: {item.createdAt}
                    {item.expiresAt ? ` · ${t("memory.expires")}: ${item.expiresAt}` : ""}
                  </p>
                  <div className="flex flex-wrap gap-2 pt-0.5">
                    {item.status === "candidate" ? (
                      <button
                        type="button"
                        onClick={() => void confirmCandidate(item.id)}
                        className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)]"
                      >
                        {t("memory.confirm")}
                      </button>
                    ) : null}
                    {confirmDelete === item.id ? (
                      <>
                        <button
                          type="button"
                          onClick={() => void remove(item.id)}
                          className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-danger)]"
                        >
                          {t("memory.deleteConfirm")}
                        </button>
                        <button
                          type="button"
                          onClick={() => setConfirmDelete(null)}
                          className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                        >
                          {t("common.cancel")}
                        </button>
                      </>
                    ) : (
                      <button
                        type="button"
                        onClick={() => setConfirmDelete(item.id)}
                        disabled={deletingId === item.id}
                        className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
                      >
                        {t("memory.delete")}
                      </button>
                    )}
                    <span className="text-[10px] text-[color:var(--anvil-muted)]">{t("memory.deleteHint")}</span>
                  </div>
                </article>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function MemoryHeader() {
  return (
    <header className="space-y-1">
      <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">{t("memory.title")}</h1>
      <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">{t("memory.subtitle")}</p>
    </header>
  );
}

function asCallError(e: unknown): CallError {
  if (typeof e === "object" && e !== null && "code" in e) {
    return e as CallError;
  }
  return { code: "INTERNAL_ERROR", message: String(e), retryable: false };
}
