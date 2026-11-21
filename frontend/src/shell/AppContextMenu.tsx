import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useI18n } from '../i18n/LanguageProvider'
import type { MessageKey } from '../i18n/catalogue'
import { readClipboard, writeClipboard } from './clipboard'
import './appContextMenu.css'

type Action = 'cut' | 'copy' | 'paste' | 'selectAll' | 'copyLink' | 'copyMessage' | 'sessionActions' | 'openSession' | 'newChat' | 'search'
type Glyph = Action | 'divider'

interface MenuItem {
  glyph: Glyph
  label: MessageKey
  action?: Action
  value?: string
  field?: HTMLElement
}

interface OpenMenu {
  x: number
  y: number
  items: MenuItem[]
}

const GLYPHS: Record<Action, React.ReactNode> = {
  cut: (
    <>
      <circle cx="6" cy="17" r="2.2" />
      <circle cx="18" cy="17" r="2.2" />
      <path d="M8 15 18 5M16 5h4v4M7.5 4.5l12 15" />
    </>
  ),
  copy: (
    <>
      <rect x="8" y="8" width="12" height="12" rx="2" />
      <path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" />
    </>
  ),
  paste: (
    <>
      <rect x="5" y="5" width="14" height="16" rx="2" />
      <path d="M9 5V3h6v2M9 12h6M9 16h6" />
    </>
  ),
  selectAll: <path d="M4 7h16M4 12h16M4 17h16" />,
  copyLink: <path d="M10 14a4 4 0 0 0 6 .4l2-2a4 4 0 0 0-5.7-5.7L11 8M14 10a4 4 0 0 0-6-.4l-2 2A4 4 0 0 0 11.7 17L13 16" />,
  copyMessage: (
    <>
      <rect x="4" y="4" width="16" height="16" rx="2" />
      <path d="M8 9h8M8 13h8M8 17h5" />
    </>
  ),
  sessionActions: (
    <>
      <circle cx="5" cy="12" r="1.4" />
      <circle cx="12" cy="12" r="1.4" />
      <circle cx="19" cy="12" r="1.4" />
    </>
  ),
  openSession: <path d="M14 4h6v6M20 4l-9 9M18 13v6H5V6h6" />,
  newChat: <path d="M12 5v14M5 12h14" />,
  search: (
    <>
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="m15.5 15.5 5 5" />
    </>
  ),
}

function selectedText(): string {
  return String(document.getSelection()?.toString() ?? '').trim()
}

function asElement(target: EventTarget | null | undefined): HTMLElement | null {
  const node = target as Node | null
  if (!node) return null
  const element = node.nodeType === 1 ? (node as HTMLElement) : node.parentElement
  return element
}

function isEditable(node: HTMLElement | null | undefined): node is HTMLElement {
  if (!node) return false
  const tag = node.tagName.toLowerCase()
  return (
    tag === 'input' ||
    tag === 'textarea' ||
    node.isContentEditable ||
    node.getAttribute('contenteditable') === 'true'
  )
}

function selectionOf(field: HTMLElement): { start: number; end: number; text: string } {
  if (field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement) {
    const start = field.selectionStart ?? field.value.length
    const end = field.selectionEnd ?? start
    return { start, end, text: field.value.slice(start, end) }
  }
  return { start: 0, end: 0, text: selectedText() }
}

function replaceField(field: HTMLElement, value: string, start: number, end: number) {
  if (field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement) {
    field.setRangeText(value, start, end, 'end')
    field.dispatchEvent(new Event('input', { bubbles: true }))
    field.dispatchEvent(new Event('change', { bubbles: true }))
    return
  }
  field.focus()
  document.execCommand?.('insertText', false, value)
}

/**
 * The app's own context menu. WebView2's native menu offers copy/paste on a
 * page the reader is using as an application surface, with no knowledge of
 * links, fields or the app's theme. This replaces it everywhere in the main
 * window and only lists commands that are meaningful where the click landed.
 */
