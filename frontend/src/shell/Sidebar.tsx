import { Link, NavLink, useLocation } from 'react-router-dom'
import ChatHistoryPanel from '../pages/chat/ChatHistoryPanel'
import { useChatSample } from '../pages/chat/sampleData'
import { useI18n } from '../i18n/LanguageProvider'
import Icon from './Icon'
import { PRIMARY_ROUTES } from './routes'
import ToolsMenu from './ToolsMenu'

export default function Sidebar() {
  const { t } = useI18n()
  const location = useLocation()
  const chatSample = useChatSample()
  const isChat = location.pathname === '/chat' || location.pathname === '/__sample/chat' || chatSample
  return (
    <aside className="sidebar" id="sidebar">
      <Link className="brand" to="/" aria-label="Anvil">
        <span className="brand-mark">
          <Icon name="layers" size={15} />
        </span>
        <span className="brand-copy">
          <span className="brand-name">Anvil</span>
          <span className="brand-sub">{t('brand.subtitle')}</span>
        </span>
      </Link>

      <div className="nav-group">
        <div className="nav-label">{t('nav.group.start')}</div>
        {PRIMARY_ROUTES.map((route) => (
          <NavLink
            key={route.id}
            id={`nav-${route.id}`}
            to={route.path}
            className={({ isActive }) => `nav-item${isActive || (chatSample && route.id === 'chat') ? ' is-active' : ''}`}
            aria-label={t(route.navKey)}
            data-nav-entry
            data-route-path={route.path}
            data-group={route.group}
          >
            <Icon name={route.id} size={15} />
            <span>{t(route.navKey)}</span>
          </NavLink>
        ))}
      </div>

      <ToolsMenu />

      {isChat ? <ChatHistoryPanel sample={chatSample} /> : null}

      <div className="sidebar-foot">
        <div className="runtime-mini" aria-label={t('runtime.title')}>
          <span className="runtime-mini-icon" aria-hidden="true">
            π
          </span>
          <span>
            <b>{t('runtime.title')}</b>
            <small>
              <i aria-hidden="true" />
              {t('runtime.status.unknown')}
            </small>
          </span>
        </div>
        <div className="user-chip" aria-label={t('user.name')}>
          <span className="avatar" aria-hidden="true">
            A
          </span>
          <span className="user-chip-copy">
            <span className="user-chip-name">{t('user.name')}</span>
            <span className="user-chip-plan">{t('user.plan')}</span>
          </span>
        </div>
        <span className="shell-build-marker" data-testid="build-marker">
          anvil-rebuild-shell
        </span>
      </div>
    </aside>
  )
}
