import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  CatalogUnavailable,
  loadCatalog,
  refreshCatalog,
  syncCatalogInventory,
  syncCatalogRuntime,
  type CatalogModel,
  type CatalogRuntime,
  type CatalogSnapshot,
  type CatalogStatus,
} from './client'
import { PREF_AUTO_SYNC, useBoolPreference } from '../preferences/flags'
import { readCachedSnapshot, writeCachedSnapshot } from './cache'
import { takeCatalogPrefetch } from './prefetch'
import { onCatalogScanned } from './watch'

/** How often a visible window re-reads local skills/tools/MCP declarations. */
const AUTO_SYNC_INTERVAL_MS = 5 * 60 * 1000

export interface CatalogView {
  status: CatalogStatus
  snapshot: CatalogSnapshot | null
  error: string | null
  /** Re-scan and re-sync through the host. */
  refresh: () => Promise<void>
  /** A manual sync is in flight; the snapshot on screen is still the old one. */
  refreshing: boolean
  /** Re-read local skills/tools/MCP without probing models. */
  syncInventory: () => Promise<void>
  inventorySyncing: boolean
  /**
   * Re-read one runtime's models. Resolves to whether the host answered, so a
   * tab that failed can be retried on the next open.
   */
  syncRuntime: (runtimeId: string) => Promise<boolean>
  /** Runtime whose model list is being re-read, if any. */
  syncingRuntime: string | null
  lastInventorySyncAt: string | null
  /** Whether the background poll is enabled in settings. */
  autoSync: boolean
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
  const [inventorySyncing, setInventorySyncing] = useState(false)
  const [syncingRuntime, setSyncingRuntime] = useState<string | null>(null)
  const [lastInventorySyncAt, setLastInventorySyncAt] = useState<string | null>(null)
  const [snapshot, setSnapshot] = useState<CatalogSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(true)
  const inventoryInFlight = useRef(false)
  /**
   * Which read is allowed to land.
   *
   * Several readers run at once (the startup snapshot, the cheap inventory
   * sync right after mount, a manual tab sync). The last one to *start* is not
   * necessarily the last to *finish*, so each read remembers its ticket and
   * only the newest one writes to state.
   */
  const readSeq = useRef(0)
  const [autoSync] = useBoolPreference(PREF_AUTO_SYNC, true)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  /**
   * Land a snapshot and decide what it means.
   *
   * An empty snapshot is only "nothing is installed" once discovery has
   * finished. While the host is still scanning it means "nothing read yet", and
   * the pages must show their skeleton instead of an empty state that reads as
   * a broken machine.
   */
  const settle = useCallback((next: CatalogSnapshot) => {
    setSnapshot(next)
    writeCachedSnapshot(next)
    setStatus(next.scanning === true && next.runtimes.length === 0 ? 'loading' : 'ready')
  }, [])

  const load = useCallback(async () => {
    if (!enabled) {
      setStatus('unavailable')
      return
    }
    // Show the last verified snapshot immediately. The host re-discovers in
    // the background below; a returning window no longer waits on a probe that
    // only needs to happen once per process.
    const cached = readCachedSnapshot()
    if (cached) {
      setSnapshot(cached)
      setStatus('ready')
    } else {
      setStatus('loading')
    }
    setError(null)
    const mine = ++readSeq.current
    try {
      // The boot-time prefetch (main.tsx) already asked the host, so the first
      // reader rides along instead of starting a second probe. Later reads —
      // and a retry after a failure — go to the host directly.
      const result = await (takeCatalogPrefetch() ?? loadCatalog())
      if (!mounted.current || mine !== readSeq.current) return
      settle(result)
    } catch (cause) {
      if (!mounted.current) return
      if (cause instanceof CatalogUnavailable) {
        setStatus(cached ? 'ready' : 'unavailable')
        return
      }
      // A background refresh that fails must never blank rows the user is
      // already looking at; keep the cache and surface nothing intrusive.
      if (cached) return
      setError(cause instanceof Error ? cause.message : String(cause))
      setStatus('error')
    }
  }, [enabled, settle])

  useEffect(() => {
    void load()
  }, [load])

