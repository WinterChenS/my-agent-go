import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'

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

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

const user = (id: string, text: string, extra: Record<string, unknown> = {}) => ({
  id,
  scope: 'user',
  sessionId: '',
  sessionTitle: '',
  agentId: '',
  kind: 'preference',
  text,
  source: 'manual',
  pinned: false,
  enabled: true,
  createdAt: '2026-11-22T02:00:00Z',
  updatedAt: '2026-11-22T02:00:00Z',
  ...extra,
})

const concluded = {
  id: 'session-memory:s1',
  scope: 'session',
  sessionId: 's1',
  sessionTitle: '整理目录',
  agentId: 'default',
  kind: 'conclusion',
  text: '目录里有三个文件。',
  source: 'session',
  pinned: false,
  enabled: true,
  createdAt: '2026-11-22T03:00:00Z',
  updatedAt: '2026-11-22T03:00:00Z',
}

/** One host whose rows change exactly as the commands say they would. */
function installMemoryHost() {
  const calls: Array<{ command: string; args: Record<string, unknown> }> = []
  let rows = [user('mem-1', '回答先给结论，不要寒暄'), user('mem-2', '不要改 ~/.pi', { pinned: true })]
  let sessions = [concluded]
  let settings = { inject: true, recordSessions: true }
  const overview = () => ({
    settings,
    user: rows,
    session: sessions,
    counts: {
      user: rows.length,
      session: sessions.length,
      enabled: rows.filter((row) => row.enabled).length,
    },
    database: 'D:/anvil/sessions.sqlite',
  })
  const internals = {
    invoke: async (command: string, args: Record<string, unknown> = {}) => {
      calls.push({ command, args })
      if (command === 'catalog_snapshot') return { agents: [], runtimes: [], models: [], skills: [], tools: [], mcp: [], syncs: [], adapterFailures: [] }
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_projects') return []
      if (command === 'memory_overview') return overview()
      if (command === 'memory_save') {
        const memory = args.memory as { text: string; scope?: string; kind?: string }
        if (memory.scope === 'session') {
          sessions = [{ ...concluded, id: 'mem-3', text: memory.text }]
        } else {
          rows = [{ ...user(`mem-${rows.length + 1}`, memory.text, { kind: memory.kind }), ...rows }]
        }
        return overview()
      }
      if (command === 'memory_update') {
        const patch = args.patch as { text?: string; pinned?: boolean; enabled?: boolean }
        rows = rows.map((row) => (row.id === args.id ? { ...row, ...patch } : row))
        return overview()
      }
      if (command === 'memory_delete') {
        rows = rows.filter((row) => row.id !== args.id)
        sessions = sessions.filter((row) => row.id !== args.id)
        return overview()
      }
      if (command === 'memory_clear') {
        if (args.scope === 'session') sessions = []
        else rows = []
        return overview()
      }
      if (command === 'memory_set_switches') {
        settings = { ...settings, ...(args as Partial<typeof settings>) }
        return overview()
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

describe('设置 → 记忆', () => {
  it('lists long-term memory and says what is actually injected', async () => {
    installMemoryHost()
    renderAt('/settings#memory')

    const panel = await screen.findByTestId('memory-panel')
    await waitFor(() => expect(within(panel).getAllByTestId('memory-row')).toHaveLength(2))
    expect(within(panel).getByTestId('memory-tab-user')).toHaveTextContent('长期记忆 2')
    expect(within(panel).getByTestId('memory-tab-session')).toHaveTextContent('会话记忆 1')
    expect(panel).toHaveTextContent('每轮都会带给 Agent')

    // The session row is a record, not an input: switching tabs shows it, and
    // the compose box only exists on the long-term tab.
    await userEvent.setup().click(within(panel).getByTestId('memory-tab-session'))
    const rows = await within(panel).findAllByTestId('memory-row')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('目录里有三个文件')
    expect(rows[0]).toHaveTextContent('整理目录')
    expect(within(panel).queryByTestId('memory-draft-text')).toBeNull()
  })

  it('saves a new long-term memory with its kind', async () => {
    const calls = installMemoryHost()
    renderAt('/settings#memory')
    const panel = await screen.findByTestId('memory-panel')
    const input = within(panel).getByTestId('memory-draft-text')

    await userEvent.setup().type(input, '提交前先跑测试')
    await userEvent.setup().selectOptions(within(panel).getByTestId('memory-draft-kind'), 'workflow')
    await userEvent.setup().click(within(panel).getByTestId('memory-save'))

    await waitFor(() => expect(calls.map((call) => call.command)).toContain('memory_save'))
    const saved = calls.find((call) => call.command === 'memory_save')
    expect(saved?.args.memory).toMatchObject({
      scope: 'user',
      kind: 'workflow',
      text: '提交前先跑测试',
    })
    await waitFor(() =>
      expect(within(panel).getByTestId('memory-notice')).toHaveTextContent('已保存'),
    )
  })

  it('pins, disables and deletes through the host, not a local copy', async () => {
    const calls = installMemoryHost()
    renderAt('/settings#memory')
    const panel = await screen.findByTestId('memory-panel')
    await waitFor(() => expect(within(panel).getAllByTestId('memory-row')).toHaveLength(2))

    const first = within(panel).getAllByTestId('memory-row')[0]
    await userEvent.setup().click(within(first).getByRole('button', { name: '停用' }))
    await waitFor(() => expect(calls.map((call) => call.command)).toContain('memory_update'))
    expect(calls.find((call) => call.command === 'memory_update')?.args.patch).toEqual({ enabled: false })

    await userEvent.setup().click(within(first).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(within(panel).getAllByTestId('memory-row')).toHaveLength(1))
    expect(calls.find((call) => call.command === 'memory_delete')?.args.id).toBe('mem-1')
  })

  it('promotes a session conclusion by copying it and dropping the record', async () => {
    const calls = installMemoryHost()
    renderAt('/settings#memory')
    const panel = await screen.findByTestId('memory-panel')
    await userEvent.setup().click(await within(panel).findByTestId('memory-tab-session'))
    const row = (await within(panel).findAllByTestId('memory-row'))[0]

    await userEvent.setup().click(within(row).getByTestId('memory-promote'))

    await waitFor(() =>
      expect(calls.map((call) => call.command)).toEqual(
        expect.arrayContaining(['memory_save', 'memory_delete']),
      ),
    )
    const saved = calls.find((call) => call.command === 'memory_save')
    expect(saved?.args.memory).toMatchObject({ scope: 'user', text: '目录里有三个文件。' })
    expect(calls.find((call) => call.command === 'memory_delete')?.args.id).toBe('session-memory:s1')
    await waitFor(() =>
      expect(within(panel).getByTestId('memory-notice')).toHaveTextContent('已提升为长期记忆'),
    )
  })

  it('flips the two switches and needs a second click to clear', async () => {
    const calls = installMemoryHost()
    renderAt('/settings#memory')
    const panel = await screen.findByTestId('memory-panel')
    const inject = await within(panel).findByTestId('memory-switch-inject')

    await userEvent.setup().click(inject)
    await waitFor(() => expect(calls.map((call) => call.command)).toContain('memory_set_switches'))
    expect(calls.find((call) => call.command === 'memory_set_switches')?.args).toMatchObject({ inject: false })

    const clear = within(panel).getByTestId('memory-clear')
    await userEvent.setup().click(clear)
    expect(within(panel).getByTestId('memory-clear')).toHaveTextContent('确认清空')
    expect(calls.filter((call) => call.command === 'memory_clear')).toHaveLength(0)

    await userEvent.setup().click(within(panel).getByTestId('memory-clear'))
    await waitFor(() => expect(calls.map((call) => call.command)).toContain('memory_clear'))
    expect(calls.find((call) => call.command === 'memory_clear')?.args.scope).toBe('user')
  })
})
