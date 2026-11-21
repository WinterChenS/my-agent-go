import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'
import {
  describeSessionError,
  latestSeq,
  mergeDeltaText,
  type RailView,
  type SessionDetail,
  type SessionEvent,
  type SessionSummary,
} from '../client'

const SKILL_PATH = 'C:/Users/Administrator/.pi/skills/code-review'

function event(seq: number, kind: SessionEvent['kind'], payload: Record<string, unknown> = {}): SessionEvent {
  return { sessionId: 'pi-1', runId: 'pi-1:r1', seq, kind, at: '2026-11-13T00:00:00Z', payload }
}

/** Open a composer listbox and click the option carrying this value. */
async function pickMenu(triggerId: string, menuId: string, value: string) {
  await userEvent.click(screen.getByTestId(triggerId))
  const menu = await screen.findByTestId(menuId)
  const option = within(menu)
    .getAllByRole('option')
    .find((row) => row.getAttribute('data-value') === value)
  if (!option) throw new Error(`no option "${value}" in ${menuId}`)
  await userEvent.click(option)
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
      enabled: true,
      updatedAt: '2026-11-13T00:00:00Z',
      hasCredential: true,
    },
    {
      runtimeId: 'pi',
      modelId: 'auto/still-off',
      displayName: 'Never enabled',
      origin: 'discovered',
      protocol: 'openai',
      endpoint: null as unknown as string,
      verified: false,
      verifiedAt: null as unknown as string,
      isDefault: false,
      enabled: false,
      updatedAt: '2026-11-13T00:00:00Z',
      hasCredential: false,
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
      enabled: true,
      updatedAt: '2026-11-13T00:00:00Z',
      hasCredential: false,
    })
  }
  return {
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
    models,
    skills: [],
    tools: [],
    mcp: [],
    syncs: [],
    adapterFailures: [],
    scannedAt: '2026-11-13T00:00:00Z',
  }
}

function installHost(
  options: {
    /** What `pi_subagent_status` answers; `null` means "no host answer". */
    subagent?: Record<string, unknown> | null
    /** What `pi_subagent_install` answers. */
    install?: Record<string, unknown>
    /** Leave `session_send` unanswered, so only the page's own state is visible. */
    hangSend?: boolean
  } = {},
): Host {
  const calls: string[] = []
  const lastArgs = new Map<string, unknown>()
  let abortCalled = false
  let rescanned = false
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push(command)
      if (args) lastArgs.set(command, args)
      if (command === 'pi_subagent_status') return options.subagent ?? null
      if (command === 'pi_subagent_install') return options.install ?? null
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
      if (command === 'workspace_defaults') {
        return { defaultWorkspace: 'C:////Users////A////Anvil', projectsRoot: 'C:////Users////A////Anvil////Projects' }
      }
      if (command === 'workspace_ensure') return String(args?.path ?? '')
      if (command === 'workspace_pick') return 'D:////picked////folder'
      if (command === 'workspace_pick_file') return 'D:////picked////folder////spec.txt'
      if (command === 'workspace_pick_files') {
        // Two paths in one answer: the composer must attach both, not one.
        return ['D:/picked/folder/spec.txt', 'D:/picked/folder/plan.md']
      }
      if (command === 'workspace_create') return `${String(args?.name ?? '')} (created)`
      if (command === 'session_events') {
        return [
          event(1, 'turn_started', { prompt: 'Reply with exactly: ok' }),
          event(2, 'text_delta', { delta: 'o' }),
          event(3, 'text_delta', { delta: 'k' }),
        ]
      }
      if (command === 'session_send') {
        if (options.hangSend) {
          // Never answers: whatever the page shows now came from its own state,
          // not from reading the ledger back after the turn.
          await new Promise(() => {})
        }
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

/** Catalog plus a session: enough to drive the composer's Skill pickers. */
function installInventoryHost(
  extra: Array<{ runtimeId: string; name: string; path: string }> = [],
): Host {
  const calls: string[] = []
  const lastArgs = new Map<string, unknown>()
  const skill = (runtimeId: string, name: string, path: string) => ({
    runtimeId,
    name,
    description: `${name} skill`,
    root: path.replace(/\/[^/]+$/, ''),
    path,
    state: 'ready',
    reason: null as string | null,
    updatedAt: '2026-11-13T00:00:00Z',
  })
  const skills = [skill('pi', 'code-review', SKILL_PATH), ...extra.map((row) => skill(row.runtimeId, row.name, row.path))]
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push(command)
      if (args) lastArgs.set(command, args)
      // The page mounts, reads the snapshot, then re-reads it after the
      // inventory sync. Every one of those replies must carry the skills, or
      // the last reply (which the store adopts) blanks the list.
      if (
        command === 'catalog_snapshot' ||
        command === 'catalog_refresh' ||
        command === 'catalog_sync_inventory' ||
        command === 'catalog_sync'
      ) {
        return { ...catalogSnapshot(false), skills }
      }
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_projects') return []
      if (command === 'session_context_list') {
        return [
          { path: 'D:/work/alpha/src/main.rs', relative: 'src/main.rs', kind: 'file', bytes: 120 },
        ]
      }
      if (command === 'session_open') return { ...summary(), messageCount: 0 }
      if (command === 'session_events') return [event(1, 'turn_started', { prompt: 'first turn' })]
      if (command === 'session_send') {
        await new Promise((resolve) => setTimeout(resolve, 200))
        return { runId: 'pi-1:r1', status: 'completed', eventCount: 1 }
      }
      if (command === 'session_detail') return detail('done')
      if (command === 'session_abort') return true
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return { calls, aborted: () => false, args: (command) => lastArgs.get(command) }
}

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
    const openPicker = async () => {
      await userEvent.click(picker())
      return screen.findByTestId('chat-model-menu')
    }
    let menu = await openPicker()
    expect(menu).not.toHaveTextContent('auto/glm-5.3-flash')
    await userEvent.keyboard('{Escape}')

    const sync = screen.getByTestId('chat-models-sync')
    expect(sync).toHaveAccessibleName('同步模型列表')
    await userEvent.click(sync)

    await waitFor(() => expect(host.calls).toContain('catalog_refresh'))
    // The picker is rebuilt from the rescan, not from the startup snapshot.
    await waitFor(async () => {
      menu = await openPicker()
      expect(menu).toHaveTextContent('auto/glm-5.3-flash')
    })
  })

  it('offers only enabled models, grouped with the agents that bind them', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const picker = () => screen.getByTestId('chat-model-select')
    await waitFor(() => expect(picker()).not.toBeDisabled())
    await userEvent.click(picker())
    const menu = await screen.findByTestId('chat-model-menu')

    // The default agent is derived from the default model, so it is always
    // there and always first.
    expect(menu).toHaveTextContent('默认 Agent')
    expect(menu).toHaveTextContent('pi · auto/gpt-5.6-sol')

    // A model the user never turned on stays out of the picker: discovery
    // reports the machine, not the choice.
    expect(menu).not.toHaveTextContent('auto/still-off')
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
    expect(screen.getByTestId('chat-project-trigger')).toBeInTheDocument()

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

  it('clears the transcript when a new conversation is started', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.type(composer, 'first turn')
    await userEvent.click(screen.getByTestId('chat-send'))
    await waitFor(() => expect(screen.getByTestId('chat-message-agent')).toBeInTheDocument())

    // "New conversation" used to bounce back: the page re-published the id it
    // was still holding, the sidebar selected it again, and the blank page
    // came back filled with the previous transcript.
    await userEvent.click(screen.getByTestId('chat-history-new'))

    await waitFor(() => expect(screen.getByTestId('chat-project-trigger')).toBeInTheDocument())
    expect(screen.queryByTestId('chat-message-agent')).toBeNull()
    expect(screen.queryByTestId('chat-message-user')).toBeNull()
  })
})

