// The catalog snapshot is persisted between launches so a returning window can
// render runtimes/models immediately from disk while the host re-discovers them
// in the background. Discovery (the first `catalog_snapshot` of a process) probes
// every runtime and can take seconds; without a cache every page mount showed a
// "loading" state and the composer stayed disabled until that probe finished.
//
// A stale cache is still useful: the rows it holds are real, previously verified
// entries, and the background refresh reconciles them. A malformed or old-version
// cache is discarded rather than trusted.

import type { CatalogSnapshot } from './client'

const CACHE_KEY = 'anvil.catalog.snapshot'
const CACHE_VERSION = 1

interface CachedSnapshot {
  version: number
  snapshot: CatalogSnapshot
  savedAt: number
}

export function readCachedSnapshot(): CatalogSnapshot | null {
  try {
    const raw = window.localStorage.getItem(CACHE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as CachedSnapshot
    if (parsed.version !== CACHE_VERSION || !parsed.snapshot) return null
    const snap = parsed.snapshot
    if (!Array.isArray(snap.runtimes) || !Array.isArray(snap.models)) return null
    return snap
  } catch {
    return null
  }
}

export function writeCachedSnapshot(snapshot: CatalogSnapshot | null): void {
  try {
    if (!snapshot) {
      window.localStorage.removeItem(CACHE_KEY)
      return
    }
    // A snapshot taken while discovery is still running carries no rows on a
    // fresh profile, and the cache's whole job is to have rows on the next
    // launch. Keeping the previous (real) snapshot is strictly better than
    // overwriting it with an empty one — which would also read as "this profile
    // has been set up" to the wizard's returning-window check.
    if (snapshot.scanning === true && snapshot.runtimes.length === 0) return
    const payload: CachedSnapshot = { version: CACHE_VERSION, snapshot, savedAt: Date.now() }
    window.localStorage.setItem(CACHE_KEY, JSON.stringify(payload))
  } catch {
    // Storage can be full or disabled; caching is an optimization, never a
    // correctness requirement. The live snapshot stays authoritative.
  }
}

/**
 * A cache that can actually open the composer: a runtime that can start + a
 * model.
 *
 * `unauthenticated` counts — it is discovery failing to find a credential file,
 * and the runtime authenticates itself when it starts. Leaving it out made a
 * working install wait on a full startup probe before the picker would paint.
 */
export function cacheUsable(snapshot: CatalogSnapshot | null): boolean {
  if (!snapshot) return false
  return (
    snapshot.runtimes.some(
      (runtime) => runtime.status === 'available' || runtime.status === 'unauthenticated',
    ) && snapshot.models.length > 0
  )
}
