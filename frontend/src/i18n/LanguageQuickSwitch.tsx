import { useI18n } from './LanguageProvider'

// Reusable quick switch for R0-05's chat header. R0-03 does not mount it in the
// chat page body; the component exposes the same persisted language selection.
export default function LanguageQuickSwitch() {
  const { language, setLanguage, t } = useI18n()
  const next = language === 'zh-CN' ? 'en-US' : 'zh-CN'
  return (
    <button
      type="button"
      className="iconbtn"
      aria-label={t('language.quickSwitch')}
      title={t('language.quickSwitch')}
      data-testid="language-quick-switch"
      onClick={() => setLanguage(next)}
    >
      <span aria-hidden="true">{language === 'zh-CN' ? 'EN' : '中文'}</span>
    </button>
  )
}