describe('coming back to the conversation', () => {
  /** A session whose last run is still going when the page opens. */
  function installRunningHost(): Host {
    const calls: string[] = []
    const lastArgs = new Map<string, unknown>()
    let polls = 0
    let finished = false
    const internals = {
      invoke: async (command: string, args?: Record<string, unknown>) => {
        calls.push(command)
        if (args) lastArgs.set(command, args)
        if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
          return catalogSnapshot(false)
        }
        if (command === 'session_list') {
          return { sessions: [{ ...summary(), status: finished ? 'idle' : 'running' }], total: 1 }
        }
        if (command === 'session_projects') {
          return [
            { path: 'D:////project', name: 'project', sessionCount: 1, lastUpdated: '2026-11-13T00:00:00Z' },
          ]
        }
        if (command === 'session_events') {
          polls += 1
          // The first read is the ledger tail the page reattaches from; the
          // turn then runs for a few ticks and reports itself finished.
          if (polls === 1) {
            // Everything this turn did before the page came back: it reasoned,
            // ran a command, and started writing an answer.
            return [
              event(1, 'turn_started', { prompt: 'long task' }),
              event(2, 'thinking', { delta: '先看目录' }),
              event(3, 'tool_call', { tool: 'bash', args: 'ls' }),
              event(4, 'tool_result', { tool: 'bash', ok: true, output: 'start.bat' }),
              event(5, 'text_delta', { delta: 'half ' }),
            ]
          }
          if (polls < 6) return []
          finished = true
          return [event(6, 'turn_completed', { status: 'completed' })]
        }
        if (command === 'session_detail') {
          return {
            ...detail('half done'),
            runs: [
              {
                id: 'pi-1:r1',
                sessionId: 'pi-1',
                status: finished ? ('completed' as const) : ('running' as const),
                startedAt: new Date(Date.now() - 4000).toISOString(),
                prompt: 'long task',
              },
            ],
          }
        }
        if (command === 'session_rail') {
          return {
            turns: [],
            exec: [],
            artifacts: [],
            sources: [],
            references: [],
            browser: { state: 'unavailable', reason: 'no local page in this session' },
            outsidePaths: 0,
            empty: true,
          }
        }
        return null
      },
    }
    ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
      internals as unknown as TauriInternals
    return { calls, aborted: () => false, args: (command) => lastArgs.get(command) }
  }

  it('reopens the conversation the reader left, instead of a blank page', async () => {
    const host = installHost()
    window.localStorage.setItem('anvil.chat.session', 'pi-a')
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    await waitFor(() => expect(host.args('session_detail')).toMatchObject({ sessionId: 'pi-a' }))
    // The stored transcript is on screen again, not the empty state.
    expect(await screen.findByTestId('chat-message-agent')).toHaveTextContent('ok')
    // The workspace chip belongs to the blank page only: a real conversation
    // hides it above the field.
    expect(screen.queryByTestId('chat-project-trigger')).toBeNull()
  })

  it('follows a run that is still going instead of showing a stopped turn', async () => {
    const host = installRunningHost()
    window.localStorage.setItem('anvil.chat.session', 'pi-1')
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    // The page was away while the host kept writing this turn: it reattaches
    // and shows it as still generating rather than as a conversation that
    // stopped after the prompt.
    const bubble = await screen.findByTestId('chat-message-streaming')
    // The name no longer carries a "generating" badge beside it: the live
    // bubble itself and the typing dots say the turn is still running.
    expect(bubble.querySelector('.chat-agent-head')?.querySelector('.chat-badge')).toBeNull()
    // The fixture's ledger already carries this run's assistant message while
    // the run is still marked running. The streaming bubble owns that run —
    // there must be no second, "settled" copy of it beside the generating one.
    expect(screen.queryByTestId('chat-message-agent')).toBeNull()
    // Resume, not restart: the reasoning and the tool call this turn already
    // produced are rebuilt from the ledger, in their original order.
    expect(bubble).toHaveTextContent('先看目录')
    expect(bubble).toHaveTextContent('half')
    expect(bubble.querySelector('[data-testid="chat-live-tool"]')).not.toBeNull()
    const blocks = Array.from(
      bubble.querySelectorAll<HTMLElement>('[data-testid="chat-live-thinking"], [data-testid="chat-live-tool"]'),
    )
    expect(blocks.map((block) => block.dataset.testid)).toEqual([
      'chat-live-thinking',
      'chat-live-tool',
    ])

    await waitFor(() => expect(screen.getByTestId('chat-message-agent')).toHaveTextContent('half done'), {
      timeout: 3000,
    })
    // The reload that swaps the streamed bubble for the stored message lands a
    // moment before the turn is marked idle, so wait for both.
    await waitFor(() => expect(screen.queryByTestId('chat-message-streaming')).toBeNull())
    expect(screen.queryByTestId('chat-live-failure')).toBeNull()
    expect(host.calls.filter((call) => call === 'session_list').length).toBeGreaterThan(1)
  })

})

