// Tauri host bridge for the React app.
//
// R0-RS02 replaces the Go host's `/__anvil/*` HTTP endpoints with typed Tauri
// commands and a pushed system-theme event. The browser/jsdom paths keep the
// existing `fetch` fallbacks, so the 117 existing tests and the Vite dev server
// behave exactly as before.
//
// The module intentionally does not depend on `@tauri-apps/api`: the two calls
// it needs (`invoke`, `plugin:event|listen/unlisten`) are stable Tauri v2 IPC
// entry points, and avoiding a new npm package keeps `npm ci` deterministic for
// the acceptance runs.

export interface TauriInternals {
  invoke<T = unknown>(command: string, args?: Record<string, unknown>): Promise<T>
  transformCallback?: (callback: (payload: unknown) => void, once?: boolean) => number
}

/** Persisted preferences plus resolved theme, as returned by the host. */
export interface HostPreferences {
  theme: string
  systemDark: boolean
  resolved: string
  language: string
}

/** Event the host emits when the Windows app theme flips. */
export const SYSTEM_THEME_EVENT = 'anvil://system-theme'

/** Event the host emits when the pane should come forward on a URL. */
export const BROWSER_SHOW_EVENT = 'anvil://browser-show'

/** Event payload of {@link BROWSER_SHOW_EVENT}. */
export interface BrowserShowPayload {
  url: string
}

/** Event payload of {@link SYSTEM_THEME_EVENT}. */
export interface SystemThemePayload {
  systemDark: boolean
  resolved: string
}

interface TauriWindow extends Window {
  __TAURI_INTERNALS__?: TauriInternals
}

function internals(): TauriInternals | null {
  if (typeof window === 'undefined') return null
  const candidate = (window as TauriWindow).__TAURI_INTERNALS__
  if (!candidate || typeof candidate.invoke !== 'function') return null
  return candidate
}

/** Whether the page runs inside the Tauri desktop host. */
export function isTauriHost(): boolean {
  return internals() !== null
}

/** Invokes a host command, rejecting when the host is unavailable. */
export function invokeHost<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  const host = internals()
  if (!host) return Promise.reject(new Error('Tauri host is not available'))
  return host.invoke<T>(command, args)
}

/**
 * Subscribes to a host event. Resolves with an unsubscribe function; the caller
 * must be able to unsubscribe synchronously, so the returned cleanup closes a
 * listener that may still be registering.
 */
export async function listenHost<T>(
  event: string,
  handler: (payload: T) => void,
): Promise<() => void> {
  const host = internals()
  if (!host || typeof host.transformCallback !== 'function') {
    throw new Error('Tauri host does not support events')
  }
  // Tauri delivers `{ event, id, payload }` to the registered callback.
  const callbackId = host.transformCallback((event) => {
    const payload = (event as { payload?: T } | null)?.payload
    handler(payload as T)
  })
  await host.invoke('plugin:event|listen', {
    event,
    target: { kind: 'Any' },
    handler: callbackId,
  })
  return () => {
    void host.invoke('plugin:event|unlisten', { event, eventId: callbackId }).catch(() => {})
  }
}

/** Reads the persisted theme/language and the current system theme. */
export function getHostPreferences(): Promise<HostPreferences> {
  return invokeHost<HostPreferences>('get_preferences')
}

/** Persists a theme and/or language patch. */
export function setHostPreferences(patch: {
  theme?: string
  language?: string
}): Promise<HostPreferences> {
  return invokeHost<HostPreferences>('set_preferences', patch)
}

/**
 * Writes one diagnostic line. Uses the typed `log_event` command under Tauri and
 * the historical `/__anvil/log` endpoint elsewhere, so `theme:`/`i18n:`/
 * `layout-probe:` signals keep reaching the host log in both modes.
 */
export function reportHostLog(message: string): void {
  if (isTauriHost()) {
    void invokeHost('log_event', { message }).catch(() => {})
    return
  }
  try {
    // `keepalive` keeps the line alive when the page is unloading (the layout
    // probe fires on unload too), matching the retired host's fetch call.
    void fetch('/__anvil/log', {
      method: 'POST',
      headers: { 'Content-Type': 'text/plain' },
      body: message,
      keepalive: true,
    }).catch(() => {})
  } catch {
    // Diagnostics must never break the UI.
  }
}
