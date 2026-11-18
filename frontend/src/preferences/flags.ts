// Local boolean preferences shared by pages that are mounted at different
// times. They are deliberately localStorage, not host preferences: these are
// per-window UI choices, and the settings page must be able to change one
// while the chat page stays mounted.

import { useCallback, useEffect, useState } from 'react'

/** Show the turn-timeline card in chat. Off by default. */
export const PREF_SHOW_TIMELINE = 'anvil.chat.showTimeline'
/** Poll local skills/tools/MCP declarations in the background. On by default. */
export const PREF_AUTO_SYNC = 'anvil.catalog.autoSync'
/** First-run setup (pick a runtime + model) has been completed. */
export const PREF_ONBOARDING_DONE = 'anvil.onboarding.done'
/**
 * Which key sends a composer draft.
 *
 * `enter` means Enter sends and Shift+Enter breaks the line; `ctrl-enter` keeps
 * the old behaviour where Enter is a newline. Stored as a string, not a bool,
 * because there are exactly two named choices and the settings page shows them.
 */
export const PREF_SEND_SHORTCUT = 'anvil.chat.sendShortcut'
export type SendShortcut = 'enter' | 'ctrl-enter'

const EVENT = 'anvil:preference'

export function readBoolPreference(key: string, fallback: boolean): boolean {
  try {
    const raw = window.localStorage.getItem(key)
    if (raw === null) return fallback
    return raw === '1' || raw === 'true'
  } catch {
    return fallback
  }
}

export function writeBoolPreference(key: string, value: boolean): void {
  try {
    window.localStorage.setItem(key, value ? '1' : '0')
  } catch {
    // Private mode / disabled storage: the choice still applies this session.
  }
  window.dispatchEvent(new CustomEvent(EVENT, { detail: { key, value } }))
}

/** A controlled boolean preference that follows changes made on another page. */
export function useBoolPreference(key: string, fallback = false): [boolean, (value: boolean) => void] {
  const [value, setValue] = useState(() => readBoolPreference(key, fallback))
  useEffect(() => {
    const onPreference = (event: Event) => {
      const detail = (event as CustomEvent<{ key: string; value: boolean }>).detail
      if (detail?.key === key) setValue(detail.value)
    }
    const onStorage = (event: StorageEvent) => {
      if (event.key === key) setValue(readBoolPreference(key, fallback))
    }
    window.addEventListener(EVENT, onPreference)
    window.addEventListener('storage', onStorage)
    return () => {
      window.removeEventListener(EVENT, onPreference)
      window.removeEventListener('storage', onStorage)
    }
  }, [key, fallback])
  const set = useCallback(
    (next: boolean) => {
      writeBoolPreference(key, next)
      setValue(next)
    },
    [key],
  )
  return [value, set]
}

export function readStringPreference(key: string, fallback: string): string {
  try {
    return window.localStorage.getItem(key) ?? fallback
  } catch {
    return fallback
  }
}

export function writeStringPreference(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value)
  } catch {
    // Same trade-off as the boolean writer: the choice still applies now.
  }
  window.dispatchEvent(new CustomEvent(EVENT, { detail: { key, value } }))
}

/** A controlled string preference that follows changes made on another page. */
export function useStringPreference(
  key: string,
  fallback: string,
): [string, (value: string) => void] {
  const [value, setValue] = useState(() => readStringPreference(key, fallback))
  useEffect(() => {
    const reread = () => setValue(readStringPreference(key, fallback))
    const onPreference = (event: Event) => {
      const detail = (event as CustomEvent<{ key: string }>).detail
      if (detail?.key === key) reread()
    }
    const onStorage = (event: StorageEvent) => {
      if (event.key === key) reread()
    }
    window.addEventListener(EVENT, onPreference)
    window.addEventListener('storage', onStorage)
    return () => {
      window.removeEventListener(EVENT, onPreference)
      window.removeEventListener('storage', onStorage)
    }
  }, [key, fallback])
  const set = useCallback(
    (next: string) => {
      writeStringPreference(key, next)
      setValue(next)
    },
    [key],
  )
  return [value, set]
}

/** The composer's send key, with a guard against an unexpected stored value. */
export function useSendShortcut(): [SendShortcut, (value: SendShortcut) => void] {
  const [raw, setRaw] = useStringPreference(PREF_SEND_SHORTCUT, 'enter')
  const value: SendShortcut = raw === 'ctrl-enter' ? 'ctrl-enter' : 'enter'
  return [value, (next) => setRaw(next)]
}
