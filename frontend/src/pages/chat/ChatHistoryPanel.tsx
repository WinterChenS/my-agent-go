import { useMemo, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { sampleChat } from './sampleData'

type Session = { id: string; title: string; meta: string; state?: 'pending'; pinned?: boolean }

export default function ChatHistoryPanel({
  sample,
  variant = 'sidebar',
  onClose,
}: {
  sample: boolean
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
  const noResults = normalized && visibleProjects.length === 0 && visiblePersonal.length === 0
  const allLoaded = visible >= personal.length

  const toggleProject = (id: string) => {
    if (normalized) return
    setProjects((current) => current.map((project) => (project.id === id ? { ...project, open: !project.open } : project)))
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
            aria-label={t('chat.history.new')}
            title={sample ? t('chat.action.unavailable') : t('chat.action.unavailable')}
            disabled={!sample}
            onClick={() => setNotice(t('chat.notice.history'))}
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
        <div className="chat-history-label">{t('chat.history.projects')}</div>
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
        <div className="chat-history-list">
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

        {noResults || (!sample && !normalized) ? (
          <div className="chat-history-empty" data-testid="chat-history-empty">
            {noResults ? t('chat.history.noMatches') : t('chat.history.empty')}
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
}: {
  session: Session
  active: boolean
  menuOpen: boolean
  onOpen: () => void
  onMenu: () => void
  onCloseMenu: () => void
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
          {(['pin', 'rename', 'archive', 'delete'] as const).map((action) => (
            <button
              key={action}
              type="button"
              role="menuitem"
              className={action === 'delete' ? 'is-danger' : ''}
              disabled
              title={t('chat.action.unavailable')}
              onClick={() => {
                onCloseMenu()
                onOpen()
              }}
            >
              <Icon name={action === 'pin' ? 'pin' : action === 'delete' ? 'trash' : action === 'archive' ? 'archive' : 'file'} size={11} />
              <span>{t(`chat.history.${action}` as 'chat.history.pin')}</span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}
