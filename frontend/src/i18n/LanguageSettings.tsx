import { useI18n } from './LanguageProvider'
import type { Language } from './catalogue'

const OPTIONS: Array<{ value: Language; labelKey: 'settings.language.zh' | 'settings.language.en' }> = [
  { value: 'zh-CN', labelKey: 'settings.language.zh' },
  { value: 'en-US', labelKey: 'settings.language.en' },
]

export default function LanguageSettings() {
  const { language, notice, setLanguage, t } = useI18n()
  const noticeKey =
    notice === 'invalid'
      ? 'settings.language.notice.invalid'
      : notice === 'read-failed'
        ? 'settings.language.notice.readFailed'
        : notice === 'save-failed'
          ? 'settings.language.notice.saveFailed'
          : null

  return (
    <section className="theme-settings" aria-labelledby="language-settings-title">
      <div className="card">
        <div className="card-head">
          <h3 id="language-settings-title">{t('settings.language.title')}</h3>
          <span className="card-head-sub">{t('settings.language.subtitle')}</span>
        </div>
        <div className="card-pad">
          <div className="theme-options language-options" role="radiogroup" aria-label={t('settings.language.title')}>
            {OPTIONS.map((option) => (
              <label key={option.value} className={`theme-option${language === option.value ? ' is-active' : ''}`}>
                <input
                  type="radio"
                  name="anvil-language"
                  value={option.value}
                  checked={language === option.value}
                  aria-checked={language === option.value}
                  aria-label={t(option.labelKey)}
                  onChange={() => setLanguage(option.value)}
                />
                <span>
                  <b>{t(option.labelKey)}</b>
                </span>
              </label>
            ))}
          </div>
          <p className="theme-status" data-testid="language-status" role="status">
            {t('settings.language.status', { language: t(language === 'en-US' ? 'settings.language.en' : 'settings.language.zh') })}
          </p>
          {noticeKey ? (
            <p className="theme-notice" role="status">
              {t(noticeKey)}
            </p>
          ) : null}
        </div>
      </div>
    </section>
  )
}
