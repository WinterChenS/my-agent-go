import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import Icon from './Icon'
import { TOOL_ROUTES } from './routes'

export default function ToolsMenu() {
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
        aria-label="工具与自动化"
        onClick={() => setOpen((value) => !value)}
      >
        <Icon name="layers" size={15} />
        <span>工具与自动化</span>
        <Icon name="chevDown" size={12} />
      </button>
      <div
        ref={menuRef}
        id="tools-menu"
        className="nav-flyout"
        role="menu"
        aria-label="工具与自动化"
        hidden={!open}
        onKeyDown={onMenuKeyDown}
      >
        <div className="nav-flyout-head">工具与自动化</div>
        {TOOL_ROUTES.map((route) => (
          <NavLink
            key={route.id}
            id={`nav-${route.id}`}
            to={route.path}
            role="menuitem"
            className={({ isActive }) => `nav-flyout-item${isActive ? ' is-active' : ''}`}
            aria-label={route.navLabel}
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
              <b>{route.navLabel}</b>
              {route.navMeta ? <small>{route.navMeta}</small> : null}
            </span>
          </NavLink>
        ))}
      </div>
    </div>
  )
}
