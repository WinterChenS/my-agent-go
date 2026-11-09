import { useI18n } from '../i18n/LanguageProvider'
import type { ShellRoute } from './routes'

export default function PagePlaceholder({
  route,
  fallbackPath,
}: {
  route: ShellRoute
  fallbackPath?: string
}) {
  const { t } = useI18n()
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
          {t('placeholder.fallback', { path: fallbackPath })}
        </p>
      ) : null}
      <header className="page-head">
        <div className="page-head-copy">
          <h1>{t(route.titleKey)}</h1>
          <p className="page-desc">
            {t('placeholder.description', {
              path: route.path,
              task: route.futureTask,
              source: route.source,
            })}
          </p>
        </div>
        <span className="badge">{t('placeholder.badge')}</span>
      </header>

      <div className="card">
        <div className="card-head">
          <h3>{t('placeholder.card.title')}</h3>
          <span className="card-head-sub">{t('placeholder.card.sub', { task: route.futureTask })}</span>
        </div>
        <div className="card-pad">
          <p>{t('placeholder.body', { path: route.path })}</p>
          <p className="placeholder-note">{t('placeholder.note')}</p>
          <p id="route-marker" className="placeholder-marker" data-testid="page-marker">
            {route.marker}
          </p>
        </div>
      </div>
    </section>
  )
}
