import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'
import {
  describeSessionError,
  latestSeq,
  mergeDeltaText,
  type SessionDetail,
  type SessionEvent,
  type SendReport,
  type SessionSummary,
} from '../client'
import { raceTurn } from '../useChatSession'

function event(seq: number, kind: SessionEvent['kind'], payload: Record<string, unknown> = {}): SessionEvent {
  return { sessionId: 'pi-1', runId: 'pi-1:r1', seq, kind, at: '2026-11-13T00:00:00Z', payload }
}

describe('session wire helpers', () => {
  it('merges only text deltas and keeps the order of the batch', () => {
    const batch = [
      event(1, 'turn_started', { prompt: 'hi' }),
      event(2, 'text_delta', { delta: 'o' }),
      event(3, 'text_delta', { delta: 'k' }),
      event(4, 'usage', { totalTokens: 3 }),
    ]
    expect(mergeDeltaText(batch, '')).toBe('ok')
    // A second batch appends: polling must never re-emit what it already had.
    expect(mergeDeltaText([event(5, 'text_delta', { delta: '!' })], 'ok')).toBe('ok!')
    expect(mergeDeltaText([], 'ok')).toBe('ok')
  })

  it('resumes after the highest sequence number, not the last row', () => {
    expect(latestSeq([event(2, 'text_delta'), event(7, 'usage'), event(3, 'text_delta')], 0)).toBe(7)
    expect(latestSeq([], 9)).toBe(9)
  })

  it('unwraps the host error shape and leaves anything else alone', () => {
    expect(describeSessionError('{"code":"rejected","message":"session is archived"}')).toBe(
      'rejected: session is archived',
    )
    expect(describeSessionError('boom')).toBe('boom')
    expect(describeSessionError(new Error('boom'))).toBe('boom')
    expect(describeSessionError(undefined)).toBe('undefined')
  })

  it('cancels a turn that never answers instead of waiting forever', async () => {
    vi.useFakeTimers()
    try {
      const cancel = vi.fn(async () => undefined)
      const never = new Promise<SendReport>(() => {})
      const outcome = raceTurn(never, 120_000, cancel)
      await vi.advanceTimersByTimeAsync(120_000)
      expect(await outcome).toEqual({ report: null, timedOut: true })
      expect(cancel).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('clears its timer when the turn finishes before the timeout', async () => {
    vi.useFakeTimers()
    try {
      const cancel = vi.fn(async () => undefined)
      const report: SendReport = { runId: 'pi-1:r1', status: 'completed', eventCount: 1 }
      expect(await raceTurn(Promise.resolve(report), 120_000, cancel)).toEqual({
        report,
        timedOut: false,
      })
      expect(cancel).not.toHaveBeenCalled()
      expect(vi.getTimerCount()).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })
})

function summary(): SessionSummary {
  return {
    id: 'pi-1',
    runtimeId: 'pi',
    title: 'host test',
    status: 'idle',
    nativeId: 'native-1',
    model: 'auto/gpt-5.6-sol',
    projectPath: 'D:\\project',
    pinned: false,
    archived: false,
    messageCount: 2,
    parentId: '',
    branchAt: 0,
    createdAt: '2026-11-13T00:00:00Z',
    updatedAt: '2026-11-13T00:00:00Z',
    preview: 'ok',
  }
}

function detail(text: string): SessionDetail {
  return {
    session: { ...summary(), messageCount: 2 },
    messages: [
      {
        id: 'pi-1:m1',
        sessionId: 'pi-1',
        runId: 'pi-1:r1',
        role: 'user',
        text: 'Reply with exactly: ok',
        seq: 1,
        createdAt: '2026-11-13T00:00:00Z',
      },
      {
        id: 'pi-1:m2',
        sessionId: 'pi-1',
        runId: 'pi-1:r1',
        role: 'assistant',
        text,
        seq: 2,
        createdAt: '2026-11-13T00:00:01Z',
      },
    ],
    runs: [
      {
        id: 'pi-1:r1',
        sessionId: 'pi-1',
        status: 'completed',
        startedAt: '2026-11-13T00:00:00Z',
        endedAt: '2026-11-13T00:00:01Z',
        prompt: 'Reply with exactly: ok',
      },
    ],
  }
}

interface Host {
  calls: string[]
  aborted: () => boolean
  /** Arguments the host received for a command, when the test cares. */
  args: (command: string) => unknown
}

function catalogSnapshot(withExtra: boolean) {
  const models = [
    {
      runtimeId: 'pi',
      modelId: 'auto/gpt-5.6-sol',
      displayName: 'Gateway Sol',
      origin: 'custom',
      protocol: 'openai',
      endpoint: 'http://127.0.0.1:3425/v1',
      verified: true,
      verifiedAt: '2026-11-13T00:00:00Z',
      isDefault: true,
      updatedAt: '2026-11-13T00:00:00Z',
      hasCredential: true,
    },
  ]
  if (withExtra) {
    models.push({
      runtimeId: 'pi',
      modelId: 'auto/glm-5.3-flash',
      displayName: 'Gateway GLM',
      origin: 'runtime',
      protocol: 'openai',
      endpoint: null as unknown as string,
      verified: false,
      verifiedAt: null as unknown as string,
      isDefault: false,
      updatedAt: '2026-11-13T00:00:00Z',
      hasCredential: false,
    })
  }
  return {
    runtimes: [
      {
        id: 'pi:1',
        runtimeId: 'pi',
        displayName: 'Pi Agent',
        version: '1.5.0',
        installPath: 'C:////pi.CMD',
        installSource: 'path',
        configPath: null,
        status: 'available',
        capabilities: {
          models: 'supported',
          customModels: 'supported',
          skills: 'supported',
          tools: 'supported',
          mcp: 'supported',
          sessions: 'supported',
        },
        health: { checkedAt: '2026-11-13T00:00:00Z', latencyMs: 5, detail: null },
        lastSeen: '2026-11-13T00:00:00Z',
        firstSeen: '2026-11-13T00:00:00Z',
        snapshotState: 'fresh',
        adapterRegistered: true,
      },
    ],
    models,
    skills: [],
    tools: [],
    mcp: [],
    syncs: [],
    adapterFailures: [],
    scannedAt: '2026-11-13T00:00:00Z',
  }
}

function installHost(): Host {
  const calls: string[] = []
  const lastArgs = new Map<string, unknown>()
  let abortCalled = false
  let rescanned = false
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push(command)
      if (args) lastArgs.set(command, args)
      if (command === 'catalog_refresh') {
        // A model that only exists after the manual sync: the picker must be
        // rebuilt from the rescan, not from the startup snapshot.
        rescanned = true
        return catalogSnapshot(rescanned)
      }
      if (command === 'catalog_snapshot') return catalogSnapshot(rescanned)
      if (command === 'session_list') {
        // Two sessions in two directories: the history panel must group them by
        // the project index rather than list them flat.
        return {
          sessions: [
            { ...summary(), id: 'pi-a', title: 'Alpha chat', projectPath: 'D:////work////alpha' },
            { ...summary(), id: 'pi-b', title: 'Beta chat', projectPath: 'D:////work////beta' },
          ],
          total: 2,
        }
      }
      if (command === 'session_projects') {
        return [
          { path: 'D:////work////alpha', name: 'alpha', sessionCount: 1, lastUpdated: '2026-11-13T00:00:00Z' },
          { path: 'D:////work////beta', name: 'beta', sessionCount: 1, lastUpdated: '2026-11-13T00:00:00Z' },
        ]
      }
      if (command === 'session_fork') {
        return { ...summary(), id: 'pi-a-branch', title: 'Alpha chat · branch', parentId: 'pi-a', branchAt: 2 }
      }
      if (command === 'session_compact') return { collapsed: 4, kept: 6, summarySeq: 1 }
      if (command === 'session_context_list') {
        return [
          { path: 'D:/work/alpha/src/main.rs', relative: 'src/main.rs', kind: 'file', bytes: 120 },
          { path: 'D:/work/alpha/src', relative: 'src/', kind: 'dir', bytes: 0 },
        ]
      }
      if (command === 'session_approve') return null
      if (command === 'session_open') return { ...summary(), messageCount: 0 }
      if (command === 'session_events') {
        return [
          event(1, 'turn_started', { prompt: 'Reply with exactly: ok' }),
          event(2, 'text_delta', { delta: 'o' }),
          event(3, 'text_delta', { delta: 'k' }),
        ]
      }
      if (command === 'session_send') {
        // Long enough for the poll to run at least once, short enough to keep
        // the test fast.
        await new Promise((resolve) => setTimeout(resolve, 200))
        return { runId: 'pi-1:r1', status: 'completed', eventCount: 6 }
      }
      if (command === 'session_detail') return detail('ok')
      if (command === 'session_abort') {
        abortCalled = true
        return true
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return { calls, aborted: () => abortCalled, args: (command) => lastArgs.get(command) }
}

function clearHost() {
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  clearHost()
})

describe('chat live session', () => {
  it('syncs the model list from the composer and shows what the rescan found', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const picker = () => screen.getByTestId('chat-model-select')
    await waitFor(() => expect(picker()).not.toBeDisabled())
    expect(picker()).not.toHaveTextContent('auto/glm-5.3-flash')

    const sync = screen.getByTestId('chat-models-sync')
    expect(sync).toHaveAccessibleName('同步模型列表')
    await userEvent.click(sync)

    await waitFor(() => expect(host.calls).toContain('catalog_refresh'))
    // The picker is rebuilt from the rescan, not from the startup snapshot.
    await waitFor(() => expect(picker()).toHaveTextContent('auto/glm-5.3-flash'))
  })

  it('sends a real turn through the host and renders the stored reply', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    // The model already on the machine is offered, and the page does not
    // pretend to have a conversation before one exists.
    expect(screen.getByTestId('chat-model-select')).toHaveTextContent('auto/gpt-5.6-sol')
    expect(screen.getByTestId('chat-empty')).toHaveTextContent('还没有会话')

    await userEvent.type(composer, 'Reply with exactly: ok')
    await userEvent.click(screen.getByTestId('chat-send'))

    await waitFor(() => expect(host.calls).toContain('session_send'))
    expect(host.calls).toContain('session_open')
    expect(screen.getByTestId('chat-message-user')).toHaveTextContent('Reply with exactly: ok')

    await waitFor(() => expect(screen.getByTestId('chat-message-agent')).toHaveTextContent('ok'), {
      timeout: 3000,
    })
    // The composer is usable again once the turn closed.
    await waitFor(() => expect(screen.getByTestId('chat-send')).toBeDisabled())
    expect(host.aborted()).toBe(false)
  })
})

describe('chat history grouping', () => {
  async function openHistory() {
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    await userEvent.click(screen.getByRole('button', { name: '历史会话' }))
    // The drawer is the panel under test: the page also carries a static
    // history column, and two identical panels would make every query ambiguous.
    return within(screen.getByRole('dialog', { name: '历史会话' })).getByTestId('chat-history')
  }

  it('groups the live rows by the project index instead of listing them flat', async () => {
    installHost()
    const panel = await openHistory()

    await waitFor(() => expect(panel).toHaveTextContent('Alpha chat'))
    const labels = within(panel).getAllByTestId('chat-history-live-project')
    expect(labels).toHaveLength(2)
    expect(labels[0]).toHaveTextContent('alpha')
    expect(labels[0]).toHaveTextContent('1 个会话')
    expect(labels[1]).toHaveTextContent('beta')
  })

  it('branches and compacts a session from its row menu', async () => {
    const host = installHost()
    const panel = await openHistory()
    await waitFor(() => expect(panel).toHaveTextContent('Alpha chat'))

    const row = within(panel)
      .getByText('Alpha chat')
      .closest('.chat-history-row')
    expect(row).not.toBeNull()
    await userEvent.click(within(row as HTMLElement).getByLabelText('会话操作'))
    await userEvent.click(within(row as HTMLElement).getByText('分支会话'))
    await waitFor(() => expect(host.calls).toContain('session_fork'))
    expect(host.args('session_fork')).toMatchObject({ sessionId: 'pi-a' })

    // The branch is selected by the host's reply, and the panel stays usable.
    await waitFor(() => expect(host.calls).toContain('session_detail'))
    await userEvent.click(within(row as HTMLElement).getByLabelText('会话操作'))
    await userEvent.click(within(row as HTMLElement).getByText('压缩上下文'))
    await waitFor(() => expect(host.calls).toContain('session_compact'))
    expect(host.args('session_compact')).toMatchObject({ sessionId: 'pi-a', keepLast: 6 })
  })
})

function installToolHost(options: { fail?: boolean } = {}): Host {
  const calls: string[] = []
  const lastArgs = new Map<string, unknown>()
  let polls = 0
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push(command)
      if (args) lastArgs.set(command, args)
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') return catalogSnapshot(false)
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_open') return { ...summary(), messageCount: 0 }
      if (command === 'session_events') {
        polls += 1
        if (polls > 1) return []
        return [
          event(1, 'turn_started', { prompt: 'run it' }),
          event(2, 'tool_call', { tool: 'command', args: 'echo anvil' }),
          event(3, 'tool_result', { tool: 'command', ok: true, output: 'anvil' }),
          {
            sessionId: 'pi-1',
            runId: 'pi-1:r1',
            seq: 4,
            kind: 'approval_request' as const,
            at: '2026-11-13T00:00:00Z',
            payload: {
              id: 'pi-1:a1-0',
              sessionId: 'pi-1',
              runId: 'pi-1:r1',
              kind: 'read_outside_project',
              title: '读取项目之外的路径',
              detail: 'D:/outside/notes.txt',
              createdAt: '2026-11-13T00:00:00Z',
            },
          },
        ]
      }
      if (command === 'session_send') {
        await new Promise((resolve) => setTimeout(resolve, 600))
        if (options.fail) {
          return {
            runId: 'pi-1:r1',
            status: 'failed',
            error: 'gateway unavailable',
            eventCount: 4,
          }
        }
        return { runId: 'pi-1:r1', status: 'completed', eventCount: 4 }
      }
      if (command === 'session_abort') return true
      if (command === 'session_detail') return detail('done')
      if (command === 'session_approve') return null
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return { calls, aborted: () => false, args: (command) => lastArgs.get(command) }
}

describe('chat context and approvals', () => {
  async function sendOnce(text: string) {
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.type(composer, text)
    await userEvent.click(screen.getByTestId('chat-send'))
  }

  it('offers project files for @ and sends the chosen ones as context', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    // A session has to exist before the host knows which project to list.
    await sendOnce('first turn')
    await waitFor(() => expect(host.calls).toContain('session_detail'))

    const composer = screen.getByTestId('chat-composer')
    await userEvent.clear(composer)
    await userEvent.type(composer, 'look at @')
    await waitFor(() => expect(host.calls).toContain('session_context_list'))
    const entries = await screen.findAllByTestId('chat-mention-entry')
    expect(entries).toHaveLength(2)
    await userEvent.click(entries[0])

    // The chip carries the absolute path; the draft loses the `@query`.
    await waitFor(() => expect(screen.getByTestId('chat-attachments')).toHaveTextContent('src/main.rs'))
    // The `@query` token is gone; the reference now lives in the chip.
    expect(composer).toHaveValue('look at ')

    await userEvent.type(composer, 'this file')
    await userEvent.click(screen.getByTestId('chat-send'))
    await waitFor(() => expect(host.args('session_send')).toMatchObject({
      text: 'look at this file',
      context: [{ kind: 'file', path: 'D:/work/alpha/src/main.rs' }],
    }))
  })

  it('renders a tool card and answers the approval card from the live stream', async () => {
    const host = installToolHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')

    // The live turn now has the prototype's stage list, derived from the same
    // ledger events rather than from invented reasoning text.
    const timeline = await screen.findByTestId('chat-thinking')
    expect(timeline).toHaveTextContent('本轮执行过程')
    expect(timeline).toHaveTextContent('调用工具 command')

    const tool = await screen.findByTestId('chat-live-tool')
    expect(tool).toHaveAttribute('data-status', 'ok')
    expect(tool).toHaveTextContent('echo anvil')

    const card = await screen.findByTestId('chat-approval')
    expect(card).toHaveTextContent('读取项目之外的路径')
    expect(card).toHaveTextContent('D:/outside/notes.txt')

    await userEvent.click(within(card).getByText('允许本次'))
    await waitFor(() => expect(host.calls).toContain('session_approve'))
    expect(host.args('session_approve')).toMatchObject({
      sessionId: 'pi-1',
      approvalId: 'pi-1:a1-0',
      decision: 'allow',
    })
  })

  it('shows a retryable state card instead of an endless generating bubble', async () => {
    const host = installToolHost({ fail: true })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')

    const card = await screen.findByTestId('chat-live-failure')
    expect(card).toHaveTextContent('gateway unavailable')
    expect(screen.queryByTestId('chat-message-streaming')).toBeNull()

    await userEvent.click(within(card).getByText('重试'))
    await waitFor(() => {
      expect(host.calls.filter((call) => call === 'session_send')).toHaveLength(2)
    })
  })
})

describe('chat rail', () => {
  /** A rail view shaped like the host's, with one of everything. */
  function railView(empty = false) {
    if (empty) {
      return {
        turns: [],
        exec: [],
        artifacts: [],
        sources: [],
        browser: { state: 'unavailable', reason: 'no local page in this session' },
        outsidePaths: 0,
        empty: true,
      }
    }
    return {
      turns: [
        {
          turn: 1,
          runId: 'pi-1:r1',
          status: 'completed',
          startedAt: '2026-11-13T00:00:00Z',
          endedAt: '2026-11-13T00:00:01Z',
          calls: 1,
        },
      ],
      exec: [
        {
          turn: 1,
          runId: 'pi-1:r1',
          tool: 'write',
          label: 'out/report.md',
          status: 'done',
          seq: 3,
          at: '2026-11-13T00:00:00Z',
        },
      ],
      artifacts: [
        {
          path: 'D:/project/out/report.md',
          relative: 'out/report.md',
          bytes: 2048,
          turn: 1,
          runId: 'pi-1:r1',
          seq: 3,
          touches: 1,
          ext: 'md',
        },
      ],
      sources: [
        {
          number: 1,
          url: 'https://docs.example.com/a',
          host: 'docs.example.com',
          turn: 1,
          runId: 'pi-1:r1',
          origin: 'message',
        },
      ],
      usage: { input: 240, output: 9, cacheRead: 6912, cacheWrite: 0, cost: 0.5, events: 1 },
      browser: {
        state: 'local',
        reason: '',
        page: { relative: 'out/page.html', sandbox: '', html: '<h1>preview</h1>', bytes: 19 },
      },
      outsidePaths: 1,
      empty: false,
    }
  }

  function installRailHost(empty = false) {
    const calls: string[] = []
    const internals = {
      invoke: async (command: string) => {
        calls.push(command)
        if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
          return catalogSnapshot(false)
        }
        if (command === 'session_list') return { sessions: [], total: 0 }
        if (command === 'session_projects') return []
        if (command === 'session_open') return summary()
        if (command === 'session_events') return []
        if (command === 'session_send') return { runId: 'pi-1:r1', status: 'completed', eventCount: 0 }
        if (command === 'session_detail') return detail('see [1] for details')
        if (command === 'session_rail') return railView(empty)
        if (command === 'session_context_list') return []
        return null
      },
    }
    ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
      internals as unknown as TauriInternals
    return { calls }
  }

  async function sendOneTurn() {
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.type(composer, 'write the report')
    await userEvent.click(screen.getByTestId('chat-send'))
    // The rail is read after the turn, and the artifact pane is not the tab
    // that opens first: wait for the count it announces, then switch.
    await waitFor(() => expect(screen.getByRole('tab', { name: '输出物 1' })).toBeInTheDocument())
  }

  it('lists real artifacts and sends the reader back to the turn that wrote them', async () => {
    installRailHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    await sendOneTurn()

    await userEvent.click(screen.getByRole('tab', { name: '输出物 1' }))
    const row = screen.getByTestId('chat-rail-artifact')
    expect(row).toHaveTextContent('out/report.md')
    expect(row).toHaveTextContent('第 1 轮')
    expect(row).toHaveAttribute('data-turn', '1')

    // Clicking the row highlights the turn it came from: the artifact is
    // traceable, not just listed.
    const turn = document.querySelector('[data-testid="chat-message-agent"][data-run="pi-1:r1"]')
    expect(turn).not.toBeNull()
    await userEvent.click(row)
    await waitFor(() => expect(turn).toHaveClass('is-focused'))
  })

  it('shows execution rows with their turn and the runtime-reported usage', async () => {
    installRailHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    await sendOneTurn()

    await userEvent.click(screen.getByRole('tab', { name: '执行' }))
    const row = screen.getByTestId('chat-rail-exec')
    expect(row).toHaveAttribute('data-status', 'done')
    expect(row).toHaveTextContent('write')
    expect(row).toHaveTextContent('out/report.md')
    expect(screen.getByTestId('chat-rail-usage')).toHaveTextContent('$0.5000')
    // Paths outside the project are counted, and say so, instead of appearing
    // as rows the user could click.
    expect(screen.getByTestId('chat-rail-outside')).toHaveTextContent('1 个路径')
  })

  it('turns a [n] marker into the link to the same numbered source', async () => {
    installRailHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    await sendOneTurn()

    const cite = await screen.findByTestId('chat-cite')
    expect(cite).toHaveAttribute('data-number', '1')
    await userEvent.click(cite)
    const source = await screen.findByTestId('chat-rail-source')
    expect(source).toHaveAttribute('data-number', '1')
    expect(source).toHaveTextContent('https://docs.example.com/a')
  })

  it('renders a local page only inside an empty sandbox', async () => {
    installRailHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    await sendOneTurn()

    await userEvent.click(screen.getByRole('tab', { name: '浏览器' }))
    const frame = screen.getByTestId('chat-rail-browser-frame')
    // sandbox="" is the permission: no scripts, no same-origin, no navigation.
    expect(frame).toHaveAttribute('sandbox', '')
    expect(frame).toHaveAttribute('title', 'out/page.html')
  })

  it('says a pane is empty instead of showing placeholder rows', async () => {
    installRailHost(true)
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    const composer = screen.getByTestId('chat-composer')
    await userEvent.type(composer, 'hello')
    await userEvent.click(screen.getByTestId('chat-send'))
    await waitFor(() => expect(screen.getByRole('tab', { name: '执行' })).toBeInTheDocument())
    await userEvent.click(screen.getByRole('tab', { name: '执行' }))
    await waitFor(() => expect(screen.getByTestId('chat-rail-exec-empty')).toBeInTheDocument())

    expect(screen.queryAllByTestId('chat-rail-exec')).toHaveLength(0)
    await userEvent.click(screen.getByRole('tab', { name: '输出物 0' }))
    expect(screen.getByTestId('chat-rail-artifacts-empty')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: '引用 0' }))
    expect(screen.getByTestId('chat-rail-sources-empty')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: '浏览器' }))
    expect(screen.getByTestId('chat-rail-browser-unavailable')).toHaveTextContent(
      '没有可在本机预览的页面',
    )
  })
})

