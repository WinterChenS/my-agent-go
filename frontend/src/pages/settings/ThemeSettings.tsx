import { useRef, type KeyboardEvent } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import { useTheme } from '../../preferences/ThemeProvider'
import type { ThemeMode } from '../../preferences/theme'
import './settings.css'

const OPTIONS: Array<{
  value: ThemeMode
  labelKey: 'settings.theme.system' | 'settings.theme.light' | 'settings.theme.dark'
  hintKey: 'settings.theme.system.hint' | 'settings.theme.light.hint' | 'settings.theme.dark.hint'
}> = [
  { value: 'system', labelKey: 'settings.theme.system', hintKey: 'settings.theme.system.hint' },
  { value: 'light', labelKey: 'settings.theme.light', hintKey: 'settings.theme.light.hint' },
  { value: 'dark', labelKey: 'settings.theme.dark', hintKey: 'settings.theme.dark.hint' },
]

export default function ThemeSettings() {
  const { t } = useI18n()
  const { mode, resolved, notice, setMode } = useTheme()
  const buttons = useRef<Array<HTMLInputElement | null>>([])

  const selectAt = (index: number) => {
    const option = OPTIONS[(index + OPTIONS.length) % OPTIONS.length]
    setMode(option.value)
    buttons.current[OPTIONS.indexOf(option)]?.focus()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const index = OPTIONS.findIndex((option) => option.value === mode)
    switch (event.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        event.preventDefault()
        selectAt(index + 1)
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        event.preventDefault()
        selectAt(index - 1)
        break
      case 'Home':
        event.preventDefault()
        selectAt(0)
        break
      case 'End':
        event.preventDefault()
        selectAt(OPTIONS.length - 1)
        break
      default:
        break
    }
  }

  const noticeKey =
    notice === 'invalid'
      ? 'settings.theme.notice.invalid'
      : notice === 'read-failed'
        ? 'settings.theme.notice.readFailed'
        : notice === 'save-failed'
          ? 'settings.theme.notice.saveFailed'
          : null
  const modeLabel = t(mode === 'system' ? 'settings.theme.system' : mode === 'dark' ? 'settings.theme.dark' : 'settings.theme.light')
  const resolvedLabel = t(resolved === 'dark' ? 'settings.theme.resolved.dark' : 'settings.theme.resolved.light')

  return (
    <section className="theme-settings" aria-labelledby="theme-settings-title">
      <div className="card">
        <div className="card-head">
          <h3 id="theme-settings-title">{t('settings.theme.title')}</h3>
          <span className="card-head-sub">{t('settings.theme.subtitle')}</span>
        </div>
        <div className="card-pad">
          <div className="theme-options" role="radiogroup" aria-label={t('settings.theme.title')} onKeyDown={onKeyDown}>
            {OPTIONS.map((option, index) => (
              <label key={option.value} className={`theme-option${mode === option.value ? ' is-active' : ''}`}>
                <input
                  ref={(element) => {
                    buttons.current[index] = element
                  }}
                  type="radio"
                  name="anvil-theme"
                  value={option.value}
                  checked={mode === option.value}
                  aria-checked={mode === option.value}
                  aria-label={t(option.labelKey)}
                  tabIndex={mode === option.value ? 0 : -1}
                  onChange={() => setMode(option.value)}
                />
                <span>
                  <b>{t(option.labelKey)}</b>
                  <small>{t(option.hintKey)}</small>
                </span>
              </label>
            ))}
          </div>
          <p className="theme-status" data-testid="theme-status" role="status">
            {t('settings.theme.status', { resolved: resolvedLabel, mode: modeLabel })}
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
