// 全局搜索（U01 / SRCH01）。
//
// 两个来源合并展示，顺序固定：
//   1. 页面与导航动作——来自静态目录（navigation.ts），永远可用；
//   2. 会话 / Agent / Skill / 产物——来自 Go 侧索引，可能"尚未建立"。
//
// 少于 3 个字符只提示、不发请求：后端也会拒绝（QUERY_TOO_SHORT），
// 但每敲一个字都打一次往返既没必要，也会把"太短"的提示变成一次失败。
//
// 索引未建立时显示 unavailable 与"重建索引"，**不显示 0 条结果**：
// "搜过了没有"和"还没法搜"是两回事。重建是用户可见、可重复的动作。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Search as SearchIcon } from "lucide-react";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatError, t } from "../../i18n";
import type { CallError } from "../../types";
import { searchPages } from "../../app/navigation";
import { searchApi, type SearchResult } from "./api";

/** 与 internal/search 的 MinQueryRunes 一致；后端仍然独立校验一次。 */
export const MIN_QUERY_LENGTH = 3;

/** 连续输入的合并窗口（毫秒）。 */
export const SEARCH_DEBOUNCE_MS = 150;

export interface GlobalSearchProps {
  onNavigate: (route: string) => void;
}

const KIND_ORDER = ["session", "agent", "skill", "artifact"] as const;

