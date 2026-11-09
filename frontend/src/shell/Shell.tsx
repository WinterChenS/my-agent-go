import { useEffect } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import Sidebar from './Sidebar'
import Topbar from './Topbar'
import { resolveRoute, WORKBENCH_ROUTE } from './routes'
import { useHistoryShortcuts } from './useHistoryShortcuts'
import { useLayoutProbe } from './useLayoutProbe'

export default function Shell() {
  const location = useLocation()
  const route = resolveRoute(location.pathname) ?? WORKBENCH_ROUTE
  const { language, t } = useI18n()
  useLayoutProbe()
  useHistoryShortcuts()

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
        {route.chrome === 'shell' ? <Topbar route={route} /> : null}
        <main className="page" tabIndex={0} aria-label={t('main.content')}>
          <div className="page-inner">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
