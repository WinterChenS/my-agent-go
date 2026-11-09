import { useState } from 'react'
import Icon from './Icon'
import type { ShellRoute } from './routes'

export default function Topbar({ route }: { route: ShellRoute }) {
  const [notice, setNotice] = useState('')
  const has = (action: string) => route.topbarActions.includes(action)

  return (
    <header className="topbar">
      <div className="topbar-crumb" id="topbar-crumb" aria-label={`${route.breadcrumbParent} / ${route.title}`}>
        <span>{route.breadcrumbParent}</span>
        <span className="sep" aria-hidden="true">
          /
        </span>
        <span className="topbar-title">{route.title}</span>
      </div>
      <div className="topbar-spacer" />
      {has('status') && route.topbarStatus ? (
        <span
          className={`badge ${route.topbarStatus.tone === 'success' ? 'badge-success' : 'badge-warn'}`}
          data-runtime-state={route.topbarStatus.tone}
        >
          {route.topbarStatus.label}
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
          aria-label="搜索"
          onClick={() => setNotice('搜索将在后续任务接入')}
        >
          <Icon name="search" size={16} />
        </button>
      ) : null}
      {has('help') ? (
        <button
          type="button"
          id="topbar-help"
          className="iconbtn"
          aria-label="帮助"
          onClick={() => setNotice('帮助将在后续任务接入')}
        >
          <Icon name="help" size={16} />
        </button>
      ) : null}
    </header>
  )
}
