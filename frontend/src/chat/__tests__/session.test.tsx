import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'
import {
  describeSessionError,
  latestSeq,
  mergeDeltaText,
  type SessionDetail,
  type SessionEvent,
  type SessionSummary,
} from '../client'

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
