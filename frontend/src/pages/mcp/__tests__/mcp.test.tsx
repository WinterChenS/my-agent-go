import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
  document.title = ''
})

afterEach(cleanup)

describe('mcp normal entry', () => {
  it('renders zero statistics, empty cards/logs and disabled actions', () => {
    renderAt('/mcp')
    expect(screen.getByTestId('mcp-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'MCP 服务' })).toBeInTheDocument()
    expect(screen.getAllByTestId(/^mcp-stat-/)).toHaveLength(4)
    expect(screen.getByTestId('mcp-stat-connected')).toHaveTextContent('0')
    expect(screen.getByTestId('mcp-stat-tools')).toHaveTextContent('0')
    expect(screen.getByTestId('mcp-stat-calls')).toHaveTextContent('0')
    expect(screen.getByTestId('mcp-stat-latency')).toHaveTextContent('—')
    expect(screen.getByTestId('mcp-empty-services')).toHaveTextContent('尚未接入 MCP 服务')
    expect(screen.getByTestId('mcp-empty-logs')).toHaveTextContent('尚未接入连接日志')
    // Reconnect is a real command now; with no host it reports the failure.
    // Adding stays disabled until a runtime that declares MCP is discovered.
    expect(screen.getByRole('button', { name: '重连全部' })).not.toBeDisabled()
    expect(screen.getByRole('button', { name: '添加服务' })).toBeDisabled()
    expect(screen.queryByTestId('mcp-card')).toBeNull()
    for (const fixture of ['filesystem', 'postgres-analytics', 'playwright', 'notion', '19', '216', '98.6%']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('stays empty in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/mcp')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'MCP services' })).toBeInTheDocument()
    expect(screen.getByTestId('mcp-empty-services')).toHaveTextContent('No MCP servers connected yet')
    expect(screen.getByRole('button', { name: 'Add server' })).toBeDisabled()
  })
})