describe('chat export', () => {
  /** A host that answers `session_export`, and remembers what it was asked. */
  function installExportHost(existing = false) {
    const calls: string[] = []
    const seen: Record<string, unknown>[] = []
    const internals = {
      invoke: async (command: string, args?: Record<string, unknown>) => {
        calls.push(command)
        if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
          return catalogSnapshot(false)
        }
        if (command === 'session_list') return { sessions: [], total: 0 }
        if (command === 'session_projects') return []
        if (command === 'session_open') return summary()
        if (command === 'session_events') return []
        if (command === 'session_send') return { runId: 'pi-1:r1', status: 'completed', eventCount: 0 }
        if (command === 'session_detail') return detail('ok')
        if (command === 'session_rail') {
          return {
            turns: [],
            exec: [],
            artifacts: [],
            sources: [],
            browser: { state: 'unavailable', reason: '' },
            outsidePaths: 0,
            empty: true,
          }
        }
        if (command === 'session_export') {
          if (args) seen.push(args)
          const overwrite = args?.overwrite === true
          if (existing && !overwrite) {
            // The store's refusal, in the shape the host reports it.
            throw '{"code":"rejected","message":"weekly.md already exists; confirm the overwrite"}'
          }
          return {
            path: `D:/data/exports/weekly.${String(args?.format === 'markdown' ? 'md' : args?.format ?? 'html')}`,
            root: 'D:/data/exports',
            fileName: 'weekly.md',
            bytes: 128,
            sha256: 'a'.repeat(64),
            format: String(args?.format ?? 'html'),
            messages: 2,
            runs: 1,
            toolRows: 0,
            toolsIncluded: false,
          }
        }
        return null
      },
    }
    ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
      internals as unknown as TauriInternals
    return { calls, seen }
  }

  async function openExportDialog() {
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.type(composer, 'weekly report')
    await userEvent.click(screen.getByTestId('chat-send'))
    await waitFor(() => expect(screen.getByTestId('chat-message-agent')).toBeInTheDocument())
    const trigger = screen.getByRole('button', { name: '导出' })
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)
    return screen.getByRole('dialog', { name: '导出会话' })
  }

  it('writes the chosen format and reports where the file went', async () => {
    const host = installExportHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const dialog = await openExportDialog()
    await userEvent.selectOptions(within(dialog).getByLabelText('导出格式'), 'markdown')
    await userEvent.click(within(dialog).getByRole('button', { name: '导出到本机' }))

    await waitFor(() => expect(host.calls).toContain('session_export'))
    expect(host.seen[0]).toMatchObject({
      sessionId: 'pi-1',
      format: 'markdown',
      includeTools: false,
      overwrite: false,
    })
    // The path is what the user needs; the dialog closes on success.
    await waitFor(() => expect(screen.getByTestId('chat-notice')).toHaveTextContent('D:/data/exports/weekly.md'))
    expect(screen.queryByRole('dialog', { name: '导出会话' })).toBeNull()
  })

  it('asks before replacing a file and sends the confirm only after a yes', async () => {
    const host = installExportHost(true)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const dialog = await openExportDialog()
    await userEvent.click(within(dialog).getByRole('button', { name: '导出到本机' }))

    await waitFor(() => expect(confirm).toHaveBeenCalled())
    await waitFor(() => expect(host.seen).toHaveLength(2))
    expect(host.seen[0]).toMatchObject({ overwrite: false })
    expect(host.seen[1]).toMatchObject({ overwrite: true })
    confirm.mockRestore()
  })

  it('writes nothing when the overwrite is declined', async () => {
    const host = installExportHost(true)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const dialog = await openExportDialog()
    await userEvent.click(within(dialog).getByRole('button', { name: '导出到本机' }))

    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(host.seen).toHaveLength(1)
    expect(screen.getByTestId('chat-notice')).toHaveTextContent('已取消')
    confirm.mockRestore()
  })
})
