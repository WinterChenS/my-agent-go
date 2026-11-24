import { useEffect, useMemo, useRef, useState } from 'react'
import type { ProjectSummary, SessionSummary } from '../../chat/client'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { useWorkspace } from '../../workspace/WorkspaceProvider'
import { sampleChat } from './sampleData'

type Session = {
  id: string
  title: string
  meta: string
  state?: 'pending'
  pinned?: boolean
  /** Archived rows are only listed when the panel asks for them. */
  archived?: boolean
  /** Directory the session ran in; the history panel groups rows by it. */
  projectPath?: string
}

type HistoryAction = 'pin' | 'rename' | 'archive' | 'unarchive' | 'fork' | 'compact' | 'delete'

/** The real ledger, when the desktop host is connected. */
export interface LiveHistory {
  sessions: SessionSummary[]
  /** Project index the rows are grouped by; empty until the first read. */
  projects: ProjectSummary[]
  activeId: string | null
  /** Session currently generating a reply; its row shows a running spinner. */
  runningId?: string | null
  onOpen: (id: string) => void
  onNew: () => void
  onRename: (id: string, title: string) => void
  onPin: (id: string) => void
  onArchive: (id: string) => void
  onDelete: (id: string) => void
  onFork: (id: string) => void
  onCompact: (id: string) => void
  /** Bring one archived conversation back into the default list. */
  onUnarchive?: (id: string) => void
  /** What the last action did, or why it could not. */
  notice?: string | null
  noticeTone?: 'ok' | 'error'
  /** Whether archived rows are in `sessions`. */
  showArchived?: boolean
  onToggleArchived?: () => void
}

