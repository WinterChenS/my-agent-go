import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import {
  browserLanguageEnvironment,
  initialLanguageFromWindow,
  normalizeLanguage,
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
  environment = browserLanguageEnvironment,
}: {
  children: ReactNode
  environment?: LanguageEnvironment
}) {
  const [language, setLanguageState] = useState<Language>(() => initialLanguageFromWindow())
  const [notice, setNotice] = useState<LanguageNotice>('')

  useEffect(() => {
    let active = true
    void Promise.resolve(environment.storage.load())
      .then((raw) => {
        if (!active) return
        const normalized = normalizeLanguage(raw)
        if (raw !== undefined && raw !== normalized) setNotice('invalid')
        setLanguageState(normalized)
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
