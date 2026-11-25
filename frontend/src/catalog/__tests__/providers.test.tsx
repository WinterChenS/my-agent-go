import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../i18n/LanguageProvider'
import type { TauriInternals } from '../../host/tauri'
import {
  CatalogProvidersPanel,
  providerMatches,
  sortProviders,
} from '../ProvidersPanel'
import type { ProviderAccount, ProviderSnapshot } from '../client'

interface HostCall {
  command: string
  args?: Record<string, unknown>
}

function account(patch: Partial<ProviderAccount> & { id: string; name: string }): ProviderAccount {
  return {
    baseUrl: null,
    api: 'openai-completions',
    keyEnv: null,
    supportsKey: true,
    oauthLabel: null,
    oauthSubscription: false,
    extra: [],
    credential: null,
    source: 'builtin',
    baseUrlOverride: null,
    keyHint: null,
    models: [],
    ...patch,
  }
}

function providersFixture(): ProviderSnapshot {
  return {
    accounts: [
      account({
        id: 'anthropic',
        name: 'Anthropic',
        baseUrl: 'https://api.anthropic.com',
        api: 'anthropic-messages',
        keyEnv: 'ANTHROPIC_API_KEY',
        oauthLabel: 'Claude Pro/Max',
        oauthSubscription: true,
      }),
      account({
        id: 'deepseek',
        name: 'DeepSeek',
        keyEnv: 'DEEPSEEK_API_KEY',
        credential: 'api_key',
        keyHint: '••••1234',
      }),
      account({
        id: 'cloudflare-workers-ai',
        name: 'Cloudflare Workers AI',
        keyEnv: 'CLOUDFLARE_API_KEY',
        extra: [{ env: 'CLOUDFLARE_ACCOUNT_ID', label: 'Account ID', required: true }],
      }),
      account({
        id: 'my-gateway',
        name: 'my-gateway',
        source: 'custom',
        baseUrl: 'http://127.0.0.1:8080/v1',
        credential: 'api_key',
        keyHint: '••••9999',
        models: ['qwen3-coder'],
      }),
    ],
    authPath: '/home/u/.pi/agent/auth.json',
    modelsPath: '/home/u/.pi/agent/models.json',
    customApis: ['openai-completions', 'anthropic-messages'],
    warnings: [],
  }
}

type Handler = (event: unknown) => void

