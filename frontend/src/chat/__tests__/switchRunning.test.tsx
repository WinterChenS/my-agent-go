// Leaving a conversation while its turn is still generating.
//
// The page keeps one live-turn slot, and it belongs to a conversation. Two
// reports from the field pinned down what happened when the reader walked away
// mid-turn and then clicked around:
//
//   * the new conversation showed the *previous* conversation's turn — the
//     agent's badge with the typing dots under it, and the composer in "stop"
//     mode for a run that is not here;
//   * after clicking back and forth a few times, the rows stopped responding:
//     the sidebar said one conversation was open while the page kept showing
//     another one.
//
// Both are the same mistake: the live turn was treated as a property of the
// page instead of a property of the conversation it belongs to.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'
import type { SessionDetail, SessionEvent, SessionSummary } from '../client'

const ALPHA = 'pi-a'
const BETA = 'pi-b'

function summary(id: string, title: string): SessionSummary {
  return {
    id,
    runtimeId: 'pi',
    title,
    status: 'idle',
    nativeId: `native-${id}`,
    model: 'auto/gpt-5.6-sol',
    projectPath: 'D:\\work\\alpha',
    pinned: false,
    archived: false,
    messageCount: 2,
    parentId: '',
    branchAt: 0,
    createdAt: '2026-11-13T00:00:00Z',
    updatedAt: '2026-11-13T00:00:00Z',
    preview: '',
  }
}

