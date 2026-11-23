import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../i18n/LanguageProvider'
import type { TauriInternals } from '../../host/tauri'
import {
  CatalogMcpPanel,
  CatalogModelsPanel,
  CatalogRuntimeTable,
  CatalogSkillsPanel,
  CatalogSummary,
  SubagentExtensionPanel,
} from '../CatalogPanels'
import { useCatalog, type CatalogView } from '../useCatalog'
import { normalizeAgent, type CatalogAgent, type CatalogSnapshot } from '../client'
import { readCachedSnapshot, writeCachedSnapshot } from '../cache'

interface HostCall {
  command: string
  args?: Record<string, unknown>
}

function snapshotFixture(): CatalogSnapshot {
  return {
    agents: [
      normalizeAgent({
        id: 'default',
        name: 'Default Agent',
        runtimeId: 'pi',
        modelId: 'custom-1',
        isDefault: true,
        updatedAt: '2026-01-01T00:00:00Z',
      } as CatalogAgent),
    ],
    runtimes: [
      {
        id: 'pi:abc',
        runtimeId: 'pi',
        displayName: 'Pi Agent',
        version: '1.5.0',
        installPath: 'C:\\Users\\Public\\pi.CMD',
        installSource: 'path',
        configPath: 'C:\\Users\\Public\\.pi\\agent',
        status: 'available',
        capabilities: {
          models: 'supported',
          customModels: 'supported',
          skills: 'supported',
          tools: 'unverified',
          mcp: 'supported',
          sessions: 'supported',
        },
        health: { checkedAt: '2026-01-01T00:00:00Z', latencyMs: 12, detail: null },
        lastSeen: '2026-01-01T00:00:00Z',
        firstSeen: '2026-01-01T00:00:00Z',
        snapshotState: 'fresh',
        adapterRegistered: true,
      },
      {
        id: 'claude:not-installed',
        runtimeId: 'claude',
        displayName: 'Claude Code',
        version: null,
        installPath: '',
        installSource: 'path',
        configPath: null,
        status: 'not_installed',
        capabilities: {
          models: 'unsupported',
          customModels: 'unsupported',
          skills: 'supported',
          tools: 'unverified',
          mcp: 'supported',
          sessions: 'supported',
        },
        health: { checkedAt: '2026-01-01T00:00:00Z', latencyMs: 0, detail: null },
        lastSeen: '2026-01-01T00:00:00Z',
        firstSeen: '2026-01-01T00:00:00Z',
        snapshotState: 'never',
        adapterRegistered: true,
      },
    ],
    models: [
      {
        runtimeId: 'pi',
        modelId: 'auto/gpt-5.6-sol',
        displayName: 'GPT-5.6 Sol',
        origin: 'discovered',
        protocol: 'openai-responses',
        endpoint: null,
        verified: false,
        verifiedAt: null,
        isDefault: false,
        enabled: true,
        updatedAt: '2026-01-01T00:00:00Z',
        hasCredential: false,
      },
      {
        runtimeId: 'pi',
        modelId: 'custom-1',
        displayName: 'Custom One',
        origin: 'custom',
        protocol: 'openai',
        endpoint: 'http://127.0.0.1:9/v1',
        verified: true,
        verifiedAt: '2026-01-01T00:00:00Z',
        isDefault: true,
        enabled: true,
        updatedAt: '2026-01-01T00:00:00Z',
        hasCredential: true,
      },
    ],
    skills: [
      {
        runtimeId: 'pi',
        name: 'demo',
        description: 'demo skill',
        root: 'C:\\Users\\Public\\.pi\\skills',
        path: 'C:\\Users\\Public\\.pi\\skills\\demo',
        state: 'valid',
        reason: null,
        updatedAt: '2026-01-01T00:00:00Z',
      },
      {
        runtimeId: 'claude',
        name: 'broken-skill',
        description: '',
        root: 'C:\\Users\\Public\\.claude\\skills',
        path: 'C:\\Users\\Public\\.claude\\skills\\broken',
        state: 'broken',
        reason: 'SKILL.md has no YAML frontmatter',
        updatedAt: '2026-01-01T00:00:00Z',
      },
    ],
    tools: [],
    mcp: [
      {
        runtimeId: 'codex',
        name: 'pencil',
        transport: 'stdio',
        command: 'D:\\Tools\\pencil.exe',
        args: ['--app', 'desktop'],
        url: null,
        envKeys: ['PENCIL_TOKEN'],
        headerKeys: [],
        source: '~/.codex/config.toml',
        state: 'declared',
        probedAt: null,
      },
    ],
    syncs: [
      {
        runtimeId: 'pi',
        kind: 'models',
        ok: true,
        itemCount: 2,
        fetchedAt: '2026-01-01T00:00:00Z',
        stale: false,
        error: null,
      },
      {
        runtimeId: 'codex',
        kind: 'mcp',
        ok: false,
        itemCount: 1,
        fetchedAt: '2026-01-01T00:00:00Z',
        stale: true,
        error: 'config unreadable',
      },
    ],
    adapterFailures: [],
    scannedAt: '2026-01-01T00:00:00Z',
  }
}