describe('the shell sidebar', () => {
  it('lists conversations while another page is open', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/settings']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    // The list used to render only on `/chat`, so leaving the conversation hid
    // every other conversation with it.
    const panel = await screen.findByTestId('chat-history')
    expect(await within(panel).findByText('Alpha chat')).toBeInTheDocument()
    expect(within(panel).getByText('Beta chat')).toBeInTheDocument()
  })

  it('starts a new conversation from the 对话 nav entry', async () => {
    const user = userEvent.setup()
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const panel = await screen.findByTestId('chat-history')
    await user.click(await within(panel).findByText('Alpha chat'))
    await waitFor(() =>
      expect(host.args('session_detail')).toMatchObject({ sessionId: 'pi-a' }),
    )
    await waitFor(() => expect(screen.queryByTestId('chat-project-trigger')).toBeNull())

    // The nav entry means "start a conversation". Clicking it used to reopen the
    // last session, which looked like a dead button when that session was
    // already on screen; resuming belongs to the history list.
    await user.click(screen.getByRole('link', { name: '对话' }))
    await waitFor(() => expect(screen.getByTestId('chat-project-trigger')).toBeInTheDocument())
  })

  it('opens a conversation from another page', async () => {
    const user = userEvent.setup()
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/settings']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const panel = await screen.findByTestId('chat-history')
    await user.click(await within(panel).findByText('Alpha chat'))

    // The row lives in the shell, so opening it has to move the window to the
    // chat route as well — a list that only highlighted would leave the reader
    // on the settings page.
    expect(await screen.findByTestId('chat-page')).toBeInTheDocument()
    await waitFor(() =>
      expect(host.args('session_detail')).toMatchObject({ sessionId: 'pi-a' }),
    )
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

  it('lists stored sessions in the shell sidebar and opens one from there', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())

    // The sidebar is the history the user sees without opening a drawer. It
    // used to be rendered with sample data only, so a real conversation never
    // showed up there.
    const panel = screen.getByTestId('chat-history')
    await waitFor(() => expect(within(panel).getByText('Alpha chat')).toBeInTheDocument())
    expect(within(panel).getByText('Beta chat')).toBeInTheDocument()

    await userEvent.click(within(panel).getByText('Alpha chat'))
    await waitFor(() => expect(host.args('session_detail')).toMatchObject({ sessionId: 'pi-a' }))
  })

  it('re-reads the list after a turn so the sidebar gains the new session', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    const before = host.calls.filter((call) => call === 'session_list').length

    const composer = screen.getByTestId('chat-composer')
    await userEvent.type(composer, 'hi')
    await userEvent.click(screen.getByTestId('chat-send'))
    await waitFor(() =>
      expect(host.calls.filter((call) => call === 'session_list').length).toBeGreaterThan(before),
    )
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
    await userEvent.click(within(screen.getByTestId('chat-history-action-menu')).getByText('分支会话'))
    await waitFor(() => expect(host.calls).toContain('session_fork'))
    expect(host.args('session_fork')).toMatchObject({ sessionId: 'pi-a' })

    // The branch is selected by the host's reply, and the panel stays usable.
    await waitFor(() => expect(host.calls).toContain('session_detail'))
    await userEvent.click(within(row as HTMLElement).getByLabelText('会话操作'))
    await userEvent.click(within(screen.getByTestId('chat-history-action-menu')).getByText('压缩上下文'))
    await waitFor(() => expect(host.calls).toContain('session_compact'))
    // The last exchange stays verbatim. It used to be six, which on a short
    // conversation is "nothing to compact" — the action did nothing and said
    // nothing, so it read as broken.
    expect(host.args('session_compact')).toMatchObject({ sessionId: 'pi-a', keepLast: 2 })
  })
})

function installToolHost(options: {
  fail?: boolean
  thinking?: boolean
  /** Two reasoning bursts with a tool call between them. */
  interleave?: boolean
  answer?: string
} = {}): Host {
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
        // Serve the batch to a poll that starts from the beginning of the
        // ledger, not to "the first call": a poll left over from a previous
        // test must not swallow this test's batch.
        polls += 1
        const after = Number((args as { afterSeq?: number } | undefined)?.afterSeq ?? 0)
        if (after > 0) return []
        if (options.interleave) {
          // Text, reasoning, a tool call, then more text: the order a real turn
          // produces when the model narrates before and after acting.
          return [
            event(1, 'turn_started', { prompt: 'run it' }),
            event(2, 'text_delta', { delta: '先说明一下。' }),
            event(3, 'thinking', { delta: '看看目录' }),
            event(4, 'tool_call', { tool: 'command', args: 'echo anvil' }),
            event(5, 'tool_result', { tool: 'command', ok: true, output: 'anvil' }),
            event(6, 'text_delta', { delta: '结论是用 start.bat 启动。' }),
            // The shape pi really writes: `input`/`output`, no `inputTokens`.
            event(7, 'usage', { usage: { input: 1200, output: 80, cacheRead: 0, totalTokens: 1280 } }),
          ]
        }
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
          ...(options.thinking
            ? [event(5, 'thinking', { delta: '先读文件再回答' })]
            : []),
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
      if (command === 'session_detail') return detail(options.answer ?? 'done')
      if (command === 'session_approve') return null
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return { calls, aborted: () => false, args: (command) => lastArgs.get(command) }
}

