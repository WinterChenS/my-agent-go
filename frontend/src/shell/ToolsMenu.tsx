import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import Icon from './Icon'
import { TOOL_ROUTES } from './routes'
import type { MessageKey } from '../i18n/catalogue'
import { SHELL_SAMPLE_FIXTURE } from './sampleNavigation'

const TOOL_META_KEYS: Record<string, MessageKey | undefined> = {
  library: 'sample.nav.libraryMeta',
  skills: 'sample.nav.skillsMeta',
  mcp: 'sample.nav.mcpMeta',
  connectors: 'sample.nav.connectorsMeta',
  schedules: 'sample.nav.schedulesMeta',
}

export default function ToolsMenu({ sample = false }: { sample?: boolean }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const location = useLocation()

  useEffect(() => {
    setOpen(false)
  }, [location.pathname])

  useEffect(() => {
    if (!open) return
    const first = menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]')
    first?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (menuRef.current?.contains(target) || triggerRef.current?.contains(target)) return
      setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  const closeAndRestoreFocus = () => {
    setOpen(false)
    triggerRef.current?.focus()
  }

  const onMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      closeAndRestoreFocus()
      return
    }
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])
    const index = items.indexOf(document.activeElement as HTMLElement)
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      items[(index + 1) % items.length]?.focus()
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      items[(index - 1 + items.length) % items.length]?.focus()
    } else if (event.key === 'Home') {
      event.preventDefault()
      items[0]?.focus()
    } else if (event.key === 'End') {
      event.preventDefault()
      items[items.length - 1]?.focus()
    }
  }

  return (
    <div className={`nav-compact${open ? ' is-open' : ''}`}>
      <button
        ref={triggerRef}
        id="nav-tools-trigger"
        type="button"
        className="nav-item nav-compact-trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls="tools-menu"
        aria-label={t('nav.tools')}
        onClick={() => setOpen((value) => !value)}
      >
        <Icon name="layers" size={15} />
        <span>{t('nav.tools')}</span>
        <Icon name="chevDown" size={12} />
      </button>
      <div
        ref={menuRef}
        id="tools-menu"
        className="nav-flyout"
        role="menu"
        aria-label={t('nav.tools')}
        hidden={!open}
        onKeyDown={onMenuKeyDown}
      >
        <div className="nav-flyout-head">{t('nav.tools')}</div>
        {TOOL_ROUTES.map((route) => {
          const count = sample ? SHELL_SAMPLE_FIXTURE.toolCounts[route.id] : undefined
          const metaKey = TOOL_META_KEYS[route.id]
          const meta = count === undefined || !metaKey ? null : t(metaKey, { count })
          return (
            <NavLink
              key={route.id}
              id={`nav-${route.id}`}
              to={route.path}
              role="menuitem"
              className={({ isActive }) => `nav-flyout-item${isActive ? ' is-active' : ''}`}
              aria-label={meta === null ? t(route.navKey) : `${t(route.navKey)} · ${meta}`}
              tabIndex={open ? 0 : -1}
              data-nav-entry
              data-route-path={route.path}
              data-group={route.group}
              onClick={() => setOpen(false)}
              onKeyDown={(event) => {
                if (event.key === ' ') {
                  event.preventDefault()
                  event.currentTarget.click()
                }
              }}
            >
              <span className="nav-flyout-icon">
                <Icon name={route.id} size={14} />
              </span>
              <span>
                <b>{t(route.navKey)}</b>
                {meta === null ? null : (
                  <small data-testid={`nav-tool-meta-${route.id}`}>{meta}</small>
                )}
              </span>
            </NavLink>
          )
        })}
      </div>
    </div>
  )
}
