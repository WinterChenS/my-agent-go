// The context gauge while a turn is generating.
//
// pi's own reading (`get_session_stats`) costs a `pi` process, so the page only
// takes it when a conversation is opened a turn ends or a compaction finishes.
// During a generation nothing re-read it, and the ring sat at the pre-turn
// number on a conversation that was visibly growing — the field report was
// "执行中上下文统计没有任何变动".
//
// The runtime reports usage for every request it makes, and those reports are
// already in the stream the page polls. The last one is the current occupancy,
// so the gauge can follow the turn without a second process — and it must say
// that this is the in-flight number, not a finished measurement.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'
import type { SessionDetail, SessionEvent, SessionSummary } from '../client'

const SESSION = 'pi-live'

function summary(): SessionSummary {
  return {
    id: SESSION,
    runtimeId: 'pi',
    title: 'Live chat',
    status: 'running',
    nativeId: 'native-live',
    model: 'auto/gpt-5.6-sol',
    projectPath: 'D:\\work\\alpha',
    pinned: false,
    archived: false,
    messageCount: 1,
    parentId: '',
    branchAt: 0,
    createdAt: '2026-11-26T00:00:00Z',
    updatedAt: '2026-11-26T00:00:00Z',
    preview: 'keep going',
  }
}

function detail(running: boolean): SessionDetail {
  return {
    session: { ...summary(), status: running ? 'running' : 'idle' },
    messages: [
      {
        id: `${SESSION}:m1`,
        sessionId: SESSION,
        runId: `${SESSION}:r1`,
        role: 'user',
        text: 'keep going',
        seq: 1,
        createdAt: '2026-11-26T00:00:00Z',
      },
    ],
    runs: [
      {
        id: `${SESSION}:r1`,
        sessionId: SESSION,
        status: running ? 'running' : 'completed',
        startedAt: '2026-11-26T00:00:00Z',
        ...(running ? {} : { endedAt: '2026-11-26T00:00:05Z' }),
        prompt: 'keep going',
      },
    ],
  }
}

const catalog = {
  agents: [
    {
      id: 'default',
      name: 'Default Agent',
      runtimeId: 'pi',
      modelId: 'auto/gpt-5.6-sol',
      isDefault: true,
      updatedAt: '2026-11-26T00:00:00Z',
    },
  ],
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
      health: { checkedAt: '2026-11-26T00:00:00Z', latencyMs: 5, detail: null },
      lastSeen: '2026-11-26T00:00:00Z',
      firstSeen: '2026-11-26T00:00:00Z',
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
      verifiedAt: '2026-11-26T00:00:00Z',
      isDefault: true,
      enabled: true,
      updatedAt: '2026-11-26T00:00:00Z',
      hasCredential: true,
    },
  ],
  skills: [],
  tools: [],
  mcp: [],
  syncs: [],
  adapterFailures: [],
  scannedAt: '2026-11-26T00:00:00Z',
}

/** The window pi measured before this turn started. */
const WINDOW = 200_000

function event(seq: number, kind: SessionEvent['kind'], payload: Record<string, unknown>): SessionEvent {
  return { sessionId: SESSION, runId: `${SESSION}:r1`, seq, kind, at: '2026-11-26T00:00:00Z', payload }
}

/** What the host writes as the turn progresses: one report per model request. */
const stages: SessionEvent[][] = [
  [event(1, 'turn_started', { prompt: 'keep going' }), event(2, 'usage', { usage: { cacheRead: 0, input: 9023, output: 38 } })],
  [event(3, 'usage', { usage: { cacheRead: 8960, input: 139, output: 78 } })],
  [event(4, 'tool_call', { tool: 'bash', args: '{}', ok: true, output: '' })],
  [event(5, 'usage', { usage: { cacheRead: 9100, input: 40, output: 260 } })],
  [event(6, 'turn_completed', { status: 'completed' })],
]

const timers: number[] = []
let written: SessionEvent[] = []
let finished = false
let step = 0

/**
 * Write the turn a step at a time, on the test's command.
 *
 * Test-driven rather than clock-driven: the measured reading has to be on
 * screen *before* the turn starts producing reports, or the assertion races the
 * app's own boot. Once started, the steps are slower than one poll, because the
 * point is that each number is observable while the turn is still running.
 */
function startWriting() {
  if (step > 0) return
  const tick = () => {
    if (step >= stages.length) return
    written = [...written, ...stages[step]]
    if (stages[step].some((row) => row.kind === 'turn_completed')) finished = true
    step += 1
    timers.push(window.setTimeout(tick, STEP_MS))
  }
  tick()
}

const STEP_MS = 220

function installHost() {
  written = []
  finished = false
  step = 0

  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      const sessionId = String(args?.sessionId ?? '')
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') return catalog
      if (command === 'session_list') return { sessions: [summary()], total: 1 }
      if (command === 'session_projects') return []
      if (command === 'session_detail') return detail(!finished)
      if (command === 'session_events') {
        const since = Number(args?.since ?? 0)
        if (sessionId !== SESSION) return []
        return written.filter((row) => row.seq > since)
      }
      // The authoritative reading: taken when the conversation opened, and
      // again once the turn is over.
      if (command === 'session_context') {
        return finished
          ? { tokens: 12_500, contextWindow: WINDOW, percent: 6.25 }
          : { tokens: 5_000, contextWindow: WINDOW, percent: 2.5 }
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
}

const ring = () => screen.getByTestId('chat-context')

beforeEach(() => {
  window.localStorage.clear()
  window.localStorage.setItem('anvil.chat.session', SESSION)
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  while (timers.length) window.clearTimeout(timers.pop() as number)
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('the context gauge during a turn', () => {
  it('follows the runtime usage of the turn in flight', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    // Opened, before the turn has produced anything: the measured reading.
    await waitFor(() => expect(ring().title).toContain('5,000'))
    expect(ring().getAttribute('data-percent')).toBe('2.5')

    // The turn resumes and reports its first request: 9,023 + 38 in the window.
    startWriting()
    await waitFor(() => expect(ring().title).toContain('9,061'))
    // ...then the second: 139 + 8,960 cached + 78.
    await waitFor(() => expect(ring().title).toContain('9,177'))
    // ...then a tool call and another request: 40 + 9,100 cached + 260.
    await waitFor(() => expect(ring().title).toContain('9,400'))
    expect(ring().getAttribute('data-percent')).toBe('4.7')

    // The card says which moment the number is from: a gauge that moves during
    // generation must not read like a finished measurement.
    fireEvent.mouseEnter(screen.getByTestId('chat-context-wrap'))
    expect(screen.getByTestId('chat-context-card')).toHaveTextContent('已用 9,400 / 200K')
    expect(screen.getByTestId('chat-context-live')).toHaveTextContent('生成中')
  })

  it('hands the gauge back to the runtime measurement when the turn ends', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    // Nothing has been written yet, so this is the measurement alone.
    await waitFor(() => expect(ring().title).toContain('5,000'))
    startWriting()
    await waitFor(() => expect(ring().title).toContain('9,061'))

    // The turn ends, and the page re-measures instead of trusting the estimate.
    await waitFor(() => expect(ring().title).toContain('12,500'))
    fireEvent.mouseEnter(screen.getByTestId('chat-context-wrap'))
    expect(screen.getByTestId('chat-context-card')).toHaveTextContent('已用 12,500 / 200K')
    expect(screen.queryByTestId('chat-context-live')).toBeNull()
    expect(Number(ring().getAttribute('data-percent'))).toBeCloseTo(6.25, 1)
  })
})
