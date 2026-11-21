import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'
import type { AuditReport } from '../../../chat/client'

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

const report: AuditReport = {
  period: '7',
  since: '2026-11-15',
  today: '2026-11-21',
  usage: {
    days: [
      { date: '2026-11-20', input: 1200, output: 400, cached: 100, cost: 0.4, calls: 3 },
      { date: '2026-11-21', input: 300, output: 100, cached: 0, cost: 0.1, calls: 1 },
    ],
    by_model: [{ label: 'claude-sonnet-4', tokens: 2000, calls: 4 }],
    by_agent: [{ label: 'agent-a', tokens: 2000, calls: 4 }],
    totals: {
      tokens: 2000,
      input: 1500,
      output: 500,
      cached: 100,
      cost: 0.5,
      calls: 4,
      tools: 2,
      reported: true,
    },
  },
  traces: [
    {
      id: 'run-abcdefgh',
      session_id: 's1',
      session: '报告会话',
      model: 'claude-sonnet-4',
      agent: 'agent-a',
      steps: 2,
      tokens: 2000,
      started_at: '2026-11-21T10:00:00Z',
      duration_ms: 4200,
      state: 'success',
    },
  ],
  executions: [
    {
      at: '2026-11-21T10:00:01Z',
      action: 'bash',
      subject: 'curl example.com',
      session: '报告会话',
      model: 'claude-sonnet-4',
      ok: true,
      duration_ms: 1200,
    },
  ],
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

function installHost(overrides: Partial<AuditReport> = {}) {
  const calls: Array<{ command: string; args?: Record<string, unknown> }> = []
  const value = { ...report, ...overrides }
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      if (command === 'session_audit') return value
      if (command === 'session_audit_export') return { path: 'D:/anvil/exports/audit.csv', rows: 1 }
      if (command === 'catalog_snapshot' || command === 'catalog_sync_inventory') {
        return { runtimes: [], models: [], skills: [], tools: [], mcp: [], syncs: [], adapterFailures: [], scannedAt: '2026-11-21T00:00:00Z' }
      }
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_projects') return []
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

describe('the observability view reads the real ledger', () => {
  it('shows the ledger totals instead of a "not connected" empty state', async () => {
    const calls = installHost()
    renderAt('/settings#observability')
    await waitFor(() => expect(calls.some((call) => call.command === 'session_audit')).toBe(true))
    const totals = await screen.findByTestId('settings-audit-totals')
    expect(within(totals).getByText('2.0K')).toBeInTheDocument()
    expect(screen.queryByTestId('settings-usage-empty')).toBeNull()
    expect(screen.getByTestId('settings-audit-by-model').textContent).toContain('claude-sonnet-4')
  })

  it('re-reads the ledger when the period changes', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    renderAt('/settings#observability')
    await waitFor(() => expect(calls.some((call) => call.command === 'session_audit')).toBe(true))
    await user.selectOptions(screen.getByTestId('settings-audit-period'), '30')
    await waitFor(() =>
      expect(calls.some((call) => call.command === 'session_audit' && call.args?.period === '30')).toBe(true),
    )
  })

  it('renders traces and executions from the ledger', async () => {
    installHost()
    const user = userEvent.setup()
    renderAt('/settings#observability')
    await screen.findByTestId('settings-audit-totals')

    await user.click(screen.getByRole('tab', { name: '链路' }))
    expect(await screen.findByText('报告会话')).toBeInTheDocument()
    expect(screen.getByTestId('settings-traces').textContent).toContain('4.2s')

    await user.click(screen.getByRole('tab', { name: '执行' }))
    const rows = screen.getAllByTestId('settings-execution-row')
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain('bash')
  })

  it('says "not reported" instead of printing a measured zero', async () => {
    installHost({
      ...report,
      usage: {
        ...report.usage,
        days: [],
        by_model: [],
        by_agent: [],
        totals: { ...report.usage.totals, tokens: 0, input: 0, output: 0, reported: false },
      },
    })
    renderAt('/settings#observability')
    const totals = await screen.findByTestId('settings-audit-totals')
    expect(totals.textContent).toContain('运行时未上报 Token 计数')
    expect(within(totals).getByText('—')).toBeInTheDocument()
  })

  it('exports through the host and reports where the CSV went', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    renderAt('/settings#observability')
    await screen.findByTestId('settings-audit-totals')
    await user.click(screen.getByTestId('settings-audit-export'))
    await waitFor(() =>
      expect(calls.some((call) => call.command === 'session_audit_export')).toBe(true),
    )
    expect(await screen.findByTestId('settings-notice')).toHaveTextContent('audit.csv')
  })
})

describe('the security audit export is real', () => {
  it('passes the chosen range and masking preference to the host', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    renderAt('/settings#security')

    const scope = screen.getByTestId('settings-audit-scope')
    await user.selectOptions(scope, 'month')
    const maskSwitch = screen.getByRole('switch', { name: '敏感参数脱敏' })
    expect(maskSwitch.getAttribute('aria-checked')).toBe('true')
    await user.click(screen.getByTestId('settings-audit-generate'))

    await waitFor(() => {
      const call = calls.find((entry) => entry.command === 'session_audit_export')
      expect(call?.args).toEqual({ period: 'month', mask: true })
    })
    expect(await screen.findByTestId('settings-notice')).toHaveTextContent('已导出 1 条审计记录')
  })

  it('persists the masking preference across a remount', async () => {
    installHost()
    const user = userEvent.setup()
    const first = renderAt('/settings#security')
    await user.click(screen.getByRole('switch', { name: '敏感参数脱敏' }))
    expect(window.localStorage.getItem('anvil.settings.auditMask')).toBe('0')
    first.unmount()
    cleanup()

    renderAt('/settings#security')
    expect(screen.getByRole('switch', { name: '敏感参数脱敏' }).getAttribute('aria-checked')).toBe('false')
  })
})