describe('mcp sample entry', () => {
  it('renders four statistics and four service cards with tools/status/footers', () => {
    renderAt('/?sample=mcp')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByTestId('mcp-stat-connected')).toHaveTextContent('3')
    expect(screen.getByTestId('mcp-stat-tools')).toHaveTextContent('19')
    expect(screen.getByTestId('mcp-stat-calls')).toHaveTextContent('216')
    expect(screen.getByTestId('mcp-stat-latency')).toHaveTextContent('142ms')
    expect(screen.getAllByTestId('mcp-card')).toHaveLength(4)
    const cards = screen.getAllByTestId('mcp-card')
    expect(cards[0]).toHaveTextContent('filesystem')
    expect(cards[0]).toHaveTextContent('已连接')
    expect(cards[0]).toHaveTextContent('npx -y @modelcontextprotocol/server-filesystem ~/work')
    expect(cards[0]).toHaveTextContent('4 个工具')
    expect(within(cards[0]).getAllByTestId('mcp-tool')).toHaveLength(4)
    expect(within(cards[0]).getByText('read_file')).toBeInTheDocument()
    expect(within(cards[0]).getByText('读取工作目录内的文件，单次最多 200 KB。')).toBeInTheDocument()
    expect(cards[1]).toHaveTextContent('postgres-analytics')
    expect(within(cards[1]).getAllByTestId('mcp-tool')).toHaveLength(3)
    expect(cards[2]).toHaveTextContent('需授权')
    expect(cards[2]).toHaveTextContent('等待你在对话中确认授权')
    expect(within(cards[2]).getByRole('button', { name: '去确认' })).toBeInTheDocument()
    expect(cards[3]).toHaveTextContent('未连接')
    expect(cards[3]).toHaveTextContent('上次尝试 · 昨天 20:14 失败')
    expect(within(cards[3]).getByRole('button', { name: '授权' })).toBeInTheDocument()
    expect(within(cards[0]).getByRole('switch')).toHaveAttribute('aria-checked', 'true')
  })

  it('filters by service, tool, transport and status and shows an empty result', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=mcp')
    const search = screen.getByTestId('mcp-search')
    await user.type(search, 'filesystem')
    expect(screen.getAllByTestId('mcp-card')).toHaveLength(1)
    await user.clear(search)
    await user.type(search, 'run_query')
    expect(screen.getAllByTestId('mcp-card')).toHaveLength(1)
    expect(screen.getByTestId('mcp-card')).toHaveTextContent('postgres-analytics')
    await user.clear(search)
    await user.selectOptions(screen.getByLabelText('传输方式'), 'http')
    expect(screen.getAllByTestId('mcp-card')).toHaveLength(3)
    await user.selectOptions(screen.getByLabelText('状态'), 'connected')
    expect(screen.getAllByTestId('mcp-card')).toHaveLength(1)
    await user.type(search, '不存在')
    expect(screen.getByText('没有匹配的 MCP 服务')).toBeInTheDocument()
  })

  it('renders six log entries and refreshes only with a sample notice', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=mcp')
    expect(screen.getAllByTestId('mcp-log')).toHaveLength(6)
    expect(screen.getByTestId('mcp-logs')).toHaveTextContent('14:32:07')
    expect(screen.getByTestId('mcp-logs')).toHaveTextContent('protocol 2025-06-18 · 4 tools')
    await user.click(screen.getByRole('button', { name: '刷新' }))
    expect(screen.getByTestId('mcp-notice')).toHaveTextContent('样例模式：日志刷新未接入')
  })

  it('opens the add-server dialog, switches transport, masks env and validates', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=mcp')
    const opener = screen.getByRole('button', { name: '添加服务' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '添加 MCP 服务' })
    expect(within(dialog).getByText('stdio 使用命令，远端服务填写 https://…/sse。')).toBeInTheDocument()
    await user.selectOptions(within(dialog).getByLabelText('传输方式'), 'http')
    expect(within(dialog).getByText('填写远端 MCP 服务的 http/https 地址。')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '添加' }))
    expect(within(dialog).getByText('请填写服务名称')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('服务名称'), 'gitlab')
    await user.type(within(dialog).getByLabelText('启动命令或地址'), 'not-a-url')
    await user.click(within(dialog).getByRole('button', { name: '添加' }))
    expect(within(dialog).getByText('远端服务地址必须是有效的 http/https URL')).toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('启动命令或地址'))
    await user.type(within(dialog).getByLabelText('启动命令或地址'), 'https://mcp.example.com/sse')

    await user.type(within(dialog).getByLabelText('环境变量（每行一条）'), 'TOKEN=supersecret\nBAD')
    expect(within(dialog).getByTestId('mcp-env-preview')).toHaveTextContent('TOKEN=••••')
    expect(within(dialog).getByText('环境变量格式应为 KEY=VALUE')).toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('环境变量（每行一条）'))
    await user.type(within(dialog).getByLabelText('环境变量（每行一条）'), 'TOKEN=supersecret')
    expect(within(dialog).getByTestId('mcp-env-preview')).toHaveTextContent('TOKEN=••••')

    await user.click(within(dialog).getByRole('radio', { name: '完全控制' }))
    expect(within(dialog).getByRole('radio', { name: '完全控制' })).toHaveAttribute('aria-checked', 'true')
    await user.click(within(dialog).getByRole('checkbox', { name: '迁移工程师' }))
    expect(within(dialog).getByRole('checkbox', { name: '迁移工程师' })).toBeChecked()
    await user.click(within(dialog).getByRole('button', { name: '添加' }))
    expect(screen.getByTestId('mcp-notice')).toHaveTextContent('样例模式：添加服务未接入')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '添加 MCP 服务' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(opener))
    expect(screen.queryByText('supersecret')).toBeNull()
  })

  it('translates statistics, cards, logs and validation', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=mcp')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByTestId('mcp-stat-connected')).toHaveTextContent('Connected servers')
    expect(screen.getByTestId('mcp-stat-tools')).toHaveTextContent('Exposed tools')
    expect(screen.getAllByTestId('mcp-card')[0]).toHaveTextContent('Local filesystem')
    expect(screen.getByTestId('mcp-logs')).toHaveTextContent('Connection log')
    await user.click(screen.getByRole('button', { name: 'Add server' }))
    const dialog = screen.getByRole('dialog', { name: 'Add MCP server' })
    await user.click(within(dialog).getByRole('button', { name: 'Add' }))
    expect(within(dialog).getByText('Enter a server name')).toBeInTheDocument()
  })
})

describe('mcp responsive contract', () => {
  it('declares two-column and single-column cards with scrollable tools', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/mcp/mcp.css'), 'utf8')
    expect(css).toContain('grid-template-columns: repeat(2, minmax(0, 1fr))')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('grid-template-columns: minmax(0, 1fr)')
    expect(css).not.toMatch(/\.mcp-tools\s*\{[^}]*max-height/)
    expect(css).not.toMatch(/\.mcp-tools\s*\{[^}]*overflow-y/)
    expect(css).toContain('min-width: 0')
  })
})

