import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'
import type { SessionEvent } from '../../../chat/client'

afterEach(() => cleanup())

interface HostCall {
  command: string
  args: Record<string, unknown>
}

/**
 * A live host whose runtime asks the user a question and then waits for the
 * answer, exactly like pi's extension UI does.
 */
function installHost(
  options: {
    method?: string
    resolved?: boolean
    auto?: boolean
    message?: string
    title?: string
  } = {},
) {
  const method = options.method ?? 'confirm'
  const calls: HostCall[] = []
  const events: SessionEvent[] = [
    {
      sessionId: 'pi-1',
      runId: 'pi-1:r1',
      seq: 1,
      kind: 'turn_started',
      at: '2026-11-13T00:00:00Z',
      payload: { prompt: 'hello' },
    },
    {
      sessionId: 'pi-1',
      runId: 'pi-1:r1',
      seq: 2,
      kind: 'extension_ui',
      at: '2026-11-13T00:00:01Z',
      payload: {
        id: 'uuid-1',
        sessionId: 'pi-1',
        method,
        title: options.title ?? '允许这个操作吗',
        message: options.message ?? '它会修改工作区',
        options: method === 'select' ? ['允许', '拒绝'] : [],
      },
    },
  ]
  if (options.resolved || options.auto) {
    events.push({
      sessionId: 'pi-1',
      runId: 'pi-1:r1',
      seq: 3,
      kind: 'extension_ui_resolved',
      at: '2026-11-13T00:00:02Z',
      payload: { id: 'uuid-1', confirmed: true, auto: options.auto === true },
    })
  }
  // An answer, so the transcript has a settled message to look at: "nothing
  // was rendered" is impossible to check while the turn is still empty.
  events.push({
    sessionId: 'pi-1',
    runId: 'pi-1:r1',
    seq: 4,
    kind: 'text_delta',
    at: '2026-11-13T00:00:03Z',
    payload: { delta: '好的' },
  })
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args: args ?? {} })
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
        return {
          agents: [
            {
              id: 'default',
              slug: 'default',
              name: 'Default Agent',
              runtimeId: 'pi',
              modelId: 'auto/gpt-5',
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
              installPath: 'C://pi.CMD',
              installSource: 'path',
              configPath: null,
              status: 'available',
              capabilities: {
                models: 'supported',
                tools: 'supported',
                images: 'unsupported',
                subagents: 'unsupported',
              },
              installed: true,
              detail: '',
            },
          ],
          models: [
            {
              runtimeId: 'pi',
              modelId: 'auto/gpt-5',
              displayName: 'GPT 5',
              origin: 'runtime',
              protocol: 'openai',
              endpoint: null,
              verified: true,
              verifiedAt: '2026-11-13T00:00:00Z',
              isDefault: true,
              enabled: true,
              updatedAt: '2026-11-13T00:00:00Z',
              hasCredential: true,
            },
          ],
          catalogVersion: 2,
          scanning: false,
        }
      }
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_projects') return []
      if (command === 'workspace_defaults') {
        return { defaultWorkspace: 'C://Users//A//Anvil', projectsRoot: 'C://Users//A//Anvil//Projects' }
      }
      if (command === 'workspace_ensure') return 'C://Users//A//Anvil'
      if (command === 'pi_subagent_status') {
        return { installed: false, canInstall: false, extensionDir: '', detail: '' }
      }
      if (command === 'session_open') {
        return {
          id: 'pi-1',
          runtimeId: 'pi',
          title: 'host test',
          status: 'idle',
          nativeId: 'native-1',
          model: 'auto/gpt-5',
          projectPath: 'D:\\project',
          pinned: false,
          archived: false,
          messageCount: 0,
          parentId: '',
          branchAt: 0,
          createdAt: '2026-11-13T00:00:00Z',
          updatedAt: '2026-11-13T00:00:00Z',
          preview: '',
        }
      }
      if (command === 'session_events') return events
      if (command === 'session_send') {
        await new Promise((resolve) => setTimeout(resolve, 100))
        return { runId: 'pi-1:r1', status: 'completed', eventCount: events.length }
      }
      if (command === 'session_extension_ui') return null
      return null
    },
    listen: async () => () => undefined,
  }
  Object.defineProperty(window, '__TAURI_INTERNALS__', {
    configurable: true,
    value: internals as unknown as TauriInternals,
  })
  return { calls }
}

