import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { LanguageProvider } from '../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../i18n/language'
import AppContextMenu from '../AppContextMenu'

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function renderMenu(children: ReactNode) {
  return render(
    <MemoryRouter>
      <LanguageProvider environment={environment}>
        <AppContextMenu />
        {children}
      </LanguageProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: vi.fn(async () => undefined), readText: vi.fn(async () => 'pasted') },
  })
})

afterEach(cleanup)

describe('app context menu', () => {
  it('replaces the native menu with client actions', async () => {
    renderMenu(<div data-testid="plain">右键这里</div>)
    const target = screen.getByTestId('plain')
    const event = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 30, clientY: 40 })
    target.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
    const menu = await waitFor(() => screen.getByTestId('app-context-menu'))
    expect(within(menu).getByRole('menuitem', { name: '新建对话' })).toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: '全局搜索' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: '返回' })).toBeNull()
  })

  it('copies a link address from the right-clicked link', async () => {
    renderMenu(<a data-testid="link" href="https://example.com/a">例子</a>)
    fireEvent.contextMenu(screen.getByTestId('link'), { clientX: 20, clientY: 20 })
    const menu = screen.getByTestId('app-context-menu')
    const copy = within(menu).getByRole('menuitem', { name: '复制链接地址' })
    await userClick(copy)
    await waitFor(() =>
      expect(navigator.clipboard.writeText).toHaveBeenCalledWith('https://example.com/a'),
    )
    expect(screen.getByTestId('app-context-toast')).toHaveTextContent('已复制')
    expect(screen.queryByTestId('app-context-menu')).toBeNull()
  })

  it('pastes into an input at the caret', async () => {
    renderMenu(<input data-testid="field" defaultValue="ab" />)
    const field = screen.getByTestId('field') as HTMLInputElement
    field.focus()
    field.setSelectionRange(1, 1)
    fireEvent.contextMenu(field, { clientX: 20, clientY: 20 })
    const menu = screen.getByTestId('app-context-menu')
    expect(within(menu).getByRole('menuitem', { name: '粘贴' })).toBeInTheDocument()
    await userClick(within(menu).getByRole('menuitem', { name: '粘贴' }))
    await waitFor(() => expect(field.value).toBe('apastedb'))
    expect(screen.getByTestId('app-context-toast')).toHaveTextContent('已粘贴')
  })

  it('copies a whole chat message from its bubble', async () => {
    renderMenu(
      <article data-testid="chat-message-agent">
        <div className="chat-body-with-copy"><div className="chat-bubble">完整的回答</div></div>
      </article>,
    )
    fireEvent.contextMenu(screen.getByText('完整的回答'), { clientX: 20, clientY: 20 })
    const menu = await waitFor(() => screen.getByTestId('app-context-menu'))
    await fireEvent.click(within(menu).getByRole('menuitem', { name: '复制这条消息' }))
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('完整的回答'))
  })

  it('opens the existing session menu from a history row', async () => {
    const onOpen = vi.fn()
    renderMenu(
      <div className="chat-history-row">
        <button type="button" className="chat-history-item" onClick={onOpen}>会话标题</button>
        <button type="button" data-testid="chat-history-actions-row-1">更多</button>
      </div>,
    )
    fireEvent.contextMenu(screen.getByText('会话标题'), { clientX: 20, clientY: 20 })
    const menu = await waitFor(() => screen.getByTestId('app-context-menu'))
    await fireEvent.click(within(menu).getByRole('menuitem', { name: '会话操作…' }))
    expect(screen.getByTestId('chat-history-actions-row-1')).toBeInTheDocument()
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('cuts the selected text out of an input', async () => {
    renderMenu(<input data-testid="field" defaultValue="hello" />)
    const field = screen.getByTestId('field') as HTMLInputElement
    field.focus()
    field.setSelectionRange(0, 2)
    fireEvent.contextMenu(field, { clientX: 20, clientY: 20 })
    const menu = screen.getByTestId('app-context-menu')
    await userClick(within(menu).getByRole('menuitem', { name: '剪切' }))
    await waitFor(() => expect(field.value).toBe('llo'))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('he')
  })
})

/** The menu closes on pointerdown; React's synthetic click still fires. */
async function userClick(element: HTMLElement) {
  fireEvent.click(element)
}
