import {
  getHostPreferences,
  isTauriHost,
  reportHostLog,
  setHostPreferences,
} from '../host/tauri'
import { DEFAULT_LANGUAGE, LANGUAGES, type Language } from './catalogue'

export { LANGUAGES, DEFAULT_LANGUAGE }
export type { Language }

export const LANGUAGE_STORAGE_KEY = 'anvil.language'
/**
 * Whether the user ever picked a language by hand.
 *
 * The stored value alone cannot answer that: `zh-CN` is also the built-in
 * default, and the host writes it into preferences.json for a profile that never
 * chose anything. Until a choice exists the UI follows the operating system.
 */
export const LANGUAGE_CHOSEN_KEY = 'anvil.language.chosen'

export interface LanguageStorage {
  load(): Promise<unknown>
  save(language: Language): Promise<void>
}

export interface LanguageEnvironment {
  initialLanguage(): Language
  storage: LanguageStorage
  apply(language: Language): void
}

declare global {
  interface Window {
    __ANVIL_INITIAL_LANGUAGE__?: unknown
  }
}

export function normalizeLanguage(value: unknown): Language {
  if (typeof value !== 'string') return DEFAULT_LANGUAGE
  if (value === 'zh-CN' || value === 'en-US') return value
  return DEFAULT_LANGUAGE
}

/** The UI language the operating system asks for, as far as this build offers it. */
export function systemLanguage(): Language {
  const nav = typeof navigator === 'undefined' ? undefined : navigator
  const tags = [...(nav?.languages ?? []), nav?.language].filter(
    (tag): tag is string => typeof tag === 'string' && tag.trim() !== '',
  )
  for (const tag of tags) {
    const lower = tag.toLowerCase()
    if (lower.startsWith('zh')) return 'zh-CN'
    if (lower.startsWith('en')) return 'en-US'
  }
  // Any other locale reads English more often than Chinese; an empty navigator
  // (very old webview) keeps the build's own default.
  return tags.length > 0 ? 'en-US' : DEFAULT_LANGUAGE
}

export function languageWasChosen(): boolean {
  try {
    return window.localStorage.getItem(LANGUAGE_CHOSEN_KEY) === '1'
  } catch {
    return false
  }
}

function markLanguageChosen(): void {
  try {
    window.localStorage.setItem(LANGUAGE_CHOSEN_KEY, '1')
  } catch {
    // Storage can be unavailable; the host preference still carries the value.
  }
}

/**
 * A stored preference, or `undefined` when the stored value is only the default.
 *
 * `undefined` means "nothing was ever chosen" — the caller keeps following the
 * system instead of pinning the UI to the default.
 */
export function resolveStoredLanguage(raw: unknown): Language | undefined {
  if (typeof raw !== 'string' || raw.trim() === '') return undefined
  const normalized = normalizeLanguage(raw)
  if (normalized !== DEFAULT_LANGUAGE) return normalized
  return languageWasChosen() ? normalized : undefined
}

function injectedLanguage(): Language | null {
  if (typeof window === 'undefined') return null
  const injected = window.__ANVIL_INITIAL_LANGUAGE__
  return typeof injected === 'string' ? normalizeLanguage(injected) : null
}

/**
 * The language of the first frame, before the preferences file is read.
 *
 * Order: a saved choice beats the system; otherwise the system wins, so a
 * Chinese Windows opens in Chinese and an English one in English without anyone
 * touching a setting. `document.documentElement.lang` is deliberately not a
 * signal — index.html ships `zh-CN`, which says nothing about this machine.
 */
export function initialLanguageFromWindow(): Language {
  const injected = injectedLanguage()
  if (injected && injected !== DEFAULT_LANGUAGE) return injected
  if (languageWasChosen()) {
    const stored = readStoredLanguage()
    if (stored) return stored
    if (injected) return injected
  }
  return systemLanguage()
}

function readStoredLanguage(): Language | null {
  try {
    const raw = window.localStorage.getItem(LANGUAGE_STORAGE_KEY)
    return raw ? normalizeLanguage(raw) : null
  } catch {
    return null
  }
}

async function fetchPreferences(): Promise<Record<string, unknown> | null> {
  try {
    const response = await fetch('/__anvil/preferences', { cache: 'no-store' })
    if (!response.ok) return null
    return (await response.json()) as Record<string, unknown>
  } catch {
    return null
  }
}

export function createBrowserLanguageEnvironment(): LanguageEnvironment {
  return {
    initialLanguage: initialLanguageFromWindow,
    storage: {
      async load() {
        const preferences = await fetchPreferences()
        if (preferences && typeof preferences.language === 'string') return preferences.language
        try {
          return window.localStorage.getItem(LANGUAGE_STORAGE_KEY) ?? undefined
        } catch {
          return undefined
        }
      },
      async save(language) {
        markLanguageChosen()
        try {
          window.localStorage.setItem(LANGUAGE_STORAGE_KEY, language)
        } catch {
          // The host preferences file is authoritative.
        }
        try {
          await fetch('/__anvil/preferences', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ language }),
          })
        } catch {
          // Keep the localStorage cache.
        }
      },
    },
    apply(language) {
      document.documentElement.lang = language
      void fetch('/__anvil/log', {
        method: 'POST',
        body: `i18n: language=${language} htmlLang=${document.documentElement.lang}`,
      }).catch(() => {})
    },
  }
}

export const browserLanguageEnvironment = createBrowserLanguageEnvironment()

// ---------------------------------------------------------------------------
// Tauri desktop host (R0-RS02): typed commands, same document/lang side effects.
// ---------------------------------------------------------------------------

export function createTauriLanguageEnvironment(): LanguageEnvironment {
  return {
    initialLanguage: initialLanguageFromWindow,
    storage: {
      async load() {
        try {
          const preferences = await getHostPreferences()
          return preferences.language
        } catch {
          return undefined
        }
      },
      async save(language) {
        markLanguageChosen()
        try {
          await setHostPreferences({ language })
        } catch {
          throw new Error('set_preferences failed')
        }
        try {
          window.localStorage.setItem(LANGUAGE_STORAGE_KEY, language)
        } catch {
          // The host preferences file is authoritative.
        }
      },
    },
    apply(language) {
      document.documentElement.lang = language
      reportHostLog(`i18n: language=${language} htmlLang=${document.documentElement.lang}`)
    },
  }
}

export const tauriLanguageEnvironment = isTauriHost() ? createTauriLanguageEnvironment() : null

/** Environment used by the app: Tauri commands in the desktop host, `fetch`
 * plus `localStorage` in a browser or jsdom. */
export function createHostLanguageEnvironment(): LanguageEnvironment {
  return isTauriHost() ? createTauriLanguageEnvironment() : createBrowserLanguageEnvironment()
}

export const hostLanguageEnvironment = createHostLanguageEnvironment()
