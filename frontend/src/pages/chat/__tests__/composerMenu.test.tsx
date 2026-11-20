import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'

afterEach(() => cleanup())

/** Minimal live host: one pi model, one agent, no turns needed for the picker. */
function installHost() {
  const calls: string[] = []
  const internals = {
    invoke: async (command: string) => {
      calls.push(command)
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
        return {
          agents: [
            {
              id: 'default',
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

async function mount() {
  render(
    <MemoryRouter initialEntries={['/chat']}>
      <AppRoutes />
    </MemoryRouter>,
  )
  await waitFor(() => expect(screen.getByTestId('chat-composer')).not.toBeDisabled())
}

describe('the themed composer pickers', () => {
  it('opens a themed listbox instead of a native select', async () => {
    installHost()
    await mount()
    expect(screen.queryByTestId('chat-model-menu')).toBeNull()
    await userEvent.click(screen.getByTestId('chat-model-select'))
    const menu = await screen.findByTestId('chat-model-menu')
    expect(menu.getAttribute('role')).toBe('listbox')
    expect(screen.getByTestId('chat-model-select').getAttribute('aria-expanded')).toBe('true')
    // Options are real buttons with a selected row, not OS-painted <option>s.
    expect(within(menu).getAllByRole('option').length).toBeGreaterThan(0)
    expect(within(menu).getAllByRole('option').some((row) => row.getAttribute('aria-selected') === 'true')).toBe(true)
  })

  it('navigates with the arrow keys and picks with Enter', async () => {
    installHost()
    await mount()
    await userEvent.click(screen.getByTestId('chat-thinking-select'))
    const menu = await screen.findByTestId('chat-thinking-menu')
    const rows = within(menu).getAllByRole('option')
    const start = rows.findIndex((row) => row.getAttribute('aria-selected') === 'true')
    await userEvent.keyboard('{ArrowDown}')
    const expected = rows[(start + 1) % rows.length].getAttribute('data-value')
    // The focused row follows the arrow; Enter commits it and closes the menu.
    expect(document.activeElement?.getAttribute('data-value')).toBe(expected)
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(screen.queryByTestId('chat-thinking-menu')).toBeNull())
    expect(screen.getByTestId('chat-thinking-select').getAttribute('data-value')).toBe(expected)
  })

  it('closes on outside click and on Escape', async () => {
    installHost()
    await mount()
    await userEvent.click(screen.getByTestId('chat-thinking-select'))
    await screen.findByTestId('chat-thinking-menu')
    fireEvent.pointerDown(document.body)
    await waitFor(() => expect(screen.queryByTestId('chat-thinking-menu')).toBeNull())

    await userEvent.click(screen.getByTestId('chat-thinking-select'))
    await screen.findByTestId('chat-thinking-menu')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByTestId('chat-thinking-menu')).toBeNull())
  })

  it('tints only the full-control row red, never the whole menu', async () => {
    installHost()
    await mount()
    await userEvent.click(screen.getByTestId('chat-permission-select'))
    const menu = await screen.findByTestId('chat-permission-menu')
    const rows = within(menu).getAllByRole('option')
    const red = rows.filter((row) => row.className.includes('is-danger'))
    expect(red).toHaveLength(1)
    expect(red[0].getAttribute('data-value')).toBe('full')
    expect(rows.filter((row) => row.className.includes('is-danger')).length).toBeLessThan(rows.length)
  })
})
