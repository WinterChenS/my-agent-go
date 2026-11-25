// Whether a newer Anvil exists, shared by the sidebar badge and the settings
// card.
//
// This is a module-level store rather than context because the two places that
// show it (sidebar, settings) are mounted at different times and one of them is
// often unmounted: a check started from the sidebar must survive a navigation,
// and a download must not restart because the page re-rendered.
//
// The update itself is performed by the host (`app_update_install`), which
// downloads, verifies the signature and installs before restarting the app. The
// page never sees a file path and never decides whether a build is trustworthy.

import { invokeHost, listenHost } from '../host/tauri'

/** Streamed by the host while a download / install runs. */
export const UPDATE_EVENT = 'anvil://app-update'

export interface UpdateInfo {
  version: string
  notes: string | null
  date: string | null
  /** Platform key the endpoint matched, e.g. `windows-x86_64`. */
  target: string
  size: number | null
}

export interface UpdateCheck {
  current: string
  available: UpdateInfo | null
  /** Whether this build can replace itself (false for a `.deb` install). */
  installable: boolean
  /** `package` | `development` | `platform`, when it cannot. */
  reason: string | null
  /** Where a build that cannot install itself should send the reader. */
  releaseUrl: string
}

export interface UpdateProgress {
  phase: 'downloading' | 'installing' | 'done'
  received: number
  total: number | null
  rate: number | null
}

export type UpdateStatus =
  | 'idle'
  | 'checking'
  | 'current'
  | 'available'
  | 'downloading'
  | 'installing'
  | 'installed'
  | 'error'

export interface UpdateState {
  status: UpdateStatus
  check: UpdateCheck | null
  progress: UpdateProgress | null
  error: string | null
  /** When the last successful check finished, for "checked 2 min ago". */
  checkedAt: number | null
}

const initial: UpdateState = {
  status: 'idle',
  check: null,
  progress: null,
  error: null,
  checkedAt: null,
}

let state: UpdateState = initial
const listeners = new Set<() => void>()

function set(patch: Partial<UpdateState>) {
  state = { ...state, ...patch }
  for (const listener of listeners) listener()
}

export function updateSnapshot(): UpdateState {
  return state
}

export function subscribeUpdates(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause)
}

/** True while the host is doing something that cannot be interrupted. */
function busy(status: UpdateStatus): boolean {
  return status === 'checking' || status === 'downloading' || status === 'installing'
}

/**
 * Ask the release endpoint for the newest build.
 *
 * `quiet` is the background check: a failure leaves the status alone instead of
 * turning the sidebar into an error message the reader never asked for. The
 * settings card always shows whatever happened, so a check that could not reach
 * GitHub is still visible where the reader went looking for it.
 */
export async function checkForUpdate(options: { quiet?: boolean } = {}): Promise<void> {
  if (busy(state.status)) return
  const before = state.status
  set({ status: 'checking', error: null })
  try {
    const check = await invokeHost<UpdateCheck>('app_update_check')
    set({
      status: check.available ? 'available' : 'current',
      check,
      checkedAt: Date.now(),
      error: null,
    })
  } catch (cause) {
    if (options.quiet) {
      // Nothing to report: the endpoint is unreachable, which on a laptop that
      // is offline is the normal case, not a bug.
      set({ status: before === 'checking' ? 'idle' : before })
      return
    }
    set({ status: 'error', error: message(cause) })
  }
}

/**
 * Download, verify and install, then restart into the new build.
 *
 * The host restarts the process, so this promise normally never resolves: the
 * page is torn down while the new build starts. The states before that are what
 * the card renders, and they are driven by the host's progress events rather
 * than by guesswork here.
 */
export async function installUpdate(): Promise<void> {
  if (busy(state.status)) return
  if (!state.check?.available) return
  set({
    status: 'downloading',
    progress: { phase: 'downloading', received: 0, total: null, rate: null },
    error: null,
  })
  let unsubscribe: (() => void) | null = null
  try {
    unsubscribe = await listenHost<UpdateProgress>(UPDATE_EVENT, (progress) => {
      if (progress.phase === 'installing') set({ status: 'installing', progress })
      else if (progress.phase === 'done') set({ status: 'installed', progress })
      else set({ status: 'downloading', progress })
    })
  } catch {
    // No event channel (host without events): the button still works, it just
    // cannot show a progress bar.
  }
  try {
    await invokeHost('app_update_install')
    // Reached only when the host could not restart itself; say so rather than
    // leave the card claiming it is installing for ever.
    set({ status: 'installed' })
  } catch (cause) {
    set({ status: 'error', error: message(cause) })
  } finally {
    unsubscribe?.()
  }
}

/** Reset a failed attempt so the button is usable again. */
export function dismissUpdateError(): void {
  set({ status: state.check?.available ? 'available' : 'current', error: null })
}

let watching = false

/**
 * Return the store to its initial state.
 *
 * Exported for tests, which share one module instance across cases: without it
 * a check that ran in an earlier case decides what the next one sees.
 */
export function resetUpdateStore(): void {
  state = initial
  watching = false
  for (const listener of listeners) listener()
}

/**
 * Start the background check, once per app run.
 *
 * Delayed on purpose: the first seconds after a launch belong to the window and
 * the session list, and a network request for a file nobody clicked would only
 * compete with them.
 */
export function startUpdateWatch(delayMs = 8000): void {
  if (watching) return
  watching = true
  window.setTimeout(() => {
    void checkForUpdate({ quiet: true })
  }, delayMs)
}