/** Host whose poll stops before the turn's tail is written, like the real one. */
function installReplayHost(): Host {
  const calls: string[] = []
  const lastArgs = new Map<string, unknown>()
  let turns = 0
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push(command)
      if (args) lastArgs.set(command, args)
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') return catalogSnapshot(false)
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_open') return { ...summary(), messageCount: 0 }
      if (command === 'session_events') {
        // Turn one's rows only become readable after the turn resolved, which
        // is what a poll that stops with the turn leaves behind.
        if (turns === 0) return []
        return [event(1, 'text_delta', { delta: '第一轮的回答' })]
      }
      if (command === 'session_send') {
        const turn = turns
        turns += 1
        await new Promise((resolve) => setTimeout(resolve, turn === 0 ? 40 : 500))
        return { runId: `pi-1:r${turn + 1}`, status: 'completed', eventCount: 1 }
      }
      if (command === 'session_abort') return true
      if (command === 'session_detail') return detail(turns >= 2 ? '第二轮的回答' : '第一轮的回答')
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return { calls, aborted: () => false, args: (command) => lastArgs.get(command) }
}

describe('each turn only shows its own output', () => {
  it('does not replay the previous answer into the next turn', async () => {
    installReplayHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const send = async (text: string) => {
      const composer = screen.getByTestId('chat-composer')
      await waitFor(() => expect(composer).not.toBeDisabled())
      await userEvent.clear(composer)
      await userEvent.type(composer, text)
      await userEvent.click(screen.getByTestId('chat-send'))
    }

    await send('first')
    await waitFor(() =>
      expect(screen.getByTestId('chat-message-agent')).toHaveTextContent('第一轮的回答'),
    )

    await send('second')
    const streaming = await screen.findByTestId('chat-message-streaming')
    // Several poll ticks: the leftover rows from turn one are readable now.
    await new Promise((resolve) => setTimeout(resolve, 320))
    expect(within(streaming).queryByText(/第一轮的回答/)).toBeNull()

    // Let the turn finish: a poll still running after the test would read the
    // *next* test's host and eat its first batch.
    await waitFor(
      () => expect(screen.queryByTestId('chat-message-streaming')).toBeNull(),
      { timeout: 4000 },
    )
  })
})

