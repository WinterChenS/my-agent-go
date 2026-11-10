import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { sampleAgents, type McpTransport } from './sampleData'

type Permission = 'readonly' | 'medium' | 'full'

const TRANSPORT_KEYS: Record<McpTransport, MessageKey> = {
  stdio: 'mcp.transport.stdio',
  http: 'mcp.transport.http',
  websocket: 'mcp.transport.websocket',
}

const PERMISSION_KEYS: Record<Permission, MessageKey> = {
  readonly: 'mcp.form.permission.readonly',
  medium: 'mcp.form.permission.medium',
  full: 'mcp.form.permission.full',
}

export default function AddMcpDialog({
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
  const [transport, setTransport] = useState<McpTransport>('stdio')
  const [address, setAddress] = useState('')
  const [env, setEnv] = useState('')
  const [permission, setPermission] = useState<Permission>('readonly')
  const [agents, setAgents] = useState<string[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setName('')
    setTransport('stdio')
    setAddress('')
    setEnv('')
    setPermission('readonly')
    setAgents([])
    setError('')
  }, [open])

  useEffect(() => {
    if (!open) return
    const root = ref.current
    if (!root) return
    const focusables = () =>
      Array.from(root.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'))
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

  const envLines = env.split('\n').map((line) => line.trim()).filter(Boolean)
  const envInvalid = envLines.some((line) => !/^[A-Za-z_][A-Za-z0-9_]*=.*$/.test(line))
  const envKeys = envLines.map((line) => line.slice(0, line.indexOf('='))).filter((key) => key && /^[A-Za-z_][A-Za-z0-9_]*$/.test(key))
  const remote = transport !== 'stdio'

  const validate = () => {
    if (!name.trim()) return t('mcp.validation.nameRequired')
    if (!address.trim()) return t('mcp.validation.addressRequired')
    if (remote && !/^https?:\/\/\S+$/i.test(address.trim())) return t('mcp.validation.urlInvalid')
    if (envInvalid) return t('mcp.validation.envInvalid')
    return ''
  }
  const submit = (kind: 'add' | 'test') => {
    const next = validate()
    setError(next)
    if (next) return
    onNotice(kind === 'add' ? t('mcp.notice.add') : t('mcp.notice.test'))
  }

  return (
    <div
      className="mcp-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.stopPropagation()
          onClose()
        }
      }}
    >
      <div ref={ref} className="mcp-dialog" role="dialog" aria-modal="true" aria-labelledby="mcp-dialog-title">
        <div className="mcp-dialog-head">
          <div>
            <h2 id="mcp-dialog-title">{t('mcp.dialog.title')}</h2>
            <p>{t('mcp.dialog.description')}</p>
          </div>
          <button type="button" className="mcp-iconbtn" aria-label={t('mcp.form.close')} onClick={onClose}><Icon name="x" size={15} /></button>
        </div>
        <div className="mcp-dialog-body">
          <div className="mcp-form-grid">
            <label className="mcp-field"><span>{t('mcp.form.name')}</span><input className="mono" value={name} placeholder={t('mcp.form.namePlaceholder')} onChange={(event) => { setName(event.target.value); setError('') }} /></label>
            <label className="mcp-field"><span>{t('mcp.form.transport')}</span>
              <select value={transport} onChange={(event) => { setTransport(event.target.value as McpTransport); setError('') }}>
                {Object.entries(TRANSPORT_KEYS).map(([value, key]) => <option key={value} value={value}>{t(key)}</option>)}
              </select>
            </label>
          </div>
          <label className="mcp-field"><span>{t('mcp.form.command')}</span><input className="mono" value={address} placeholder={t('mcp.form.commandPlaceholder')} onChange={(event) => { setAddress(event.target.value); setError('') }} /></label>
          <small className="mcp-hint">{remote ? t('mcp.form.commandHint.remote') : t('mcp.form.commandHint.stdio')}</small>
          <label className="mcp-field"><span>{t('mcp.form.env')}</span><textarea className="mono" rows={3} value={env} placeholder={t('mcp.form.envPlaceholder')} onChange={(event) => { setEnv(event.target.value); setError('') }} /></label>
          <small className="mcp-hint">{t('mcp.form.envHint')}</small>
          {envInvalid ? <p className="mcp-error" data-testid="mcp-env-error">{t('mcp.validation.envInvalid')}</p> : null}
          {envKeys.length > 0 ? (
            <div className="mcp-env-preview" data-testid="mcp-env-preview">
              <span>{t('mcp.form.envPreview')}</span>
              {envKeys.map((key) => <code key={key}>{t('mcp.form.envMasked', { key })}</code>)}
            </div>
          ) : null}
          <div className="mcp-form-grid">
            <div className="mcp-field">
              <span>{t('mcp.form.permission')}</span>
              <div className="mcp-permission" role="radiogroup" aria-label={t('mcp.form.permission')}>
                {(Object.keys(PERMISSION_KEYS) as Permission[]).map((value) => (
                  <button key={value} type="button" role="radio" aria-checked={permission === value} className={permission === value ? 'is-selected' : ''} onClick={() => setPermission(value)}>
                    <span className="mcp-radio" aria-hidden="true" />{t(PERMISSION_KEYS[value])}
                  </button>
                ))}
              </div>
            </div>
            <div className="mcp-field">
              <span>{t('mcp.form.agents')}</span>
              <div className="mcp-agents">
                {sampleAgents.map((agent) => (
                  <label key={agent} className="mcp-agent">
                    <input
                      type="checkbox"
                      checked={agents.includes(agent)}
                      onChange={(event) => setAgents((current) => event.target.checked ? [...current, agent] : current.filter((item) => item !== agent))}
                    />
                    <span>{agent}</span>
                  </label>
                ))}
              </div>
            </div>
          </div>
          <div className="mcp-notice"><Icon name="alert" size={14} /><span>{t('mcp.form.readonly24')}</span></div>
          {error ? <p className="mcp-error" role="alert">{error}</p> : null}
        </div>
        <div className="mcp-dialog-foot">
          <button type="button" className="mcp-btn" onClick={() => submit('test')}>{t('mcp.form.test')}</button>
          <span className="mcp-spacer" />
          <button type="button" className="mcp-btn" onClick={onClose}>{t('mcp.form.cancel')}</button>
          <button type="button" className="mcp-btn mcp-btn-primary" onClick={() => submit('add')}>{t('mcp.form.add')}</button>
        </div>
      </div>
    </div>
  )
}
