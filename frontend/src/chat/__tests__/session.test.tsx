import { cleanup, render, screen, waitFor } from '@testing-library/react'
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
}

function installHost(): Host {
  const calls: string[] = []
  let abortCalled = false
  const internals = {
    invoke: async (command: string) => {
      calls.push(command)
      if (command === 'catalog_snapshot') {
        return {
          runtimes: [
            {
              id: 'pi:1',
              runtimeId: 'pi',
              displayName: 'Pi Agent',
              version: '1.5.0',
              installPath: 'C:\\pi.CMD',
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
          models: [
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
          ],
          skills: [],
          tools: [],
          mcp: [],
          syncs: [],
          adapterFailures: [],
          scannedAt: '2026-11-13T00:00:00Z',
        }
      }
      if (command === 'session_list') return { sessions: [], total: 0 }
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
  return { calls, aborted: () => abortCalled }
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