describe('chat context and approvals', () => {
  async function sendOnce(text: string) {
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.type(composer, text)
    await userEvent.click(screen.getByTestId('chat-send'))
  }

  it('runs a new conversation in the workspace chosen in the project dialog', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    // The chip is the way in: it names the directory the next turn will use.
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)

    const current = await screen.findByTestId('workspace-current')
    expect(current).toHaveTextContent('默认工作区')

    const rows = screen.getAllByTestId('workspace-option')
    const alpha = rows.find((row) => (row.dataset.path ?? '').includes('alpha'))
    expect(alpha).toBeDefined()
    await userEvent.click(alpha!)
    await waitFor(() => expect(trigger).toHaveTextContent('alpha'))
    // A successful switch closes the dialog so the chip shows the new project.
    await waitFor(() => expect(screen.queryByTestId('workspace-current')).toBeNull())

    await sendOnce('hello')

    // The directory travels with the session: this is what puts the
    // conversation under that project in the sidebar.
    await waitFor(() => expect(host.args('session_open')).toMatchObject({ cwd: alpha!.dataset.path }))
  })

  it('turns the browse button into a real folder pick and switches to it', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)

    await userEvent.click(screen.getByTestId('workspace-browse'))
    await waitFor(() => expect(host.calls).toContain('workspace_pick'))
    await waitFor(() => expect(trigger).toHaveTextContent('folder'))
  })

  it('dismisses the inline new-project field when clicking outside it', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)

    const toggle = screen.getByTestId('workspace-new')
    expect(screen.queryByTestId('workspace-name')).toBeNull()
    await userEvent.click(toggle)
    expect(screen.getByTestId('workspace-name')).toBeTruthy()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')

    // A pointer press on other dialog content closes just the popover.
    await userEvent.click(screen.getByTestId('workspace-current'))
    expect(screen.queryByTestId('workspace-name')).toBeNull()
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
  })

  it('creates a project from the inline field and switches to it', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)

    await userEvent.click(screen.getByTestId('workspace-new'))
    const input = screen.getByTestId('workspace-name')
    await userEvent.type(input, 'demo{Enter}')
    await waitFor(() => expect(host.calls).toContain('workspace_create'))
    expect(host.args('workspace_create')).toMatchObject({ name: 'demo' })
    await waitFor(() => expect(trigger).toHaveTextContent('demo'))
  })

  it('offers folder and file pickers from the + menu', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)

    await userEvent.click(screen.getByTestId('workspace-new'))
    expect(screen.getByTestId('workspace-add-folder')).toBeTruthy()
    expect(screen.getByTestId('workspace-add-file')).toBeTruthy()

    await userEvent.click(screen.getByTestId('workspace-add-file'))
    await waitFor(() => expect(host.calls).toContain('workspace_pick_file'))
    // The parent directory of the chosen file becomes the workspace.
    await waitFor(() => expect(trigger).toHaveTextContent('folder'))
    await waitFor(() => expect(screen.queryByTestId('workspace-current')).toBeNull())
  })

  it('closes the project dialog when the backdrop is pressed', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)
    expect(screen.getByTestId('workspace-current')).toBeTruthy()

    const backdrop = document.querySelector('.chat-backdrop') as HTMLElement
    await userEvent.click(backdrop)
    await waitFor(() => expect(screen.queryByTestId('workspace-current')).toBeNull())
  })

  it('can promote the current directory to the default workspace', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = await screen.findByTestId('chat-project-trigger')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    await userEvent.click(trigger)

    const rows = screen.getAllByTestId('workspace-option')
    const alpha = rows.find((row) => (row.dataset.path ?? '').includes('alpha'))
    await userEvent.click(alpha!)
    await waitFor(() => expect(screen.queryByTestId('workspace-current')).toBeNull())

    await userEvent.click(trigger)
    const setDefault = await screen.findByTestId('workspace-set-default')
    await userEvent.click(setDefault)
    await waitFor(() => expect(host.calls).toContain('workspace_ensure'))
    // The chip shows the "default" label now; the chosen directory is persisted.
    await waitFor(() =>
      expect(window.localStorage.getItem('anvil.chat.defaultWorkspace')).toBe(alpha!.dataset.path),
    )
    await waitFor(() => expect(trigger).toHaveTextContent('默认工作区'))
  })

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
    // The timeline is off by default; this case explicitly opts in.
    window.localStorage.setItem('anvil.chat.showTimeline', '1')
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

  it('asks before switching to full control, and the control reads as a warning', async () => {
    window.localStorage.removeItem('anvil.chat.permission')
    const host = installToolHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = screen.getByTestId('chat-permission-select')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    // "Medium" is the safe default; "full" is the trust-workspace answer the
    // user picks so a path outside the project stops raising a card.
    expect(trigger).toHaveAttribute('data-value', 'medium')
    expect(trigger.className).not.toContain('is-danger')

    // Only the full row is red — the other three modes stay neutral.
    await userEvent.click(trigger)
    const menu = await screen.findByTestId('chat-permission-menu')
    const rows = within(menu).getAllByRole('option')
    expect(rows.filter((row) => row.className.includes('is-danger')).map((row) => row.dataset.value)).toEqual(['full'])
    await userEvent.keyboard('{Escape}')

    // Picking full raises the warning instead of switching.
    await pickMenu('chat-permission-select', 'chat-permission-menu', 'full')
    const points = await screen.findByTestId('chat-trust-points')
    expect(points).toHaveTextContent('读取工作区之外的路径不再逐次询问')
    expect(trigger).toHaveAttribute('data-value', 'medium')
    expect(window.localStorage.getItem('anvil.chat.permission')).not.toBe('full')

    // Declining leaves the picker where it was.
    await userEvent.click(screen.getByTestId('chat-trust-cancel'))
    await waitFor(() => expect(screen.queryByTestId('chat-trust-points')).toBeNull())
    expect(trigger).toHaveAttribute('data-value', 'medium')

    // Confirming switches, and only the trigger turns red from then on.
    await pickMenu('chat-permission-select', 'chat-permission-menu', 'full')
    await userEvent.click(await screen.findByTestId('chat-trust-confirm'))
    await waitFor(() => expect(trigger).toHaveAttribute('data-value', 'full'))
    expect(window.localStorage.getItem('anvil.chat.permission')).toBe('full')
    expect(trigger.className).toContain('is-danger')

    await sendOnce('run it')
    await waitFor(() =>
      expect(host.args('session_send')).toMatchObject({ permission: 'full' }),
    )
  })

  it('sends the chosen thinking level with the turn, labelled as pi labels it', async () => {
    window.localStorage.removeItem('anvil.chat.thinking')
    const host = installToolHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const trigger = screen.getByTestId('chat-thinking-select')
    await waitFor(() => expect(trigger).not.toBeDisabled())
    expect(trigger).toHaveAttribute('data-value', 'medium')
    // The picker shows the runtime's own flag values: a translated label would
    // hide what `--thinking` was actually told.
    await userEvent.click(trigger)
    const menu = await screen.findByTestId('chat-thinking-menu')
    const xhigh = within(menu).getAllByRole('option').find((row) => row.dataset.value === 'xhigh')
    expect(xhigh).toHaveTextContent('Extra high')
    await userEvent.click(xhigh!)
    expect(window.localStorage.getItem('anvil.chat.thinking')).toBe('xhigh')

    await sendOnce('run it')
    await waitFor(() =>
      expect(host.args('session_send')).toMatchObject({ thinking: 'xhigh' }),
    )
  })

  it('no longer offers to save the composer model as an agent', async () => {
    installToolHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    // The composer is live (its model picker is populated) yet the save-agent
    // control is gone: a binding is made on the agents page now.
    await waitFor(() => expect(screen.getByTestId('chat-model-select')).not.toBeDisabled())
    expect(screen.queryByTestId('chat-save-agent')).toBeNull()
  })

  it('sends deny and cancel from the approval card, and the card goes away', async () => {
    // Both buttons must reach the host as their own decision; a card whose
    // "取消" quietly sends "allow" would be worse than no card at all.
    for (const [label, decision] of [
      ['拒绝', 'deny'],
      ['取消发送', 'cancel'],
    ] as const) {
      cleanup()
      const host = installToolHost()
      render(
        <MemoryRouter initialEntries={['/chat']}>
          <AppRoutes />
        </MemoryRouter>,
      )
      await sendOnce('run it')
      const card = await screen.findByTestId('chat-approval')
      await userEvent.click(within(card).getByText(label))
      await waitFor(() =>
        expect(host.args('session_approve')).toMatchObject({
          sessionId: 'pi-1',
          approvalId: 'pi-1:a1-0',
          decision,
        }),
      )
      await waitFor(() => expect(screen.queryByTestId('chat-approval')).toBeNull())
    }
  })

  it('drops the approval card once the turn has already failed', async () => {
    installToolHost({ fail: true })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')
    // The ledger still carries the approval_request, but the run is over and
    // the host dropped the question: leaving the card up is what makes "allow"
    // look like a button that does nothing.
    await screen.findByTestId('chat-live-failure')
    expect(screen.queryByTestId('chat-approval')).toBeNull()
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
  it('offers installed skills in @ and sends the chosen one as skill context', async () => {
    const host = installInventoryHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    // A session must exist before the composer resolves its runtime.
    await sendOnce('first turn')
    await waitFor(() => expect(host.calls).toContain('session_send'))

    const composer = screen.getByTestId('chat-composer')
    await userEvent.clear(composer)
    await userEvent.type(composer, '@code')
    await waitFor(() =>
      expect(
        screen.getAllByTestId('chat-mention-entry').some(
          (entry) => entry.getAttribute('data-mention-kind') === 'skill',
        ),
      ).toBe(true),
    )
    const skill = screen
      .getAllByTestId('chat-mention-entry')
      .find((entry) => entry.getAttribute('data-mention-kind') === 'skill')
    expect(skill).toBeTruthy()
    await userEvent.click(skill as HTMLElement)

    // The skill is part of the draft itself now, not a chip above the field.
    expect(screen.queryByTestId('chat-attachments')).toBeNull()
    expect(composer).toHaveValue('@code-review ')
    expect(document.querySelector('.chat-inline-skill')).toHaveTextContent('@code-review')

    await userEvent.type(composer, 'review this')
    await userEvent.click(screen.getByTestId('chat-send'))
    await waitFor(() =>
      expect(host.args('session_send')).toMatchObject({
        text: '@code-review review this',
        context: [{ kind: 'skill', path: SKILL_PATH, label: 'code-review' }],
      }),
    )
  })

  it('opens a skill-only palette with / and filters it as you type', async () => {
    const host = installInventoryHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('first turn')
    await waitFor(() => expect(host.calls).toContain('session_send'))

    const composer = screen.getByTestId('chat-composer')
    await userEvent.clear(composer)
    await userEvent.type(composer, '/')
    const entries = await screen.findAllByTestId('chat-mention-entry')
    // A `/` palette is skills only: project files never join this list.
    expect(entries).toHaveLength(1)
    expect(entries.every((entry) => entry.getAttribute('data-mention-kind') === 'skill')).toBe(true)
    expect(screen.getByRole('listbox', { name: 'Skill 列表' })).toHaveAttribute('data-mode', 'slash')
    expect(host.calls).not.toContain('session_context_list')

    await userEvent.type(composer, 'zzz')
    await waitFor(() =>
      expect(screen.getByTestId('chat-mention-empty')).toHaveTextContent('没有匹配的 Skill'),
    )
  })

  it('accepts a / skill with Tab and drops the slash token', async () => {
    installInventoryHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('first turn')
    const composer = screen.getByTestId('chat-composer')
    await userEvent.clear(composer)
    await userEvent.type(composer, '/code')

    const entry = await screen.findByTestId('chat-mention-entry')
    expect(entry).toHaveAttribute('aria-selected', 'true')
    await userEvent.tab()

    expect(composer).toHaveValue('@code-review ')
    expect(screen.queryByTestId('chat-attachments')).toBeNull()
    expect(screen.queryByTestId('chat-mention-entry')).toBeNull()
    // The skill is still one inline reference in the draft. Its count used to
    // print in a "上下文 N" button; that button is now the context-window ring.
    expect(document.querySelector('.chat-inline-skill')).not.toBeNull()

    // One Backspace from just after the block removes the name and its space.
    await userEvent.keyboard('{Backspace}')
    await waitFor(() => expect(composer).toHaveValue(''))
    expect(document.querySelector('.chat-inline-skill')).toBeNull()
  })

  it('refuses typing inside an inline skill block', async () => {
    installInventoryHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('first turn')
    const composer = screen.getByTestId('chat-composer')
    await userEvent.clear(composer)
    await userEvent.type(composer, '/code')
    await userEvent.tab()
    expect(composer).toHaveValue('@code-review ')
    expect(document.querySelector('.chat-inline-skill')).toHaveAttribute('data-skill', 'code-review')

    // An edit that lands inside the name is refused: the chip can never be
    // renamed into a different skill by accident.
    ;(composer as HTMLTextAreaElement).setSelectionRange(4, 4)
    fireEvent.input(composer, { target: { value: '@coxxde-review ' } })
    expect(composer).toHaveValue('@code-review ')
  })

  it('moves the / cursor with the arrow keys before accepting it', async () => {
    installInventoryHost([
      { runtimeId: 'codex', name: 'release-notes', path: 'D:/skills/release-notes.md' },
    ])
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('first turn')
    const composer = screen.getByTestId('chat-composer')
    await userEvent.clear(composer)
    await userEvent.type(composer, '/')

    const entries = await screen.findAllByTestId('chat-mention-entry')
    expect(entries).toHaveLength(2)
    // The running runtime's skill sorts first, and that row starts selected.
    expect(entries[0]).toHaveAttribute('aria-selected', 'true')
    await userEvent.keyboard('{ArrowDown}')
    expect(screen.getAllByTestId('chat-mention-entry')[1]).toHaveAttribute('aria-selected', 'true')

    await userEvent.tab()
    expect(composer).toHaveValue('@release-notes ')
  })

  it('the + drawer skill entry opens a skill-only picker', async () => {
    installInventoryHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))
    await userEvent.click(screen.getByTestId('chat-add-skill'))
    expect(composer).toHaveValue('@')
    const entries = await screen.findAllByTestId('chat-mention-entry')
    expect(entries.length).toBeGreaterThan(0)
    expect(entries.every((entry) => entry.getAttribute('data-mention-kind') === 'skill')).toBe(true)
  })

  it('the + drawer picks a file and attaches it as context', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))

    const fileButton = screen.getByTestId('chat-add-file')
    expect(fileButton).not.toBeDisabled()

    await userEvent.click(fileButton)
    await waitFor(() => expect(host.calls).toContain('workspace_pick_files'))
    await waitFor(() => expect(screen.queryByTestId('chat-attachment-drawer')).toBeNull())
    const chips = screen.getByTestId('chat-attachments')
    expect(chips).toHaveTextContent('spec.txt')
    // Multi-select: the dialog returned two paths and both became chips.
    expect(chips).toHaveTextContent('plan.md')
  })

  it('shows the attachments on the bubble while the turn is still running', async () => {
    const host = installHost({ hangSend: true })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))
    await userEvent.click(screen.getByTestId('chat-add-file'))
    await waitFor(() => expect(screen.getByTestId('chat-attachments')).toHaveTextContent('spec.txt'))

    await userEvent.type(composer, 'read them')
    await userEvent.keyboard('{Enter}')

    // The host never answers, so nothing was read back: these chips can only be
    // the ones the composer handed to the turn.
    const bubble = await screen.findByTestId('chat-message-user')
    expect(bubble).toHaveTextContent('read them')
    const chips = await screen.findByTestId('chat-context-chips')
    expect(chips).toHaveTextContent('spec.txt')
    expect(chips).toHaveTextContent('plan.md')
    expect(host.calls).toContain('session_send')
  })

  it('clears the composer attachments once the turn carries them', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))
    await userEvent.click(screen.getByTestId('chat-add-file'))
    await waitFor(() => expect(screen.getByTestId('chat-attachments')).toHaveTextContent('spec.txt'))

    await userEvent.type(composer, 'read them')
    await userEvent.keyboard('{Enter}')

    await waitFor(() =>
      expect(host.args('session_send')).toMatchObject({
        context: [
          { kind: 'file', path: 'D:/picked/folder/spec.txt' },
          { kind: 'file', path: 'D:/picked/folder/plan.md' },
        ],
      }),
    )
    // The chips belonged to the turn that just started, so they leave the field
    // with it: the transcript keeps the record.
    await waitFor(() => expect(screen.queryByTestId('chat-attachments')).toBeNull())
  })

  it('the + drawer attaches a picked folder too', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))

    const dirButton = screen.getByTestId('chat-add-dir')
    expect(dirButton).not.toBeDisabled()

    await userEvent.click(dirButton)
    await waitFor(() => expect(host.calls).toContain('workspace_pick'))
    await waitFor(() => expect(screen.queryByTestId('chat-attachment-drawer')).toBeNull())
    expect(screen.getByTestId('chat-attachments')).toHaveTextContent('folder')
  })

  it('the + drawer dismisses when pressing outside it', async () => {
    installHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))
    expect(screen.getByTestId('chat-attachment-drawer')).toBeTruthy()

    await userEvent.click(screen.getByTestId('chat-thread'))
    expect(screen.queryByTestId('chat-attachment-drawer')).toBeNull()

    // The plus button still toggles it back open.
    await userEvent.click(screen.getByTestId('chat-composer-plus'))
    expect(screen.getByTestId('chat-attachment-drawer')).toBeTruthy()
  })

  it('offers every catalog skill in @ across runtimes, past the old 12-item cap', async () => {
    const extra = Array.from({ length: 14 }, (_, index) => ({
      runtimeId: index % 2 === 0 ? 'pi' : 'codex',
      name: `skill-${String(index).padStart(2, '0')}`,
      path: `D:/skills/skill-${index}.md`,
    }))
    installInventoryHost(extra)
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const composer = screen.getByTestId('chat-composer')
    await waitFor(() => expect(composer).not.toBeDisabled())
    await userEvent.click(screen.getByTestId('chat-composer-plus'))
    await userEvent.click(screen.getByTestId('chat-add-skill'))

    const entries = await screen.findAllByTestId('chat-mention-entry')
    expect(entries).toHaveLength(15)
    expect(entries.every((entry) => entry.getAttribute('data-mention-kind') === 'skill')).toBe(true)
    // A codex skill is still selectable while the running runtime is pi.
    expect(screen.getByText('skill-13')).toBeInTheDocument()
  })

  it('keeps streamed reasoning attached above its own answer, collapsed once settled', async () => {
    installToolHost({ thinking: true })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')
    const thinking = await screen.findByTestId('chat-live-thinking')
    expect(thinking).toHaveTextContent('先读文件再回答')
    await waitFor(() => expect(screen.getByTestId('chat-message-agent')).toBeInTheDocument())
    // The old disclosure was a panel at the end of the thread: it sat above
    // the answer while streaming and fell below it once the answer landed.
    const agent = screen.getByTestId('chat-message-agent')
    expect(within(agent).getByTestId('chat-live-thinking')).toHaveAttribute('data-open', 'false')
    const prose = agent.querySelector('.chat-live-text')
    expect(prose).not.toBeNull()
    expect(
      thinking.compareDocumentPosition(prose as Node) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(screen.queryByTestId('chat-thinking')).toBeNull()
  })

  it('keeps text, reasoning and tools in the order the turn produced them', async () => {
    installToolHost({ interleave: true })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')

    // Every kind of output is a block in ledger order. The answer used to be
    // hoisted into one prose block at the end, so a turn that spoke, thought,
    // ran a command and then spoke again read as "everything else, then the
    // whole answer".
    const agent = await screen.findByTestId('chat-message-agent')
    const blocks = Array.from(
      agent.querySelectorAll<HTMLElement>(
        '[data-testid="chat-live-thinking"], [data-testid="chat-live-tool"], [data-testid="chat-live-text"]',
      ),
    )
    expect(blocks.map((block) => block.dataset.testid)).toEqual([
      'chat-live-text',
      'chat-live-thinking',
      'chat-live-tool',
      'chat-live-text',
    ])
    expect(blocks[0]).toHaveTextContent('先说明一下')
    expect(blocks[1]).toHaveTextContent('看看目录')
    expect(blocks[3]).toHaveTextContent('start.bat')
    // The stored message is the same words: it must not be printed twice.
    expect(agent).not.toHaveTextContent('done')

    // Tokens under the answer, with a real output count. The old reader asked
    // for `outputTokens`, which no runtime sends, so output always read zero.
    const usageLine = await within(agent).findByTestId('chat-usage')
    expect(usageLine).toHaveTextContent('用量：输入 1,200 · 输出 80 tokens')
  })

  it('renders the finished answer as markdown in the real chat page', async () => {
    installToolHost({ answer: '# 结论\n\n**已验证** 与 `code`' })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')
    await screen.findByRole('heading', { level: 1, name: '结论' })
    expect(screen.getByText('已验证').tagName).toBe('STRONG')
    expect(screen.getByText('code').tagName).toBe('CODE')
  })

  it('leaves no turn-timeline card after a normal tool turn by default', async () => {
    installToolHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOnce('run it')
    await screen.findByTestId('chat-live-tool')
    expect(screen.queryByTestId('chat-thinking')).toBeNull()
  })

})

