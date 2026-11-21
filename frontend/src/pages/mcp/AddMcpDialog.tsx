import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { addCatalogMcp, testCatalogMcp } from '../../catalog/client'
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

export interface McpRuntimeChoice {
  id: string
  label: string
}

export default function AddMcpDialog({
  open,
  sample = false,
  runtimes = [],
  onClose,
  onNotice,
  onSaved,
}: {
  open: boolean
  /** Sample mode keeps the prototype notices and never touches the host. */
  sample?: boolean
  runtimes?: McpRuntimeChoice[]
  onClose: () => void
  onNotice: (message: string) => void
  onSaved?: (runtimeId: string) => void
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [runtimeId, setRuntimeId] = useState('')
  const [name, setName] = useState('')
  const [transport, setTransport] = useState<McpTransport>('stdio')
  const [address, setAddress] = useState('')
  const [env, setEnv] = useState('')
  const [permission, setPermission] = useState<Permission>('readonly')
  const [agents, setAgents] = useState<string[]>([])
  const [error, setError] = useState('')
  const [result, setResult] = useState('')
  const [busy, setBusy] = useState<'add' | 'test' | null>(null)

  useEffect(() => {
    if (!open) return
    setRuntimeId(runtimes[0]?.id ?? '')
    setName('')
    setTransport('stdio')
    setAddress('')
    setEnv('')
    setPermission('readonly')
    setAgents([])
    setError('')
    setResult('')
    setBusy(null)
  }, [open, runtimes])

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
  }, [open, onClose, busy])

  if (!open) return null

  const envLines = env.split('\n').map((line) => line.trim()).filter(Boolean)
  const envInvalid = envLines.some((line) => !/^[A-Za-z_][A-Za-z0-9_]*=.*$/.test(line))
  const envPairs: Array<[string, string]> = envLines.map((line) => {
    const at = line.indexOf('=')
    return [line.slice(0, at), line.slice(at + 1)]
  })
  const envKeys = envPairs.map(([key]) => key).filter((key) => key && /^[A-Za-z_][A-Za-z0-9_]*$/.test(key))
  const remote = transport !== 'stdio'

  const validate = () => {
    if (!sample && !runtimes.length) return t('mcp.action.unavailable')
    if (!name.trim()) return t('mcp.validation.nameRequired')
    if (!address.trim()) return t('mcp.validation.addressRequired')
    if (remote && !/^https?:\/\/\S+$/i.test(address.trim())) return t('mcp.validation.urlInvalid')
    if (envInvalid) return t('mcp.validation.envInvalid')
    return ''
  }

  const draft = () => {
    const words = address.trim().split(/\s+/)
    return {
      runtimeId,
      name: name.trim(),
      transport,
      command: remote ? address.trim() : words[0],
      args: remote ? [] : words.slice(1),
      url: remote ? address.trim() : null,
      env: envPairs,
    }
  }

  const submit = async (kind: 'add' | 'test') => {
    const next = validate()
    setError(next)
    setResult('')
    if (next) return
    if (sample) {
      onNotice(kind === 'add' ? t('mcp.notice.add') : t('mcp.notice.test'))
      return
    }
    setBusy(kind)
    try {
      if (kind === 'test') {
        const report = await testCatalogMcp(draft())
        setResult(
          report.ok
            ? t('mcp.form.testOk', { count: report.tools.length })
            : t('mcp.form.testFailed', { message: report.detail || 'unknown error' }),
        )
      } else {
        await addCatalogMcp(draft())
        onNotice(t('mcp.notice.added'))
        onSaved?.(runtimeId)
        onClose()
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(null)
    }
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
          {!sample ? (
            <label className="mcp-field"><span>{t('mcp.form.runtime')}</span>
              <select value={runtimeId} onChange={(event) => { setRuntimeId(event.target.value); setError('') }}>
                {runtimes.map((runtime) => <option key={runtime.id} value={runtime.id}>{runtime.label}</option>)}
              </select>
            </label>
          ) : null}
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
                {(sample ? sampleAgents : runtimes.map((runtime) => runtime.label)).map((agent) => (
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
          {result ? <p className="mcp-test-result" data-testid="mcp-test-result" role="status">{result}</p> : null}
          {error ? <p className="mcp-error" role="alert">{error}</p> : null}
        </div>
        <div className="mcp-dialog-foot">
          <button type="button" className="mcp-btn" disabled={busy !== null} onClick={() => void submit('test')}>
            {busy === 'test' ? t('mcp.form.testing') : t('mcp.form.test')}
          </button>
          <span className="mcp-spacer" />
          <button type="button" className="mcp-btn" disabled={busy !== null} onClick={onClose}>{t('mcp.form.cancel')}</button>
          <button type="button" className="mcp-btn mcp-btn-primary" disabled={busy !== null} onClick={() => void submit('add')}>
            {busy === 'add' ? t('mcp.form.saving') : t('mcp.form.add')}
          </button>
        </div>
      </div>
    </div>
  )
}
