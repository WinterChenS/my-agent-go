import { useState } from 'react'
import { useI18n } from '../i18n/LanguageProvider'
import Icon from './Icon'
import type { ShellRoute } from './routes'

export default function Topbar({ route }: { route: ShellRoute }) {
  const { t } = useI18n()
  const [notice, setNotice] = useState('')
  const has = (action: string) => route.topbarActions.includes(action)

  return (
    <header className="topbar">
      <div
        className="topbar-crumb"
        id="topbar-crumb"
        aria-label={`${t(route.breadcrumbKey)} / ${t(route.titleKey)}`}
      >
        <span>{t(route.breadcrumbKey)}</span>
        <span className="sep" aria-hidden="true">
          /
        </span>
        <span className="topbar-title">{t(route.titleKey)}</span>
      </div>
      <div className="topbar-spacer" />
      {has('status') && route.topbarStatusKey ? (
        <span className="badge badge-warn" data-runtime-state="warn">
          {t(route.topbarStatusKey)}
        </span>
      ) : null}
      <span className="topbar-status" role={notice ? 'status' : undefined} aria-live="polite">
        {notice}
      </span>
      {has('search') ? (
        <button
          type="button"
          id="topbar-search"
          className="iconbtn"
          aria-label={t('topbar.search')}
          onClick={() => setNotice(t('topbar.notice.search'))}
        >
          <Icon name="search" size={16} />
        </button>
      ) : null}
      {has('help') ? (
        <button
          type="button"
          id="topbar-help"
          className="iconbtn"
          aria-label={t('topbar.help')}
          onClick={() => setNotice(t('topbar.notice.help'))}
        >
          <Icon name="help" size={16} />
        </button>
      ) : null}
    </header>
  )
}
