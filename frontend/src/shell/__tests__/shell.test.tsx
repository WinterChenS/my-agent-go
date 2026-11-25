import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import { PRIMARY_ROUTES, SHELL_ROUTES, TOOL_ROUTES } from '../routes'

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

afterEach(cleanup)

describe('shared shell', () => {
  it('renders nine navigation entries with the prototype names', () => {
    const { container } = renderAt('/')
    const entries = Array.from(container.querySelectorAll<HTMLElement>('[data-nav-entry]'))
    expect(entries).toHaveLength(9)
    expect(entries.map((entry) => entry.getAttribute('aria-label'))).toEqual(
      SHELL_ROUTES.map((route) => route.navLabel),
    )
    for (const [index, entry] of entries.entries()) {
      expect(entry.tagName).toBe('A')
      expect(entry.getAttribute('href')).toBe(SHELL_ROUTES[index].path)
      expect(entry.getAttribute('data-route-path')).toBe(SHELL_ROUTES[index].path)
    }
  })

  it('keeps aria-current on exactly the active route for all nine paths', () => {
    for (const route of SHELL_ROUTES) {
      cleanup()
      const { container } = renderAt(route.path)
      const current = container.querySelectorAll('[aria-current="page"]')
      expect(current, `aria-current for ${route.path}`).toHaveLength(1)
      expect(current[0].getAttribute('data-route-path')).toBe(route.path)
    }
  })

  it('renders the placeholder heading and marker for each route', () => {
    for (const route of SHELL_ROUTES) {
      cleanup()
      renderAt(route.path)
      expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument()
      if (route.id === 'workbench') {
        expect(screen.getByTestId('workbench-page')).toBeInTheDocument()
      } else if (route.id === 'chat') {
        expect(screen.getByTestId('chat-page')).toBeInTheDocument()
      } else if (route.id === 'agents') {
        expect(screen.getByTestId('agents-page')).toBeInTheDocument()
        expect(screen.getByTestId('page-marker')).toHaveTextContent(route.marker)
      } else if (route.id === 'skills') {
        expect(screen.getByTestId('skills-page')).toBeInTheDocument()
        expect(screen.getByTestId('page-marker')).toHaveTextContent(route.marker)
      } else if (route.id === 'mcp') {
        expect(screen.getByTestId('mcp-page')).toBeInTheDocument()
        expect(screen.getByTestId('page-marker')).toHaveTextContent(route.marker)
      } else {
        expect(screen.getByTestId('page-marker')).toHaveTextContent(route.marker)
      }
    }
  })

  it('gives icon-only controls accessible names and no extra back button', () => {
    renderAt('/')
    expect(screen.getByRole('link', { name: 'Anvil' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '搜索' })).toBeInTheDocument()
    // The topbar's placeholder help button went with the topbar itself.
    expect(screen.queryByRole('button', { name: '帮助' })).toBeNull()
    expect(screen.getByRole('button', { name: '工具与自动化' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '返回上一页' })).toBeNull()
    expect(screen.getByRole('main', { name: '主内容' })).toBeInTheDocument()
    // The build marker and the wordmark's subtitle were chrome, not content.
    expect(screen.queryByTestId('build-marker')).toBeNull()
    expect(screen.queryByText('本地智能体客户端 · v0.9')).toBeNull()
  })

  it('gives the page the full height: no topbar on any route', () => {
    for (const route of SHELL_ROUTES) {
      cleanup()
      const { container } = renderAt(route.path)
      expect(container.querySelector('.topbar'), `topbar for ${route.path}`).toBeNull()
      expect(container.querySelector('#topbar-crumb')).toBeNull()
      // Every page names itself; that h1 is what the removed strip duplicated.
      expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: '返回上一页' })).toBeNull()
    }
  })

  it('keeps search reachable from the sidebar on every route', () => {
    for (const route of SHELL_ROUTES) {
      cleanup()
      renderAt(route.path)
      expect(
        screen.getByRole('button', { name: '搜索' }),
        `search for ${route.path}`,
      ).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: '帮助' })).toBeNull()
    }
  })

  it('does not render prototype fixture identity or counts in the normal entry', () => {
    renderAt('/')
    for (const fake of ['陈羽', '28 个输出物', '9 个已安装', '3 个服务', '5 个连接', '6 个任务', '本机运行中']) {
      expect(screen.queryByText(fake), `fixture text ${fake}`).toBeNull()
    }
    expect(screen.getByText('本地运行时')).toBeInTheDocument()
    expect(screen.getByText('未检测')).toBeInTheDocument()
    expect(screen.getByText('本地工作区')).toBeInTheDocument()
    expect(screen.getByText('未连接账号')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Anvil' })).toBeInTheDocument()
  })

  it('keeps the tools menu closed and out of the tab order by default', () => {
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '工具与自动化' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    const menu = document.getElementById('tools-menu')
    expect(menu).not.toBeNull()
    const items = within(menu as HTMLElement).getAllByRole('menuitem', { hidden: true })
    expect(items).toHaveLength(TOOL_ROUTES.length)
    for (const item of items) {
      expect(item).toHaveAttribute('tabindex', '-1')
      expect(item).toHaveAttribute('aria-label')
    }
  })

  it('shows all nine entries through primary links and the tools menu', () => {
    const { container } = renderAt('/')
    const primary = Array.from(container.querySelectorAll('[data-nav-entry][data-group="primary"]'))
    const tools = Array.from(container.querySelectorAll('[data-nav-entry][data-group="tools"]'))
    expect(primary).toHaveLength(PRIMARY_ROUTES.length)
    expect(tools).toHaveLength(TOOL_ROUTES.length)
    expect([...primary, ...tools]).toHaveLength(SHELL_ROUTES.length)
  })

  it('falls back to the workbench with a diagnosable status for unknown paths', () => {
    renderAt('/unknown/route')
    expect(screen.getByTestId('page-placeholder')).toHaveAttribute('data-route', '/')
    expect(screen.getByTestId('page-marker')).toHaveTextContent('R0-04 workbench · /')
    const status = screen.getByRole('status')
    expect(status).toHaveTextContent('未找到路由 /unknown/route')
    expect(status).toHaveAttribute('data-route-status', 'fallback')
  })
})
