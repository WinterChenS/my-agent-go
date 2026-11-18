// Start the host's first catalog read before React asks for it.
//
// The first `catalog_snapshot` of a process is what makes the host probe every
// installed runtime and build the catalog. On a cold profile that probe is the
// multi-second stall the first screen used to sit behind. Firing it from
// `main.tsx` lets it overlap with the app booting and with the parts of setup
// that need no data at all (the theme step), so by the time the runtime list is
// on screen discovery is usually already finished.
//
// The promise is handed out once and only for the first read; a retry must hit
// the host again rather than replay a failure.

import { isTauriHost } from '../host/tauri'
import { writeCachedSnapshot } from './cache'
import { loadCatalog, type CatalogSnapshot } from './client'

let pending: Promise<CatalogSnapshot> | null = null

/** Kick off discovery. Safe to call more than once; only the first hits the host. */
export function startCatalogPrefetch(): Promise<CatalogSnapshot> | null {
  if (!isTauriHost()) return null
  if (pending) return pending
  pending = loadCatalog()
    .then((snapshot) => {
      // Write it through so every other reader (pages, chat) starts from the
      // same rows instead of each waiting on its own read.
      writeCachedSnapshot(snapshot)
      return snapshot
    })
    .catch((cause) => {
      pending = null
      throw cause
    })
  return pending
}

/** Consume the prefetch, if one is in flight. Later calls get nothing. */
export function takeCatalogPrefetch(): Promise<CatalogSnapshot> | null {
  const value = pending
  pending = null
  return value
}
