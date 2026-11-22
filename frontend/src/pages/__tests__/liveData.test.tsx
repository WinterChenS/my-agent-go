import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'
import type { ScheduleView } from '../schedules/client'
import type { LibraryView } from '../library/client'
import type { ConnectorOverview } from '../connectors/client'

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

const emptyCatalog = {
  runtimes: [],
  models: [],
  agents: [
    {
      id: 'agent-1',
      slug: 'agent-1',
      name: '文档助手',
      runtimeId: 'pi',
      modelId: 'claude-haiku-4.5',
      enabled: true,
      isDefault: true,
      description: '',
      systemPrompt: '',
      thinking: 'off',
      skills: [],
      tools: [],
      allowTools: true,
      memory: false,
      createdAt: '2026-11-01T00:00:00Z',
      updatedAt: '2026-11-01T00:00:00Z',
    },
  ],
  skills: [],
  tools: [],
  mcp: [],
  syncs: [],
  adapterFailures: [],
  scannedAt: null,
}

const scheduleView: ScheduleView = {
  schedules: [
    {
      id: 'sch-1',
      name: '每日站会纪要',
      cron: '0 9 * * 1-5',
      timeZone: 'Asia/Shanghai',
      utcOffsetMinutes: 480,
      agentId: 'agent-1',
      agentName: '文档助手',
      runtimeId: 'pi',
      model: 'claude-haiku-4.5',
      prompt: '汇总今天的会议记录，按项目分组提炼结论与待办。',
      permission: 'medium',
      outputTarget: 'library',
      enabled: true,
      lastRunAt: '2026-11-21T01:00:00Z',
      lastStatus: 'success',
      lastDurationMs: 102_000,
      nextRunAt: '2026-11-24T01:00:00Z',
      consecutiveFailures: 0,
      createdAt: '2026-11-20T00:00:00Z',
      updatedAt: '2026-11-21T01:00:00Z',
    },
  ],
  runs: [
    {
      scheduleId: 'sch-1',
      scheduledAt: '2026-11-21T01:00:00Z',
      state: 'success',
      reason: '',
      sessionId: 'sess-9',
      runId: 'run-9',
      startedAt: '2026-11-21T01:00:00Z',
      finishedAt: '2026-11-21T01:01:42Z',
      durationMs: 102_000,
    },
  ],
  stats: {
    total: 1,
    enabled: 1,
    running: 0,
    todayRuns: 1,
    weekRuns: 1,
    weekFailed: 0,
    successRate: 100,
    avgDurationMs: 102_000,
    nextRunAt: '2026-11-24T01:00:00Z',
  },
  trend: [
    { date: '2026-11-15', runs: 0, failed: 0 },
    { date: '2026-11-16', runs: 0, failed: 0 },
    { date: '2026-11-17', runs: 0, failed: 0 },
    { date: '2026-11-18', runs: 0, failed: 0 },
    { date: '2026-11-19', runs: 0, failed: 0 },
    { date: '2026-11-20', runs: 0, failed: 0 },
    { date: '2026-11-21', runs: 1, failed: 0 },
  ],
}

const libraryView: LibraryView = {
  assets: [
    {
      id: 'd:/proj/out/report.md',
      name: 'report.md',
      path: 'D:/proj/out/report.md',
      relative: 'out/report.md',
      ext: 'md',
      kind: 'doc',
      bytes: 18_432,
      session: '季度审计',
      sessionId: 'sess-1',
      at: '2026-11-21T10:05:00Z',
      source: 'session',
    },
    {
      id: 'd:/proj/out/table.csv',
      name: 'table.csv',
      path: 'D:/proj/out/table.csv',
      relative: 'out/table.csv',
      ext: 'csv',
      kind: 'sheet',
      bytes: 2048,
      session: '季度审计',
      sessionId: 'sess-1',
      at: '2026-11-20T10:05:00Z',
      source: 'session',
    },
  ],
  stats: {
    assets: 2,
    bytes: 20_480,
    weekAdded: 2,
    sessions: 1,
    imported: 0,
    byKind: { doc: 1, sheet: 1 },
  },
  root: 'C:/Users/me/.anvil/library',
}

const connectorView: ConnectorOverview = {
  types: [
    {
      id: 'MAIL',
      name: '邮件',
      description: 'IMAP + SMTP',
      capabilities: ['read', 'search', 'send'],
      fields: [],
      tools: [
        { name: 'mail_list', description: '列出最近邮件', write: false, calls: 5, failures: 0 },
        { name: 'mail_send', description: '发送邮件', write: true, calls: 2, failures: 1 },
      ],
      count: 1,
    },
  ],
  connectors: [
    {
      id: 'cn-1',
      kind: 'MAIL',
      kindName: '邮件',
      name: '工作邮箱',
      enabled: true,
      state: 'connected',
      probedAt: '2026-11-21T10:00:00Z',
      lastError: null,
      config: { email: 'me@corp.com', imapHost: 'imap.corp.com' },
      secretsSet: ['password'],
      capabilities: ['read', 'search', 'send'],
      tools: [
        { name: 'mail_list', description: '列出最近邮件', write: false, calls: 5, failures: 0 },
        { name: 'mail_send', description: '发送邮件', write: true, calls: 2, failures: 1 },
      ],
      calls: 7,
      failures: 1,
      lastCallAt: '2026-11-21T10:30:00Z',
    },
  ],
  stats: {
    total: 1,
    connected: 1,
    unverified: 0,
    failed: 0,
    calls: 7,
    failures: 1,
    tools: 2,
  },
  activity: [
    {
      connectorId: 'cn-1',
      name: '工作邮箱',
      kindName: '邮件',
      tool: 'mail_send',
      ok: false,
      at: '2026-11-21T10:30:00Z',
      detail: 'SMTP 拒绝了这封信',
    },
  ],
  updatedAt: '2026-11-15T00:00:00Z',
}