async function mountAndSend() {
  render(
    <MemoryRouter initialEntries={['/chat']}>
      <AppRoutes />
    </MemoryRouter>,
  )
  const composer = await screen.findByTestId('chat-composer')
  await waitFor(() => expect(composer).not.toBeDisabled())
  await userEvent.type(composer, 'hello')
  await userEvent.click(screen.getByTestId('chat-send'))
}

describe('a question the runtime asked', () => {
  it('shows the question as a card instead of dropping it', async () => {
    installHost()
    await mountAndSend()
    const card = await screen.findByTestId('chat-extension-ui', {}, { timeout: 3000 })
    expect(card.getAttribute('data-method')).toBe('confirm')
    expect(card).toHaveTextContent('允许这个操作吗')
    expect(card).toHaveTextContent('它会修改工作区')
    expect(screen.getByTestId('chat-extension-allow')).toBeInTheDocument()
  })

  it('sends the answer back to the runtime', async () => {
    const host = installHost()
    await mountAndSend()
    await screen.findByTestId('chat-extension-ui', {}, { timeout: 3000 })
    await userEvent.click(screen.getByTestId('chat-extension-allow'))
    await waitFor(() => {
      const call = host.calls.find((entry) => entry.command === 'session_extension_ui')
      expect(call).toBeDefined()
      expect(call?.args.sessionId).toBe('pi-1')
      expect(call?.args.requestId).toBe('uuid-1')
      expect(call?.args.answer).toEqual({ confirmed: true })
    })
  })

  it('offers the choices of a select', async () => {
    const host = installHost({ method: 'select' })
    await mountAndSend()
    const card = await screen.findByTestId('chat-extension-ui', {}, { timeout: 3000 })
    expect(card.getAttribute('data-method')).toBe('select')
    const options = screen.getAllByTestId('chat-extension-option')
    expect(options).toHaveLength(2)
    await userEvent.click(options[1])
    await waitFor(() => {
      const call = host.calls.find((entry) => entry.command === 'session_extension_ui')
      expect(call?.args.answer).toEqual({ value: '拒绝' })
    })
  })

  it('shows the answer once the runtime got it', async () => {
    installHost({ resolved: true })
    await mountAndSend()
    const card = await screen.findByTestId('chat-extension-ui', {}, { timeout: 3000 })
    await waitFor(() => expect(card).toHaveTextContent('已允许'))
    // Answered questions are not asked twice.
    expect(screen.queryByTestId('chat-extension-allow')).toBeNull()
  })

  it('leaves nothing behind when the trusted workspace answered on its own', async () => {
    installHost({ auto: true })
    await mountAndSend()
    // Wait for the turn to settle: "nothing was shown" only means something
    // once the whole transcript is there.
    await screen.findByText('好的', {}, { timeout: 3000 })
    // Nobody was asked, so there is no question to show — not even a card
    // saying it was answered.
    expect(screen.queryByTestId('chat-extension-ui')).toBeNull()
    expect(screen.queryByTestId('chat-extension-allow')).toBeNull()
  })

  it('prints nothing for a widget an extension is clearing', async () => {
    // pi-subagents clears its panel with `setWidget(key, undefined)` on session
    // start: the request carries no words, and a bare 「扩展面板:」 line over the
    // first answer of every conversation is noise, not information.
    installHost({ method: 'setWidget', message: '', title: '' })
    await mountAndSend()
    await screen.findByText('好的', {}, { timeout: 3000 })
    expect(screen.queryByTestId('chat-extension-note')).toBeNull()
  })

  it('prints a widget that has something to say', async () => {
    installHost({ method: 'setWidget', message: '子代理 2 个运行中', title: '' })
    await mountAndSend()
    const note = await screen.findByTestId('chat-extension-note', {}, { timeout: 3000 })
    expect(note).toHaveTextContent('扩展面板: 子代理 2 个运行中')
  })

  it('tells a notice apart from a question', async () => {
    installHost({ method: 'notify' })
    await mountAndSend()
    const note = await screen.findByTestId('chat-extension-note', {}, { timeout: 3000 })
    expect(note).toHaveTextContent('它会修改工作区')
    // A notice is one quiet line: no card frame, and nothing to answer.
    expect(screen.queryByTestId('chat-extension-ui')).toBeNull()
    expect(screen.queryByTestId('chat-extension-allow')).toBeNull()
  })
})
