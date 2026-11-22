import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { normalizeAgent, type CatalogAgent, type CatalogSnapshot } from '../../../catalog/client'
import { LanguageProvider } from '../../../i18n/LanguageProvider'

const agent = (id: string, name: string, extra: Partial<CatalogAgent> = {}): CatalogAgent =>
  normalizeAgent({
    id,
    name,
    slug: id,
    description: '',
    runtimeId: 'pi',
    modelId: 'auto/fast',
    isDefault: id === 'default',
    updatedAt: '2026-11-14T00:00:00Z',
    ...extra,
  } as CatalogAgent)

const agents = [agent('default', 'Default Agent'), agent('agent-1', '代码助手')]

const snapshot: CatalogSnapshot = {
  runtimes: [
    {
      id: 'pi:tools',
      runtimeId: 'pi',
      displayName: 'Pi Agent',
      version: '1.2.3',
      installPath: 'D://tools//pi',
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
  agents,
  skills: [
    {
      runtimeId: 'pi',
      name: 'code-review',
      description: '按 diff 出具风险分级',
      root: '~/.pi/skills',
      path: 'D:/skills/code-review.md',
      state: 'valid',
      reason: null,
      updatedAt: '2026-11-14T00:00:00Z',
    },
    {
      runtimeId: 'pi',
      name: 'release-notes',
      description: '生成发布说明',
      root: '~/.pi/skills',
      path: 'D:/skills/release-notes.md',
      state: 'valid',
      reason: null,
      updatedAt: '2026-11-14T00:00:00Z',
    },
    {
      runtimeId: 'pi',
      name: 'test-plan',
      description: '列出回归用例',
      root: '~/.pi/skills',
      path: 'D:/skills/test-plan.md',
      state: 'valid',
      reason: null,
      updatedAt: '2026-11-14T00:00:00Z',
    },
  ],
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
  loadAgentRuns: vi.fn(),
  refreshCatalog: vi.fn(),
}))

vi.mock('../../../catalog/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../catalog/client')>()
  return { ...actual, ...mocks }
})

import AgentsLive from '../AgentsLive'

function LocationProbe() {
  const location = useLocation()
  return <span data-testid="location-probe">{`${location.pathname}${location.search}`}</span>
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/agents']}>
      <LanguageProvider>
        <Routes>
          <Route
            path="/agents"
            element={
              <>
                <AgentsLive />
                <LocationProbe />
              </>
            }
          />
          <Route path="/chat" element={<LocationProbe />} />
        </Routes>
      </LanguageProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
  vi.clearAllMocks()
  mocks.loadCatalog.mockResolvedValue(snapshot)
  mocks.saveCatalogAgent.mockResolvedValue(snapshot)
  mocks.deleteCatalogAgent.mockResolvedValue(snapshot)
  mocks.loadAgentRuns.mockResolvedValue([])
  mocks.refreshCatalog.mockResolvedValue(snapshot)
})

afterEach(cleanup)