function installHost(responses: Record<string, unknown>) {
  const calls: Array<{ command: string; args?: Record<string, unknown> }> = []
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      if (command === 'catalog_snapshot') return emptyCatalog
      if (command in responses) return responses[command]
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('schedules read the ledger and write through the host', () => {
  it('renders the scheduled task, its real last run and the stats', async () => {
    const calls = installHost({ schedule_overview: scheduleView })
    renderAt('/schedules')
    await waitFor(() => expect(screen.getAllByTestId('schedule-row')).toHaveLength(1))
    const row = screen.getByTestId('schedule-row')
    expect(row).toHaveTextContent('每日站会纪要')
    expect(row).toHaveTextContent('0 9 * * 1-5')
    expect(row).toHaveTextContent('文档助手')
    expect(screen.getByTestId('schedule-stat-enabled')).toHaveTextContent('1')
    expect(screen.getByTestId('schedule-stat-today')).toHaveTextContent('1')
    expect(screen.getByTestId('schedule-stat-success')).toHaveTextContent('100.0%')
    expect(screen.getByTestId('schedule-detail')).toHaveTextContent('汇总今天的会议记录')
    expect(calls.some((call) => call.command === 'schedule_overview')).toBe(true)
  })

  it('pauses and runs a schedule through the host commands', async () => {
    const user = userEvent.setup()
    const calls = installHost({
      schedule_overview: scheduleView,
      schedule_toggle: { ...scheduleView.schedules[0], enabled: false },
      schedule_run_now: { sessionId: 'sess-9', runId: 'run-9', status: 'success', error: null },
    })
    renderAt('/schedules')
    await waitFor(() => expect(screen.getAllByTestId('schedule-row')).toHaveLength(1))
    await user.click(screen.getByRole('switch'))
    await waitFor(() => expect(calls.some((call) => call.command === 'schedule_toggle')).toBe(true))
    const toggle = calls.find((call) => call.command === 'schedule_toggle')
    expect(toggle?.args).toMatchObject({ id: 'sch-1', enabled: false })

    await user.click(screen.getByRole('button', { name: '立即运行' }))
    await waitFor(() => expect(calls.some((call) => call.command === 'schedule_run_now')).toBe(true))
  })
})

describe('the library lists the files sessions actually wrote', () => {
  it('renders real names, sizes and tab counts', async () => {
    installHost({ library_overview: libraryView })
    renderAt('/library')
    await waitFor(() => expect(screen.getAllByTestId('library-card')).toHaveLength(2))
    const cards = screen.getAllByTestId('library-card')
    expect(cards[0]).toHaveTextContent('report.md')
    expect(cards[1]).toHaveTextContent('table.csv')
    expect(screen.getByTestId('library-stat-assets')).toHaveTextContent('2')
    expect(screen.getByTestId('library-stat-storage')).toHaveTextContent('20 KB')
    const tabs = screen.getAllByRole('tab')
    expect(tabs[1]).toHaveTextContent('1')
    expect(screen.getAllByText('季度审计').length).toBeGreaterThan(0)
  })

  it('loads the text preview for the selected asset', async () => {
    const user = userEvent.setup()
    const calls = installHost({
      library_overview: libraryView,
      library_preview: '# 季度审计\n\n- 通过',
    })
    renderAt('/library')
    await waitFor(() => expect(screen.getAllByTestId('library-card')).toHaveLength(2))
    await user.click(within(screen.getAllByTestId('library-card')[0]).getByRole('button'))
    await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent('季度审计'))
    await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent('通过'))
    expect(calls.some((call) => call.command === 'library_preview')).toBe(true)
  })
})

describe('connectors are services with their own tools', () => {
  it('shows the connector, its tool counts and the recorded calls', async () => {
    const calls = installHost({ connectors_overview: connectorView })
    renderAt('/connectors')
    await waitFor(() => expect(screen.getAllByTestId('connector-card')).toHaveLength(1))
    const card = screen.getByTestId('connector-card')
    expect(card).toHaveTextContent('工作邮箱')
    expect(card).toHaveTextContent('me@corp.com')
    expect(screen.getByTestId('connector-stat-connected')).toHaveTextContent('1')
    expect(screen.getByTestId('connector-stat-actions')).toHaveTextContent('2')
    expect(screen.getByTestId('connector-stat-calls')).toHaveTextContent('7')
    expect(screen.getByTestId('connector-policy')).toHaveTextContent('mail_list')
    expect(screen.getByTestId('connector-activity')).toHaveTextContent('调用失败')
    expect(calls.some((call) => call.command === 'connectors_overview')).toBe(true)
  })
})