  /**
   * Re-read once the host's discovery pass commits.
   *
   * The first snapshot of a process answers from disk, so the rows on screen
   * are one pass behind until this lands. A quiet read: no spinner, no status
   * flip, and a failure leaves the current rows alone.
   */
  useEffect(() => {
    if (!enabled) return
    return onCatalogScanned(() => {
      const mine = ++readSeq.current
      void loadCatalog()
        .then((next) => {
          if (!mounted.current || mine !== readSeq.current) return
          settle(next)
        })
        .catch(() => {
          // Keep what is on screen; the next manual sync retries.
        })
    })
  }, [enabled, settle])

  const refresh = useCallback(async () => {
    if (!enabled) return
    setRefreshing(true)
    setError(null)
    // Keep the current rows on screen while the rescan runs: blanking the model
    // table for several seconds is worse than showing slightly stale data with
    // a spinning control on top.
    setStatus((current) => (current === 'ready' ? 'ready' : 'loading'))
    const mine = ++readSeq.current
    try {
      const result = await refreshCatalog()
      if (!mounted.current || mine !== readSeq.current) return
      setSnapshot(result)
      setStatus('ready')
      writeCachedSnapshot(result)
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

  /**
   * Cheap, silent re-read of the on-disk resource families. Used both by the
   * manual "sync" buttons and by the background poll; a failure must never
   * blank the page or surface an error the user cannot act on.
   */
  const syncInventory = useCallback(async () => {
    if (!enabled || inventoryInFlight.current) return
    inventoryInFlight.current = true
    if (mounted.current) setInventorySyncing(true)
    try {
      const result = await syncCatalogInventory()
      if (mounted.current) {
        readSeq.current += 1
        settle(result)
        setLastInventorySyncAt(new Date().toISOString())
      }
    } catch {
      // No host, a locked database or one broken adapter: keep the rows on
      // screen. The next poll (or the manual button) tries again.
    } finally {
      inventoryInFlight.current = false
      if (mounted.current) setInventorySyncing(false)
    }
  }, [enabled])

  /**
   * Re-read one runtime's models.
   *
   * The models page is tabbed by runtime and calls this when a tab opens, so
   * entering the page never waits on a rescan of every adapter. The snapshot it
   * returns still covers all runtimes, so applying it refreshes the whole view.
   */
  const syncRuntime = useCallback(
    async (runtimeId: string): Promise<boolean> => {
      if (!enabled || !runtimeId) return false
      setSyncingRuntime(runtimeId)
      setError(null)
      try {
        const result = await syncCatalogRuntime(runtimeId)
        if (!mounted.current) return true
        readSeq.current += 1
        settle(result)
        return true
      } catch (cause) {
        // A tab that cannot sync keeps the rows it already had; the page shows
        // its own error line rather than blanking the table.
        if (mounted.current && !(cause instanceof CatalogUnavailable)) {
          setError(cause instanceof Error ? cause.message : String(cause))
        }
        return false
      } finally {
        if (mounted.current) setSyncingRuntime(null)
      }
    },
    [enabled],
  )

  useEffect(() => {
    if (!enabled || !autoSync) return
    const tick = () => {
      if (!document.hidden) void syncInventory()
    }
    // The startup scan only reads model lists; skills, tools and MCP are read
    // here, right after the first paint, instead of delaying the window. The
    // cache means the page is already usable when this lands; model lists are
    // refreshed by the background `catalog_snapshot` after first paint and can
    // be re-synced per-tab from the models page without a blocking rescan.
    tick()
    const timer = window.setInterval(tick, AUTO_SYNC_INTERVAL_MS)
    document.addEventListener('visibilitychange', tick)
    window.addEventListener('focus', tick)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', tick)
      window.removeEventListener('focus', tick)
    }
  }, [enabled, autoSync, syncInventory])

  const apply = useCallback(
    (next: CatalogSnapshot) => {
      settle(next)
    },
    [settle],
  )

  return {
    status,
    snapshot,
    error,
    refresh,
    refreshing,
    syncInventory,
    inventorySyncing,
    syncRuntime,
    syncingRuntime,
    lastInventorySyncAt,
    autoSync,
    apply,
    reload: load,
  }
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
