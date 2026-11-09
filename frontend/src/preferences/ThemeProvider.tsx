import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import {
  browserThemeEnvironment,
  initialThemeFromWindow,
  normalizeTheme,
  resolveTheme,
  type ResolvedTheme,
  type ThemeEnvironment,
  type ThemeMode,
} from './theme'

export interface ThemeContextValue {
  mode: ThemeMode
  resolved: ResolvedTheme
  systemDark: boolean
  notice: string
  setMode(mode: ThemeMode): void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

const INVALID_NOTICE = '主题设置无效，已回退到跟随系统'

export function ThemeProvider({
  children,
  environment = browserThemeEnvironment,
}: {
  children: ReactNode
  environment?: ThemeEnvironment
}) {
  const initial = initialThemeFromWindow()
  const [mode, setModeState] = useState<ThemeMode>(initial?.mode ?? 'system')
  const [systemDark, setSystemDark] = useState<boolean>(() =>
    initial ? initial.resolved === 'dark' : environment.systemDark(),
  )
  const [notice, setNotice] = useState('')

  useEffect(() => {
    let active = true
    void Promise.resolve(environment.storage.load())
      .then((raw) => {
        if (!active) return
        const normalized = normalizeTheme(raw)
        if (raw !== undefined && raw !== normalized) {
          setNotice(INVALID_NOTICE)
        }
        setModeState(normalized)
        setSystemDark(environment.systemDark())
      })
      .catch(() => {
        if (active) setNotice('主题设置读取失败，已回退到跟随系统')
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
        setNotice('主题设置保存失败，本次选择仍然生效')
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
