import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'

afterEach(() => cleanup())

/**
 * A live host whose catalog knows one custom agent besides the built-in
 * default, and whose sessions carry the agent slug the host would store.
 */
function installHost(options: { agentId?: string } = {}) {
  const agentId = options.agentId ?? ''
  const agents = [
    {
      id: 'default',
      slug: 'default',
      name: 'Default Agent',
      runtimeId: 'pi',
      modelId: 'auto/gpt-5',
      isDefault: true,
      updatedAt: '2026-11-13T00:00:00Z',
    },
    {
      id: 'agent-1',
      slug: 'migration-engineer',
      name: '迁移工程师',
      runtimeId: 'pi',
      modelId: 'auto/gpt-5',
      isDefault: false,
      updatedAt: '2026-11-13T00:00:00Z',
    },
  ]
  const summary = () => ({
    id: 'pi-1',
    runtimeId: 'pi',
    title: 'host test',
    status: 'idle',
    nativeId: 'native-1',
    model: 'auto/gpt-5',
    projectPath: 'D:\\project',
    agentId,
    pinned: false,
    archived: false,
    messageCount: 2,
    parentId: '',
    branchAt: 0,
    createdAt: '2026-11-13T00:00:00Z',
    updatedAt: '2026-11-13T00:00:00Z',
    preview: 'ok',
  })
  const internals = {
    invoke: async (command: string) => {
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
        return {
          agents,
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
      if (command === 'session_open') return { ...summary(), messageCount: 0 }
      if (command === 'session_detail') {
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
              text: 'ok',
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
      if (command === 'session_events') {
        return [
          { sessionId: 'pi-1', runId: 'pi-1:r1', seq: 1, kind: 'turn_started', at: '2026-11-13T00:00:00Z', payload: { prompt: 'Reply with exactly: ok' } },
          { sessionId: 'pi-1', runId: 'pi-1:r1', seq: 2, kind: 'text_delta', at: '2026-11-13T00:00:00Z', payload: { delta: 'o' } },
          { sessionId: 'pi-1', runId: 'pi-1:r1', seq: 3, kind: 'text_delta', at: '2026-11-13T00:00:00Z', payload: { delta: 'k' } },
        ]
      }
      if (command === 'session_send') {
        await new Promise((resolve) => setTimeout(resolve, 200))
        return { runId: 'pi-1:r1', status: 'completed', eventCount: 3 }
      }
      return null
    },
    listen: async () => () => undefined,
  }
  Object.defineProperty(window, '__TAURI_INTERNALS__', {
    configurable: true,
    value: internals as unknown as TauriInternals,
  })
}

async function mountAndAnswer() {
  render(
    <MemoryRouter initialEntries={['/chat']}>
      <AppRoutes />
    </MemoryRouter>,
  )
  const composer = await screen.findByTestId('chat-composer')
  await waitFor(() => expect(composer).not.toBeDisabled())
  await userEvent.type(composer, 'Reply with exactly: ok')
  await userEvent.click(screen.getByTestId('chat-send'))
  await waitFor(() => expect(screen.getByTestId('chat-message-agent')).toBeInTheDocument(), {
    timeout: 3000,
  })
}

describe('who answers the turn', () => {
  it('shows the saved agent name when the session runs a custom agent', async () => {
    installHost({ agentId: 'migration-engineer' })
    await mountAndAnswer()
    // The agent speaks under its own name, not under the runtime.
    expect(screen.getByTestId('chat-speaker-name')).toHaveTextContent('迁移工程师')
    expect(screen.getByTestId('chat-speaker-badge')).toHaveTextContent('迁')
    expect(screen.getByTestId('chat-speaker-name')).not.toHaveTextContent('pi')
  })

  it('shows the runtime when the session runs a plain model', async () => {
    installHost({ agentId: '' })
    await mountAndAnswer()
    expect(screen.getByTestId('chat-speaker-name')).toHaveTextContent('pi')
    expect(screen.getByTestId('chat-speaker-badge')).toHaveTextContent('p')
  })

  it('shows the runtime when the session runs the built-in default agent', async () => {
    // The default agent is derived from the default model and has no persona of
    // its own, so it reads like any other model choice.
    installHost({ agentId: 'default' })
    await mountAndAnswer()
    expect(screen.getByTestId('chat-speaker-name')).toHaveTextContent('pi')
  })
})
