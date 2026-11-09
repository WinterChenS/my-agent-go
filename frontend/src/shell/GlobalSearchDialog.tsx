import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import { sampleWorkbench } from '../pages/workbench/sampleData'
import { SHELL_ROUTES } from './routes'

interface SearchEntry {
  id: string
  label: string
  sub: string
  group: 'pages' | 'agents' | 'artifacts'
  path?: string
}

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
  const location = useLocation()
  const inputRef = useRef<HTMLInputElement>(null)
  const dialogRef = useRef<HTMLDivElement>(null)
  const [query, setQuery] = useState('')
  const sample = new URLSearchParams(location.search).get('sample') === 'workbench'

  const entries = useMemo(() => {
    const pages: SearchEntry[] = SHELL_ROUTES.map((route) => ({
      id: `page-${route.id}`,
      label: t(route.navKey),
      sub: route.path,
      group: 'pages',
      path: route.path,
    }))
    if (!sample) return pages
    return pages.concat(
      sampleWorkbench.agents.map((agent) => ({
        id: `agent-${agent.name}`,
        label: agent.name,
        sub: agent.meta,
        group: 'agents' as const,
      })),
      sampleWorkbench.artifacts.map((artifact) => ({
        id: `artifact-${artifact.name}`,
        label: artifact.name,
        sub: artifact.meta,
        group: 'artifacts' as const,
      })),
    )
  }, [sample, t])

  const filtered = entries.filter((entry) => {
    if (!query.trim()) return true
    const needle = query.trim().toLowerCase()
    return entry.label.toLowerCase().includes(needle) || entry.sub.toLowerCase().includes(needle)
  })

  useEffect(() => {
    if (!open) return
    setQuery('')
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

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'Tab') return
    const focusables = Array.from(
      dialogRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input, [href], [tabindex]:not([tabindex="-1"])',
      ) ?? [],
    )
    if (focusables.length === 0) return
    const first = focusables[0]
    const last = focusables[focusables.length - 1]
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  const groups: Array<{ key: 'pages' | 'agents' | 'artifacts'; labelKey: 'workbench.search.pages' | 'workbench.search.agents' | 'workbench.search.artifacts' }> = [
    { key: 'pages', labelKey: 'workbench.search.pages' },
    { key: 'agents', labelKey: 'workbench.search.agents' },
    { key: 'artifacts', labelKey: 'workbench.search.artifacts' },
  ]

  return (
    <div className="wb-search-backdrop" onMouseDown={(event) => event.target === event.currentTarget && close()}>
      <div
        ref={dialogRef}
        className="wb-search-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="global-search-title"
        onKeyDown={onKeyDown}
      >
        <div className="wb-search-head">
          <div>
            <h2 id="global-search-title">{t('workbench.search.title')}</h2>
            <p>{t('workbench.search.subtitle')}</p>
          </div>
          <button type="button" className="iconbtn" aria-label={t('workbench.search.close')} onClick={close}>
            ×
          </button>
        </div>
        <div className="wb-search-body">
          <input
            ref={inputRef}
            className="wb-search-input"
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t('workbench.search.placeholder')}
            aria-label={t('workbench.search.placeholder')}
          />
          <p className="wb-search-hint">{t('workbench.search.hint', { shortcut: 'Ctrl/⌘ K' })}</p>
          <div className="wb-search-results">
            {filtered.length === 0 ? <p className="wb-empty">{t('workbench.search.empty')}</p> : null}
            {groups.map((group) => {
              const groupEntries = filtered.filter((entry) => entry.group === group.key)
              if (groupEntries.length === 0) return null
              return (
                <div className="wb-search-group" key={group.key}>
                  <div className="wb-search-group-label">{t(group.labelKey)}</div>
                  {groupEntries.map((entry) => (
                    <button
                      type="button"
                      className="wb-search-result"
                      key={entry.id}
                      onClick={() => {
                        if (entry.path) navigate(entry.path)
                        close()
                      }}
                    >
                      <span>{entry.label}</span>
                      <small>{entry.sub}</small>
                    </button>
                  ))}
                </div>
              )
            })}
          </div>
        </div>
        <div className="wb-search-foot">
          <button type="button" className="wb-search-close" onClick={close}>
            {t('workbench.search.close')}
          </button>
        </div>
      </div>
    </div>
  )
}
