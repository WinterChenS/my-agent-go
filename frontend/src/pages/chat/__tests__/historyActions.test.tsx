import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'

afterEach(() => cleanup())

/**
 * A live history list whose actions all succeed unless a command is made to
 * fail, exactly like the desktop host.
 */
function installHost(options: { failing?: string } = {}) {
  const calls: { command: string; args: Record<string, unknown> }[] = []
  const row = (id: string, title: string) => ({
    id,
    runtimeId: 'pi',
    title,
    status: 'idle',
    nativeId: `native-${id}`,
    model: 'auto/gpt-5',
    projectPath: 'D:\\project',
    pinned: false,
    archived: false,
    messageCount: 4,
    parentId: '',
    branchAt: 0,
    createdAt: '2026-11-13T00:00:00Z',
    updatedAt: '2026-11-13T00:00:00Z',
    preview: '',
  })
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args: args ?? {} })
      if (options.failing === command) throw new Error(`${command} refused`)
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
        return {
          agents: [],
          runtimes: [],
          models: [],
          catalogVersion: 2,
          scanning: false,
        }
      }
      if (command === 'session_list') {
        const query = (args?.query ?? {}) as { includeArchived?: boolean }
        const rows = [row('s-1', '第一个会话'), row('s-2', '第二个会话')]
        return {
          sessions: query.includeArchived ? [...rows, { ...row('s-3', '已归档的会话'), archived: true }] : rows,
          total: rows.length,
        }
      }
      if (command === 'session_projects') {
        return [{ path: 'D:\\project', name: 'project', sessions: 2, updatedAt: '2026-11-13T00:00:00Z' }]
      }
      if (command === 'workspace_defaults') {
        return { defaultWorkspace: 'C://Users//A//Anvil', projectsRoot: 'C://Users//A//Anvil//Projects' }
      }
      if (command === 'workspace_ensure') return 'C://Users//A//Anvil'
      if (command === 'pi_subagent_status') {
        return { installed: false, canInstall: false, extensionDir: '', detail: '' }
      }
      if (command === 'session_fork') {
        return { ...row('s-branch', '分支会话'), nativeId: 'native-branch' }
      }
      if (command === 'session_compact') return { collapsed: 3, kept: 4, summarySeq: 12 }
      if (command === 'session_events') return []
      if (command === 'session_open') {
        return { ...row('s-1', '第一个会话'), messageCount: 0 }
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

async function openMenu(user: ReturnType<typeof userEvent.setup>, sessionId: string) {
  await user.click(await screen.findByTestId(`chat-history-actions-${sessionId}`))
  return screen.findByTestId('chat-history-action-menu')
}

describe('history row actions', () => {
  it('pins, renames, archives, compacts and deletes through the menu', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    // pin
    let menu = await openMenu(user, 's-1')
    await user.click(within(menu).getByRole('menuitem', { name: '置顶 / 取消置顶' }))
    await waitFor(() => {
      expect(host.calls.some((call) => call.command === 'session_pin' && call.args.pinned === true)).toBe(true)
    })
    expect(await screen.findByTestId('chat-history-notice')).toHaveTextContent('已置顶')

    // rename: the row becomes an editor, Enter commits
    menu = await openMenu(user, 's-1')
    await user.click(within(menu).getByRole('menuitem', { name: '重命名' }))
    const editor = await screen.findByTestId('chat-history-rename-input')
    await user.clear(editor)
    await user.type(editor, '改过的标题{Enter}')
    await waitFor(() => {
      expect(
        host.calls.some(
          (call) => call.command === 'session_rename' && call.args.title === '改过的标题',
        ),
      ).toBe(true)
    })

    // archive
    menu = await openMenu(user, 's-2')
    await user.click(within(menu).getByRole('menuitem', { name: '归档' }))
    await waitFor(() => {
      expect(
        host.calls.some((call) => call.command === 'session_archive' && call.args.archived === true),
      ).toBe(true)
    })
    expect(await screen.findByTestId('chat-history-notice')).toHaveTextContent('已归档')

    // compact reports what it did
    menu = await openMenu(user, 's-1')
    await user.click(within(menu).getByRole('menuitem', { name: '压缩上下文' }))
    await waitFor(() => {
      expect(host.calls.some((call) => call.command === 'session_compact')).toBe(true)
    })
    expect(await screen.findByTestId('chat-history-notice')).toHaveTextContent('已压缩：合并 3 条，保留 4 条')

    // delete: the first press arms, the second one deletes
    menu = await openMenu(user, 's-2')
    await user.click(within(menu).getByRole('menuitem', { name: '删除' }))
    menu = await screen.findByTestId('chat-history-action-menu')
    await user.click(within(menu).getByRole('menuitem', { name: '确认删除' }))
    await waitFor(() => {
      expect(host.calls.some((call) => call.command === 'session_delete')).toBe(true)
    })
    expect(await screen.findByTestId('chat-history-notice')).toHaveTextContent('已删除')
  })

  it('branches a conversation and opens the branch', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const menu = await openMenu(user, 's-1')
    await user.click(within(menu).getByRole('menuitem', { name: '分支会话' }))
    await waitFor(() => {
      expect(host.calls.some((call) => call.command === 'session_fork')).toBe(true)
    })
    expect(await screen.findByTestId('chat-history-notice')).toHaveTextContent('已创建分支并打开')
  })

  it('says why an action did not happen instead of closing in silence', async () => {
    installHost({ failing: 'session_rename' })
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const menu = await openMenu(user, 's-1')
    await user.click(within(menu).getByRole('menuitem', { name: '重命名' }))
    const editor = await screen.findByTestId('chat-history-rename-input')
    await user.clear(editor)
    await user.type(editor, '新名字{Enter}')
    expect(await screen.findByTestId('chat-history-notice')).toHaveTextContent('操作失败：session_rename refused')
  })

  it('asks for archived rows only when the toggle is on, and can bring one back', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/chat']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    expect(screen.queryByText('已归档的会话')).toBeNull()

    await user.click(await screen.findByTestId('chat-history-archived-toggle'))
    expect(await screen.findByText('已归档的会话')).toBeInTheDocument()
    expect(
      host.calls.some(
        (call) => call.command === 'session_list' && (call.args.query as { includeArchived?: boolean })?.includeArchived === true,
      ),
    ).toBe(true)

    const menu = await openMenu(user, 's-3')
    await user.click(within(menu).getByRole('menuitem', { name: '取消归档' }))
    await waitFor(() => {
      expect(
        host.calls.some((call) => call.command === 'session_archive' && call.args.archived === false),
      ).toBe(true)
    })
  })
})