describe('agents live page — prototype parity and real persona', () => {
  it('shows the runtime and model bound to each agent', async () => {
    renderPage()
    const list = await screen.findByTestId('agents-live-list')
    const builtIn = within(list).getByTestId('agents-live-row-default')
    expect(builtIn).toHaveTextContent('Default Agent')
    expect(builtIn).toHaveTextContent('auto/fast')
  })

  it('offers the prototype’s five tabs', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    const tabs = screen.getAllByRole('tab').map((node) => node.textContent)
    expect(tabs).toEqual(['基础配置', '指令与工具', '子 Agent 0', '权限边界', '运行记录'])
  })

  it('saves the persona: system prompt, thinking level, tool switch and skill', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')

    await userEvent.selectOptions(screen.getByLabelText('思考深度'), 'high')
    await userEvent.click(screen.getByRole('tab', { name: '指令与工具' }))

    const prompt = screen.getByLabelText('System prompt')
    await userEvent.type(prompt, '你是迁移工程师')
    await userEvent.click(screen.getByRole('button', { name: '{{repo_path}}' }))
    await userEvent.click(screen.getByRole('switch', { name: /bash 启用/ }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'code-review' }))

    await userEvent.click(screen.getByTestId('agent-save'))
    await waitFor(() => expect(mocks.saveCatalogAgent).toHaveBeenCalledTimes(1))
    const draft = mocks.saveCatalogAgent.mock.calls[0][0]
    expect(draft.id).toBe('default')
    expect(draft.thinking).toBe('high')
    expect(draft.systemPrompt).toContain('你是迁移工程师')
    expect(draft.systemPrompt).toContain('{{repo_path}}')
    expect(draft.tools).not.toContain('bash')
    expect(draft.skills).toContain('code-review')
  })

  it('selects all skills in one click and clears them just as fast', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.click(screen.getByRole('tab', { name: '指令与工具' }))

    const selectAll = screen.getByTestId('agent-skill-select-all')
    const clearAll = screen.getByTestId('agent-skill-clear-all')
    await userEvent.click(selectAll)

    expect(screen.getByTestId('agent-skill-counter')).toHaveTextContent('已选 3 / 3')
    for (const name of ['code-review', 'release-notes', 'test-plan']) {
      expect(screen.getByRole('checkbox', { name })).toBeChecked()
    }
    expect(selectAll).toBeDisabled()

    await userEvent.click(screen.getByTestId('agent-save'))
    await waitFor(() => expect(mocks.saveCatalogAgent).toHaveBeenCalledTimes(1))
    expect(mocks.saveCatalogAgent.mock.calls[0][0].skills).toHaveLength(3)

    await userEvent.click(clearAll)
    expect(screen.getByTestId('agent-skill-counter')).toHaveTextContent('已选 0 / 3')
    expect(clearAll).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: 'code-review' })).not.toBeChecked()
  })

  it('binds the agent to another model from the runtime’s real catalog', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.selectOptions(screen.getByLabelText('运行时模型'), 'auto/deep')
    await userEvent.click(screen.getByTestId('agent-save'))
    await waitFor(() => expect(mocks.saveCatalogAgent).toHaveBeenCalledTimes(1))
    expect(mocks.saveCatalogAgent.mock.calls[0][0]).toMatchObject({ modelId: 'auto/deep', runtimeId: 'pi' })
  })

  it('persists the permission, sub-agent and non-enforceable fields too', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.click(screen.getByRole('tab', { name: '权限边界' }))
    await userEvent.click(screen.getByRole('radio', { name: /只读/ }))
    await userEvent.type(screen.getByLabelText('可写目录'), 'docs/**')
    expect(screen.getByTestId('agent-permission-limit')).toHaveTextContent('不会强制拦截')

    await userEvent.click(screen.getByRole('tab', { name: /子 Agent/ }))
    // The hint now describes the real mechanism: a file pi reads, plus a roster
    // in the parent's prompt — not "saved but never used".
    expect(screen.getByTestId('agent-sub-limit')).toHaveTextContent('~/.pi/agent/agents')
    expect(screen.getByTestId('agent-sub-dir')).toHaveTextContent('主 Agent 的名单里就有它')
    await userEvent.selectOptions(screen.getByLabelText('委派策略'), 'explicit')

    await userEvent.click(screen.getByTestId('agent-save'))
    await waitFor(() => expect(mocks.saveCatalogAgent).toHaveBeenCalledTimes(1))
    expect(mocks.saveCatalogAgent.mock.calls[0][0]).toMatchObject({
      permission: 'readonly',
      writableDirs: 'docs/**',
      delegationPolicy: 'explicit',
    })
  })

  it('reads run records from the ledger instead of showing sample rows', async () => {
    mocks.loadAgentRuns.mockResolvedValue([
      {
        sessionId: 's-1',
        title: '迁移评估',
        at: '2026-11-14T00:00:00Z',
        turns: 3,
        toolCalls: 5,
        tokens: 1200,
        status: 'idle',
        kind: 'main',
        agentName: 'Default Agent',
        runKey: 's-1',
      },
    ])
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.click(screen.getByRole('tab', { name: '运行记录' }))
    // id and slug both go down: the ledger stores the slug, the row has an id.
    expect(mocks.loadAgentRuns).toHaveBeenCalledWith('default', 20, 'default')
    expect(screen.getByTestId('agent-runs-table')).toHaveTextContent('主 Agent')
    expect(await screen.findByText('迁移评估')).toBeTruthy()
    expect(screen.getByTestId('agent-runs-table')).toHaveTextContent('1200')
  })

  it('refuses a nameless agent and creates a named one', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.click(screen.getByTestId('agents-live-new'))
    await userEvent.click(screen.getByTestId('agent-save'))
    expect(await screen.findByTestId('agents-live-error')).toHaveTextContent('请填写 Agent 名称')
    expect(mocks.saveCatalogAgent).not.toHaveBeenCalled()

    await userEvent.type(screen.getByLabelText('名称'), '研究助手')
    await userEvent.type(screen.getByLabelText('标识'), 'research')
    await userEvent.click(screen.getByTestId('agent-save'))
    await waitFor(() => expect(mocks.saveCatalogAgent).toHaveBeenCalledTimes(1))
    expect(mocks.saveCatalogAgent.mock.calls[0][0]).toMatchObject({
      name: '研究助手',
      slug: 'research',
      runtimeId: 'pi',
      modelId: 'auto/fast',
    })
  })

  it('deletes a saved agent only after a second click and never the built-in one', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    expect(screen.queryByTestId('agent-delete')).toBeNull()
    await userEvent.click(screen.getByTestId('agents-live-row-agent-1'))
    const remove = screen.getByTestId('agent-delete')
    await userEvent.click(remove)
    expect(mocks.deleteCatalogAgent).not.toHaveBeenCalled()
    await userEvent.click(screen.getByTestId('agent-delete'))
    await waitFor(() => expect(mocks.deleteCatalogAgent).toHaveBeenCalledWith('agent-1'))
  })

  it('starts a conversation with the saved agent in the URL', async () => {
    renderPage()
    await screen.findByTestId('agents-live-list')
    await userEvent.click(screen.getByTestId('agent-start-chat'))
    expect(await screen.findByTestId('location-probe')).toHaveTextContent('/chat?agent=default')
  })
})
