import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import {
  hostThemeEnvironment,
  initialThemeFromWindow,
  normalizeTheme,
  resolveTheme,
  type ResolvedTheme,
  type ThemeEnvironment,
  type ThemeMode,
} from './theme'

export type ThemeNotice = '' | 'invalid' | 'read-failed' | 'save-failed'

export interface ThemeContextValue {
  mode: ThemeMode
  resolved: ResolvedTheme
  systemDark: boolean
  notice: ThemeNotice
  setMode(mode: ThemeMode): void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

export function ThemeProvider({
  children,
  environment = hostThemeEnvironment,
}: {
  children: ReactNode
  environment?: ThemeEnvironment
}) {
  const initial = initialThemeFromWindow()
  const [mode, setModeState] = useState<ThemeMode>(initial?.mode ?? 'system')
  const [systemDark, setSystemDark] = useState<boolean>(() =>
    initial ? initial.resolved === 'dark' : environment.systemDark(),
  )
  const [notice, setNotice] = useState<ThemeNotice>('')

  useEffect(() => {
    let active = true
    void Promise.resolve(environment.storage.load())
      .then((raw) => {
        if (!active) return
        const normalized = normalizeTheme(raw)
        if (raw !== undefined && raw !== normalized) {
          setNotice('invalid')
        }
        setModeState(normalized)
        setSystemDark(environment.systemDark())
      })
      .catch(() => {
        if (active) setNotice('read-failed')
      })
    return () => {
      active = false
    }
  }, [environment])

  useEffect(() => environment.subscribeSystem((dark) => setSystemDark(dark)), [environment])

  const resolved = resolveTheme(mode, systemDark)

  useEffect(() => {
    environment.apply(resolved, mode)
  }, [environment, mode, resolved])

  const setMode = useCallback(
    (next: ThemeMode) => {
      const normalized = normalizeTheme(next)
      if (normalized === 'system') setSystemDark(environment.systemDark())
      setModeState(normalized)
      setNotice('')
      void Promise.resolve(environment.storage.save(normalized)).catch(() => {
        setNotice('save-failed')
      })
    },
    [environment],
  )

  return (
    <ThemeContext.Provider value={{ mode, resolved, systemDark, notice, setMode }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme(): ThemeContextValue {
  const value = useContext(ThemeContext)
  if (!value) throw new Error('useTheme must be used inside ThemeProvider')
  return value
}
