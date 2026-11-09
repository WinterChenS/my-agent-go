import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import Icon from './Icon'
import type { ShellRoute } from './routes'

export default function Topbar({ route }: { route: ShellRoute }) {
  const navigate = useNavigate()
  const location = useLocation()
  const [depth, setDepth] = useState(0)
  const [notice, setNotice] = useState('')

  useEffect(() => {
    setDepth((value) => value + 1)
  }, [location.key])

  const canGoBack = depth > 1

  return (
    <header className="topbar">
      <button
        type="button"
        id="topbar-back"
        className="iconbtn"
        aria-label="返回上一页"
        disabled={!canGoBack}
        onClick={() => navigate(-1)}
      >
        <Icon name="arrowLeft" size={16} />
      </button>
      <div className="topbar-crumb">
        <span>工作区</span>
        <span className="sep" aria-hidden="true">
          /
        </span>
        <span className="topbar-title">{route.title}</span>
      </div>
      <div className="topbar-spacer" />
      <span className="topbar-status" role={notice ? 'status' : undefined} aria-live="polite">
        {notice}
      </span>
      <button
        type="button"
        id="topbar-search"
        className="iconbtn"
        aria-label="搜索"
        onClick={() => setNotice('搜索将在后续任务接入')}
      >
        <Icon name="search" size={16} />
      </button>
      <button
        type="button"
        id="topbar-help"
        className="iconbtn"
        aria-label="帮助"
        onClick={() => setNotice('帮助将在后续任务接入')}
      >
        <Icon name="help" size={16} />
      </button>
    </header>
  )
}
