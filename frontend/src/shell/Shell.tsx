import { useEffect, useRef, useState } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import GlobalSearchDialog from './GlobalSearchDialog'
import Sidebar from './Sidebar'
import Topbar from './Topbar'
import { resolveRoute, ROUTES_BY_ID, WORKBENCH_ROUTE } from './routes'
import { useHistoryShortcuts } from './useHistoryShortcuts'
import { useLayoutProbe } from './useLayoutProbe'

export default function Shell() {
  const location = useLocation()
  const chatSample =
    location.pathname === '/__sample/chat' ||
    (location.pathname === '/' && new URLSearchParams(location.search).get('sample') === 'chat')
  const agentsSample =
    location.pathname === '/__sample/agents' ||
    (location.pathname === '/' && new URLSearchParams(location.search).get('sample') === 'agents')
  const route = (chatSample
    ? ROUTES_BY_ID.get('chat')
    : agentsSample
      ? ROUTES_BY_ID.get('agents')
      : resolveRoute(location.pathname)) ?? WORKBENCH_ROUTE
  const { language, t } = useI18n()
  const searchButtonRef = useRef<HTMLButtonElement>(null)
  const [searchOpen, setSearchOpen] = useState(false)
  useLayoutProbe()
  useHistoryShortcuts()

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
    void fetch('/__anvil/log', {
      method: 'POST',
      body: `i18n-title: title=${document.title}`,
    }).catch(() => {})
  }, [language, route.titleKey, t])

  return (
    <div className="shell" data-shell>
      <Sidebar />
      <div className="main">
        {route.chrome === 'shell' ? (
          <Topbar route={route} onOpenSearch={() => setSearchOpen(true)} searchButtonRef={searchButtonRef} />
        ) : null}
        <main className={`page${route.chrome === 'chat' ? ' page-chat' : ''}`} tabIndex={0} aria-label={t('main.content')}>
          <div className="page-inner">
            <Outlet />
          </div>
        </main>
      </div>
      <GlobalSearchDialog open={searchOpen} onClose={() => setSearchOpen(false)} returnFocus={searchButtonRef} />
    </div>
  )
}
