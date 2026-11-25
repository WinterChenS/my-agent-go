import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useSyncExternalStore, type RefObject } from 'react'
import ChatHistoryPanel from '../pages/chat/ChatHistoryPanel'
import { useChatSample } from '../pages/chat/sampleData'
import { useChatHistory } from '../chat/ChatHistoryProvider'
import { isTauriHost } from '../host/tauri'
import { subscribeUpdates, updateSnapshot } from '../updates/store'
import { useI18n } from '../i18n/LanguageProvider'
import Icon from './Icon'
import { PRIMARY_ROUTES } from './routes'
import ToolsMenu from './ToolsMenu'
import { SHELL_SAMPLE_FIXTURE, useShellSample } from './sampleNavigation'

/**
 * Messages kept verbatim when a session is compacted from the sidebar.
 *
 * The last exchange, not the last four: most conversations here are short, and
 * `keep_last = 4` on a four-message session is "nothing to compact" — the action
 * looked broken because there was nothing for it to do.
 */
const COMPACT_KEEP_LAST = 2

export default function Sidebar({
  onOpenSearch,
  searchButtonRef,
}: {
  onOpenSearch: () => void
  searchButtonRef: RefObject<HTMLButtonElement | null>
}) {
  const { t } = useI18n()
  const update = useSyncExternalStore(subscribeUpdates, updateSnapshot)
  const pendingUpdate = update.status === 'available' ? update.check?.available ?? null : null
  const location = useLocation()
  const navigate = useNavigate()
  const chatSample = useChatSample()
  const history = useChatHistory()
  const shellSample = useShellSample()
  const agentsSample =
    location.pathname === '/__sample/agents' || new URLSearchParams(location.search).get('sample') === 'agents'
  const skillsSample =
    location.pathname === '/__sample/skills' || new URLSearchParams(location.search).get('sample') === 'skills'
  const mcpSample =
    location.pathname === '/__sample/mcp' || new URLSearchParams(location.search).get('sample') === 'mcp'

  // The "对话" nav entry means "start a conversation", not "go back to the one
  // I left". Reopening the previous session made a click on a navigation item
  // feel like it did nothing when that session was already on screen; the
  // history list right below is where resuming belongs.
  //
  // No `navigate` here: the NavLink already pushes `/chat`, and adding a second
  // push put two entries in the history for one click (browser back then stayed
  // on the chat route).
  const onPrimaryNav = (routeId: string) => {
    if (routeId !== 'chat' || chatSample || shellSample) return
    history.startNew()
  }

  return (
    <aside className="sidebar" id="sidebar">
      <Link className="brand" to="/" aria-label="Anvil">
        <img className="brand-logo" src="/brand-logo.png" alt="Anvil" />
        <img className="brand-mark-img" src="/brand-mark.png" alt="" aria-hidden="true" />
        <span className="brand-sub">{t('brand.subtitle')}</span>
      </Link>

      {/* Search lives here now that the topbar is gone. It keeps the nav row's
          shape so it reads as part of the sidebar rather than a floating icon,
          and the shortcut is the fast path for anyone who never clicks it. */}
      <button
        type="button"
        id="sidebar-search"
        ref={searchButtonRef}
        className="nav-item sidebar-search"
        aria-label={t('topbar.search')}
        onClick={onOpenSearch}
      >
        <Icon name="search" size={15} />
        <span>{t('topbar.search')}</span>
        <kbd className="nav-kbd">{navigator.platform.toLowerCase().includes('mac') ? '⌘K' : 'Ctrl K'}</kbd>
      </button>

      <div className="nav-group">
        <div className="nav-label">{t('nav.group.start')}</div>
        {PRIMARY_ROUTES.map((route) => {
          const count = shellSample && route.id === 'chat' ? SHELL_SAMPLE_FIXTURE.chatCount : null
          return (
            <NavLink
              key={route.id}
              id={`nav-${route.id}`}
              to={route.path}
              onClick={() => onPrimaryNav(route.id)}
              className={({ isActive }) => `nav-item${isActive || (chatSample && route.id === 'chat') || (agentsSample && route.id === 'agents') || (skillsSample && route.id === 'skills') || (mcpSample && route.id === 'mcp') ? ' is-active' : ''}`}
              aria-label={count === null ? t(route.navKey) : t('sample.nav.chat', { count })}
              data-nav-entry
              data-route-path={route.path}
              data-group={route.group}
            >
              <Icon name={route.id} size={15} />
              <span>{t(route.navKey)}</span>
              {count === null ? null : (
                <span className="nav-count" data-testid="nav-count-chat">
                  {count}
                </span>
              )}
            </NavLink>
          )
        })}
      </div>

      <ToolsMenu sample={shellSample} />

      {/* The conversation list belongs to the window, not to one route. It used
          to render only while `/chat` was open, so every other page hid the
          history — and with it the only way back into a conversation. The rows
          are the same shared ledger; `open` navigates to the chat route. */}
      <ChatHistoryPanel
        sample={chatSample}
        live={
          chatSample || !isTauriHost()
            ? undefined
            : {
                sessions: history.sessions,
                projects: history.projects,
                activeId: history.activeId,
                runningId: history.runningId,
                onOpen: history.open,
                onNew: () => {
                  // A new conversation is a state of the chat page, so the
                  // shell asks for it the same way it asks for a session.
                  history.startNew()
                  if (location.pathname !== '/chat') navigate('/chat')
                },
                onRename: (id, title) => void history.rename(id, title),
                onPin: (id) => {
                  const row = history.sessions.find((session) => session.id === id)
                  void history.setPinned(id, !(row?.pinned ?? false))
                },
                onArchive: (id) => void history.setArchived(id, true),
                onDelete: (id) => void history.remove(id),
                onFork: (id) => void history.fork(id),
                onCompact: (id) => void history.compact(id, COMPACT_KEEP_LAST),
                onUnarchive: (id) => void history.setArchived(id, false),
                notice: history.notice,
                showArchived: history.showArchived,
                onToggleArchived: () => history.setShowArchived(!history.showArchived),
              }
        }
      />

      <div className="sidebar-foot is-collapsed">
        {/* The local-runtime / local-workspace footer cards are parked for now;
            keeping them in the DOM (visually hidden) preserves the history
            list's height and the shell contract tests. */}
        <div className="runtime-mini" aria-label={t('runtime.title')}>
          <span className="runtime-mini-icon" aria-hidden="true">
            π
          </span>
          <span>
            <b>{t('runtime.title')}</b>
            <small>
              <i aria-hidden="true" />
              {t('runtime.status.unknown')}
            </small>
          </span>
        </div>
        <div className="user-chip" aria-label={t('user.name')}>
          <span className="avatar" aria-hidden="true">
            A
          </span>
          <span className="user-chip-copy">
            <span className="user-chip-name">{t('user.name')}</span>
            <span className="user-chip-plan">{t('user.plan')}</span>
          </span>
        </div>
        {/* Discovered by the background check, so it is visible from wherever
            the reader is — the settings card is not a place anyone visits
            daily. */}
        {pendingUpdate ? (
          <button
            type="button"
            className="sidebar-update"
            data-testid="sidebar-update"
            onClick={() => navigate('/settings#general')}
          >
            <span className="sidebar-update-dot" aria-hidden="true" />
            <span>{t('settings.update.badge', { version: pendingUpdate.version })}</span>
          </button>
        ) : null}
        <span className="shell-build-marker" data-testid="build-marker">
          anvil-rebuild-shell
        </span>
      </div>
    </aside>
  )
}