describe('chat rail', () => {
  /** A rail view shaped like the host's, with one of everything. */
  function railView(empty = false): RailView {
    if (empty) {
      return {
        turns: [],
        exec: [],
        artifacts: [],
        sources: [],
        references: [],
        subagents: [],
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
      references: [
        {
          path: 'D:/project/src/main.rs',
          label: 'main.rs',
          kind: 'file',
          turn: 1,
          runId: 'pi-1:r1',
          bytes: 1200,
          truncated: false,
          outsideRoot: false,
          touches: 2,
        },
      ],
      subagents: [],
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

  function installRailHost(
    empty = false,
    patch: (view: RailView) => RailView = (view) => view,
  ) {
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
        if (command === 'session_rail') return patch(railView(empty))
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

    // The file itself is the button: clicking it opens the preview in place.
    // The small side button is the jump back to the turn that wrote it.
    const turn = document.querySelector('[data-testid="chat-message-agent"][data-run="pi-1:r1"]')
    expect(turn).not.toBeNull()
    await userEvent.click(within(row).getByTestId('chat-rail-artifact-open'))
    // The file opens as a tab of the pane, not as a dialog over the answer it
    // belongs to: the reader keeps both on screen.
    await waitFor(() =>
      expect(within(screen.getByTestId('chat-rail')).getByTestId('file-preview')).toBeInTheDocument(),
    )
    expect(screen.getByRole('tab', { name: '预览' })).toHaveAttribute('aria-selected', 'true')
    await userEvent.click(screen.getByTestId('file-preview-close'))
    await waitFor(() => expect(screen.queryByTestId('file-preview')).toBeNull())
    // Closing goes back to the list the file came from.
    expect(screen.getByRole('tab', { name: '输出物 1' })).toHaveAttribute('aria-selected', 'true')
    // Re-query: the overlay's open/close re-rendered the rail, so the row
    // element captured above belongs to the previous render.
    await userEvent.click(within(screen.getByTestId('chat-rail-artifact')).getByTestId('chat-rail-artifact-return'))
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
    // A parent re-render must not detach this button mid-click: the Markdown
    // component map is memoised so the generated DOM survives unrelated reads.
    expect(cite.isConnected).toBe(true)
    fireEvent.click(cite)
    expect(cite.isConnected).toBe(true)
    await waitFor(() =>
      // Two things are cited in this turn: the attached file and the URL.
      expect(screen.getByRole('tab', { name: '引用 2' })).toHaveAttribute('aria-selected', 'true'),
    )
    const source = await screen.findByTestId('chat-rail-source')
    expect(source).toHaveAttribute('data-number', '1')
    expect(source).toHaveTextContent('https://docs.example.com/a')
  })

  it('lists the attached files in the rail 引用 pane', async () => {
    installRailHost()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
    await sendOneTurn()

    await userEvent.click(screen.getByRole('tab', { name: '引用 2' }))
    const rows = await screen.findAllByTestId('chat-rail-reference')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('main.rs')
    expect(rows[0]).toHaveAttribute('data-kind', 'file')
    // It says which turn carried it and that two turns used it.
    expect(rows[0]).toHaveTextContent('第 1 轮')
    expect(rows[0]).toHaveTextContent('2 轮共用')
    // The URL is still there, in its own section, below.
    expect(screen.getByTestId('chat-rail-source')).toHaveTextContent('https://docs.example.com/a')
  })

  it('jumps to the turn by index when its run has no bubble yet', async () => {
    // A turn that is still starting has a stored run id but the bubble on
    // screen is the optimistic message, which has none. Clicking 定位 there used
    // to answer "找不到该轮对话" about a turn the reader was watching.
    installRailHost(false, (view) => ({
      ...view,
      references: view.references.map((reference) => ({ ...reference, runId: 'pi-1:not-yet' })),
    }))
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await sendOneTurn()
    await userEvent.click(screen.getByRole('tab', { name: /引用/ }))
    const go = await screen.findByText('定位')
    await userEvent.click(go)
    expect(screen.queryByText('找不到该轮对话')).toBeNull()
    expect(screen.getAllByTestId('chat-message-user')[0]).toHaveClass('is-focused')
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
            references: [],
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

describe("pi's subagent extension", () => {
  const missing = {
    installed: false,
    agentDir: 'C:/Users/A/.pi/agent',
    extensionDir: 'C:/Users/A/.pi/agent/extensions/subagent',
    source: 'C:/Users/A/.pi-node/current/node_modules/@earendil-works/pi-coding-agent/examples/extensions/subagent',
    agents: 0,
    prompts: 0,
    canInstall: true,
    detail: 'the subagent extension is not installed in the pi agent directory',
  }
  const installed = {
    ...missing,
    installed: true,
    agents: 4,
    prompts: 3,
    canInstall: false,
    detail: undefined,
  }

  it('offers the install and drops the notice once the host reports it installed', async () => {
    const host = installHost({
      subagent: missing,
      install: { written: 6, skipped: 0, status: installed },
    })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const notice = await screen.findByTestId('chat-subagent-notice')
    expect(notice).toHaveTextContent('subagent 扩展未安装')
    // The path is part of the promise: the user can see where it lands.
    expect(notice).toHaveTextContent('extensions/subagent')

    await userEvent.click(within(notice).getByTestId('chat-subagent-install'))

    await waitFor(() => expect(host.calls).toContain('pi_subagent_install'))
    await waitFor(() => expect(screen.queryByTestId('chat-subagent-notice')).toBeNull())
    expect(screen.getByTestId('chat-notice')).toHaveTextContent('已安装 subagent 扩展（4 个可用子代理）')
  })

  it('cannot install when the pi package ships no example, and says why', async () => {
    installHost({ subagent: { ...missing, canInstall: false, source: undefined } })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    const notice = await screen.findByTestId('chat-subagent-notice')
    expect(within(notice).getByTestId('chat-subagent-install')).toBeDisabled()
    expect(notice).toHaveTextContent('请先更新 pi')
  })

  it('says nothing when the extension is already active', async () => {
    const host = installHost({ subagent: installed })
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    // The check has to have run before the absence means anything.
    await waitFor(() => expect(host.calls).toContain('pi_subagent_status'))
    expect(screen.queryByTestId('chat-subagent-notice')).toBeNull()
  })
})
