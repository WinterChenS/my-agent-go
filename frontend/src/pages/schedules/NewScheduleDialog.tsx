import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import {
  localTimeZone,
  saveSchedule,
  utcOffsetMinutes,
  type ScheduleRecord,
} from './client'

const CRON_PATTERN = /^\s*\S+(\s+\S+){4}\s*$/

const PERMISSIONS = ['medium', 'high', 'full', 'readonly'] as const
const PERMISSION_KEYS: Record<(typeof PERMISSIONS)[number], MessageKey> = {
  medium: 'schedules.form.permission.medium',
  high: 'schedules.form.permission.high',
  full: 'schedules.form.permission.full',
  readonly: 'schedules.form.permission.readonly',
}
const OUTPUTS = ['library', 'inline', 'mail'] as const
const OUTPUT_KEYS: Record<(typeof OUTPUTS)[number], MessageKey> = {
  library: 'schedules.form.output.library',
  inline: 'schedules.form.output.inline',
  mail: 'schedules.form.output.mail',
}

export interface ScheduleAgentChoice {
  id: string
  name: string
  model: string
}

export default function NewScheduleDialog({
  open,
  sample = false,
  agents = [],
  initial = null,
  onClose,
  onNotice,
  onSaved,
}: {
  open: boolean
  /** Sample mode keeps the prototype's notices and never touches the host. */
  sample?: boolean
  agents?: ScheduleAgentChoice[]
  /** A row being edited; `null` creates a new schedule. */
  initial?: ScheduleRecord | null
  onClose: () => void
  onNotice: (message: string) => void
  onSaved?: () => Promise<void> | void
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [name, setName] = useState('')
  const [agentId, setAgentId] = useState('')
  const [cron, setCron] = useState('0 9 * * 1-5')
  const [timezone, setTimezone] = useState('')
  const [prompt, setPrompt] = useState('')
  const [permission, setPermission] = useState<(typeof PERMISSIONS)[number]>('medium')
  const [output, setOutput] = useState<(typeof OUTPUTS)[number]>('library')
  const [notify, setNotify] = useState(true)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    setName(initial?.name ?? '')
    setAgentId(initial?.agentId ?? agents[0]?.id ?? '')
    setCron(initial?.cron ?? '0 9 * * 1-5')
    setTimezone(initial?.timeZone ?? localTimeZone())
    setPrompt(initial?.prompt ?? '')
    setPermission(
      (PERMISSIONS as readonly string[]).includes(initial?.permission ?? '')
        ? (initial?.permission as (typeof PERMISSIONS)[number])
        : 'medium',
    )
    setOutput(
      (OUTPUTS as readonly string[]).includes(initial?.outputTarget ?? '')
        ? (initial?.outputTarget as (typeof OUTPUTS)[number])
        : 'library',
    )
    setNotify(true)
    setError('')
    setSaving(false)
  }, [agents, initial, open])

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

  const submit = async () => {
    if (!name.trim()) {
      setError(t('schedules.validation.nameRequired'))
      return
    }
    if (!CRON_PATTERN.test(cron)) {
      setError(t('schedules.validation.cronInvalid'))
      return
    }
    if (!sample && !agentId) {
      setError(t('schedules.validation.agentRequired'))
      return
    }
    if (sample) {
      setError('')
      onClose()
      onNotice(t('schedules.notice.create'))
      return
    }
    setSaving(true)
    try {
      const saved = await saveSchedule({
        id: initial?.id,
        name: name.trim(),
        cron: cron.trim(),
        timeZone: timezone,
        utcOffsetMinutes: utcOffsetMinutes(),
        agentId,
        prompt: prompt.trim() || name.trim(),
        permission,
        outputTarget: output,
        enabled: initial ? initial.enabled : true,
      })
      setError('')
      onClose()
      await onSaved?.()
      onNotice(
        initial
          ? t('schedules.notice.saved', { name: saved.name })
          : t('schedules.notice.created', { name: saved.name }),
      )
    } catch (cause) {
      setSaving(false)
      setError(cause instanceof Error ? cause.message : String(cause))
    }
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
            <h2 id="sc-dialog-title">{initial ? t('schedules.dialog.editTitle') : t('schedules.dialog.title')}</h2>
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
              <select value={agentId} onChange={(event) => { setAgentId(event.target.value); setError('') }}>
                {agents.length === 0 ? <option value="">{t('schedules.form.noAgent')}</option> : null}
                {agents.map((choice) => (
                  <option key={choice.id} value={choice.id}>{choice.name}</option>
                ))}
              </select>
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.cron')}</span>
              <input className="mono" value={cron} onChange={(event) => { setCron(event.target.value); setError('') }} />
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.timezone')}</span>
              <input className="mono" value={timezone} onChange={(event) => setTimezone(event.target.value)} />
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
              <select value={permission} onChange={(event) => setPermission(event.target.value as (typeof PERMISSIONS)[number])}>
                {PERMISSIONS.map((key) => <option key={key} value={key}>{t(PERMISSION_KEYS[key])}</option>)}
              </select>
            </label>
            <label className="sc-field">
              <span>{t('schedules.form.output')}</span>
              <select value={output} onChange={(event) => setOutput(event.target.value as (typeof OUTPUTS)[number])}>
                {OUTPUTS.map((key) => <option key={key} value={key}>{t(OUTPUT_KEYS[key])}</option>)}
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
          <button type="button" className="sc-btn sc-btn-primary" disabled={saving} onClick={() => void submit()}>
            {initial ? t('schedules.form.save') : t('schedules.form.create')}
          </button>
        </div>
      </div>
    </div>
  )
}