export function GlobalSearch({ onNavigate }: GlobalSearchProps) {
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<SearchResult | null>(null);
  const [error, setError] = useState<CallError | null>(null);
  const [loading, setLoading] = useState(false);
  const [rebuilding, setRebuilding] = useState(false);
  const [rebuilt, setRebuilt] = useState(0);
  const seq = useRef(0);

  const trimmed = query.trim();
  const tooShort = trimmed.length > 0 && trimmed.length < MIN_QUERY_LENGTH;
  const pages = useMemo(() => searchPages(trimmed), [trimmed]);

  const runSearch = useCallback(async (text: string) => {
    const current = ++seq.current;
    setLoading(true);
    setError(null);
    try {
      const res = await searchApi.search(text, [], 20);
      if (current !== seq.current) return;
      setResult(res);
      if (res.error) setError(res.error);
    } catch (e) {
      if (current !== seq.current) return;
      setError({ code: "INTERNAL_ERROR", message: String(e), retryable: true });
      setResult(null);
    } finally {
      if (current === seq.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (trimmed.length < MIN_QUERY_LENGTH) {
      // 太短时清掉上一次结果：留着旧结果会让用户以为它对应新的输入。
      setResult(null);
      setError(null);
      setLoading(false);
      return;
    }
    // 输入过程中每个字符都打一次往返既浪费也会让结果闪烁；
    // 150ms 足够把一次连续输入合并成一次查询，又不至于让人等。
    const timer = setTimeout(() => void runSearch(trimmed), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [trimmed, runSearch]);

  const rebuild = useCallback(async () => {
    setRebuilding(true);
    setError(null);
    try {
      const res = await searchApi.rebuildIndex();
      if (res.error) {
        setError(res.error);
        return;
      }
      const summary = res.summary;
      setRebuilt(
        (summary?.sessions ?? 0) + (summary?.agents ?? 0) + (summary?.skills ?? 0) + (summary?.artifacts ?? 0)
      );
      if (trimmed.length >= MIN_QUERY_LENGTH) await runSearch(trimmed);
    } catch (e) {
      setError({ code: "INTERNAL_ERROR", message: String(e), retryable: true });
    } finally {
      setRebuilding(false);
    }
  }, [runSearch, trimmed]);

  const items = useMemo(() => {
    const list = result?.items ?? [];
    return [...list].sort((a, b) => {
      const ka = KIND_ORDER.indexOf(a.kind);
      const kb = KIND_ORDER.indexOf(b.kind);
      if (ka !== kb) return ka - kb;
      return a.rank - b.rank;
    });
  }, [result]);

  const unavailable = result?.availability === "unavailable" && !error;
  const hasResults = items.length > 0 || pages.length > 0;

  return (
    <div className="relative w-full" data-testid="global-search">
      <label className="sr-only" htmlFor="global-search-input">
        {t("search.title")}
      </label>
      <div className="flex items-center gap-1.5 rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-1">
        <SearchIcon className="h-3.5 w-3.5 shrink-0 text-[color:var(--anvil-muted)]" aria-hidden />
        <input
          id="global-search-input"
          aria-label={t("search.title")}
          placeholder={t("search.placeholder")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              setQuery("");
              setResult(null);
            }
          }}
          className="min-w-0 flex-1 bg-transparent text-[11px] text-[color:var(--anvil-foreground)] outline-none placeholder:text-[color:var(--anvil-muted)]"
        />
        {loading ? (
          <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">{t("state.loading")}</span>
        ) : null}
      </div>

      {trimmed.length > 0 ? (
        <div className="absolute left-0 right-0 z-20 mt-1 max-h-80 overflow-y-auto rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] p-2 shadow-lg">
          {tooShort ? (
            <p className="px-1 py-1 text-[11px] text-[color:var(--anvil-muted)]">{t("search.tooShort")}</p>
          ) : null}

          {rebuilt > 0 ? (
            <p className="px-1 py-1 text-[11px] text-[color:var(--anvil-success)]">
              {t("search.rebuilt", { count: rebuilt })}
            </p>
          ) : null}

          {error ? (
            <div role="alert" className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px]">
              <span className="mr-1 font-mono text-[10px]">{error.code}</span>
              {formatError(error)}
            </div>
          ) : null}

          {unavailable ? (
            <div className="space-y-1 rounded border border-[color:var(--anvil-border)] px-2 py-1.5">
              <div className="flex items-center gap-1.5">
                <StatusBadge status="unavailable" />
                <span className="text-[11px] text-[color:var(--anvil-muted)]">
                  {t("search.indexUnavailable")}
                </span>
              </div>
              <button
                type="button"
                onClick={() => void rebuild()}
                disabled={rebuilding}
                className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40"
              >
                {rebuilding ? t("search.rebuilding") : t("search.rebuild")}
              </button>
            </div>
          ) : null}

          {pages.length > 0 && !tooShort ? (
            <div data-testid="search-pages" className="mt-1">
              <h3 className="px-1 py-0.5 text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
                {t("search.pagesSection")}
              </h3>
              <ul>
                {pages.map((page) => (
                  <li key={page.route}>
                    <button
                      type="button"
                      onClick={() => onNavigate(page.route)}
                      className="flex w-full items-center justify-between gap-2 rounded px-1.5 py-1 text-left hover:bg-[color:var(--anvil-surface)]"
                    >
                      <span className="truncate text-[11px] text-[color:var(--anvil-foreground)]">
                        {page.title}
                      </span>
                      <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                        {t("search.kinds.page")}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {items.length > 0 ? (
            <div data-testid="search-results" className="mt-1">
              <h3 className="px-1 py-0.5 text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
                {t("search.resultsSection")}
              </h3>
              <ul>
                {items.map((item) => (
                  <li key={`${item.kind}:${item.refId}`}>
                    <button
                      type="button"
                      onClick={() => onNavigate(item.route)}
                      className="flex w-full items-center justify-between gap-2 rounded px-1.5 py-1 text-left hover:bg-[color:var(--anvil-surface)]"
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[11px] text-[color:var(--anvil-foreground)]">
                          {item.title}
                        </span>
                        {item.subtitle ? (
                          <span className="block truncate text-[10px] text-[color:var(--anvil-muted)]">
                            {item.subtitle}
                          </span>
                        ) : null}
                      </span>
                      <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                        {t(`search.kinds.${item.kind}`)}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {!tooShort && !error && !unavailable && !hasResults ? (
            <p className="px-1 py-1 text-[11px] text-[color:var(--anvil-muted)]">{t("search.noResults")}</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
