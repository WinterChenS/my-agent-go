import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CatalogSnapshot } from '../../../catalog/client'
import { LanguageProvider } from '../../../i18n/LanguageProvider'

// The page's only dependency on the host is the catalog client: mock it, so the
// binding flow (pick a runtime, pick a model, save) is exercised without a
// desktop build.
const snapshot: CatalogSnapshot = {
  runtimes: [
    {
      id: 'pi:D:\\\\tools\\\\pi',
      runtimeId: 'pi',
      displayName: 'Pi Agent',
      version: '1.2.3',
      installPath: 'D:\\tools\\pi',
      installSource: 'path',
      configPath: null,
      status: 'available',
      capabilities: {},
      health: {},
      lastSeen: '2026-11-14T00:00:00Z',
      firstSeen: '2026-11-14T00:00:00Z',
      snapshotState: 'ready',
      adapterRegistered: true,
    },
  ],
  models: [
    {
      runtimeId: 'pi',
      modelId: 'auto/fast',
      displayName: 'Fast',
      origin: 'discovered',
      protocol: null,
      endpoint: null,
      verified: true,
      verifiedAt: null,
      isDefault: true,
      enabled: true,
      updatedAt: '2026-11-14T00:00:00Z',
      hasCredential: false,
    },
    {
      runtimeId: 'pi',
      modelId: 'auto/deep',
      displayName: 'Deep',
      origin: 'discovered',
      protocol: null,
      endpoint: null,
      verified: true,
      verifiedAt: null,
      isDefault: false,
      enabled: false,
      updatedAt: '2026-11-14T00:00:00Z',
      hasCredential: false,
    },
  ],
  agents: [
    {
      id: 'default',
      name: 'Default Agent',
      runtimeId: 'pi',
      modelId: 'auto/fast',
      isDefault: true,
      updatedAt: '2026-11-14T00:00:00Z',
    },
    {
      id: 'agent-1',
      name: '代码助手',
      runtimeId: 'pi',
      modelId: 'auto/fast',
      isDefault: false,
      updatedAt: '2026-11-14T00:00:00Z',
    },
  ],
  skills: [],
  tools: [],
  mcp: [],
  syncs: [],
  adapterFailures: [],
  scannedAt: '2026-11-14T00:00:00Z',
} as unknown as CatalogSnapshot

const mocks = vi.hoisted(() => ({
  loadCatalog: vi.fn(),
  saveCatalogAgent: vi.fn(),
  deleteCatalogAgent: vi.fn(),
  refreshCatalog: vi.fn(),
}))

vi.mock('../../../catalog/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../catalog/client')>()
  return { ...actual, ...mocks }
})

import AgentsLive from '../AgentsLive'

function renderPage() {
  return render(
    <LanguageProvider>
      <AgentsLive />
    </LanguageProvider>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
  vi.clearAllMocks()
  mocks.loadCatalog.mockResolvedValue(snapshot)
  mocks.saveCatalogAgent.mockResolvedValue(snapshot)
  mocks.deleteCatalogAgent.mockResolvedValue(snapshot)
  mocks.refreshCatalog.mockResolvedValue(snapshot)
})

afterEach(cleanup)

describe('agents live bindings', () => {
  it('shows the runtime and model each agent is bound to', async () => {
    renderPage()
    const list = await screen.findByTestId('agents-live-list')
    const builtIn = within(list).getByTestId('agents-live-row-default')
    expect(builtIn).toHaveTextContent('默认 Agent')
    expect(builtIn).toHaveTextContent('pi')
    expect(builtIn).toHaveTextContent('auto/fast')
  })

  it('binds the default agent to a runtime and a model the user picks', async () => {
    renderPage()
    const list = await screen.findByTestId('agents-live-list')
    const builtIn = within(list).getByTestId('agents-live-row-default')
    await userEvent.click(within(builtIn).getByRole('button', { name: '编辑' }))

    const runtime = await screen.findByLabelText('运行时')
    expect(runtime).toHaveValue('pi')
    const model = screen.getByLabelText('模型')
    await userEvent.selectOptions(model, 'auto/deep')
    await userEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() =>
      expect(mocks.saveCatalogAgent).toHaveBeenCalledWith({
        id: 'default',
        name: 'Default Agent',
        runtimeId: 'pi',
        modelId: 'auto/deep',
      }),
    )
  })

  it('creates a named agent and refuses a nameless one', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.click(screen.getByRole('button', { name: '新建 Agent' }))

    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByTestId('agents-live-error')).toHaveTextContent('请填写 Agent 名称')
    expect(mocks.saveCatalogAgent).not.toHaveBeenCalled()

    await userEvent.type(screen.getByLabelText('名称'), '研究助手')
    await userEvent.selectOptions(screen.getByLabelText('模型'), 'auto/fast')
    await userEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() =>
      expect(mocks.saveCatalogAgent).toHaveBeenCalledWith({
        id: undefined,
        name: '研究助手',
        runtimeId: 'pi',
        modelId: 'auto/fast',
      }),
    )
  })

  it('deletes a saved agent only after a second click', async () => {
    renderPage()
    const list = await screen.findByTestId('agents-live-list')
    const row = within(list).getByTestId('agents-live-row-agent-1')
    await userEvent.click(within(row).getByRole('button', { name: '删除' }))
    expect(mocks.deleteCatalogAgent).not.toHaveBeenCalled()
    await userEvent.click(within(row).getByRole('button', { name: '确认删除' }))
    await waitFor(() => expect(mocks.deleteCatalogAgent).toHaveBeenCalledWith('agent-1'))
  })

  it('never offers to delete the built-in agent', async () => {
    renderPage()
    const list = await screen.findByTestId('agents-live-list')
    const builtIn = within(list).getByTestId('agents-live-row-default')
    expect(within(builtIn).queryByRole('button', { name: '删除' })).toBeNull()
  })
})
