import { useMemo, useState } from 'react'
import type { ProjectSummary, SessionSummary } from '../../chat/client'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { sampleChat } from './sampleData'

type Session = {
  id: string
  title: string
  meta: string
  state?: 'pending'
  pinned?: boolean
  /** Directory the session ran in; the history panel groups rows by it. */
  projectPath?: string
}

type HistoryAction = 'pin' | 'rename' | 'archive' | 'fork' | 'compact' | 'delete'

/** The real ledger, when the desktop host is connected. */
export interface LiveHistory {
  sessions: SessionSummary[]
  /** Project index the rows are grouped by; empty until the first read. */
  projects: ProjectSummary[]
  activeId: string | null
  onOpen: (id: string) => void
  onNew: () => void
  onRename: (id: string) => void
  onPin: (id: string) => void
  onArchive: (id: string) => void
  onDelete: (id: string) => void
  onFork: (id: string) => void
  onCompact: (id: string) => void
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
  const [query, setQuery] = useState('')
  const [notice, setNotice] = useState('')
  const [visible, setVisible] = useState(4)
  const [menuFor, setMenuFor] = useState<string | null>(null)
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
      projectPath: session.projectPath,
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
      name: names.get(path) ?? (path || t('chat.history.noProject')),
      sessions: groups.get(path) ?? [],
    }))
  }, [live?.projects, t, visibleLive])

  const noResults = normalized
    ? visibleProjects.length === 0 && visiblePersonal.length === 0 && visibleLive.length === 0
    : !sample && visibleLive.length === 0
  const allLoaded = visible >= personal.length
  const actionsEnabled = !sample && live !== undefined

  const toggleProject = (id: string) => {
    if (normalized) return
    setProjects((current) => current.map((project) => (project.id === id ? { ...project, open: !project.open } : project)))
  }

  const runAction = (action: HistoryAction, sessionId: string) => {
    setMenuFor(null)
    if (!live) return
    if (action === 'pin') live.onPin(sessionId)
    if (action === 'rename') live.onRename(sessionId)
    if (action === 'archive') live.onArchive(sessionId)
    if (action === 'fork') live.onFork(sessionId)
    if (action === 'compact') live.onCompact(sessionId)
    if (action === 'delete') live.onDelete(sessionId)
  }

  return (
    <section
      className={`chat-history${variant === 'drawer' ? ' chat-history-drawer-panel' : ''}`}
      data-testid="chat-history"
      aria-label={t('chat.history.title')}
    >
      <div className="chat-history-head">
        <span>{t('chat.history.title')}</span>
        <div className="chat-history-head-actions">
          <button
            type="button"
            className="chat-iconbtn"
            data-testid="chat-history-new"
            aria-label={t('chat.live.newSession')}
            title={actionsEnabled ? t('chat.live.newSession') : t('chat.action.unavailable')}
            disabled={!sample && !actionsEnabled}
            onClick={() => {
              if (live) {
                live.onNew()
                return
              }
              setNotice(t('chat.notice.history'))
            }}
          >
            <Icon name="plus" size={12} />
          </button>
          {variant === 'drawer' ? (
            <button type="button" className="chat-iconbtn" aria-label={t('chat.dialog.close')} onClick={onClose}>
              <Icon name="x" size={12} />
            </button>
          ) : null}
        </div>
      </div>
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
      {notice ? (
        <p className="chat-history-notice" data-testid="chat-history-notice" role="status">
          {notice}
        </p>
      ) : null}
      <div className="chat-history-scroll">
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
                    />
                  ))}
                </div>
              ) : null}
            </div>
          )
        })}

        <div className="chat-history-label chat-history-personal-label">{t('chat.history.personal')}</div>
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
                  onOpen={() => live?.onOpen(session.id)}
                  onMenu={() => setMenuFor((current) => (current === session.id ? null : session.id))}
                  onCloseMenu={() => setMenuFor(null)}
                  onAction={actionsEnabled ? (action) => runAction(action, session.id) : undefined}
                />
              ))}
            </div>
          ))}
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
            />
          ))}
        </div>

        {noResults ? (
          <div className="chat-history-empty" data-testid="chat-history-empty">
            {normalized ? t('chat.history.noMatches') : live ? t('chat.live.historyEmpty') : t('chat.history.empty')}
          </div>
        ) : null}

        {!normalized && sample ? (
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
      </div>
    </section>
  )
}

function SessionRow({
  session,
  active,
  menuOpen,
  onOpen,
  onMenu,
  onCloseMenu,
  onAction,
}: {
  session: Session
  active: boolean
  menuOpen: boolean
  onOpen: () => void
  onMenu: () => void
  onCloseMenu: () => void
  onAction?: (action: HistoryAction) => void
}) {
  const { t } = useI18n()
  const id = `session-${session.id}`
  return (
    <div className={`chat-history-row${session.pinned ? ' is-pinned' : ''}${menuOpen ? ' is-menu-open' : ''}`} id={id}>
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
          <span className="chat-history-title">{session.title}</span>
          <span className="chat-history-meta">
            {session.state === 'pending' ? <i aria-hidden="true" /> : null}
            {session.meta}
          </span>
        </span>
      </button>
      <button
        type="button"
        className="chat-history-actions-trigger"
        aria-label={t('chat.history.actions')}
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={(event) => {
          event.stopPropagation()
          onMenu()
        }}
      >
        <Icon name="more" size={11} />
      </button>
      {menuOpen ? (
        <div className="chat-history-action-menu" role="menu" aria-label={t('chat.history.actions')}>
          {(['pin', 'rename', 'archive', 'fork', 'compact', 'delete'] as const).map((action) => (
            <button
              key={action}
              type="button"
              role="menuitem"
              className={action === 'delete' ? 'is-danger' : ''}
              disabled={!onAction}
              title={onAction ? t(`chat.history.${action}` as 'chat.history.pin') : t('chat.action.unavailable')}
              onClick={() => {
                if (!onAction) {
                  onCloseMenu()
                  onOpen()
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
                      : action === 'archive'
                        ? 'archive'
                        : action === 'fork'
                          ? 'branch'
                          : 'file'
                }
                size={11}
              />
              <span>{t(`chat.history.${action}` as 'chat.history.pin')}</span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}
