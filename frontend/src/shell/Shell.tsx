import { useEffect, useRef, useState } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import { ChatHistoryProvider } from '../chat/ChatHistoryProvider'
import { WorkspaceProvider } from '../workspace/WorkspaceProvider'
import GlobalSearchDialog from './GlobalSearchDialog'
import Sidebar from './Sidebar'
import WindowChrome from './WindowChrome'
import { resolveRoute, ROUTES_BY_ID, WORKBENCH_ROUTE } from './routes'
import { useContextMenuGuard } from './useContextMenuGuard'
import { useHistoryShortcuts } from './useHistoryShortcuts'
import { useLayoutProbe } from './useLayoutProbe'
import { reportHostLog } from '../host/tauri'
import { startUpdateWatch } from '../updates/store'

export default function Shell() {
  const location = useLocation()
  const chatSample =
    location.pathname === '/__sample/chat' ||
    (location.pathname === '/' && new URLSearchParams(location.search).get('sample') === 'chat')
  const agentsSample =
    location.pathname === '/__sample/agents' ||
    (location.pathname === '/' && new URLSearchParams(location.search).get('sample') === 'agents')
  const skillsSample =
    location.pathname === '/__sample/skills' ||
    (location.pathname === '/' && new URLSearchParams(location.search).get('sample') === 'skills')
  const mcpSample =
    location.pathname === '/__sample/mcp' ||
    (location.pathname === '/' && new URLSearchParams(location.search).get('sample') === 'mcp')
  const route = (chatSample
    ? ROUTES_BY_ID.get('chat')
    : agentsSample
      ? ROUTES_BY_ID.get('agents')
      : skillsSample
        ? ROUTES_BY_ID.get('skills')
        : mcpSample
          ? ROUTES_BY_ID.get('mcp')
          : resolveRoute(location.pathname)) ?? WORKBENCH_ROUTE
  const { language, t } = useI18n()
  const searchButtonRef = useRef<HTMLButtonElement>(null)
  const [searchOpen, setSearchOpen] = useState(false)
  useLayoutProbe()
  useHistoryShortcuts()
  useContextMenuGuard()

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setSearchOpen(true)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  useEffect(() => {
    document.title = t('document.titleFormat', { title: t(route.titleKey) })
    reportHostLog(`i18n-title: title=${document.title}`)
  }, [language, route.titleKey, t])

  // One background check per launch, a few seconds in: the first seconds belong
  // to the window and the session list. The fixture pages render without a host
  // and must not reach for one.
  const fixturePage =
    location.pathname.startsWith('/__sample') ||
    new URLSearchParams(location.search).get('sample') !== null
  useEffect(() => {
    if (fixturePage) return
    startUpdateWatch()
  }, [fixturePage])

  return (
    <ChatHistoryProvider>
      <WorkspaceProvider>
        <div className="shell" data-shell>
          <WindowChrome />
          <Sidebar onOpenSearch={() => setSearchOpen(true)} searchButtonRef={searchButtonRef} />
          <div className="main">
            {/* No topbar: every page already opens with its own h1, so a strip
                repeating the title, a breadcrumb and two icons only pushed the
                content down. Search moved into the sidebar; Ctrl/Cmd+K still
                opens it from anywhere. */}
            <main className={`page${route.chrome === 'chat' ? ' page-chat' : ''}`} tabIndex={0} aria-label={t('main.content')}>
              <div className="page-inner">
                <Outlet />
              </div>
            </main>
          </div>
          <GlobalSearchDialog open={searchOpen} onClose={() => setSearchOpen(false)} returnFocus={searchButtonRef} />
        </div>
      </WorkspaceProvider>
    </ChatHistoryProvider>
  )
}