export default function ChatHistoryPanel({
  sample,
  live,
  variant = 'sidebar',
  onClose,
}: {
  sample: boolean
  live?: LiveHistory
  variant?: 'sidebar' | 'drawer'
  onClose?: () => void
}) {
  const { t } = useI18n()
  const workspace = useWorkspace()
  const [query, setQuery] = useState('')
  const [notice, setNotice] = useState('')
  const [visible, setVisible] = useState(4)
  const [menuFor, setMenuFor] = useState<string | null>(null)
  /** Row being renamed in place. `window.prompt` is not implemented by the
   * desktop webview, so the name is edited in the row itself. */
  const [renaming, setRenaming] = useState<{ id: string; value: string } | null>(null)
  /** Row whose delete was armed; a second click confirms. */
  const [confirming, setConfirming] = useState<string | null>(null)
  const [projects, setProjects] = useState(() =>
    sample ? sampleChat.projects.map((project) => ({ ...project })) : [],
  )

  const personal = sample ? sampleChat.personal : []
  const normalized = query.trim().toLowerCase()
  const matches = (value: string) => value.toLowerCase().includes(normalized)

  const visibleProjects = useMemo(() => {
    if (!sample) return []
    return projects
      .map((project) => {
        const projectMatch = !normalized || matches(`${project.name} ${project.path}`)
        const sessions = project.sessions.filter(
          (session) => !normalized || projectMatch || matches(`${session.title} ${session.meta}`),
        )
        return { ...project, sessions, forcedOpen: normalized ? sessions.length > 0 || projectMatch : false }
      })
      .filter((project) => !normalized || project.sessions.length > 0 || matches(`${project.name} ${project.path}`))
  }, [projects, normalized])

  const visiblePersonal = personal
    .filter((session) => !normalized || matches(`${session.title} ${session.meta}`))
    .slice(0, normalized ? personal.length : visible)

  // Live rows are the ledger's own list: already ordered (pinned first, then
  // most recent) and already paged by the host, so only the text filter runs
  // here.
  const visibleLive = (live?.sessions ?? [])
    .filter((session) => !normalized || matches(`${session.title} ${session.preview} ${session.runtimeId}`))
    .map<Session>((session) => ({
      id: session.id,
      title: session.title,
      meta: t('chat.live.sessionMeta', {
        runtime: session.runtimeId,
        count: session.messageCount,
      }),
      pinned: session.pinned,
      archived: session.archived,
      projectPath: session.projectPath,
      state: session.id === live?.runningId ? 'pending' : undefined,
    }))

  // Group live rows by the project index the host returned. The index decides
  // the order (most recently used first); a row whose directory is missing from
  // it still renders, under "no project", instead of disappearing.
  const liveGroups = useMemo(() => {
    if (visibleLive.length === 0) return []
    const names = new Map((live?.projects ?? []).map((project) => [project.path, project.name]))
    const order: string[] = (live?.projects ?? []).map((project) => project.path)
    const groups = new Map<string, Session[]>()
    for (const session of visibleLive) {
      const key = session.projectPath ?? ''
      groups.set(key, [...(groups.get(key) ?? []), session])
    }
    const keys = [...groups.keys()].sort((left, right) => {
      const leftIndex = order.indexOf(left)
      const rightIndex = order.indexOf(right)
      if (leftIndex === -1 && rightIndex === -1) return left.localeCompare(right)
      if (leftIndex === -1) return 1
      if (rightIndex === -1) return -1
      return leftIndex - rightIndex
    })
    return keys.map((path) => ({
      path,
      // The default workspace is a real directory with a name of its own; the
      // folder is called "Anvil", which says nothing about why every new chat
      // starts there.
      name:
        path && path === workspace.defaultPath
          ? t('workspace.defaultName')
          : names.get(path) ?? (path || t('chat.history.noProject')),
      sessions: groups.get(path) ?? [],
    }))
  }, [live?.projects, t, visibleLive, workspace.defaultPath])

  // Two sources: the sample panel keeps its own line, the live panel gets one
  // from the last action. Either way the reader gets an answer to "did it work".
  const banner = live?.notice ?? notice
  const noResults = normalized
    ? visibleProjects.length === 0 && visiblePersonal.length === 0 && visibleLive.length === 0
    : !sample && visibleLive.length === 0
  const allLoaded = visible >= personal.length
  const actionsEnabled = !sample && live !== undefined

  const toggleProject = (id: string) => {
    if (normalized) return
    setProjects((current) => current.map((project) => (project.id === id ? { ...project, open: !project.open } : project)))
  }

  const runAction = (action: HistoryAction, session: Session) => {
    setMenuFor(null)
    setConfirming(null)
    if (!live) return
    if (action === 'rename') {
      setRenaming({ id: session.id, value: session.title })
      return
    }
    if (action === 'delete') {
      // Deleting is irreversible here, and the webview's confirm dialog is not
      // something the page can rely on: arm the row, then require a second
      // click on the same action.
      if (confirming !== session.id) {
        setConfirming(session.id)
        setMenuFor(session.id)
        return
      }
      setConfirming(null)
      live.onDelete(session.id)
      return
    }
    if (action === 'pin') live.onPin(session.id)
    if (action === 'archive') live.onArchive(session.id)
    if (action === 'unarchive') live.onUnarchive?.(session.id)
    if (action === 'fork') live.onFork(session.id)
    if (action === 'compact') live.onCompact(session.id)
  }

  return (
    <section
      className={`chat-history${variant === 'drawer' ? ' chat-history-drawer-panel' : ''}`}
      data-testid="chat-history"
      aria-label={t('chat.history.title')}
    >
      <div className="chat-history-head">
        <span>{t('chat.history.title')}</span>
        {variant === 'drawer' ? (
          <div className="chat-history-head-actions">
            <button type="button" className="chat-iconbtn" aria-label={t('chat.dialog.close')} onClick={onClose}>
              <Icon name="x" size={12} />
            </button>
          </div>
        ) : null}
      </div>
      {/* A full-width, labelled entry. The old 22px "+" icon in the heading was
          easy to miss and did not read as "start a new conversation". */}
      <button
        type="button"
        className="chat-history-new-btn"
        data-testid="chat-history-new"
        aria-label={t('chat.live.newSession')}
        title={sample || actionsEnabled ? t('chat.history.newChat') : t('chat.action.unavailable')}
        disabled={!sample && !actionsEnabled}
        onClick={() => {
          if (live) {
            live.onNew()
            return
          }
          setNotice(t('chat.notice.history'))
        }}
      >
        <Icon name="plus" size={13} />
        <span>{t('chat.history.newChat')}</span>
      </button>
      <label className="chat-history-search">
        <Icon name="search" size={11} />
        <input
          id={variant === 'drawer' ? 'chat-history-search-drawer' : 'chat-history-search'}
          data-testid="chat-history-search"
          type="search"
          value={query}
          onChange={(event) => {
            setQuery(event.target.value)
            setNotice('')
            setMenuFor(null)
          }}
          placeholder={t('chat.history.search')}
          aria-label={t('chat.history.search')}
        />
      </label>
      {live?.onToggleArchived ? (
        // Archiving is only usable if there is a way back: this is it.
        <button
          type="button"
          className={`chat-history-archived-toggle${live.showArchived ? ' is-on' : ''}`}
          data-testid="chat-history-archived-toggle"
          aria-pressed={live.showArchived === true}
          onClick={() => live.onToggleArchived?.()}
        >
          <Icon name="archive" size={10} />
          <span>{live.showArchived ? t('chat.history.hideArchived') : t('chat.history.showArchived')}</span>
        </button>
      ) : null}
      {banner ? (
        <p
          className={`chat-history-notice${live?.noticeTone === 'error' ? ' is-error' : ''}`}
          data-testid="chat-history-notice"
          role="status"
        >
          {banner}
        </p>
      ) : null}
      <div className="chat-history-scroll">
        {sample ? (
          <>
            {visibleProjects.length > 0 ? (
              <div className="chat-history-label">{t('chat.history.projects')}</div>
            ) : null}
            {visibleProjects.map((project) => {
              const open = project.forcedOpen || project.open
              return (
                <div key={project.id} className={`chat-history-project${open ? ' is-open' : ''}`}>
                  <button
                    type="button"
                    className="chat-history-project-head"
                    aria-expanded={open}
                    onClick={() => toggleProject(project.id)}
                  >
                    <span className="chat-history-project-icon">
                      <Icon name="folder" size={12} />
                    </span>
                    <span className="chat-history-project-copy">
                      <b>{project.name}</b>
                      <small>
                        {t('chat.history.sessions', { count: project.sessions.length })} · {project.path}
                      </small>
                    </span>
                    <span className="chat-history-caret" aria-hidden="true">
                      <Icon name="chevDown" size={10} />
                    </span>
                  </button>
                  {open ? (
                    <div className="chat-history-project-sessions">
                      {project.sessions.map((session) => (
                        <SessionRow
                          key={session.id}
                          session={session}
                          active={session.id === 'session-api-migration'}
                          menuOpen={menuFor === session.id}
                          onOpen={() => {
                            setNotice(t('chat.notice.history'))
                            onClose?.()
                          }}
                          onMenu={() => {
                            setNotice(t('chat.notice.history'))
                            setMenuFor((current) => (current === session.id ? null : session.id))
                          }}
                          onCloseMenu={() => setMenuFor(null)}
                          onAction={() => setNotice(t('chat.notice.history'))}
                        />
                      ))}
                    </div>
                  ) : null}
                </div>
              )
            })}

            <div className="chat-history-label chat-history-personal-label">{t('chat.history.personal')}</div>
            <div className="chat-history-list" data-testid="chat-history-live-list">
              {visiblePersonal.map((session) => (
                <SessionRow
                  key={session.id}
                  session={session}
                  active={false}
                  menuOpen={menuFor === session.id}
                  onOpen={() => {
                    setNotice(t('chat.notice.history'))
                    onClose?.()
                  }}
                  onMenu={() => {
                    setNotice(t('chat.notice.history'))
                    setMenuFor((current) => (current === session.id ? null : session.id))
                  }}
                  onCloseMenu={() => setMenuFor(null)}
                  onAction={() => setNotice(t('chat.notice.history'))}
                />
              ))}
            </div>

            {!normalized ? (
              <button
                type="button"
                className={`chat-history-sentinel${allLoaded ? ' is-done' : ''}`}
                disabled={allLoaded}
                onClick={() => setVisible((count) => Math.min(count + 3, personal.length))}
              >
                <span className="chat-history-sentinel-dot" aria-hidden="true" />
                <span>{allLoaded ? t('chat.history.loadedAll') : t('chat.history.loadMore')}</span>
              </button>
            ) : null}
          </>
        ) : (
          <div className="chat-history-list" data-testid="chat-history-live-list">
            {liveGroups.map((group) => (
              <div key={group.path || 'unassigned'} className="chat-history-live-group">
                {live?.projects.length ? (
                  <div
                    className="chat-history-label chat-history-live-project"
                    data-testid="chat-history-live-project"
                    data-project-path={group.path}
                  >
                    <Icon name="folder" size={10} />
                    <span>{group.name}</span>
                    <small>{t('chat.history.sessions', { count: group.sessions.length })}</small>
                  </div>
                ) : null}
                {group.sessions.map((session) => (
                  <SessionRow
                    key={session.id}
                    session={session}
                    active={session.id === live?.activeId}
                    menuOpen={menuFor === session.id}
                    confirmingDelete={confirming === session.id}
                    rename={
                      renaming?.id === session.id
                        ? {
                            value: renaming.value,
                            onChange: (value) => setRenaming({ id: session.id, value }),
                            commit: () => {
                              const next = renaming.value.trim()
                              setRenaming(null)
                              if (next && next !== session.title) live?.onRename(session.id, next)
                            },
                            cancel: () => setRenaming(null),
                          }
                        : null
                    }
                    onOpen={() => live?.onOpen(session.id)}
                    onMenu={() => {
                      setConfirming(null)
                      setMenuFor((current) => (current === session.id ? null : session.id))
                    }}
                    onCloseMenu={() => setMenuFor(null)}
                    onAction={actionsEnabled ? (action) => runAction(action, session) : undefined}
                  />
                ))}
              </div>
            ))}
          </div>
        )}

        {noResults ? (
          <div className="chat-history-empty" data-testid="chat-history-empty">
            {normalized ? t('chat.history.noMatches') : live ? t('chat.live.historyEmpty') : t('chat.history.empty')}
          </div>
        ) : null}
      </div>
    </section>
  )
}

