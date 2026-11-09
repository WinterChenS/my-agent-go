import type { ShellRoute } from './routes'

export default function PagePlaceholder({
  route,
  fallbackPath,
}: {
  route: ShellRoute
  fallbackPath?: string
}) {
  return (
    <section
      id="route-page"
      className="placeholder"
      data-testid="page-placeholder"
      data-route={route.path}
      data-fallback-path={fallbackPath}
      data-route-status={fallbackPath ? 'fallback' : 'registered'}
    >
      {fallbackPath ? (
        <p className="route-status" role="status" data-route-status="fallback">
          未找到路由 {fallbackPath}
        </p>
      ) : null}
      <header className="page-head">
        <div className="page-head-copy">
          <h1>{route.title}</h1>
          <p className="page-desc">
            共用壳已锁定 <code>{route.path}</code> 路由。页面正文由 {route.futureTask} 按原型{' '}
            <code>{route.source}</code> 实现，R0-01 只交付壳层。
          </p>
        </div>
        <span className="badge">R0-01 · shell</span>
      </header>

      <div className="card">
        <div className="card-head">
          <h3>路由占位</h3>
          <span className="card-head-sub">{route.futureTask} 实现页面内容</span>
        </div>
        <div className="card-pad">
          <p>
            当前地址 <code>{route.path}</code> 已由共用壳承载；侧栏、顶栏、滚动容器、焦点与
            导航行为可用。
          </p>
          <p className="placeholder-note">业务内容、弹窗与数据状态不在本任务范围。</p>
          <p id="route-marker" className="placeholder-marker" data-testid="page-marker">
            {route.marker}
          </p>
        </div>
      </div>
    </section>
  )
}