function installHost(overrides: Partial<Record<string, (args?: Record<string, unknown>) => unknown>> = {}) {
  const calls: HostCall[] = []
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      const override = overrides[command]
      if (override) return override(args)
      if (command === 'catalog_snapshot') return snapshotFixture()
      if (command === 'catalog_add_model') return snapshotFixture()
      if (command === 'catalog_refresh') return snapshotFixture()
      // The models panel syncs the tab it opens; the inventory poll is what
      // fills skills/MCP after mount.
      if (command === 'catalog_sync') return snapshotFixture()
      if (command === 'catalog_sync_inventory') return snapshotFixture()
      if (command === 'catalog_verify_model') return { ok: true, detail: 'HTTP 200' }
      if (command === 'catalog_set_default_model') return snapshotFixture()
      if (command === 'catalog_probe_mcp') return { ok: true, tools: ['echo', 'ping'], detail: '2 tools' }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

function clearHost() {
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
}

function Harness({ children }: { children: (view: CatalogView) => React.ReactNode }) {
  const view = useCatalog(true)
  return <div>{children(view)}</div>
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  clearHost()
})

describe('catalog panels', () => {
  it('stays unavailable outside the desktop host', async () => {
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogSummary view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-summary')).toHaveTextContent('桌面宿主'))
  })

  it('treats an empty snapshot that is still scanning as loading, not empty', async () => {
    // The host answers from disk and discovers behind it. On a fresh profile
    // that first answer has no rows — which must not read as "nothing is
    // installed" while the pass is still running.
    const scanning = () => ({ ...snapshotFixture(), runtimes: [], models: [], scanning: true })
    installHost({ catalog_snapshot: scanning, catalog_sync_inventory: scanning })
    render(
      <LanguageProvider>
        <Harness>
          {(view) => (
            <>
              <span data-testid="status">{view.status}</span>
              <CatalogRuntimeTable view={view} />
            </>
          )}
        </Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('loading'))
    expect(screen.queryByTestId('catalog-runtimes-empty')).not.toBeInTheDocument()
  })

  it('does not cache an empty snapshot taken mid-discovery', async () => {
    // "There is a cache" is what keeps the wizard closed for a returning
    // window, so an empty one must never be written.
    writeCachedSnapshot({ ...snapshotFixture(), runtimes: [], models: [], scanning: true })
    expect(readCachedSnapshot()).toBeNull()
    writeCachedSnapshot(snapshotFixture())
    expect(readCachedSnapshot()?.runtimes.length).toBeGreaterThan(0)
  })

  it('renders runtime status, versions and the rescan control', async () => {
    installHost()
    render(
      <LanguageProvider>
        <Harness>
          {(view) => (
            <>
              <CatalogSummary view={view} />
              <CatalogRuntimeTable view={view} />
            </>
          )}
        </Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-runtimes')).toBeInTheDocument())
    const pi = screen.getByTestId('catalog-runtime-pi')
    expect(within(pi).getByText('可用')).toBeInTheDocument()
    expect(within(pi).getByText('1.5.0')).toBeInTheDocument()
    const claude = screen.getByTestId('catalog-runtime-claude')
    expect(within(claude).getByText('未安装')).toBeInTheDocument()
    // The stale sync is surfaced instead of being hidden.
    expect(screen.getByTestId('catalog-problems')).toHaveTextContent('1 项同步问题')
  })

  it('lists models with origin, verification and the default marker', async () => {
    installHost()
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogModelsPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-models')).toBeInTheDocument())
    const discovered = screen.getByTestId('catalog-model-pi:auto/gpt-5.6-sol')
    expect(within(discovered).getByText('运行时提供')).toBeInTheDocument()
    expect(within(discovered).getByText('未验证')).toBeInTheDocument()
    const custom = screen.getByTestId('catalog-model-pi:custom-1')
    expect(within(custom).getByText('自定义')).toBeInTheDocument()
    expect(within(custom).getByText('默认')).toBeInTheDocument()
    expect(within(custom).getByText('已存凭据')).toBeInTheDocument()
    // Only a verified, non-default model can be promoted.
    const promote = within(discovered).getByRole('button', { name: '设为默认' })
    expect(promote).toBeDisabled()
  })

  it('syncs the open runtime and keeps the rows on screen while it runs', async () => {
    // A holder object, not a `let`: the compiler cannot see that the executor
    // runs, so it would narrow a `let` to `never` at the call site.
    const gate: { release?: () => void } = {}
    // The first read belongs to the tab itself; only the manual sync hangs.
    let reads = 0
    const calls = installHost({
      catalog_sync: () => {
        reads += 1
        if (reads === 1) return snapshotFixture()
        return new Promise((resolve) => {
          gate.release = () => resolve(snapshotFixture())
        })
      },
    })
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogModelsPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-models')).toBeInTheDocument())
    const sync = screen.getByTestId('catalog-models-sync')
    expect(sync).toHaveAccessibleName('同步模型')
    await waitFor(() => expect(sync).not.toBeDisabled())

    await user.click(sync)
    await waitFor(() =>
      expect(calls.filter((call) => call.command === 'catalog_sync')).toHaveLength(2),
    )
    // The table is still there and the control reports work in progress: a
    // sync must not blank the page it is refreshing.
    expect(screen.getByTestId('catalog-models')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('catalog-models-sync')).toHaveAttribute('aria-busy', 'true'))

    gate.release?.()
    await waitFor(() =>
      expect(screen.getByTestId('catalog-models-sync')).toHaveAttribute('aria-busy', 'false'),
    )
  })

  it('adds a custom model with the credential and re-reads the snapshot', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogModelsPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-models')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '添加自定义模型' }))
    const form = screen.getByTestId('catalog-model-form')
    await user.type(within(form).getByLabelText('模型 ID'), 'custom-2')
    await user.type(within(form).getByLabelText('端点'), 'https://example.invalid/v1')
    await user.type(within(form).getByLabelText('访问凭据'), 'sk-test')
    await user.click(within(form).getByRole('button', { name: '保存' }))

    await waitFor(() => {
      const added = calls.find((call) => call.command === 'catalog_add_model')
      expect(added).toBeTruthy()
      expect(added?.args).toMatchObject({
        runtimeId: 'pi',
        modelId: 'custom-2',
        endpoint: 'https://example.invalid/v1',
        credential: 'sk-test',
      })
    })
    expect(await screen.findByText('已添加自定义模型')).toBeInTheDocument()
  })

  it('verifies a model through the host and reports the result', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogModelsPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-models')).toBeInTheDocument())
    const row = screen.getByTestId('catalog-model-pi:custom-1')
    await user.click(within(row).getByRole('button', { name: '验证' }))
    await waitFor(() => {
      expect(calls.some((call) => call.command === 'catalog_verify_model')).toBe(true)
    })
    expect(await screen.findByText('custom-1 验证通过')).toBeInTheDocument()
  })

  it('shows skill counts and isolates broken skills', async () => {
    installHost()
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogSkillsPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-skills')).toBeInTheDocument())
    expect(screen.getByTestId('catalog-skills')).toHaveTextContent('2 个，来自 2 个运行时')
    const problems = screen.getByTestId('catalog-skill-problems')
    expect(problems).toHaveTextContent('broken-skill')
    expect(problems).toHaveTextContent('SKILL.md has no YAML frontmatter')
  })

  it('imports MCP declarations without values and probes on demand', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <Harness>{(view) => <CatalogMcpPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('catalog-mcp')).toBeInTheDocument())
    const row = screen.getByTestId('catalog-mcp-codex:pencil')
    expect(within(row).getByText('1 个键（值未导入）')).toBeInTheDocument()
    expect(within(row).queryByText('PENCIL_TOKEN')).toBeNull()

    await user.click(within(row).getByRole('button', { name: '连接并枚举' }))
    await waitFor(() => {
      expect(calls.some((call) => call.command === 'catalog_probe_mcp')).toBe(true)
    })
    expect(await screen.findByText('pencil 连接成功，2 个工具')).toBeInTheDocument()
  })
})