/** One settled exchange, so each conversation is recognisable on screen. */
function detail(id: string): SessionDetail {
  const question = id === ALPHA ? 'alpha question' : 'beta question'
  const answer = id === ALPHA ? 'alpha answer' : 'beta answer'
  return {
    session: { ...summary(id, id === ALPHA ? 'Alpha chat' : 'Beta chat'), messageCount: 2 },
    messages: [
      {
        id: `${id}:m1`,
        sessionId: id,
        runId: `${id}:r1`,
        role: 'user',
        text: question,
        seq: 1,
        createdAt: '2026-11-13T00:00:00Z',
      },
      {
        id: `${id}:m2`,
        sessionId: id,
        runId: `${id}:r1`,
        role: 'assistant',
        text: answer,
        seq: 2,
        createdAt: '2026-11-13T00:00:01Z',
      },
    ],
    runs: [
      {
        id: `${id}:r1`,
        sessionId: id,
        status: 'completed',
        startedAt: '2026-11-13T00:00:00Z',
        endedAt: '2026-11-13T00:00:01Z',
        prompt: question,
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
      updatedAt: '2026-11-13T00:00:00Z',
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
      enabled: true,
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

/** `true` once the turn has been sent, so the poll has something to stream. */
let streaming = false
const streamed: SessionEvent[] = []
/** How long a ledger read takes, so a second click can interrupt it. */
let readDelay = 0
/** How many turns the page has actually handed to the host. */
let sendCount = 0

function installHost() {
  streaming = false
  readDelay = 0
  sendCount = 0
  streamed.length = 0
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      const sessionId = String(args?.sessionId ?? '')
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') return catalog
      if (command === 'session_list') {
        return {
          sessions: [summary(ALPHA, 'Alpha chat'), summary(BETA, 'Beta chat')],
          total: 2,
        }
      }
      if (command === 'session_projects') return []
      if (command === 'session_open') return summary('pi-new', 'New chat')
      if (command === 'session_detail') {
        if (readDelay) await new Promise((resolve) => setTimeout(resolve, readDelay))
        const base = detail(sessionId === BETA ? BETA : ALPHA)
        if (sessionId !== ALPHA || !streaming) return base
        // The running turn's prompt is part of this conversation's transcript
        // from the first frame; the answer is not written yet.
        return {
          ...base,
          session: { ...base.session, status: 'running' as const },
          messages: [
            ...base.messages,
            {
              id: `${ALPHA}:m3`,
              sessionId: ALPHA,
              runId: `${ALPHA}:r2`,
              role: 'user' as const,
              text: 'keep going',
              seq: 3,
              createdAt: '2026-11-13T00:00:02Z',
            },
          ],
          runs: [
            ...base.runs,
            {
              id: `${ALPHA}:r2`,
              sessionId: ALPHA,
              status: 'running' as const,
              startedAt: '2026-11-13T00:00:02Z',
              prompt: 'keep going',
            },
          ],
        }
      }
      if (command === 'session_events') {
        const since = Number(args?.since ?? 0)
        if (sessionId !== ALPHA || !streaming) return []
        // Honest cursor: the same rows are never handed out twice, or the
        // streamed turn would grow every poll.
        return streamed.filter((row) => row.seq > since)
      }
      if (command === 'session_send') {
        sendCount += 1
        streaming = true
        streamed.push(
          { sessionId: ALPHA, runId: `${ALPHA}:r2`, seq: 3, kind: 'turn_started', at: 'x', payload: {} },
          {
            sessionId: ALPHA,
            runId: `${ALPHA}:r2`,
            seq: 4,
            kind: 'text_delta',
            at: 'x',
            payload: { delta: 'half an answer' },
          },
        )
        // The turn never finishes inside this test: the reader is watching a
        // generation, which is exactly the state they walk away from.
        await new Promise(() => {})
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
}

const composer = () => screen.getByTestId('chat-composer')
const openRow = (title: string) =>
  userEvent.click(screen.getByRole('button', { name: new RegExp(title) }))

async function openAlphaAndSend() {
  await waitFor(() => expect(composer()).not.toBeDisabled())
  await openRow('Alpha chat')
  await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent('alpha answer'))
  await userEvent.type(composer(), 'keep going')
  await userEvent.click(screen.getByTestId('chat-send'))
  // The turn is live: the streaming bubble and the stop button are on screen.
  await waitFor(() => expect(screen.getByTestId('chat-message-streaming')).toBeInTheDocument())
  expect(screen.getByTestId('chat-send')).toHaveAttribute('data-mode', 'stop')
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('leaving a conversation mid-turn', () => {
  it('does not lend the running turn to the conversation the reader opens', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await openAlphaAndSend()

    await openRow('Beta chat')
    await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent('beta answer'))

    // The turn belongs to Alpha. Beta has no live run, so nothing of Alpha's
    // may show here: no agent badge with typing dots, no stop button, and no
    // spinner claimed on Beta's row.
    expect(screen.queryByTestId('chat-message-streaming')).toBeNull()
    await waitFor(() => expect(screen.getByTestId('chat-send')).toHaveAttribute('data-mode', 'send'))
    expect(screen.queryByTestId(`chat-history-running-${BETA}`)).toBeNull()
  })

  it('lands on the conversation the reader asked for last', async () => {
    // A ledger read is an IPC round trip, not a synchronous step. Clicking a
    // second row while the first read is still in flight used to be dropped
    // when that second row was the one already on screen — the id matched, so
    // nothing ran — and the first read then took the view. The reader's last
    // click has to win, and clicking the row that is still on screen has to
    // count as a click.
    installHost()
    // Long enough that both clicks land while the first read is still out.
    readDelay = 200
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(composer()).not.toBeDisabled())
    await openRow('Alpha chat')
    await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent('alpha answer'))

    fireEvent.click(screen.getByRole('button', { name: /Beta chat/ }))
    // Still on Alpha, so this is the row the reader can actually click — and a
    // synchronous click, because a real one takes no time at all.
    fireEvent.click(screen.getByRole('button', { name: /Alpha chat/ }))

    // Give Beta's interrupted read more than enough time to land.
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(screen.getByTestId('chat-thread')).toHaveTextContent('alpha answer')
    expect(screen.getByTestId('chat-thread')).not.toHaveTextContent('beta answer')
    expect(screen.getByRole('button', { name: /Alpha chat/ }).className).toContain('is-active')

    // And the row that lost is still openable — the click was not swallowed.
    await openRow('Beta chat')
    await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent('beta answer'))
  })

  it('picks the turn back up when the reader returns to it', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await openAlphaAndSend()

    await openRow('Beta chat')
    await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent('beta answer'))
    expect(screen.queryByTestId('chat-message-streaming')).toBeNull()

    // The run is still going on the host, so coming back must show it again —
    // with the text it produced while the reader was away, not a blank bubble.
    await openRow('Alpha chat')
    await waitFor(() => expect(screen.getByTestId('chat-message-streaming')).toBeInTheDocument())
    expect(screen.getByTestId('chat-message-streaming')).toHaveTextContent('half an answer')
    expect(screen.getByTestId('chat-send')).toHaveAttribute('data-mode', 'stop')
  })

  it('does not send a message queued in one conversation into another', async () => {
    // A queued message is a promise about the *next* turn of *this*
    // conversation. Walking away from a running turn made `running` fall, and
    // falling is exactly the signal the queue listens to — so the head went out
    // into whichever conversation the reader had opened. It reads like the app
    // answering a message they wrote somewhere else.
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await openAlphaAndSend()

    await userEvent.type(composer(), 'second thought{enter}')
    await waitFor(() => expect(screen.getByTestId('chat-queue-item')).toHaveTextContent('second thought'))
    expect(sendCount).toBe(1)

    await openRow('Beta chat')
    await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent('beta answer'))
    // Give a wrongly-flushed queue every chance to reach the host.
    await new Promise((resolve) => setTimeout(resolve, 250))

    expect(sendCount).toBe(1)
    expect(screen.queryByTestId('chat-queue-item')).toBeNull()
  })

  it('keeps switching conversations working when the reader clicks around', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await openAlphaAndSend()

    // Back and forth, the way a reader does when comparing two answers.
    for (const [title, words] of [
      ['Beta chat', 'beta answer'],
      ['Alpha chat', 'alpha answer'],
      ['Beta chat', 'beta answer'],
      ['Alpha chat', 'alpha answer'],
      ['Beta chat', 'beta answer'],
    ] as const) {
      await openRow(title)
      await waitFor(() => expect(screen.getByTestId('chat-thread')).toHaveTextContent(words))
      expect(
        screen.getByRole('button', { name: new RegExp(title) }).className,
      ).toContain('is-active')
    }
  })
})
