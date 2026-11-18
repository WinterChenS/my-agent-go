// The host's background discovery finished.
//
// `catalog_snapshot` answers from disk immediately and scans behind it, so the
// rows on screen are one pass behind at boot. When that pass commits, the host
// emits this event and every mounted catalog view re-reads the snapshot — no
// poll, no spinner, no page that sits on an empty table waiting for a probe.
//
// One listener for the whole page: `useCatalog` runs once per surface (chat,
// settings → runtime, settings → models, …), and subscribing each of them would
// register a handler per mount for a single host event.

import { isTauriHost, listenHost } from '../host/tauri'

/** Event the host emits after the startup discovery pass commits. */
export const CATALOG_SCANNED_EVENT = 'anvil://catalog-scanned'

type Listener = () => void

const listeners = new Set<Listener>()
let subscribed = false

function subscribe(): void {
  if (subscribed || !isTauriHost() || typeof window === 'undefined') return
  subscribed = true
  void listenHost(CATALOG_SCANNED_EVENT, () => {
    for (const listener of [...listeners]) {
      try {
        listener()
      } catch {
        // One surface failing to refresh must not stop the others.
      }
    }
  }).catch(() => {
    // No event bridge (jsdom, an old host): the pages still work, they just
    // stay on the snapshot the first read returned.
    subscribed = false
  })
}

/** Calls `listener` when the host's discovery pass lands. Returns the cleanup. */
export function onCatalogScanned(listener: Listener): () => void {
  listeners.add(listener)
  subscribe()
  return () => {
    listeners.delete(listener)
  }
}