describe("pi's subagent extension on the runtime page", () => {
  const missing = {
    installed: false,
    agentDir: 'C:/Users/A/.pi/agent',
    extensionDir: 'C:/Users/A/.pi/agent/extensions/subagent',
    source: 'C:/pi/node_modules/@earendil-works/pi-coding-agent/examples/extensions/subagent',
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

  it('shows the missing state with its install path and installs from here', async () => {
    const notices: string[] = []
    const calls = installHost({
      pi_subagent_status: () => missing,
      pi_subagent_install: () => ({ written: 6, skipped: 0, status: installed }),
    })
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <Harness>
          {(view) => <SubagentExtensionPanel view={view} onNotice={(m) => notices.push(m)} />}
        </Harness>
      </LanguageProvider>,
    )

    const row = await screen.findByTestId('catalog-subagent')
    expect(within(row).getByTestId('catalog-subagent-state')).toHaveTextContent('未安装')
    // The destination is part of the offer, not a surprise. The status arrives
    // from a host call inside an effect, so wait for it before reading the path.
    await waitFor(() => expect(row).toHaveTextContent('extensions/subagent'))

    await user.click(within(row).getByTestId('catalog-subagent-install'))

    await waitFor(() => expect(calls.some((c) => c.command === 'pi_subagent_install')).toBe(true))
    await waitFor(() =>
      expect(within(row).getByTestId('catalog-subagent-state')).toHaveTextContent('已安装'),
    )
    expect(within(row).queryByTestId('catalog-subagent-install')).toBeNull()
    expect(row).toHaveTextContent('4 个 agent、3 个工作流')
    expect(notices).toContain('已安装 subagent 扩展（4 个可用子代理）。')
  })

  it('lists every extension pi loads, not only the subagent one', async () => {
    installHost({
      pi_subagent_status: () => installed,
      pi_extensions: () => [
        {
          name: 'subagent',
          kind: 'dir',
          scope: 'global',
          path: '/Users/me/.pi/agent/extensions/subagent',
          active: true,
        },
        {
          name: 'magpie',
          kind: 'file',
          scope: 'global',
          path: '/Users/me/.pi/agent/extensions/magpie.ts',
          active: true,
        },
        {
          name: 'hollow',
          kind: 'dir',
          scope: 'global',
          path: '/Users/me/.pi/agent/extensions/hollow',
          active: false,
          detail: 'no index.ts/index.js and no package.json manifest',
        },
      ],
    })
    render(
      <LanguageProvider>
        <Harness>{(view) => <SubagentExtensionPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    const list = await screen.findByTestId('catalog-extensions')
    await waitFor(() => expect(within(list).getAllByTestId('catalog-extension-row')).toHaveLength(3))
    const rows = within(list).getAllByTestId('catalog-extension-row')
    // The count is what pi will actually load, so the broken one is not in it.
    expect(list).toHaveTextContent('2 个可加载')
    expect(rows[0]).toHaveTextContent('subagent')
    expect(rows[0]).toHaveAttribute('data-active', 'true')
    expect(rows[2]).toHaveAttribute('data-active', 'false')
  })

  it('disables the install when the pi package ships no example', async () => {
    installHost({ pi_subagent_status: () => ({ ...missing, canInstall: false, source: undefined }) })
    render(
      <LanguageProvider>
        <Harness>{(view) => <SubagentExtensionPanel view={view} />}</Harness>
      </LanguageProvider>,
    )
    const row = await screen.findByTestId('catalog-subagent')
    expect(within(row).getByTestId('catalog-subagent-install')).toBeDisabled()
    expect(row).toHaveTextContent('请先更新 pi')
  })

  it('reports the host failure instead of pretending the install worked', async () => {
    const notices: string[] = []
    installHost({
      pi_subagent_status: () => missing,
      pi_subagent_install: () => {
        throw '{"code":"not_found","message":"pi executable not found on PATH"}'
      },
    })
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <Harness>
          {(view) => <SubagentExtensionPanel view={view} onNotice={(m) => notices.push(m)} />}
        </Harness>
      </LanguageProvider>,
    )
    const row = await screen.findByTestId('catalog-subagent')
    await user.click(within(row).getByTestId('catalog-subagent-install'))
    await waitFor(() => expect(notices.length).toBeGreaterThan(0))
    expect(notices[0]).toContain('not_found: pi executable not found on PATH')
    // Still missing, so the button is still there for a retry.
    expect(within(row).getByTestId('catalog-subagent-install')).toBeInTheDocument()
  })
})
