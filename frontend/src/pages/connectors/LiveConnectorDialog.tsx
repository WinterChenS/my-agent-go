import { useEffect, useMemo, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import type {
  ConnectorDraft,
  ConnectorField,
  ConnectorProbeOutcome,
  ConnectorRecord,
  ConnectorType,
} from './client'

type Props = {
  mode: 'form' | 'detail'
  types: ConnectorType[]
  kind: string
  connector: ConnectorRecord | null
  probe?: ConnectorProbeOutcome | null
  busy?: boolean
  onClose: () => void
  onSave?: (draft: ConnectorDraft) => Promise<void>
  onProbe?: () => void
  onEdit?: (element: HTMLElement) => void
  onToggle?: () => void
  onDelete?: () => void
}

const STATUS_KEYS: Record<string, MessageKey> = {
  connected: 'connectors.status.connected',
  declared: 'connectors.status.unverified',
  failed: 'connectors.status.probeFailed',
  disabled: 'connectors.status.disabled',
}

/** The add / edit form and the detail panel, driven by the type's fields. */
export function LiveConnectorDialog({
  mode,
  types,
  kind,
  connector,
  probe,
  busy,
  onClose,
  onSave,
  onProbe,
  onEdit,
  onToggle,
  onDelete,
}: Props) {
  const { t } = useI18n()
  const [selected, setSelected] = useState(kind)
  const type = useMemo(
    () => types.find((entry) => entry.id === selected) ?? types[0] ?? null,
    [selected, types],
  )
  const [name, setName] = useState(connector?.name ?? '')
  const [values, setValues] = useState<Record<string, string>>(() => ({ ...(connector?.config ?? {}) }))
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  const title =
    mode === 'detail'
      ? connector?.name ?? t('connectors.title')
      : connector
        ? t('connectors.form.editTitle')
        : t('connectors.form.addTitle')

  const submit = async () => {
    if (!type || !onSave) return
    if (!name.trim()) {
      setError(t('connectors.validation.nameRequired'))
      return
    }
    const config: Record<string, string> = {}
    for (const field of type.fields) {
      const value = (values[field.key] ?? '').trim()
      const stored = connector?.secretsSet.includes(field.key) && field.secret
      if (field.required && !value && !stored) {
        setError(t('connectors.validation.required', { field: field.label }))
        return
      }
      // A blank secret on an edit means "leave what is stored"; the host keeps
      // it when the field is empty, so sending "" is correct here.
      config[field.key] = value
    }
    setSaving(true)
    setError('')
    try {
      await onSave({ id: connector?.id, kind: type.id, name: name.trim(), config })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div
      className="cx-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div
        className="cx-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        data-testid="connector-dialog"
      >
        <div className="cx-dialog-head">
          <div>
            <h2>{title}</h2>
            <p className="mono">{mode === 'detail' ? connector?.kindName : type?.name ?? ''}</p>
          </div>
          <button type="button" className="cx-iconbtn" aria-label={t('connectors.dialog.close')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>

        {mode === 'form' ? (
          <>
            <div className="cx-dialog-body cx-form">
              {!connector ? (
                <label className="cx-field">
                  <span>{t('connectors.form.type')}</span>
                  <select
                    value={selected}
                    onChange={(event) => {
                      setSelected(event.target.value)
                      setValues({})
                      setError('')
                    }}
                  >
                    {types.map((entry) => (
                      <option key={entry.id} value={entry.id}>{entry.name}</option>
                    ))}
                  </select>
                </label>
              ) : null}
              <label className="cx-field">
                <span>{t('connectors.form.name')}</span>
                <input
                  value={name}
                  data-testid="connector-form-name"
                  placeholder={t('connectors.form.namePlaceholder')}
                  onChange={(event) => { setName(event.target.value); setError('') }}
                />
              </label>
              {type?.fields.map((field) => (
                <FieldInput
                  key={`${type.id}-${field.key}`}
                  field={field}
                  value={values[field.key] ?? ''}
                  stored={Boolean(connector?.secretsSet.includes(field.key))}
                  onChange={(value) => setValues((current) => ({ ...current, [field.key]: value }))}
                />
              ))}
              {error ? <p className="cx-form-error" role="alert">{error}</p> : null}
            </div>
            <div className="cx-dialog-foot">
              <button type="button" className="cx-btn cx-btn-ghost" onClick={onClose}>
                {t('connectors.form.cancel')}
              </button>
              <button
                type="button"
                className="cx-btn cx-btn-primary"
                data-testid="connector-form-submit"
                disabled={saving}
                onClick={() => void submit()}
              >
                {saving ? t('connectors.form.saving') : t('connectors.form.save')}
              </button>
            </div>
          </>
        ) : (
          <>
            <div className="cx-dialog-body">
              <dl className="cx-dl">
                <dt>{t('connectors.dialog.type')}</dt>
                <dd>{connector?.kindName ?? '—'}</dd>
                <dt>{t('connectors.dialog.state')}</dt>
                <dd>
                  <span className={`cx-status is-${connector && !connector.enabled ? 'disabled' : connector?.state ?? 'declared'}`}>
                    <i aria-hidden="true" />
                    {t(STATUS_KEYS[connector && !connector.enabled ? 'disabled' : connector?.state ?? 'declared'])}
                  </span>
                </dd>
                <dt>{t('connectors.dialog.probed')}</dt>
                <dd className="mono">{connector?.probedAt ?? '—'}</dd>
                <dt>{t('connectors.dialog.calls')}</dt>
                <dd>{connector ? `${connector.calls} · ${t('connectors.dialog.failures')} ${connector.failures}` : '—'}</dd>
                {connector?.lastError ? (
                  <>
                    <dt>{t('connectors.dialog.lastError')}</dt>
                    <dd className="cx-warn-text">{connector.lastError}</dd>
                  </>
                ) : null}
              </dl>

              <div className="cx-label">{t('connectors.dialog.tools')}</div>
              {connector && connector.tools.length > 0 ? (
                <div className="cx-tool-list">
                  {connector.tools.map((tool) => (
                    <div key={tool.name} className="cx-tool-row">
                      <div>
                        <div className="cx-row-title mono">{tool.name}</div>
                        <div className="cx-row-sub">{tool.description}</div>
                      </div>
                      <span className={`cx-badge${tool.write ? ' is-warn' : ''}`}>
                        {tool.write ? t('connectors.policy.write') : t('connectors.policy.read')}
                      </span>
                      <span className="cx-num mono">{tool.calls}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="cx-muted">{t('connectors.dialog.noTools')}</p>
              )}

              {probe ? (
                <div className="cx-probe" data-testid="connector-probe-result">
                  <div className={`cx-probe-head${probe.ok ? ' is-ok' : ' is-bad'}`}>
                    <Icon name={probe.ok ? 'check' : 'alert'} size={13} />
                    <span>{probe.message}</span>
                  </div>
                  {probe.steps.length > 0 ? (
                    <ol className="cx-probe-steps">
                      {probe.steps.map((step) => <li key={step}>{step}</li>)}
                    </ol>
                  ) : null}
                </div>
              ) : null}
            </div>
            <div className="cx-dialog-foot cx-dialog-foot-split">
              <div className="cx-dialog-foot-left">
                <button
                  type="button"
                  className="cx-btn cx-btn-ghost"
                  data-testid="connector-toggle"
                  onClick={onToggle}
                >
                  {connector?.enabled ? t('connectors.action.disable') : t('connectors.action.enable')}
                </button>
                <button
                  type="button"
                  className="cx-btn cx-btn-danger"
                  data-testid="connector-delete"
                  onClick={() => (confirmDelete ? onDelete?.() : setConfirmDelete(true))}
                >
                  {confirmDelete ? t('connectors.action.deleteConfirm') : t('connectors.action.delete')}
                </button>
              </div>
              <div className="cx-dialog-foot-left">
                <button type="button" className="cx-btn cx-btn-ghost" onClick={onClose}>
                  {t('connectors.dialog.close')}
                </button>
                <button
                  type="button"
                  className="cx-btn"
                  data-testid="connector-edit"
                  onClick={(event) => onEdit?.(event.currentTarget)}
                >
                  {t('connectors.action.edit')}
                </button>
                <button
                  type="button"
                  className="cx-btn cx-btn-primary"
                  data-testid="connector-probe"
                  disabled={busy}
                  onClick={onProbe}
                >
                  <Icon name="shield" size={13} />
                  {busy ? t('connectors.dialog.probing') : t('connectors.dialog.verify')}
                </button>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function FieldInput({
  field,
  value,
  stored,
  onChange,
}: {
  field: ConnectorField
  value: string
  stored: boolean
  onChange: (value: string) => void
}) {
  const { t } = useI18n()
  const hint = field.hint || undefined
  const common = {
    value,
    'data-testid': `connector-field-${field.key}`,
    placeholder: field.secret && stored
      ? t('connectors.form.secretStored')
      : field.placeholder,
    onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => onChange(event.target.value),
  }
  return (
    <label className="cx-field">
      <span>
        {field.label}
        {field.required ? <i className="cx-required" aria-hidden="true">*</i> : null}
        {field.secret && stored ? <span className="cx-chip-stored">{t('connectors.form.stored')}</span> : null}
      </span>
      {field.kind === 'select' ? (
        <select {...common}>
          {field.options.map((option) => (
            <option key={option.value} value={option.value}>{option.label}</option>
          ))}
        </select>
      ) : field.kind === 'password' ? (
        <input type="password" autoComplete="new-password" {...common} />
      ) : field.kind === 'number' ? (
        <input type="number" inputMode="numeric" {...common} />
      ) : (
        <input type="text" {...common} />
      )}
      {hint ? <small className="cx-field-hint">{hint}</small> : null}
    </label>
  )
}
