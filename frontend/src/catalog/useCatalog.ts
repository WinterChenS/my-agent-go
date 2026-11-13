import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  CatalogUnavailable,
  loadCatalog,
  refreshCatalog,
  type CatalogModel,
  type CatalogRuntime,
  type CatalogSnapshot,
  type CatalogStatus,
} from './client'

export interface CatalogView {
  status: CatalogStatus
  snapshot: CatalogSnapshot | null
  error: string | null
  /** Re-scan and re-sync through the host. */
  refresh: () => Promise<void>
  /** A manual sync is in flight; the snapshot on screen is still the old one. */
  refreshing: boolean
  /** Replace the snapshot after an action already returned a fresh one. */
  apply: (snapshot: CatalogSnapshot) => void
  /** Re-run the first load (used by the retry control). */
  reload: () => Promise<void>
}

/**
 * Loads the catalog once and exposes refresh/apply.
 *
 * The host performs the startup scan on the first `catalog_snapshot`, so the
 * loading state also covers discovery. Outside the desktop host the hook stays
 * `unavailable` and the pages keep their honest empty state.
 */
export function useCatalog(enabled = true): CatalogView {
  const [status, setStatus] = useState<CatalogStatus>('idle')
  const [refreshing, setRefreshing] = useState(false)
  const [snapshot, setSnapshot] = useState<CatalogSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const load = useCallback(async () => {
    if (!enabled) {
      setStatus('unavailable')
      return
    }
    setStatus('loading')
    setError(null)
    try {
      const result = await loadCatalog()
      if (!mounted.current) return
      setSnapshot(result)
      setStatus('ready')
    } catch (cause) {
      if (!mounted.current) return
      if (cause instanceof CatalogUnavailable) {
        setStatus('unavailable')
        return
      }
      setError(cause instanceof Error ? cause.message : String(cause))
      setStatus('error')
    }
  }, [enabled])

  useEffect(() => {
    void load()
  }, [load])

  const refresh = useCallback(async () => {
    if (!enabled) return
    setRefreshing(true)
    setError(null)
    // Keep the current rows on screen while the rescan runs: blanking the model
    // table for several seconds is worse than showing slightly stale data with
    // a spinning control on top.
    setStatus((current) => (current === 'ready' ? 'ready' : 'loading'))
    try {
      const result = await refreshCatalog()
      if (!mounted.current) return
      setSnapshot(result)
      setStatus('ready')
    } catch (cause) {
      if (!mounted.current) return
      if (cause instanceof CatalogUnavailable) {
        setStatus('unavailable')
        return
      }
      setError(cause instanceof Error ? cause.message : String(cause))
      setStatus('error')
    } finally {
      if (mounted.current) setRefreshing(false)
    }
  }, [enabled])

  const apply = useCallback((next: CatalogSnapshot) => {
    setSnapshot(next)
    setStatus('ready')
  }, [])

  return { status, snapshot, error, refresh, refreshing, apply, reload: load }
}

/** Models of one runtime, or all of them. */
export function modelsFor(
  snapshot: CatalogSnapshot | null,
  runtimeId?: string,
): CatalogModel[] {
  if (!snapshot) return []
  return runtimeId
    ? snapshot.models.filter((model) => model.runtimeId === runtimeId)
    : snapshot.models
}

/** Runtimes that expose a usable session surface, for the Agent page. */
export function runnableRuntimes(snapshot: CatalogSnapshot | null): CatalogRuntime[] {
  if (!snapshot) return []
  return snapshot.runtimes.filter((runtime) => runtime.status === 'available')
}

/** Group a list by runtime id, keeping the snapshot order. */
export function byRuntime<T extends { runtimeId: string }>(items: T[]): Map<string, T[]> {
  const grouped = new Map<string, T[]>()
  for (const item of items) {
    const bucket = grouped.get(item.runtimeId)
    if (bucket) bucket.push(item)
    else grouped.set(item.runtimeId, [item])
  }
  return grouped
}

/** Default model across runtimes, if one was promoted. */
export function defaultModel(snapshot: CatalogSnapshot | null): CatalogModel | null {
  if (!snapshot) return null
  return snapshot.models.find((model) => model.isDefault) ?? null
}

/** Whether any runtime declared the capability as supported. */
export function supports(
  runtimes: CatalogRuntime[],
  capability: keyof CatalogRuntime['capabilities'],
): boolean {
  return runtimes.some((runtime) => runtime.capabilities[capability] === 'supported')
}

/** Stable list of sync problems for the summary line. */
export function syncProblems(snapshot: CatalogSnapshot | null): string[] {
  if (!snapshot) return []
  const stale = snapshot.syncs
    .filter((sync) => !sync.ok)
    .map((sync) => `${sync.runtimeId}/${sync.kind}: ${sync.error ?? 'failed'}`)
  const failed = snapshot.adapterFailures.map(
    (failure) => `${failure.runtimeId}/${failure.phase}: ${failure.error}`,
  )
  return [...failed, ...stale]
}

/** Memo-friendly key for a snapshot so effects can depend on identity. */
export function snapshotKey(snapshot: CatalogSnapshot | null): string {
  if (!snapshot) return ''
  return [snapshot.scannedAt, snapshot.runtimes.length, snapshot.models.length, snapshot.skills.length, snapshot.mcp.length].join(':')
}

/** Convenience hook for components that only need the grouped view. */
export function useGrouped<T extends { runtimeId: string }>(items: T[]): Map<string, T[]> {
  return useMemo(() => byRuntime(items), [items])
}