function installHost(
  overrides: Partial<Record<string, (args?: Record<string, unknown>) => unknown>> = {},
) {
  const calls: HostCall[] = []
  const handlers = new Map<number, Handler>()
  let nextId = 0
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      const override = overrides[command]
      if (override) return override(args)
      if (command === 'pi_providers') return providersFixture()
      if (command === 'pi_provider_set_key') return providersFixture()
      if (command === 'pi_provider_logout') return providersFixture()
      if (command === 'pi_provider_custom_save') return providersFixture()
      if (command === 'pi_provider_custom_remove') return providersFixture()
      if (command === 'pi_provider_login') return 'session-1'
      return null
    },
    transformCallback: (callback: Handler) => {
      const id = ++nextId
      handlers.set(id, callback)
      return id
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return {
    calls,
    emit: (payload: unknown) => {
      for (const handler of handlers.values()) handler({ payload })
    },
  }
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

describe('provider accounts panel', () => {
  it('lists the providers pi supports with the credential stored for each', async () => {
    installHost()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    const panel = await screen.findByTestId('catalog-providers')
    expect(within(panel).getByText('已连接 2 / 4 个 pi 支持的供应商')).toBeTruthy()

    const deepseek = screen.getByTestId('provider-row-deepseek')
    expect(within(deepseek).getByText('DeepSeek')).toBeTruthy()
    expect(within(deepseek).getByText('已存 Key ••••1234')).toBeTruthy()
    expect(within(deepseek).getByTestId('provider-logout-deepseek')).toBeTruthy()

    // A provider that can sign in offers both ways, and says which plan it is.
    const anthropic = screen.getByTestId('provider-row-anthropic')
    expect(within(anthropic).getByTestId('provider-key-anthropic')).toBeTruthy()
    expect(within(anthropic).getByTestId('provider-login-anthropic')).toBeTruthy()
    expect(within(anthropic).getByText('Claude Pro/Max · Pro')).toBeTruthy()

    // A custom provider is recognisable and removable, not "not installed".
    const custom = screen.getByTestId('provider-row-my-gateway')
    expect(within(custom).getByText('自定义')).toBeTruthy()
    expect(within(custom).getByText('模型 qwen3-coder')).toBeTruthy()
    expect(within(custom).getByTestId('provider-remove-my-gateway')).toBeTruthy()
  })

  it('filters by name and by connection state', async () => {
    installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )
    await screen.findByTestId('provider-row-anthropic')

    await user.type(screen.getByLabelText('搜索供应商'), 'cloud')
    await waitFor(() => expect(screen.queryByTestId('provider-row-deepseek')).toBeNull())
    expect(screen.getByTestId('provider-row-cloudflare-workers-ai')).toBeTruthy()

    await user.clear(screen.getByLabelText('搜索供应商'))
    await user.click(screen.getByRole('button', { name: '已连接' }))
    await waitFor(() => expect(screen.queryByTestId('provider-row-anthropic')).toBeNull())
    expect(screen.getByTestId('provider-row-deepseek')).toBeTruthy()
    expect(screen.getByTestId('provider-row-my-gateway')).toBeTruthy()
  })

  it('saves a key with the extra values the provider needs', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('provider-key-cloudflare-workers-ai'))
    const dialog = await screen.findByTestId('provider-key-dialog')
    await user.type(within(dialog).getByLabelText('API Key'), 'cf-token-1')
    await user.type(within(dialog).getByLabelText('Account ID'), 'acc-9')
    await user.click(within(dialog).getByTestId('provider-key-save'))

    await waitFor(() => {
      const call = host.calls.find((entry) => entry.command === 'pi_provider_set_key')
      expect(call).toBeTruthy()
      expect(call?.args).toMatchObject({
        providerId: 'cloudflare-workers-ai',
        key: 'cf-token-1',
        env: [{ name: 'CLOUDFLARE_ACCOUNT_ID', value: 'acc-9' }],
      })
    })
    await waitFor(() => expect(screen.queryByTestId('provider-key-dialog')).toBeNull())
  })

  it('refuses an empty key before calling the host', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('provider-key-deepseek'))
    const dialog = await screen.findByTestId('provider-key-dialog')
    await user.click(within(dialog).getByTestId('provider-key-save'))
    expect(await within(dialog).findByText('请填写 API Key')).toBeTruthy()
    expect(host.calls.some((entry) => entry.command === 'pi_provider_set_key')).toBe(false)
  })

  it('submits a custom provider with one model per line', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('catalog-providers-custom'))
    const dialog = await screen.findByTestId('provider-custom-dialog')
    await user.type(within(dialog).getByLabelText('供应商 ID'), 'intranet')
    await user.type(within(dialog).getByLabelText('端点地址'), 'http://10.0.0.2:8080/v1')
    await user.type(within(dialog).getByLabelText('模型 ID'), 'qwen3-coder{enter}gpt-5.6-sol')
    await user.type(within(dialog).getByLabelText('API Key（可选）'), 'sk-local')
    await user.click(within(dialog).getByTestId('provider-custom-save'))

    await waitFor(() => {
      const call = host.calls.find((entry) => entry.command === 'pi_provider_custom_save')
      expect(call).toBeTruthy()
      expect(call?.args).toMatchObject({
        draft: {
          id: 'intranet',
          baseUrl: 'http://10.0.0.2:8080/v1',
          api: 'openai-completions',
          apiKey: 'sk-local',
          models: ['qwen3-coder', 'gpt-5.6-sol'],
        },
      })
    })
  })

  it('requires a model id for a custom provider', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('catalog-providers-custom'))
    const dialog = await screen.findByTestId('provider-custom-dialog')
    await user.type(within(dialog).getByLabelText('供应商 ID'), 'intranet')
    await user.type(within(dialog).getByLabelText('端点地址'), 'http://10.0.0.2:8080/v1')
    await user.click(within(dialog).getByTestId('provider-custom-save'))
    expect(await within(dialog).findByText('至少填写一个模型 ID')).toBeTruthy()
    expect(host.calls.some((entry) => entry.command === 'pi_provider_custom_save')).toBe(false)
  })

  it('follows a browser sign-in through the host event stream', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('provider-login-anthropic'))
    const dialog = await screen.findByTestId('provider-login-dialog')
    await waitFor(() =>
      expect(host.calls.some((entry) => entry.command === 'pi_provider_login')).toBe(true),
    )

    host.emit({
      sessionId: 'session-1',
      providerId: 'anthropic',
      kind: 'auth_url',
      url: 'https://claude.ai/oauth/authorize?x=1',
      instructions: 'Complete login in your browser',
      options: [],
    })

    await waitFor(() =>
      expect(within(dialog).getByTestId('provider-login-open')).toBeTruthy(),
    )
    expect(within(dialog).getByText('Complete login in your browser')).toBeTruthy()
    // The page opens the authorization URL itself, in the system browser.
    await waitFor(() =>
      expect(
        host.calls.some(
          (entry) =>
            entry.command === 'open_external' &&
            entry.args?.url === 'https://claude.ai/oauth/authorize?x=1',
        ),
      ).toBe(true),
    )

    host.emit({
      sessionId: 'session-1',
      providerId: 'anthropic',
      kind: 'done',
      options: [],
    })
    await waitFor(() =>
      expect(within(dialog).getByTestId('provider-login-done')).toHaveTextContent(
        'Anthropic 已登录',
      ),
    )
  })

  it('answers a prompt the sign-in is waiting on', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('provider-login-anthropic'))
    const dialog = await screen.findByTestId('provider-login-dialog')
    await waitFor(() =>
      expect(host.calls.some((entry) => entry.command === 'pi_provider_login')).toBe(true),
    )

    host.emit({
      sessionId: 'session-1',
      providerId: 'anthropic',
      kind: 'prompt',
      promptId: '1',
      promptKind: 'manual_code',
      message: 'Paste the redirect URL',
      options: [],
    })

    const answer = await within(dialog).findByTestId('provider-login-answer')
    await user.type(answer, 'http://localhost:53692/callback?code=abc')
    await user.click(within(dialog).getByTestId('provider-login-submit'))

    await waitFor(() => {
      const call = host.calls.find((entry) => entry.command === 'pi_provider_login_submit')
      expect(call?.args).toMatchObject({
        sessionId: 'session-1',
        promptId: '1',
        value: 'http://localhost:53692/callback?code=abc',
      })
    })
  })

  it('cancels the sign-in when the dialog closes', async () => {
    const host = installHost()
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )

    await user.click(await screen.findByTestId('provider-login-anthropic'))
    await screen.findByTestId('provider-login-dialog')
    await waitFor(() =>
      expect(host.calls.some((entry) => entry.command === 'pi_provider_login')).toBe(true),
    )
    await user.click(screen.getByTestId('provider-login-close'))
    await waitFor(() =>
      expect(
        host.calls.some(
          (entry) =>
            entry.command === 'pi_provider_login_cancel' &&
            entry.args?.sessionId === 'session-1',
        ),
      ).toBe(true),
    )
  })

  it('surfaces a broken credential store instead of showing an empty list', async () => {
    installHost({
      pi_providers: () => ({
        ...providersFixture(),
        accounts: [],
        warnings: ['/home/u/.pi/agent/auth.json does not parse (expected value at line 1)'],
      }),
    })
    render(
      <LanguageProvider>
        <CatalogProvidersPanel />
      </LanguageProvider>,
    )
    expect(await screen.findByTestId('catalog-providers-warning')).toHaveTextContent(
      'auth.json does not parse',
    )
  })
})

describe('provider ordering helpers', () => {
  it('puts connected providers first, then the ones that can sign in', () => {
    const rows = sortProviders([
      account({ id: 'alpha', name: 'Alpha' }),
      account({ id: 'zulu', name: 'Zulu', credential: 'api_key' }),
      account({ id: 'beta', name: 'Beta', oauthLabel: 'Sign in' }),
    ])
    expect(rows.map((row) => row.id)).toEqual(['zulu', 'beta', 'alpha'])
  })

  it('matches on id and name, case-insensitively', () => {
    const row = account({ id: 'my-gateway', name: 'Internal Gateway' })
    expect(providerMatches(row, 'GATEWAY', 'all')).toBe(true)
    expect(providerMatches(row, 'internal', 'all')).toBe(true)
    expect(providerMatches(row, 'openai', 'all')).toBe(false)
    expect(providerMatches(row, 'anything', 'connected')).toBe(false)
  })
})
