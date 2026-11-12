import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { connectorSettings, connectorTypes, type ConnectorSettings } from './sampleData'

const TYPE_KEYS: Record<(typeof connectorTypes)[number], MessageKey> = {
  mail: 'connectors.type.mail',
  dingtalk: 'connectors.type.dingtalk',
  feishu: 'connectors.type.feishu',
  slack: 'connectors.type.slack',
  notion: 'connectors.type.notion',
  github: 'connectors.type.github',
}

type Permission = 'readonly' | 'write' | 'send'

const PERMISSION_KEYS: Record<Permission, MessageKey> = {
  readonly: 'connectors.form.readonly',
  write: 'connectors.form.write',
  send: 'connectors.form.send',
}

function useDialogKeys(open: boolean, onClose: () => void) {
  const ref = useRef<HTMLDivElement>(null)
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
  return ref
}

export function AddConnectorDialog({
  open,
  onClose,
  onNotice,
}: {
  open: boolean
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useDialogKeys(open, onClose)
  const [type, setType] = useState<(typeof connectorTypes)[number]>('mail')
  const [name, setName] = useState('')
  const [credentials, setCredentials] = useState('')
  const [permissions, setPermissions] = useState<Permission[]>(['readonly'])
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setType('mail')
    setName('')
    setCredentials('')
    setPermissions(['readonly'])
    setError('')
  }, [open])

  if (!open) return null

  const submit = () => {
    if (!name.trim()) {
      setError(t('connectors.validation.nameRequired'))
      return
    }
    if (!credentials.trim()) {
      setError(t('connectors.validation.credentialsRequired'))
      return
    }
    setError('')
    onClose()
    onNotice(t('connectors.notice.add'))
  }

  return (
    <div
      className="cx-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div ref={ref} className="cx-dialog cx-dialog-wide" role="dialog" aria-modal="true" aria-labelledby="cx-add-title">
        <div className="cx-dialog-head">
          <div>
            <h2 id="cx-add-title">{t('connectors.dialog.title')}</h2>
            <p>{t('connectors.dialog.description')}</p>
          </div>
          <button type="button" className="cx-iconbtn" aria-label={t('connectors.form.close')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="cx-dialog-body">
          <div className="cx-form-grid">
            <label className="cx-field">
              <span>{t('connectors.form.type')}</span>
              <select value={type} onChange={(event) => { setType(event.target.value as typeof type); setError('') }}>
                {connectorTypes.map((value) => (
                  <option key={value} value={value}>{t(TYPE_KEYS[value])}</option>
                ))}
              </select>
            </label>
            <label className="cx-field">
              <span>{t('connectors.form.name')}</span>
              <input value={name} placeholder={t('connectors.form.namePlaceholder')} onChange={(event) => { setName(event.target.value); setError('') }} />
            </label>
          </div>
          <label className="cx-field">
            <span>{t('connectors.form.credentials')}</span>
            <textarea className="mono" rows={3} value={credentials} placeholder={t('connectors.form.credentialsPlaceholder')} onChange={(event) => { setCredentials(event.target.value); setError('') }} />
          </label>
          <small className="cx-form-hint">{t('connectors.form.credentialsHint')}</small>
          <div className="cx-field">
            <span>{t('connectors.form.initialPermission')}</span>
            <div className="cx-checks">
              {(Object.keys(PERMISSION_KEYS) as Permission[]).map((value) => (
                <label key={value} className="cx-check">
                  <input
                    type="checkbox"
                    checked={permissions.includes(value)}
                    onChange={(event) =>
                      setPermissions((current) =>
                        event.target.checked ? [...current, value] : current.filter((item) => item !== value),
                      )
                    }
                  />
                  <span>{t(PERMISSION_KEYS[value])}</span>
                </label>
              ))}
            </div>
          </div>
          {error ? <p className="cx-error" role="alert">{error}</p> : null}
        </div>
        <div className="cx-dialog-foot">
          <button type="button" className="cx-btn" onClick={onClose}>{t('connectors.form.cancel')}</button>
          <button type="button" className="cx-btn cx-btn-primary" onClick={submit}>{t('connectors.form.add')}</button>
        </div>
      </div>
    </div>
  )
}

export function ConnectorSettingsDialog({
  settings,
  onClose,
  onNotice,
}: {
  settings: ConnectorSettings | null
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useDialogKeys(Boolean(settings), onClose)
  const [switches, setSwitches] = useState<Record<string, boolean>>({})

  useEffect(() => {
    if (!settings) return
    const next: Record<string, boolean> = {}
    for (const field of settings.fields) {
      if (field.kind === 'switch') next[field.labelKey] = field.checked
    }
    setSwitches(next)
  }, [settings])

  if (!settings) return null

  return (
    <div
      className="cx-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div ref={ref} className="cx-dialog" role="dialog" aria-modal="true" aria-labelledby="cx-settings-title">
        <div className="cx-dialog-head">
          <div>
            <h2 id="cx-settings-title">{t(settings.titleKey as MessageKey)}</h2>
            <p>{t(settings.subtitleKey as MessageKey)}</p>
          </div>
          <button type="button" className="cx-iconbtn" aria-label={t('connectors.form.close')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="cx-dialog-body cx-stack">
          {settings.fields.map((field) => {
            if (field.kind === 'switch') {
              return (
                <div key={field.labelKey} className="cx-setting-row">
                  <div className="cx-setting-main">
                    <div className="cx-setting-title">{t(field.labelKey as MessageKey)}</div>
                    <div className="cx-setting-sub">{t(field.subKey as MessageKey)}</div>
                  </div>
                  <button
                    type="button"
                    className="cx-switch"
                    role="switch"
                    aria-checked={switches[field.labelKey] ?? field.checked}
                    aria-label={t(field.labelKey as MessageKey)}
                    onClick={() => setSwitches((current) => ({ ...current, [field.labelKey]: !(current[field.labelKey] ?? field.checked) }))}
                  >
                    <span />
                  </button>
                </div>
              )
            }
            if (field.kind === 'select') {
              return (
                <label key={field.labelKey} className="cx-setting-row">
                  <span className="cx-setting-title">{t(field.labelKey as MessageKey)}</span>
                  <select aria-label={t(field.labelKey as MessageKey)}>
                    {field.optionKeys.map((key) => (
                      <option key={key}>{t(key as MessageKey)}</option>
                    ))}
                  </select>
                </label>
              )
            }
            return (
              <label key={field.labelKey} className="cx-field">
                <span>{t(field.labelKey as MessageKey)}</span>
                <input className="mono" defaultValue={field.value} readOnly={field.readOnly} aria-label={t(field.labelKey as MessageKey)} />
              </label>
            )
          })}
        </div>
        <div className="cx-dialog-foot">
          {settings.dangerKey ? (
            <button type="button" className="cx-btn cx-btn-danger" onClick={() => { onNotice(t('connectors.notice.disconnect')); onClose() }}>
              {t(settings.dangerKey as MessageKey)}
            </button>
          ) : null}
          <span className="cx-spacer" />
          <button type="button" className="cx-btn" onClick={onClose}>{t('connectors.form.cancel')}</button>
          <button
            type="button"
            className="cx-btn cx-btn-primary"
            onClick={() => { onNotice(t('connectors.notice.save')); onClose() }}
          >
            {t('connectors.form.save')}
          </button>
        </div>
      </div>
    </div>
  )
}

export function settingsFor(id: string): ConnectorSettings | null {
  return connectorSettings.find((entry) => entry.connectorId === id) ?? null
}
