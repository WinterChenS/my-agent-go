import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { sampleChat } from './sampleData'

export type ChatDialogKind = 'project' | 'export' | 'thread' | 'thil' | null

export default function ChatDialogs({
  kind,
  onClose,
  onNotice,
}: {
  kind: ChatDialogKind
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const dialogRef = useRef<HTMLDivElement>(null)
  const [project, setProject] = useState(sampleChat.projectOptions[0].name)
  const [thil, setThil] = useState<'tenant' | 'traffic' | 'environment'>('tenant')
  const [format, setFormat] = useState('html')
  const [includeThinking, setIncludeThinking] = useState(true)
  const [includeTools, setIncludeTools] = useState(false)

  useEffect(() => {
    if (!kind) return
    const root = dialogRef.current
    if (!root) return
    const focusables = () =>
      Array.from(
        root.querySelectorAll<HTMLElement>(
          'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
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

  const closeOnBackdrop = (event: React.MouseEvent<HTMLDivElement>) => {
    if (event.target === event.currentTarget) onClose()
  }

  return (
    <div className="chat-backdrop" onMouseDown={closeOnBackdrop}>
      <div
        ref={dialogRef}
        className={`chat-dialog${kind === 'thil' ? ' chat-dialog-wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`chat-dialog-${kind}-title`}
      >
        {kind === 'project' ? (
          <>
            <div className="chat-dialog-head">
              <div>
                <h2 id="chat-dialog-project-title">{t('chat.project.title')}</h2>
                <p>{t('chat.project.desc')}</p>
              </div>
              <button type="button" className="chat-iconbtn" aria-label={t('chat.dialog.close')} onClick={onClose}>
                <Icon name="x" size={14} />
              </button>
            </div>
            <div className="chat-dialog-body">
              <div className="chat-project-current">
                <span className="chat-project-logo">
                  <Icon name="folder" size={15} />
                </span>
                <div>
                  <small>{t('chat.project.current')}</small>
                  <b>{project}</b>
                  <span className="mono">
                    {sampleChat.projectOptions.find((item) => item.name === project)?.path}
                  </span>
                </div>
                <span className="chat-tone-success">{t('chat.project.opened')}</span>
              </div>
              <div className="chat-field-label">{t('chat.project.recent')}</div>
              <div className="chat-project-options" role="radiogroup" aria-label={t('chat.project.recent')}>
                {sampleChat.projectOptions.map((option) => (
                  <button
                    key={option.name}
                    type="button"
                    role="radio"
                    aria-checked={project === option.name}
                    className={`chat-project-option${project === option.name ? ' is-selected' : ''}`}
                    onClick={() => setProject(option.name)}
                  >
                    <span className="chat-project-logo">{option.name.slice(0, 1)}</span>
                    <span>
                      <b>{option.name}</b>
                      <small className="mono">
                        {option.path} · {t('chat.project.sessions', { count: option.sessions })}
                      </small>
                    </span>
                    <span className="chat-project-check" aria-hidden="true">
                      <Icon name="check" size={12} />
                    </span>
                  </button>
                ))}
              </div>
              <div className="chat-project-actions">
                <button type="button" className="chat-project-action" disabled title={t('chat.action.unavailable')}>
                  <Icon name="folder" size={15} />
                  <span>
                    <b>{t('chat.project.openLocal')}</b>
                    <small>{t('chat.project.openLocalDesc')}</small>
                  </span>
                </button>
                <button type="button" className="chat-project-action" disabled title={t('chat.action.unavailable')}>
                  <Icon name="plus" size={15} />
                  <span>
                    <b>{t('chat.project.new')}</b>
                    <small>{t('chat.project.newDesc')}</small>
                  </span>
                </button>
              </div>
              <p className="chat-project-security">
                <Icon name="shield" size={13} /> {t('chat.project.security')}
              </p>
            </div>
            <div className="chat-dialog-foot">
              <button type="button" className="chat-btn" onClick={onClose}>
                {t('chat.project.cancel')}
              </button>
              <button
                type="button"
                className="chat-btn chat-btn-primary"
                onClick={() => {
                  onNotice(t('chat.project.notice'))
                  onClose()
                }}
              >
                {t('chat.project.continue')}
              </button>
            </div>
          </>
        ) : null}

        {kind === 'export' ? (
          <>
            <div className="chat-dialog-head">
              <div>
                <h2 id="chat-dialog-export-title">{t('chat.export.title')}</h2>
                <p>{t('chat.export.desc')}</p>
              </div>
              <button type="button" className="chat-iconbtn" aria-label={t('chat.dialog.close')} onClick={onClose}>
                <Icon name="x" size={14} />
              </button>
            </div>
            <div className="chat-dialog-body chat-stack">
              <label className="chat-field">
                <span className="chat-field-label">{t('chat.export.format')}</span>
                <select value={format} onChange={(event) => setFormat(event.target.value)} aria-label={t('chat.export.format')}>
                  <option value="html">{t('chat.export.html')}</option>
                  <option value="markdown">{t('chat.export.markdown')}</option>
                  <option value="json">{t('chat.export.json')}</option>
                </select>
              </label>
              <label className="chat-check-row">
                <input type="checkbox" checked={includeThinking} onChange={(event) => setIncludeThinking(event.target.checked)} />
                <span>
                  <b>{t('chat.export.includeThinking')}</b>
                  <small>{t('chat.export.includeThinkingDesc')}</small>
                </span>
              </label>
              <label className="chat-check-row">
                <input type="checkbox" checked={includeTools} onChange={(event) => setIncludeTools(event.target.checked)} />
                <span>
                  <b>{t('chat.export.includeToolOutput')}</b>
                  <small>{t('chat.export.includeToolOutputDesc')}</small>
                </span>
              </label>
              <details className="chat-disclosure">
                <summary>
                  <Icon name="external" size={13} />
                  <span>{t('chat.export.cloudShare')}</span>
                  <span className="chat-badge">{t('chat.export.optional')}</span>
                </summary>
                <div>
                  <p>{t('chat.export.cloudShareDesc')}</p>
                  <label className="chat-check-row">
                    <input type="checkbox" />
                    <span>{t('chat.export.confirmUpload')}</span>
                  </label>
                  <button type="button" className="chat-btn chat-btn-sm" disabled title={t('chat.action.unavailable')}>
                    {t('chat.export.createLink')}
                  </button>
                </div>
              </details>
            </div>
            <div className="chat-dialog-foot">
              <button type="button" className="chat-btn" onClick={onClose}>
                {t('chat.export.cancel')}
              </button>
              <button
                type="button"
                className="chat-btn chat-btn-primary"
                onClick={() => onNotice(t('chat.export.notice'))}
              >
                <Icon name="download" size={12} />
                {t('chat.export.local')}
              </button>
            </div>
          </>
        ) : null}

        {kind === 'thread' ? (
          <>
            <div className="chat-dialog-head">
              <div>
                <h2 id="chat-dialog-thread-title">{t('chat.thread.title')}</h2>
                <p>{sampleChat.sessionTitle}</p>
              </div>
            </div>
            <div className="chat-dialog-body chat-stack">
              {(
                [
                  ['branch', 'chat.thread.branch'],
                  ['archive', 'chat.thread.archive'],
                  ['download', 'chat.thread.export'],
                  ['trash', 'chat.thread.delete'],
                ] as const
              ).map(([icon, key]) => (
                <button
                  key={key}
                  type="button"
                  className={`chat-btn chat-btn-wide${key === 'chat.thread.delete' ? ' chat-btn-danger' : ''}`}
                  disabled
                  title={t('chat.action.unavailable')}
                >
                  <Icon name={icon} size={14} />
                  <span>{t(key)}</span>
                </button>
              ))}
            </div>
            <div className="chat-dialog-foot">
              <button type="button" className="chat-btn" onClick={onClose}>
                {t('chat.thread.close')}
              </button>
            </div>
          </>
        ) : null}

        {kind === 'thil' ? (
          <>
            <div className="chat-thil-head">
              <div className="chat-thil-kicker">
                <Icon name="message" size={13} />
                <span>{t('chat.clarification.kicker')}</span>
              </div>
              <h2 id="chat-dialog-thil-title">{t('chat.thil.title')}</h2>
              <p>{t('chat.thil.desc')}</p>
            </div>
            <div className="chat-thil-context">
              <span className="chat-agent-badge">迁</span>
              <div>
                <b>{sampleChat.agent}</b>
                <span>{sampleChat.messages.find((message) => message.kind === 'clarification')?.body}</span>
              </div>
            </div>
            <div className="chat-dialog-body">
              <div className="chat-field-label">{t('chat.thil.choose')}</div>
              <div className="chat-thil-options" role="radiogroup" aria-label={t('chat.thil.radioLabel')}>
                {(
                  [
                    ['tenant', 'chat.thil.option.tenant', 'chat.thil.option.tenantDesc'],
                    ['traffic', 'chat.thil.option.traffic', 'chat.thil.option.trafficDesc'],
                    ['environment', 'chat.thil.option.environment', 'chat.thil.option.environmentDesc'],
                  ] as const
                ).map(([value, label, desc]) => (
                  <button
                    key={value}
                    type="button"
                    role="radio"
                    aria-checked={thil === value}
                    className={`chat-thil-option${thil === value ? ' is-selected' : ''}`}
                    onClick={() => setThil(value)}
                  >
                    <span className="chat-radio" aria-hidden="true" />
                    <span>
                      <b>{t(label)}</b>
                      <small>{t(desc)}</small>
                    </span>
                  </button>
                ))}
              </div>
              <label className="chat-field">
                <span className="chat-field-label">{t('chat.thil.note')}</span>
                <textarea rows={2} placeholder={t('chat.thil.placeholder')} />
              </label>
              <p className="chat-thil-scope">
                <Icon name="shield" size={13} /> {t('chat.thil.scope')}
              </p>
            </div>
            <div className="chat-dialog-foot">
              <button type="button" className="chat-btn" onClick={onClose}>
                {t('chat.thil.later')}
              </button>
              <button
                type="button"
                className="chat-btn chat-btn-primary"
                onClick={() => {
                  onNotice(t('chat.thil.notice'))
                  onClose()
                }}
              >
                {t('chat.thil.continue')}
              </button>
            </div>
          </>
        ) : null}
      </div>
    </div>
  )
}
