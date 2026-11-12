import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { formAgents, scheduleOutputs, schedulePermissions, scheduleTimezones } from './sampleData'

const CRON_PATTERN = /^\s*\S+(\s+\S+){4}\s*$/

export default function NewScheduleDialog({
  open,
  onClose,
  onNotice,
}: {
  open: boolean
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [name, setName] = useState('')
  const [agent, setAgent] = useState(formAgents[0])
  const [cron, setCron] = useState('0 9 * * 1-5')
  const [timezone, setTimezone] = useState(scheduleTimezones[0])
  const [prompt, setPrompt] = useState('')
  const [permission, setPermission] = useState(schedulePermissions[0])
  const [output, setOutput] = useState(scheduleOutputs[0])
  const [notify, setNotify] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setName('')
    setAgent(formAgents[0])
    setCron('0 9 * * 1-5')
    setTimezone(scheduleTimezones[0])
    setPrompt('')
    setPermission(schedulePermissions[0])
    setOutput(scheduleOutputs[0])
    setNotify(true)
    setError('')
  }, [open])

  useEffect(() => {
    if (!open) return
    const root = ref.current
    if (!root) return
    const focusables = () =>
      Array.from(
        root.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      )
    window.setTimeout(() => focusables()[0]?.focus(), 0)
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab') return
      const items = focusables()
      if (items.length === 0) {
        event.preventDefault()
        return
      }
      const first = items[0]
      const last = items[items.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onClose])

  if (!open) return null

  const submit = () => {
    if (!name.trim()) {
      setError(t('schedules.validation.nameRequired'))
      return
    }
    if (!CRON_PATTERN.test(cron)) {
      setError(t('schedules.validation.cronInvalid'))
      return
    }
    setError('')
    onClose()
    onNotice(t('schedules.notice.create'))
  }

  return (
    <div
      className="sc-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div ref={ref} className="sc-dialog" role="dialog" aria-modal="true" aria-labelledby="sc-dialog-title">
        <div className="sc-dialog-head">
          <div>
            <h2 id="sc-dialog-title">{t('schedules.dialog.title')}</h2>
            <p>{t('schedules.dialog.description')}</p>
          </div>
          <button type="button" className="sc-iconbtn" aria-label={t('schedules.form.close')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="sc-dialog-body">
          <div className="sc-form-grid">
            <label className="sc-field">
              <span>{t('schedules.form.name')}</span>
              <input value={name} placeholder={t('schedules.form.namePlaceholder')} onChange={(event) => { setName(event.target.value); setError('') }} />
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.agent')}</span>
              <select value={agent} onChange={(event) => setAgent(event.target.value)}>
                {formAgents.map((value) => <option key={value}>{value}</option>)}
              </select>
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.cron')}</span>
              <input className="mono" value={cron} onChange={(event) => { setCron(event.target.value); setError('') }} />
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.timezone')}</span>
              <select value={timezone} onChange={(event) => setTimezone(event.target.value)}>
                {scheduleTimezones.map((value) => <option key={value}>{value}</option>)}
              </select>
            </label>
          </div>
          <small className="sc-form-hint">{t('schedules.form.cronHint')}</small>
          <label className="sc-field">
            <span>{t('schedules.form.prompt')}</span>
            <textarea rows={3} value={prompt} placeholder={t('schedules.form.promptPlaceholder')} onChange={(event) => setPrompt(event.target.value)} />
          </label>
          <small className="sc-form-hint">{t('schedules.form.promptHint')}</small>
          <div className="sc-form-grid">
            <label className="sc-field">
              <span>{t('schedules.form.permission')}</span>
              <select value={permission} onChange={(event) => setPermission(event.target.value)}>
                {schedulePermissions.map((key) => <option key={key} value={key}>{t(key as MessageKey)}</option>)}
              </select>
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.output')}</span>
              <select value={output} onChange={(event) => setOutput(event.target.value)}>
                {scheduleOutputs.map((key) => <option key={key} value={key}>{t(key as MessageKey)}</option>)}
              </select>
            </label>
          </div>
          <label className="sc-check">
            <input type="checkbox" checked={notify} onChange={(event) => setNotify(event.target.checked)} />
            <span>
              <span className="sc-check-title">{t('schedules.form.notify')}</span>
              <span className="sc-check-sub">{t('schedules.form.notifySub')}</span>
            </span>
          </label>
          {error ? <p className="sc-error" role="alert">{error}</p> : null}
        </div>
        <div className="sc-dialog-foot">
          <span className="sc-muted sc-spread">{t('schedules.form.footnote')}</span>
          <button type="button" className="sc-btn" onClick={onClose}>{t('schedules.form.cancel')}</button>
          <button type="button" className="sc-btn sc-btn-primary" onClick={submit}>{t('schedules.form.create')}</button>
        </div>
      </div>
    </div>
  )
}
