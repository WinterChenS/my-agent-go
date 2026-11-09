export type ThemeMode = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

export const THEME_MODES: readonly ThemeMode[] = ['system', 'light', 'dark']
export const DEFAULT_THEME: ThemeMode = 'system'
export const THEME_STORAGE_KEY = 'anvil.theme'

export interface ThemeStorage {
  load(): Promise<unknown>
  save(mode: ThemeMode): Promise<void>
}

export interface ThemeEnvironment {
  storage: ThemeStorage
  systemDark(): boolean
  subscribeSystem(listener: (dark: boolean) => void): () => void
  apply(resolved: ResolvedTheme, mode: ThemeMode): void
}

declare global {
  interface Window {
    __ANVIL_INITIAL_THEME__?: {
      mode?: unknown
      resolved?: unknown
    }
  }
}

export function normalizeTheme(value: unknown): ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system' ? value : DEFAULT_THEME
}

export function resolveTheme(mode: ThemeMode, systemDark: boolean): ResolvedTheme {
  if (mode === 'system') return systemDark ? 'dark' : 'light'
  return mode
}

export function initialThemeFromWindow(): { mode: ThemeMode; resolved: ResolvedTheme } | null {
  if (typeof window === 'undefined') return null
  const initial = window.__ANVIL_INITIAL_THEME__
  if (!initial || typeof initial !== 'object') return null
  const mode = normalizeTheme(initial.mode)
  const resolved = initial.resolved === 'dark' || initial.resolved === 'light'
    ? initial.resolved
    : resolveTheme(mode, readSystemDark())
  return { mode, resolved }
}

function mediaQuery(): MediaQueryList | undefined {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return undefined
  return window.matchMedia('(prefers-color-scheme: dark)')
}

export function readSystemDark(): boolean {
  return mediaQuery()?.matches ?? false
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

export function createBrowserThemeEnvironment(): ThemeEnvironment {
  return {
    storage: {
      async load() {
        const preferences = await fetchPreferences()
        if (preferences && typeof preferences.theme === 'string') return preferences.theme
        try {
          return window.localStorage.getItem(THEME_STORAGE_KEY) ?? undefined
        } catch {
          return undefined
        }
      },
      async save(mode) {
        try {
          window.localStorage.setItem(THEME_STORAGE_KEY, mode)
        } catch {
          // localStorage can be unavailable; the host file is authoritative.
        }
        try {
          await fetch('/__anvil/preferences', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ theme: mode }),
          })
        } catch {
          // Keep the localStorage cache; the next launch can retry.
        }
      },
    },
    systemDark: readSystemDark,
    subscribeSystem(listener) {
      const query = mediaQuery()
      let last = query?.matches ?? false
      const onChange = () => {
        if (!query || query.matches === last) return
        last = query.matches
        listener(last)
      }
      query?.addEventListener?.('change', onChange)
      query?.addListener?.(onChange)

      // Host fallback: WebView2 theme forwarding is observed in the native
      // case, but polling the host endpoint also covers environments where
      // the media query does not fire.
      const timer = window.setInterval(() => {
        void fetchPreferences().then((preferences) => {
          if (!preferences || typeof preferences.systemDark !== 'boolean') return
          if (preferences.systemDark === last) return
          last = preferences.systemDark
          listener(last)
        })
      }, 1200)

      return () => {
        query?.removeEventListener?.('change', onChange)
        query?.removeListener?.(onChange)
        window.clearInterval(timer)
      }
    },
    apply(resolved, mode) {
      const root = document.documentElement
      root.dataset.theme = resolved
      root.style.colorScheme = resolved
      const background = typeof getComputedStyle === 'function'
        ? getComputedStyle(root).getPropertyValue('--bg').trim()
        : ''
      void fetch('/__anvil/log', {
        method: 'POST',
        body: `theme: mode=${mode} resolved=${resolved} bg=${background || 'unknown'}`,
      }).catch(() => {})
    },
  }
}

export const browserThemeEnvironment = createBrowserThemeEnvironment()
