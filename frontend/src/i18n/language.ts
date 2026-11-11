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

export function initialLanguageFromWindow(): Language {
  if (typeof window !== 'undefined' && typeof window.__ANVIL_INITIAL_LANGUAGE__ === 'string') {
    return normalizeLanguage(window.__ANVIL_INITIAL_LANGUAGE__)
  }
  if (typeof document !== 'undefined') {
    return normalizeLanguage(document.documentElement.lang)
  }
  return DEFAULT_LANGUAGE
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