function SessionRow({
  session,
  active,
  menuOpen,
  confirmingDelete,
  rename,
  onOpen,
  onMenu,
  onCloseMenu,
  onAction,
}: {
  session: Session
  active: boolean
  menuOpen: boolean
  /** The delete action was already clicked once and is waiting for a second. */
  confirmingDelete?: boolean
  /** Inline editor; `null` while the row only shows its title. */
  rename?: {
    value: string
    onChange: (value: string) => void
    commit: () => void
    cancel: () => void
  } | null
  onOpen: () => void
  onMenu: () => void
  onCloseMenu: () => void
  onAction?: (action: HistoryAction) => void
}) {
  const { t } = useI18n()
  const id = `session-${session.id}`
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [anchor, setAnchor] = useState({ top: 0, left: 0 })

  // The sidebar list is an `overflow: auto` container. An absolutely
  // positioned menu inside it is either clipped or grows a scrollbar: the
  // stylesheet makes this popover `position: fixed` and it is placed against
  // the trigger's viewport rect, so it stays out of the list's layout.
  useEffect(() => {
    if (!menuOpen) return
    const openedAt = Date.now()
    // The very click that opened the popover must not close it again in the
    // same frame, before the browser has finished dispatching it.
    const closeOnOutside = (event: MouseEvent) => {
      if (Date.now() - openedAt < 300) return
      const target = event.target as Node
      if (menuRef.current?.contains(target) || triggerRef.current?.contains(target)) return
      onCloseMenu()
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseMenu()
    }
    document.addEventListener('click', closeOnOutside)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('click', closeOnOutside)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [menuOpen, onCloseMenu])

  return (
    <div className={`chat-history-row${session.pinned ? ' is-pinned' : ''}${menuOpen ? ' is-menu-open' : ''}`} id={id}>
      {rename ? (
        <div className="chat-history-rename">
          <input
            autoFocus
            type="text"
            value={rename.value}
            aria-label={t('chat.history.rename')}
            data-testid="chat-history-rename-input"
            onChange={(event) => rename.onChange(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') rename.commit()
              if (event.key === 'Escape') rename.cancel()
            }}
            onBlur={rename.commit}
          />
        </div>
      ) : (
        <button
          type="button"
          className={`chat-history-item${active ? ' is-active' : ''}`}
          onClick={onOpen}
          title={t('chat.history.open')}
        >
          <span className="chat-history-branch" aria-hidden="true">
            <Icon name="branch" size={10} />
          </span>
          <span className="chat-history-copy">
            <span className="chat-history-title" data-pinned={session.pinned ? 'true' : undefined}>
              {session.pinned ? (
                <span className="chat-history-pin" data-testid="chat-history-pin-chip" aria-hidden="true">
                  <Icon name="pin" size={9} />
                </span>
              ) : null}
              {session.title}
            </span>
            <span className="chat-history-meta">
              {session.state === 'pending' ? (
                <i
                  className="chat-history-spinner"
                  data-testid={`chat-history-running-${session.id}`}
                  aria-label={t('chat.live.generating')}
                  role="status"
                />
              ) : null}
              {session.archived ? (
                <span className="chat-history-chip" data-testid="chat-history-archived-chip">
                  {t('chat.history.archivedBadge')}
                </span>
              ) : null}
              {session.meta}
            </span>
          </span>
        </button>
      )}
      {onAction ? (
        <button
        ref={triggerRef}
        type="button"
        className="chat-history-actions-trigger"
        aria-label={t('chat.history.actions')}
        aria-expanded={menuOpen}
        data-testid={`chat-history-actions-${session.id}`}
        onClick={(event) => {
          event.stopPropagation()
          // A single click toggles the menu, exactly like the original row.
          // Only the popover's positioning changed (fixed, out of the list's
          // scrolling layout); its open/close behaviour is untouched.
          const rect = triggerRef.current?.getBoundingClientRect()
          if (rect) {
            const width = 184
            const height = 210
            const left = Math.max(8, Math.min(rect.right - width, window.innerWidth - width - 8))
            const below = rect.bottom + 4
            const top = below + height > window.innerHeight ? Math.max(8, rect.top - height - 4) : below
            setAnchor({ top, left })
          }
          onMenu()
        }}
      >
        <Icon name="more" size={11} />
        </button>
      ) : null}
      {menuOpen ? (
        <div
              ref={menuRef}
              className="chat-history-action-menu"
              role="menu"
              aria-label={t('chat.history.actions')}
              data-testid="chat-history-action-menu"
              data-session-id={session.id}
              style={{ top: anchor.top, left: anchor.left }}
            >
              {(session.archived
                ? (['pin', 'rename', 'unarchive', 'fork', 'compact', 'delete'] as const)
                : (['pin', 'rename', 'archive', 'fork', 'compact', 'delete'] as const)
              ).map((action) => (
                <button
                  key={action}
                  type="button"
                  role="menuitem"
                  className={action === 'delete' ? 'is-danger' : ''}
                  title={t(`chat.history.${action}` as 'chat.history.pin')}
                  onClick={() => {
                    if (!onAction) {
                      onCloseMenu()
                      return
                    }
                    onAction(action)
                  }}
                >
                  <Icon
                    name={
                      action === 'pin'
                        ? 'pin'
                        : action === 'delete'
                          ? 'trash'
                          : action === 'archive' || action === 'unarchive'
                            ? 'archive'
                            : action === 'fork'
                              ? 'branch'
                              : 'file'
                    }
                    size={11}
                  />
                  <span>
                    {action === 'delete' && confirmingDelete
                      ? t('chat.history.deleteConfirm')
                      : t(`chat.history.${action}` as 'chat.history.pin')}
                  </span>
                </button>
              ))}
      </div>
      ) : null}
    </div>
  )
}
