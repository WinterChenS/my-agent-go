import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import { TOOL_ROUTES } from '../routes'

function renderAt(path = '/') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

function activeLabel(): string {
  const element = document.activeElement as HTMLElement | null
  if (!element) return ''
  return element.getAttribute('aria-label') ?? element.textContent?.trim() ?? element.tagName
}

afterEach(cleanup)

describe('shell keyboard access', () => {
  it('tabs sidebar → topbar → main content', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const order: string[] = []
    // Enough stops to get past the sidebar's conversation list, which sits
    // between the nav and the topbar and grows with the ledger.
    for (let index = 0; index < 30; index += 1) {
      await user.tab()
      order.push(activeLabel())
    }
    const at = (needle: string) => order.findIndex((entry) => entry.includes(needle))
    expect(at('Anvil')).toBe(0)
    expect(at('工作台')).toBeGreaterThan(at('Anvil'))
    expect(at('工具与自动化')).toBeGreaterThan(at('Agent'))
    expect(at('搜索')).toBeGreaterThan(at('工具与自动化'))
    expect(at('帮助')).toBeGreaterThan(at('搜索'))
    expect(at('主内容')).toBeGreaterThan(at('帮助'))
  })

  it('opens the tools menu with Enter, moves focus with arrows and closes with Escape', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '工具与自动化' })
    trigger.focus()
    await user.keyboard('{Enter}')
    expect(trigger).toHaveAttribute('aria-expanded', 'true')

    const items = screen.getAllByRole('menuitem')
    expect(document.activeElement).toBe(items[0])
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(items[1])
    await user.keyboard('{Escape}')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(document.activeElement).toBe(trigger)
  })

  it('activates a menu item with Space', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '工具与自动化' })
    trigger.focus()
    await user.keyboard('{Enter}')
    const first = screen.getByRole('menuitem', { name: TOOL_ROUTES[0].navLabel })
    expect(document.activeElement).toBe(first)
    await user.keyboard(' ')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByTestId('page-marker')).toHaveTextContent(TOOL_ROUTES[0].marker)
  })

  it('activates a menu item with Enter after arrow navigation', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '工具与自动化' })
    trigger.focus()
    await user.keyboard('{Enter}')
    await user.keyboard('{ArrowDown}')
    await user.keyboard('{Enter}')
    expect(screen.getByTestId('page-marker')).toHaveTextContent(TOOL_ROUTES[1].marker)
  })

  it('tabs into the open menu and closes it when clicking outside', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '工具与自动化' })
    await user.click(trigger)
    const items = screen.getAllByRole('menuitem')
    expect(document.activeElement).toBe(items[0])
    await user.tab()
    expect(document.activeElement).toBe(items[1])
    await user.click(document.body)
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
  })

  it('navigates to every tools entry through the menu', async () => {
    const user = userEvent.setup()
    renderAt('/')
    for (const route of TOOL_ROUTES) {
      const trigger = screen.getByRole('button', { name: '工具与自动化' })
      await user.click(trigger)
      const item = screen.getByRole('menuitem', { name: route.navLabel })
      await user.click(item)
      expect(screen.getByTestId('page-marker')).toHaveTextContent(route.marker)
      expect(trigger).toHaveAttribute('aria-expanded', 'false')
    }
  })
})
