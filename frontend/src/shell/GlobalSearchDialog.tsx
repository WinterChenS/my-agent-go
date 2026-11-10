import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from 'react'
import { useNavigate } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import type { MessageKey } from '../i18n/catalogue'
import { SHELL_ROUTES } from './routes'

interface PaletteItem {
  id: string
  label: string
  sub: string
  kind: 'page' | 'action'
  path?: string
}

const ACTIONS: Array<{ label: MessageKey; sub: MessageKey }> = [
  { label: 'workbench.palette.action.newChat', sub: 'workbench.palette.action.newChatSub' },
  { label: 'workbench.palette.action.newSchedule', sub: 'workbench.palette.action.newScheduleSub' },
  { label: 'workbench.palette.action.addMcp', sub: 'workbench.palette.action.addMcpSub' },
  { label: 'workbench.palette.action.manageRuntime', sub: 'workbench.palette.action.manageRuntimeSub' },
  { label: 'workbench.palette.action.tokenAudit', sub: 'workbench.palette.action.tokenAuditSub' },
]

export default function GlobalSearchDialog({
  open,
  onClose,
  returnFocus,
}: {
  open: boolean
  onClose: () => void
  returnFocus: RefObject<HTMLButtonElement | null>
}) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)
  const dialogRef = useRef<HTMLDivElement>(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [notice, setNotice] = useState('')

  const items = useMemo(() => {
    const pages: PaletteItem[] = SHELL_ROUTES.map((route) => ({
      id: `page-${route.id}`,
      label: t(route.navKey),
      sub: route.id === 'schedules' ? t('workbench.palette.sub.automation') : t('workbench.palette.sub.page'),
      kind: 'page',
      path: route.path,
    }))
    const actions: PaletteItem[] = ACTIONS.map((action, index) => ({
      id: `action-${index}`,
      label: t(action.label),
      sub: t(action.sub),
      kind: 'action',
    }))
    return pages.concat(actions)
  }, [t])

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) return items
    return items.filter(
      (item) => item.label.toLowerCase().includes(needle) || item.sub.toLowerCase().includes(needle),
    )
  }, [items, query])

  useEffect(() => {
    if (!open) return
    setQuery('')
    setActive(0)
    setNotice('')
    const frame = window.requestAnimationFrame(() => inputRef.current?.focus())
    return () => window.cancelAnimationFrame(frame)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        close()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open])

  if (!open) return null

  const close = () => {
    onClose()
    window.setTimeout(() => returnFocus.current?.focus(), 0)
  }

  const activate = (item: PaletteItem | undefined) => {
    if (!item) return
    if (item.kind === 'action') {
      setNotice(t('workbench.palette.unavailable'))
      return
    }
    if (item.path) {
      navigate(item.path)
      close()
    }
  }

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (filtered.length > 0) setActive((current) => (current + 1) % filtered.length)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (filtered.length > 0) setActive((current) => (current - 1 + filtered.length) % filtered.length)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      activate(filtered[active])
    } else if (event.key === 'Tab') {
      event.preventDefault()
      inputRef.current?.focus()
    }
  }

  const listboxID = 'palette-listbox'

  return (
    <div className="wb-search-backdrop" onMouseDown={(event) => event.target === event.currentTarget && close()}>
      <div
        ref={dialogRef}
        className="wb-search-dialog palette-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={t('workbench.palette.label')}
        onKeyDown={onKeyDown}
      >
        <div className="wb-search-body">
          <input
            ref={inputRef}
            className="wb-search-input"
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value)
              setActive(0)
              setNotice('')
            }}
            placeholder={t('workbench.palette.placeholder')}
            aria-label={t('workbench.palette.placeholder')}
            role="combobox"
            aria-expanded="true"
            aria-controls={listboxID}
            aria-activedescendant={filtered[active] ? `palette-${filtered[active].id}` : undefined}
          />
          <div className="wb-palette-list" id={listboxID} role="listbox" aria-label={t('workbench.palette.label')}>
            {filtered.length === 0 ? <p className="wb-empty">{t('workbench.palette.empty')}</p> : null}
            {filtered.map((item, index) => (
              <div
                key={item.id}
                id={`palette-${item.id}`}
                role="option"
                aria-selected={active === index}
                data-kind={item.kind}
                className={`wb-palette-item${active === index ? ' is-active' : ''}`}
                onMouseEnter={() => setActive(index)}
                onClick={() => activate(item)}
              >
                <span>{item.label}</span>
                <small>{item.sub}</small>
              </div>
            ))}
          </div>
          {notice ? (
            <p className="wb-palette-notice" role="status">
              {notice}
            </p>
          ) : null}
        </div>
        <div className="wb-palette-foot">
          <span>
            <span className="kbd">↑</span> <span className="kbd">↓</span> {t('workbench.palette.hint.move')}
          </span>
          <span>
            <span className="kbd">↵</span> {t('workbench.palette.hint.open')}
          </span>
          <span>
            <span className="kbd">⌘</span> <span className="kbd">K</span> {t('workbench.palette.hint.summon')}
          </span>
          <span className="wb-palette-foot-spacer" />
          <span className="kbd">{t('workbench.palette.hint.escape')}</span>
        </div>
      </div>
    </div>
  )
}
