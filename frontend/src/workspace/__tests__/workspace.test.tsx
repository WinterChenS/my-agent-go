import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { TauriInternals } from '../../host/tauri'
import { WorkspaceProvider, useWorkspace, workspaceName } from '../WorkspaceProvider'

const DEFAULT_WORKSPACE = 'C:\\Users\\A\\Anvil'
const PROJECTS_ROOT = 'C:\\Users\\A\\Anvil\\Projects'

interface HostLog {
  calls: string[]
  args: Map<string, unknown>
}

function installHost(options: { pick?: string | null; failEnsure?: boolean } = {}): HostLog {
  const calls: string[] = []
  const args = new Map<string, unknown>()
  const internals = {
    invoke: async (command: string, payload?: Record<string, unknown>) => {
      calls.push(command)
      if (payload) args.set(command, payload)
      if (command === 'workspace_defaults') {
        return { defaultWorkspace: DEFAULT_WORKSPACE, projectsRoot: PROJECTS_ROOT }
      }
      if (command === 'workspace_ensure') {
        if (options.failEnsure) throw new Error('missing')
        return String(payload?.path ?? '')
      }
      if (command === 'workspace_create') return `${PROJECTS_ROOT}\\${payload?.name ?? ''}`
      if (command === 'workspace_pick') return options.pick ?? null
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return { calls, args }
}

function Probe() {
  const workspace = useWorkspace()
  return (
    <div>
      <span data-testid="path">{workspace.path ?? ''}</span>
      <span data-testid="name">{workspace.name}</span>
      <span data-testid="default">{workspace.isDefault ? 'yes' : 'no'}</span>
      <span data-testid="error">{workspace.error ?? ''}</span>
      <button type="button" onClick={() => void workspace.choose('D:\\work\\alpha')}>
        choose
      </button>
      <button type="button" onClick={() => void workspace.browse()}>
        browse
      </button>
      <button type="button" onClick={() => void workspace.create('demo')}>
        create
      </button>
      <button type="button" onClick={() => workspace.adopt('D:\\work\\beta')}>
        adopt
      </button>
    </div>
  )
}

function renderProvider() {
  return render(
    <WorkspaceProvider>
      <Probe />
    </WorkspaceProvider>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(cleanup)

describe('workspace name', () => {
  it('takes the last component of a windows or posix path', () => {
    expect(workspaceName('C:\\Users\\A\\Anvil')).toBe('Anvil')
    expect(workspaceName('/home/a/work/')).toBe('work')
    expect(workspaceName('D:\\')).toBe('D:')
    expect(workspaceName(null)).toBe('')
  })
})

describe('workspace provider', () => {
  it('starts in the default workspace the host reports', async () => {
    const host = installHost()
    renderProvider()

    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))
    expect(screen.getByTestId('name')).toHaveTextContent('Anvil')
    expect(screen.getByTestId('default')).toHaveTextContent('yes')
    expect(host.calls).toContain('workspace_defaults')
  })

  it('remembers a chosen directory across a reload', async () => {
    const host = installHost()
    const user = userEvent.setup()
    renderProvider()
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))

    await user.click(screen.getByRole('button', { name: 'choose' }))
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent('D:\\work\\alpha'))
    expect(host.args.get('workspace_ensure')).toEqual({ path: 'D:\\work\\alpha' })
    expect(window.localStorage.getItem('anvil.chat.workspace')).toBe('D:\\work\\alpha')

    cleanup()
    installHost()
    renderProvider()
    // The stored path is re-validated and used instead of the default.
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent('D:\\work\\alpha'))
    expect(screen.getByTestId('default')).toHaveTextContent('no')
  })

  it('falls back to the default when the remembered folder is gone', async () => {
    window.localStorage.setItem('anvil.chat.workspace', 'D:\\deleted')
    installHost({ failEnsure: true })
    renderProvider()

    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))
    expect(screen.getByTestId('error')).not.toHaveTextContent('')
    // The stale path is dropped, not retried on every reload.
    expect(window.localStorage.getItem('anvil.chat.workspace')).toBeNull()
  })

  it('uses what the folder picker returns and ignores a cancel', async () => {
    const user = userEvent.setup()
    installHost({ pick: null })
    renderProvider()
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))

    await user.click(screen.getByRole('button', { name: 'browse' }))
    expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE)

    cleanup()
    installHost({ pick: 'D:\\picked' })
    renderProvider()
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))
    await user.click(screen.getByRole('button', { name: 'browse' }))
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent('D:\\picked'))
  })

  it('creates a project under the projects root and switches to it', async () => {
    const host = installHost()
    const user = userEvent.setup()
    renderProvider()
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))

    await user.click(screen.getByRole('button', { name: 'create' }))
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(`${PROJECTS_ROOT}\\demo`))
    expect(host.args.get('workspace_create')).toEqual({ name: 'demo' })
  })

  it('adopts a conversation project without asking the host', async () => {
    const host = installHost()
    const user = userEvent.setup()
    renderProvider()
    await waitFor(() => expect(screen.getByTestId('path')).toHaveTextContent(DEFAULT_WORKSPACE))
    const before = host.calls.length

    await user.click(screen.getByRole('button', { name: 'adopt' }))
    expect(screen.getByTestId('path')).toHaveTextContent('D:\\work\\beta')
    // Opening an old conversation must not fail because the folder moved: the
    // session already knows where it ran.
    expect(host.calls.length).toBe(before)
  })
})
