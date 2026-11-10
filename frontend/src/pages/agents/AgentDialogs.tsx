import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'

export type AgentDialogKind = 'new' | 'import' | null

export default function AgentDialogs({
  kind,
  onClose,
  onNotice,
}: {
  kind: AgentDialogKind
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [name, setName] = useState('')
  const [type, setType] = useState('main')
  const [runtime, setRuntime] = useState('pi-agent')
  const [model, setModel] = useState('claude-sonnet-4.5')
  const [description, setDescription] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!kind) return
    setName('')
    setType('main')
    setRuntime('pi-agent')
    setModel('claude-sonnet-4.5')
    setDescription('')
    setFile(null)
    setError('')
  }, [kind])

  useEffect(() => {
    if (!kind) return
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
  }, [kind, onClose])

  if (!kind) return null

  const title = kind === 'new' ? t('agents.dialog.newTitle') : t('agents.dialog.importTitle')
  const create = () => {
    if (kind === 'new') {
      if (!name.trim()) {
        setError(t('agents.validation.nameRequired'))
        return
      }
      setError('')
      onNotice(t('agents.notice.create'))
      return
    }
    if (!file) {
      setError(t('agents.validation.fileRequired'))
      return
    }
    setError('')
    onNotice(t('agents.notice.import'))
  }

  return (
    <div
      className="agents-backdrop"
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
      <div
        ref={ref}
        className="agents-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={`agents-dialog-${kind}-title`}
        data-testid={kind === 'new' ? 'new-agent-dialog' : 'import-agent-dialog'}
      >
        <div className="agents-dialog-head">
          <div>
            <h2 id={`agents-dialog-${kind}-title`}>{title}</h2>
            <p>
              {kind === 'new' ? t('agents.dialog.newDesc') : t('agents.dialog.importDesc')}
            </p>
          </div>
        </div>
        <div className="agents-dialog-body">
          {kind === 'new' ? (
            <div className="agents-form-stack">
              <label className="agents-field">
                <span>{t('agents.form.name')}</span>
                <input
                  value={name}
                  onChange={(event) => {
                    setName(event.target.value)
                    setError('')
                  }}
                  placeholder={t('agents.dialog.newNamePlaceholder')}
                  aria-label={t('agents.form.name')}
                />
              </label>
              <label className="agents-field">
                <span>{t('agents.dialog.newType')}</span>
                <select value={type} onChange={(event) => setType(event.target.value)} aria-label={t('agents.dialog.newType')}>
                  <option value="main">{t('agents.dialog.newTypeMain')}</option>
                  <option value="sub">{t('agents.dialog.newTypeSub')}</option>
                </select>
              </label>
              <label className="agents-field">
                <span>{t('agents.dialog.newRuntime')}</span>
                <select value={runtime} onChange={(event) => setRuntime(event.target.value)} aria-label={t('agents.dialog.newRuntime')}>
                  <option value="pi-agent">{t('agents.runtime.piOption')}</option>
                  <option value="codex-cli">{t('agents.runtime.codexOption')}</option>
                  <option value="claude-code">{t('agents.runtime.claudeOption')}</option>
                </select>
              </label>
              <label className="agents-field">
                <span>{t('agents.dialog.newModel')}</span>
                <select value={model} onChange={(event) => setModel(event.target.value)} aria-label={t('agents.dialog.newModel')}>
                  <option>claude-sonnet-4.5</option>
                  <option>gpt-5.1</option>
                  <option>gemini-3-pro</option>
                </select>
              </label>
              <label className="agents-field">
                <span>{t('agents.form.description')}</span>
                <textarea
                  rows={2}
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  placeholder={t('agents.dialog.newDescPlaceholder')}
                  aria-label={t('agents.form.description')}
                />
              </label>
              {error ? <p className="agents-error" role="alert">{error}</p> : null}
            </div>
          ) : (
            <div className="agents-form-stack">
              <label className="agents-field">
                <span>{t('agents.dialog.chooseFile')}</span>
                <input
                  type="file"
                  accept=".json,.yaml,.yml"
                  onChange={(event) => {
                    setFile(event.target.files?.[0] ?? null)
                    setError('')
                  }}
                  aria-label={t('agents.dialog.chooseFile')}
                />
              </label>
              {file ? <p className="agents-selected-file">{file.name}</p> : null}
              <div className="agents-notice">
                <Icon name="alert" size={14} />
                <span>{t('agents.dialog.importNotice')}</span>
              </div>
              {error ? <p className="agents-error" role="alert">{error}</p> : null}
            </div>
          )}
        </div>
        <div className="agents-dialog-foot">
          <button type="button" className="agents-btn" onClick={onClose}>
            {t('agents.dialog.cancel')}
          </button>
          <button type="button" className="agents-btn agents-btn-primary" onClick={create}>
            {kind === 'new' ? t('agents.dialog.create') : t('agents.dialog.importAction')}
          </button>
        </div>
      </div>
    </div>
  )
}