export default function AppContextMenu() {
  const { t } = useI18n()
  const location = useLocation()
  const [menu, setMenu] = useState<OpenMenu | null>(null)
  const [position, setPosition] = useState({ left: 0, top: 0 })
  const [toast, setToast] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const toastTimer = useRef<number | null>(null)

  useEffect(() => () => {
    if (toastTimer.current !== null) window.clearTimeout(toastTimer.current)
  }, [])

  useEffect(() => {
    const onContextMenu = (event: MouseEvent) => {
      event.preventDefault()
      event.stopPropagation()
      const target = asElement(event.target)
      const link = target?.closest<HTMLAnchorElement>('a[href]') ?? undefined
      const field = target?.closest<HTMLElement>(
        'input,textarea,[contenteditable="true"],[role="textbox"]',
      ) ?? undefined
      const chosen = selectedText()
      const message = target?.closest<HTMLElement>('[data-testid="chat-message-user"],[data-testid="chat-message-agent"]')
      const historyRow = target?.closest<HTMLElement>('.chat-history-row')
      const items: MenuItem[] = []

      if (isEditable(field)) {
        if (chosen || selectionOf(field).text) {
          items.push({ glyph: 'cut', label: 'app.context.cut', action: 'cut', field })
          items.push({ glyph: 'copy', label: 'app.context.copy', action: 'copy', field })
        }
        items.push({ glyph: 'paste', label: 'app.context.paste', action: 'paste', field })
        items.push({ glyph: 'selectAll', label: 'app.context.selectAll', action: 'selectAll', field })
      } else if (chosen) {
        items.push({ glyph: 'copy', label: 'app.context.copy', action: 'copy', value: chosen })
      }

      if (message) {
        const body = message.querySelector<HTMLElement>('.chat-bubble, .chat-body-with-copy')
        items.push({
          glyph: 'copyMessage',
          label: 'app.context.copyMessage',
          action: 'copyMessage',
          value: (body?.innerText || body?.textContent || message.innerText || '').trim(),
        })
      }

      if (historyRow) {
        items.push({ glyph: 'openSession', label: 'app.context.openSession', action: 'openSession', field: historyRow })
        items.push({ glyph: 'sessionActions', label: 'app.context.sessionActions', action: 'sessionActions', field: historyRow })
      }

      if (link) {
        if (items.length) items.push({ glyph: 'divider', label: 'app.context.copyLink' })
        items.push({ glyph: 'copyLink', label: 'app.context.copyLink', action: 'copyLink', value: link.href })
      }

      // No browser navigation here: this is an application surface. When
      // nothing else applies, offer the two commands the shell always has.
      if (!items.length) {
        items.push({ glyph: 'newChat', label: 'app.context.newChat', action: 'newChat' })
        items.push({ glyph: 'search', label: 'app.context.search', action: 'search' })
      }

      setMenu({ x: event.clientX, y: event.clientY, items })
    }
    document.addEventListener('contextmenu', onContextMenu, true)
    return () => document.removeEventListener('contextmenu', onContextMenu, true)
  }, [])

  useEffect(() => {
    setMenu(null)
  }, [location.pathname])

  useLayoutEffect(() => {
    if (!menu || !ref.current) return
    const box = ref.current.getBoundingClientRect()
    setPosition({
      left: Math.max(8, Math.min(menu.x, window.innerWidth - box.width - 8)),
      top: Math.max(8, Math.min(menu.y, window.innerHeight - box.height - 8)),
    })
  }, [menu])

  useEffect(() => {
    if (!menu) return
    const dismiss = (event: Event) => {
      if (!ref.current?.contains(event.target as Node)) setMenu(null)
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenu(null)
    }
    document.addEventListener('pointerdown', dismiss, true)
    document.addEventListener('keydown', escape, true)
    window.addEventListener('scroll', dismiss, true)
    window.addEventListener('resize', dismiss)
    return () => {
      document.removeEventListener('pointerdown', dismiss, true)
      document.removeEventListener('keydown', escape, true)
      window.removeEventListener('scroll', dismiss, true)
      window.removeEventListener('resize', dismiss)
    }
  }, [menu])

  const announce = (message: string) => {
    setToast(message)
    if (toastTimer.current !== null) window.clearTimeout(toastTimer.current)
    toastTimer.current = window.setTimeout(() => setToast(''), 1800)
  }

  const run = async (item: MenuItem) => {
    setMenu(null)
    if (!item.action) return
    try {
      if (item.action === 'newChat') {
        document.querySelector<HTMLElement>('[data-testid="chat-history-new"]')?.click()
        return
      }
      if (item.action === 'search') {
        window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }))
        return
      }
      if (item.action === 'openSession') {
        item.field?.querySelector<HTMLElement>('.chat-history-item')?.click()
        return
      }
      if (item.action === 'sessionActions') {
        item.field?.querySelector<HTMLElement>('[data-testid^="chat-history-actions-"]')?.click()
        return
      }
      if (item.action === 'copyMessage') {
        if (!(await writeClipboard(item.value ?? ''))) throw new Error('clipboard write failed')
        announce(t('app.context.copied'))
        return
      }
      if (item.action === 'copyLink' || item.action === 'copy') {
        const value = item.value ?? (item.field ? selectionOf(item.field).text : '')
        if (!(await writeClipboard(value))) throw new Error('clipboard write failed')
        announce(t('app.context.copied'))
        return
      }
      if (item.action === 'paste') {
        const value = await readClipboard()
        if (!item.field) throw new Error('no field')
        const { start, end } = selectionOf(item.field)
        replaceField(item.field, value, start, end)
        announce(t('app.context.pasted'))
        return
      }
      if (item.action === 'cut') {
        const field = item.field
        if (!field) throw new Error('no field')
        const { start, end, text } = selectionOf(field)
        if (!(await writeClipboard(text))) throw new Error('clipboard write failed')
        replaceField(field, '', start, end)
        announce(t('app.context.cutDone'))
        return
      }
      if (item.action === 'selectAll') {
        const field = item.field
        if (!field) throw new Error('no field')
        if (field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement) field.select()
        else {
          field.focus()
          document.execCommand?.('selectAll')
        }
      }
    } catch (cause) {
      announce(t('app.context.failed', { message: cause instanceof Error ? cause.message : String(cause) }))
    }
  }

  return (
    <>
      {menu ? (
        <div
          ref={ref}
          className="app-context-menu"
          data-testid="app-context-menu"
          role="menu"
          style={{ left: position.left, top: position.top }}
        >
          {menu.items.map((item, index) =>
            item.glyph === 'divider' ? (
              <div key={`d-${index}`} className="app-context-divider" />
            ) : (
              <button
                key={`${item.action}-${index}`}
                type="button"
                role="menuitem"
                className="app-context-item"
                data-testid="app-context-item"
                data-action={item.action}
                onClick={() => void run(item)}
              >
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  {GLYPHS[item.action as Action]}
                </svg>
                <span>{t(item.label)}</span>
              </button>
            ),
          )}
        </div>
      ) : null}
      {toast ? (
        <div className="app-context-toast" data-testid="app-context-toast" role="status">{toast}</div>
      ) : null}
    </>
  )
}
