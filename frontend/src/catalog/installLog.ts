// Live npm output for one-click runtime installs.
//
// The host streams each stdout/stderr line as an event instead of returning a
// single blob at the end. One page can be installing a runtime while another
// page is mounted, so the subscription is shared: the first subscriber opens
// the event bridge and the last one closes it.

import { isTauriHost, listenHost, RUNTIME_INSTALL_LOG_EVENT, type RuntimeInstallLogPayload } from '../host/tauri'

type Listener = (line: string) => void

const listeners = new Map<string, Set<Listener>>()
let subscribed = false

function subscribe(): void {
  if (subscribed || !isTauriHost() || typeof window === 'undefined') return
  subscribed = true
  void listenHost<RuntimeInstallLogPayload>(RUNTIME_INSTALL_LOG_EVENT, (payload) => {
    if (!payload || typeof payload.line !== 'string') return
    for (const listener of [...(listeners.get(payload.runtimeId) ?? [])]) {
      try {
        listener(payload.line)
      } catch {
        // A broken log view must not stop the install.
      }
    }
  }).catch(() => {
    // jsdom or an older host: the final report still carries npm's output.
    subscribed = false
  })
}

/** Subscribe to install output for one runtime. Returns a cleanup function. */
export function onRuntimeInstallLog(runtimeId: string, listener: Listener): () => void {
  let group = listeners.get(runtimeId)
  if (!group) {
    group = new Set()
    listeners.set(runtimeId, group)
  }
  group.add(listener)
  subscribe()
  return () => {
    group?.delete(listener)
    if (group?.size === 0) listeners.delete(runtimeId)
  }
}
