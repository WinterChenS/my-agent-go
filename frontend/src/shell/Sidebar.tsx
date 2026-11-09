import { Link, NavLink } from 'react-router-dom'
import Icon from './Icon'
import { PRIMARY_ROUTES } from './routes'
import ToolsMenu from './ToolsMenu'

export default function Sidebar() {
  return (
    <aside className="sidebar" id="sidebar">
      <Link className="brand" to="/" aria-label="Anvil">
        <span className="brand-mark">
          <Icon name="layers" size={15} />
        </span>
        <span className="brand-copy">
          <span className="brand-name">Anvil</span>
          <span className="brand-sub">本地智能体客户端 · v0.1</span>
        </span>
      </Link>

      <div className="nav-group">
        <div className="nav-label">开始</div>
        {PRIMARY_ROUTES.map((route) => (
          <NavLink
            key={route.id}
            id={`nav-${route.id}`}
            to={route.path}
            className={({ isActive }) => `nav-item${isActive ? ' is-active' : ''}`}
            aria-label={route.navLabel}
            data-nav-entry
            data-route-path={route.path}
            data-group={route.group}
          >
            <Icon name={route.id} size={15} />
            <span>{route.navLabel}</span>
            {route.navCount ? <span className="nav-count">{route.navCount}</span> : null}
          </NavLink>
        ))}
      </div>

      <ToolsMenu />

      <div className="sidebar-foot">
        <div className="runtime-mini" aria-label="运行时状态">
          <span className="runtime-mini-icon" aria-hidden="true">
            π
          </span>
          <span>
            <b>Pi Agent</b>
            <small>
              <i aria-hidden="true" />
              本机运行中
            </small>
          </span>
        </div>
        <div className="user-chip" aria-label="本地工作区">
          <span className="avatar" aria-hidden="true">
            陈
          </span>
          <span className="user-chip-copy">
            <span className="user-chip-name">陈羽</span>
            <span className="user-chip-plan">个人 · 本地工作区</span>
          </span>
        </div>
        <span className="shell-build-marker" data-testid="build-marker">
          anvil-rebuild-shell
        </span>
      </div>
    </aside>
  )
}
