import { Outlet, useLocation } from 'react-router-dom'
import Sidebar from './Sidebar'
import Topbar from './Topbar'
import { resolveRoute, WORKBENCH_ROUTE } from './routes'
import { useLayoutProbe } from './useLayoutProbe'

export default function Shell() {
  const location = useLocation()
  const route = resolveRoute(location.pathname) ?? WORKBENCH_ROUTE
  useLayoutProbe()

  return (
    <div className="shell" data-shell>
      <Sidebar />
      <div className="main">
        <Topbar route={route} />
        <main className="page" tabIndex={0} aria-label="主内容">
          <div className="page-inner">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
