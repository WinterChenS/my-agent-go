export {
  CATALOGUES,
  DEFAULT_LANGUAGE,
  LANGUAGES,
  MESSAGE_KEYS,
  formatMessage,
  translate,
  type Language,
  type MessageKey,
} from './catalogue'
export { LanguageProvider, useI18n, type LanguageNotice } from './LanguageProvider'
export { default as LanguageSettings } from './LanguageSettings'
export { default as LanguageQuickSwitch } from './LanguageQuickSwitch'
export {
  browserLanguageEnvironment,
  initialLanguageFromWindow,
  normalizeLanguage,
  type LanguageEnvironment,
  type LanguageStorage,
} from './language'
