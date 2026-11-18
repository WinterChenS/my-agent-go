import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import {
  hostLanguageEnvironment,
  normalizeLanguage,
  resolveStoredLanguage,
  type LanguageEnvironment,
} from './language'
import { DEFAULT_LANGUAGE, translate, type Language, type MessageKey } from './catalogue'

export type LanguageNotice = '' | 'invalid' | 'read-failed' | 'save-failed'

export interface I18nContextValue {
  language: Language
  notice: LanguageNotice
  setLanguage(language: Language): void
  t(key: MessageKey, params?: Record<string, string | number>): string
}

const I18nContext = createContext<I18nContextValue | null>(null)

export function LanguageProvider({
  children,
  environment = hostLanguageEnvironment,
}: {
  children: ReactNode
  environment?: LanguageEnvironment
}) {
  // The first frame follows the system locale unless a language was chosen
  // before; the stored preference (read below) only wins when it is a real
  // choice, not the default value the host writes for every profile.
  const [language, setLanguageState] = useState<Language>(() => environment.initialLanguage())
  const [notice, setNotice] = useState<LanguageNotice>('')

  useEffect(() => {
    let active = true
    void Promise.resolve(environment.storage.load())
      .then((raw) => {
        if (!active) return
        if (typeof raw === 'string' && raw !== normalizeLanguage(raw)) setNotice('invalid')
        const stored = resolveStoredLanguage(raw)
        // `undefined` means nothing was ever picked: keep following the system.
        if (stored) setLanguageState(stored)
      })
      .catch(() => {
        if (active) setNotice('read-failed')
      })
    return () => {
      active = false
    }
  }, [environment])

  useEffect(() => {
    environment.apply(language)
  }, [environment, language])

  const setLanguage = useCallback(
    (next: Language) => {
      const normalized = normalizeLanguage(next)
      setLanguageState(normalized)
      setNotice('')
      void Promise.resolve(environment.storage.save(normalized)).catch(() => setNotice('save-failed'))
    },
    [environment],
  )

  const t = useCallback(
    (key: MessageKey, params?: Record<string, string | number>) => translate(language, key, params),
    [language],
  )

  return <I18nContext.Provider value={{ language, notice, setLanguage, t }}>{children}</I18nContext.Provider>
}

export function useI18n(): I18nContextValue {
  const value = useContext(I18nContext)
  if (!value) throw new Error('useI18n must be used inside LanguageProvider')
  return value
}

export { DEFAULT_LANGUAGE }
export type { Language, MessageKey }
