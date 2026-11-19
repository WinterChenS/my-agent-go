import { useEffect, useRef, useState } from 'react'
import { useChatHistory } from '../../chat/ChatHistoryProvider'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { useWorkspace, workspaceName } from '../../workspace/WorkspaceProvider'
import { sampleChat } from './sampleData'

export type ChatDialogKind = 'project' | 'export' | 'thread' | 'thil' | 'trust' | null

/** What the export dialog asks the page to do. */
export type ExportRequest = { format: 'html' | 'markdown' | 'json'; includeTools: boolean }

export default function ChatDialogs({
  kind,
  onClose,
  onNotice,
  onExport,
  onConfirmTrust,
  sample = false,
}: {
  kind: ChatDialogKind
  onClose: () => void
  onNotice: (message: string) => void
  /** Prototype mode: the project panel keeps its fixture data. */
  sample?: boolean
  /** Absent in sample mode: the prototype has nothing to export. */
  onExport?: (request: ExportRequest) => Promise<{ ok: boolean; message: string }>
  /** Only called from the trust warning, after the reader says yes. */
  onConfirmTrust?: () => void
}) {
  const { t } = useI18n()
  const dialogRef = useRef<HTMLDivElement>(null)
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

  const closeOnBackdrop = (
    event: React.MouseEvent<HTMLDivElement> | React.PointerEvent<HTMLDivElement>,
  ) => {
    if (event.target === event.currentTarget) onClose()
  }

  return (
    <div
      className="chat-backdrop"
      onPointerDown={closeOnBackdrop}
      onMouseDown={closeOnBackdrop}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.stopPropagation()
          onClose()
        }
      }}
    >
      <div
        ref={dialogRef}
        className={`chat-dialog${kind === 'thil' ? ' chat-dialog-wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`chat-dialog-${kind}-title`}
      >
        {kind === 'project' ? (
          sample ? (
            <SampleProjectPanel onClose={onClose} onNotice={onNotice} />
          ) : (
            <LiveProjectPanel onClose={onClose} onNotice={onNotice} />
          )
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
                onClick={() => {
                  if (!onExport) {
                    onNotice(t('chat.export.notice'))
                    return
                  }
                  void onExport({
                    format: format as 'html' | 'markdown' | 'json',
                    includeTools,
                  }).then((result) => {
                    // A failed export keeps the dialog open with the reason in
                    // the notice; a successful one closes and reports the path.
                    onNotice(result.message)
                    if (result.ok) onClose()
                  })
                }}
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

        {kind === 'trust' ? (
          <>
            <div className="chat-dialog-head">
              <div>
                <h2 id="chat-dialog-trust-title">{t('chat.permission.trust.title')}</h2>
                <p>{t('chat.permission.trust.desc')}</p>
              </div>
              <button
                type="button"
                className="chat-iconbtn"
                aria-label={t('chat.dialog.close')}
                onClick={onClose}
              >
                <Icon name="x" size={14} />
              </button>
            </div>
            <div className="chat-dialog-body">
              {/* The three consequences worth reading before the switch, not a
                  generic "are you sure": each line is something the mode turns
                  off, and the last one says how to undo it. */}
              <ul className="chat-trust-points" data-testid="chat-trust-points">
                <li>{t('chat.permission.trust.point1')}</li>
                <li>{t('chat.permission.trust.point2')}</li>
                <li>{t('chat.permission.trust.point3')}</li>
              </ul>
            </div>
            <div className="chat-dialog-foot">
              <button type="button" className="chat-btn" data-testid="chat-trust-cancel" onClick={onClose}>
                {t('chat.permission.trust.cancel')}
              </button>
              <button
                type="button"
                className="chat-btn chat-btn-danger"
                data-testid="chat-trust-confirm"
                onClick={() => {
                  onConfirmTrust?.()
                  onClose()
                }}
              >
                {t('chat.permission.trust.confirm')}
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

/**
 * The prototype's project picker: fixture rows, nothing wired.
 *
 * Kept verbatim so the sample screens keep rendering exactly what the design
 * reference shows; the real picker lives in `LiveProjectPanel`.
 */
function SampleProjectPanel({ onClose, onNotice }: { onClose: () => void; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const [project, setProject] = useState(sampleChat.projectOptions[0].name)
  return (
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
            <span className="mono">{sampleChat.projectOptions.find((item) => item.name === project)?.path}</span>
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
  )
}

/**
 * The real picker: the workspace the next conversation runs in.
 *
 * Rows are the directories the ledger already knows about, plus the default
 * workspace, so "the conversations I had in D:/work" and "the folder I just
 * picked" are the same list. Choosing one writes it as the window's workspace;
 * the next `session_open` runs there and the history panel groups it by that
 * directory, which is the same rule the sidebar already used.
 */
function LiveProjectPanel({
  onClose,
  onNotice,
}: {
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const workspace = useWorkspace()
  const history = useChatHistory()
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const addPopoverRef = useRef<HTMLDivElement>(null)
  const addButtonRef = useRef<HTMLButtonElement>(null)

  // The "+" menu is a popover: clicking anywhere else in the dialog (or on the
  // page behind it) dismisses it, the way a menu does. Both pointerdown and
  // mousedown are watched in the capture phase because WebView2 does not always
  // deliver pointer events for native-style controls; composedPath also covers
  // targets that are reparented into a shadow root.
  useEffect(() => {
    if (!adding) return
    const onOutside = (event: Event) => {
      const node = event.target as Node | null
      if (!node) return
      const path = typeof event.composedPath === 'function' ? event.composedPath() : []
      if (addPopoverRef.current && (addPopoverRef.current.contains(node) || path.includes(addPopoverRef.current))) return
      if (addButtonRef.current && (addButtonRef.current.contains(node) || path.includes(addButtonRef.current))) return
      setAdding(false)
    }
    document.addEventListener('pointerdown', onOutside, true)
    document.addEventListener('mousedown', onOutside, true)
    return () => {
      document.removeEventListener('pointerdown', onOutside, true)
      document.removeEventListener('mousedown', onOutside, true)
    }
  }, [adding])

  const describe = (cause: unknown) => (cause instanceof Error ? cause.message : String(cause))
  const run = async (action: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (cause) {
      setError(describe(cause))
    } finally {
      setBusy(false)
    }
  }

  const currentName = workspace.isDefault
    ? t('workspace.defaultName')
    : workspace.name || t('chat.composer.project.none')

  // The default first, then whatever the ledger knows, without repeating the
  // default when it already has conversations.
  const rows = [
    {
      path: workspace.defaultPath ?? '',
      name: t('workspace.defaultName'),
      sessions: history.projects.find((project) => project.path === workspace.defaultPath)?.sessionCount ?? 0,
      isDefault: true,
    },
    ...history.projects
      .filter((project) => project.path !== workspace.defaultPath)
      .map((project) => ({
        path: project.path,
        name: project.name,
        sessions: project.sessionCount,
        isDefault: false,
      })),
  ].filter((row) => row.path !== '')

  return (
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
        <div className="chat-project-current" data-testid="workspace-current">
          <span className="chat-project-logo">
            <Icon name="folder" size={15} />
          </span>
          <div>
            <small>{t('chat.project.current')}</small>
            <b>{currentName}</b>
            <span className="mono">{workspace.path ?? t('chat.live.loading')}</span>
          </div>
          <span className="chat-tone-success">
            {workspace.isDefault ? t('workspace.defaultBadge') : t('chat.project.opened')}
          </span>
          {workspace.isDefault ? null : (
            <button
              type="button"
              className="chat-btn chat-btn-sm chat-workspace-default"
              data-testid="workspace-set-default"
              disabled={busy}
              onClick={() => void run(async () => {
                const made = await workspace.setDefault(workspace.path ?? '')
                onNotice(t('workspace.defaultSet', { name: workspaceName(made) }))
                onClose()
              })}
            >
              {t('workspace.setDefault')}
            </button>
          )}
        </div>

        <div className="chat-field-label">{t('chat.project.recent')}</div>
        <div className="chat-project-options" role="radiogroup" aria-label={t('chat.project.recent')}>
          {rows.map((row) => (
            <button
              key={row.path}
              type="button"
              role="radio"
              aria-checked={row.path === workspace.path}
              data-testid="workspace-option"
              data-path={row.path}
              className={`chat-project-option${row.path === workspace.path ? ' is-selected' : ''}`}
              disabled={busy}
              onClick={() => void run(async () => {
                const switched = await workspace.choose(row.path)
                onNotice(t('workspace.switched', { name: workspaceName(switched) }))
                onClose()
              })}
            >
              <span className="chat-project-logo">{row.isDefault ? <Icon name="layers" size={12} /> : row.name.slice(0, 1)}</span>
              <span>
                <b>{row.name}</b>
                <small className="mono">
                  {row.path}
                  {row.sessions > 0 ? ` · ${t('chat.project.sessions', { count: row.sessions })}` : ''}
                </small>
              </span>
              <span className="chat-project-check" aria-hidden="true">
                <Icon name="check" size={12} />
              </span>
            </button>
          ))}
        </div>

        {adding ? (
          <div
            className="chat-workspace-add"
            data-testid="workspace-add-popover"
            ref={addPopoverRef}
          >
            <div className="chat-workspace-add-title">{t('workspace.addTitle')}</div>
            <button
              type="button"
              className="chat-workspace-add-option"
              data-testid="workspace-add-folder"
              disabled={busy}
              onClick={() => void run(async () => {
                const switched = await workspace.browse()
                setAdding(false)
                if (switched) {
                  onNotice(t('workspace.switched', { name: workspaceName(switched) }))
                  onClose()
                }
              })}
            >
              <Icon name="folder" size={14} />
              <span>
                <b>{t('workspace.addFolder')}</b>
                <small>{t('workspace.addFolderDesc')}</small>
              </span>
            </button>
            <button
              type="button"
              className="chat-workspace-add-option"
              data-testid="workspace-add-file"
              disabled={busy}
              onClick={() => void run(async () => {
                const switched = await workspace.pickFile()
                setAdding(false)
                if (switched) {
                  onNotice(t('workspace.switched', { name: workspaceName(switched) }))
                  onClose()
                }
              })}
            >
              <Icon name="file" size={14} />
              <span>
                <b>{t('workspace.addFile')}</b>
                <small>{t('workspace.addFileDesc')}</small>
              </span>
            </button>
            <form
              className="chat-workspace-create"
              onSubmit={(event) => {
                event.preventDefault()
                const name = draft.trim()
                if (!name) return
                void run(async () => {
                  const created = await workspace.create(name)
                  setDraft('')
                  setAdding(false)
                  onNotice(t('workspace.switched', { name: workspaceName(created) }))
                  onClose()
                })
              }}
            >
              <input
                autoFocus
                type="text"
                data-testid="workspace-name"
                value={draft}
                aria-label={t('chat.project.new')}
                placeholder={workspace.projectsRoot ?? ''}
                onChange={(event) => setDraft(event.target.value)}
              />
              <button type="submit" className="chat-btn chat-btn-sm" disabled={busy || draft.trim() === ''}>
                {t('workspace.createAction')}
              </button>
            </form>
          </div>
        ) : null}

        <div className="chat-project-actions">
          <button
            type="button"
            className="chat-project-action"
            data-testid="workspace-browse"
            disabled={busy}
            onClick={() => void run(async () => {
              const switched = await workspace.browse()
              if (switched) {
                onNotice(t('workspace.switched', { name: workspaceName(switched) }))
                onClose()
              }
            })}
          >
            <Icon name="folder" size={15} />
            <span>
              <b>{t('chat.project.openLocal')}</b>
              <small>{t('chat.project.openLocalDesc')}</small>
            </span>
          </button>
          <button
            ref={addButtonRef}
            type="button"
            className="chat-project-action"
            data-testid="workspace-new"
            disabled={busy}
            aria-expanded={adding}
            onClick={() => setAdding((open) => !open)}
          >
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
        {error ?? workspace.error ? (
          <p className="chat-history-notice" role="alert" data-testid="workspace-error">
            {t('workspace.failed', { message: error ?? workspace.error ?? '' })}
          </p>
        ) : null}
      </div>
      <div className="chat-dialog-foot">
        <button type="button" className="chat-btn" onClick={onClose}>
          {t('chat.project.cancel')}
        </button>
        <button type="button" className="chat-btn chat-btn-primary" onClick={onClose}>
          {t('chat.project.continue')}
        </button>
      </div>
    </>
  )
}
