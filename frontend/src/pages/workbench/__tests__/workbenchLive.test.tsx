import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'
import type { ActivityCell, WorkbenchOverview } from '../client'

function renderAt(url = '/') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

const cells: ActivityCell[] = []
for (let index = 0; index < 21; index += 1) {
  const date = new Date(Date.UTC(2026, 10, 1 + index))
  const key = date.toISOString().slice(0, 10)
  cells.push({
    date: key,
    count: key === '2026-11-21' ? 5 : index % 3,
    runs: key === '2026-11-21' ? 2 : 0,
    tools: key === '2026-11-21' ? 3 : index % 3,
    future: false,
  })
}

const overview: WorkbenchOverview = {
  today: '2026-11-21',
  weeks: 53,
  gridStart: '2025-11-22',
  gridEnd: '2026-11-21',
  stats: {
    sessionsThisWeek: 3,
    sessionsDelta: 2,
    activeSessions: 1,
    toolCalls: 128,
    toolResults: 120,
    toolFailures: 5,
    toolSuccessRate: 96,
    tokens: 4_000_000,
    tokensInput: 3_000_000,
    tokensOutput: 1_000_000,
    tokensReported: true,
    pendingApprovals: 2,
  },
  activity: { cells, total: 2086, activeDays: 39, longestStreak: 11 },
  chats: [
    {
      id: 's-1',
      title: '生成季度审计报告',
      agent: '审计助手',
      model: 'auto/deepseek-v4',
      updatedAt: '2026-11-21T10:00:00Z',
      turns: 9,
      messages: 22,
      pending: 1,
      status: 'running',
      preview: '',
    },
  ],
  artifacts: [
    {
      name: 'audit-report.md',
      relative: 'outputs/audit-report.md',
      path: 'D:/anvil/workspace/outputs/audit-report.md',
      ext: 'md',
      bytes: 18_432,
      session: '生成季度审计报告',
      sessionId: 's-1',
      at: '2026-11-21T10:05:00Z',
    },
  ],
  agents: [
    {
      id: 'agent-1',
      name: '审计助手',
      initial: '审',
      model: 'auto/deepseek-v4',
      runtimeId: 'pi',
      skills: 4,
      runs: 12,
      enabled: true,
      isDefault: true,
    },
  ],
  schedules: [],
  status: {
    runtimeReady: 1,
    runtimeTotal: 2,
    runtimeLabel: 'pi 1.5.0',
    mcpConnected: 3,
    mcpTotal: 4,
    connectorsAvailable: false,
    quotaAvailable: false,
    schedulesEnabled: 0,
    schedulesToday: 0,
  },
  sources: {
    ledger: true,
    catalog: true,
    filesystem: true,
    schedules: false,
    connectors: false,
    quota: false,
  },
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

function installHost(value: WorkbenchOverview | Error = overview) {
  const calls: Array<{ command: string; args?: Record<string, unknown> }> = []
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      if (command === 'workbench_overview') {
        if (value instanceof Error) throw value
        return value
      }
      if (command === 'session_list') return { sessions: [], total: 0, nextCursor: null }
      if (command === 'session_projects') return []
      if (command === 'catalog_snapshot') {
        return { runtimes: [], models: [], agents: [], skills: [], tools: [], mcp: [], syncs: [], adapterFailures: [], scannedAt: null }
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

const statText = (key: string) =>
  (screen.getByTestId('workbench-page').querySelector(`[data-stat="${key}"]`) as HTMLElement).textContent ?? ''

describe('the workbench renders real ledger data', () => {
  it('replaces the empty state with the host numbers and no fixture values', async () => {
    const calls = installHost()
    renderAt()
    await waitFor(() => expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('live'))
    expect(calls.some((call) => call.command === 'workbench_overview')).toBe(true)
    expect(statText('sessions')).toContain('3')
    expect(statText('sessions')).toContain('+2')
    expect(statText('toolCalls')).toContain('128')
    expect(statText('toolCalls')).toContain('96.0%')
    expect(statText('tokens')).toContain('4M')
    expect(statText('pending')).toContain('2')
    expect(screen.queryByText('34')).toBeNull()
    expect(screen.queryByText('陈羽')).toBeNull()
    expect(screen.getByTestId('workbench-sources')).toBeInTheDocument()
  })

  it('draws the heatmap from the backend cells and explains one day on hover', async () => {
    installHost()
    const user = userEvent.setup()
    renderAt()
    await waitFor(() => expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('live'))
    const cell = document.querySelector('[data-date="2026-11-21"]') as HTMLElement
    expect(cell).toHaveAttribute('data-count', '5')
    expect(cell).toHaveAttribute('data-level', '2')
    await user.hover(cell)
    const tooltip = screen.getByRole('tooltip')
    expect(tooltip.textContent).toContain('完成 5 次 Agent 工作')
    expect(tooltip.textContent).toContain('完成回合 2 · 工具执行 3')
    await user.unhover(cell)
    expect(screen.queryByRole('tooltip')).toBeNull()
  })

  it('reads a different real range when the range select changes', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    renderAt()
    await waitFor(() => expect(calls.some((call) => call.command === 'workbench_overview')).toBe(true))
    await user.selectOptions(screen.getByRole('combobox', { name: 'Agent 工作活跃度' }), '12')
    await waitFor(() =>
      expect(calls.some((call) => call.command === 'workbench_overview' && call.args?.weeks === 12)).toBe(true),
    )
    expect(
      calls.find((call) => call.command === 'workbench_overview')?.args?.utcOffsetMinutes,
    ).toBeTypeOf('number')
  })

  it('lists real chats, artifacts and agents, and resumes the exact session', async () => {
    installHost()
    const user = userEvent.setup()
    renderAt()
    await waitFor(() => expect(screen.getByText('生成季度审计报告')).toBeInTheDocument())
    expect(screen.getByText('audit-report.md')).toBeInTheDocument()
    expect(screen.getByText('本地文件')).toBeInTheDocument()
    expect(screen.getAllByText('审计助手').length).toBeGreaterThan(0)
    expect(screen.getByText('待确认')).toBeInTheDocument()
    const resume = screen.getAllByRole('button', { name: '继续' })[0]
    await user.click(resume)
    expect(window.localStorage.getItem('anvil.chat.session')).toBe('s-1')
  })

  it('says plainly what has no source instead of showing a convincing zero', async () => {
    installHost()
    renderAt()
    await waitFor(() => expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('live'))
    expect(screen.getByText('还没有定时任务；也可以直接让 Agent 用一句话创建')).toBeInTheDocument()
    expect(screen.getByText('未接入')).toBeInTheDocument()
    expect(screen.getByText('未配置')).toBeInTheDocument()
    expect(screen.getByText('pi 1.5.0 · 1/2 可用')).toBeInTheDocument()
    expect(screen.getByText('3/4 已连接')).toBeInTheDocument()
  })

  it('does not print a measured zero when the runtime reported no tokens', async () => {
    installHost({
      ...overview,
      stats: { ...overview.stats, tokens: 0, tokensInput: 0, tokensOutput: 0, tokensReported: false, toolSuccessRate: null },
    })
    renderAt()
    await waitFor(() => expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('live'))
    expect(statText('tokens')).toContain('—')
    expect(statText('tokens')).toContain('运行时未回报 Token 用量')
    expect(statText('toolCalls')).toContain('工具未回报执行结果')
  })

  it('shows honest empty cards for an empty ledger', async () => {
    installHost({
      ...overview,
      stats: {
        ...overview.stats,
        sessionsThisWeek: 0,
        sessionsDelta: 0,
        activeSessions: 0,
        toolCalls: 0,
        toolSuccessRate: null,
        pendingApprovals: 0,
        tokensReported: false,
      },
      activity: { ...overview.activity, cells: cells.map((cell) => ({ ...cell, count: 0 })), total: 0, activeDays: 0, longestStreak: 0 },
      chats: [],
      artifacts: [],
      agents: [],
    })
    renderAt()
    await waitFor(() => expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('live'))
    expect(screen.getByText('还没有会话，新建一个开始')).toBeInTheDocument()
    expect(screen.getByText('最近的会话还没有写出文件')).toBeInTheDocument()
    expect(screen.getByText('尚未配置 Agent')).toBeInTheDocument()
    expect(screen.queryByText('迁移工程师')).toBeNull()
  })

  it('reports a read failure instead of silently falling back to fixtures', async () => {
    installHost(new Error('ledger is locked'))
    renderAt()
    await waitFor(() => expect(screen.getByTestId('workbench-failed')).toBeInTheDocument())
    expect(screen.getByTestId('workbench-failed').textContent).toContain('ledger is locked')
    expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('empty')
    expect(screen.getByRole('button', { name: '新建对话' })).toBeDisabled()
  })

  it('keeps the isolated sample entry independent of the host', async () => {
    installHost()
    renderAt('/?sample=workbench')
    expect(screen.getByTestId('sample-banner')).toBeInTheDocument()
    expect(screen.getByText('34')).toBeInTheDocument()
    expect(screen.getByTestId('workbench-page').getAttribute('data-workbench-mode')).toBe('sample')
  })
})
