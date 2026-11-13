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
} from '../CatalogPanels'
import { useCatalog, type CatalogView } from '../useCatalog'
import type { CatalogSnapshot } from '../client'

interface HostCall {
  command: string
  args?: Record<string, unknown>
}

function snapshotFixture(): CatalogSnapshot {
  return {
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

  it('syncs models on demand and keeps the rows on screen while it runs', async () => {
    // A holder object, not a `let`: the compiler cannot see that the executor
    // runs, so it would narrow a `let` to `never` at the call site.
    const gate: { release?: () => void } = {}
    const calls = installHost({
      catalog_refresh: () =>
        new Promise((resolve) => {
          gate.release = () => resolve(snapshotFixture())
        }),
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

    await user.click(sync)
    await waitFor(() => expect(calls.some((call) => call.command === 'catalog_refresh')).toBe(true))
    // The table is still there and the control reports work in progress: a
    // rescan must not blank the page it is refreshing.
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
