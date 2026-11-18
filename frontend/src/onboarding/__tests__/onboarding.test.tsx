// The first-run wizard is the first thing a new profile shows, so its behaviour
// has to hold without a device: it opens on the theme step, the theme it picks
// is the app's own, and the runtime step enables exactly the models that are
// checked. Colour/contrast is checked against rendered output in
// .tmp/coordination/cdp-onboarding-theme-verify.mjs — jsdom has no colour
// pipeline, so this file deliberately stops at behaviour.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import App from '../../App'
import type { TauriInternals } from '../../host/tauri'

const PI_MODEL = {
  runtimeId: 'pi',
  modelId: 'deepseek-v4',
  displayName: 'DeepSeek V4.1 Flash',
  enabled: false,
  verified: true,
  isDefault: false,
}

function catalog(runtimes: Array<Record<string, unknown>>, models: Array<Record<string, unknown>>) {
  return {
    runtimes,
    models,
    agents: [],
    skills: [],
    tools: [],
    mcp: [],
    syncs: [],
    adapterFailures: [],
    scannedAt: '2026-11-18T00:00:00Z',
  }
}

function installHost(snapshot = catalog([], [])) {
  const calls: Array<{ command: string; args: Record<string, unknown> | undefined }> = []
  const internals = {
    invoke: async (command: string, args: Record<string, unknown> | undefined) => {
      calls.push({ command, args })
      switch (command) {
        case 'catalog_snapshot':
        case 'catalog_refresh':
        case 'catalog_sync_inventory':
        case 'catalog_set_model_enabled':
        case 'catalog_set_default_model':
          return snapshot
        default:
          return null
      }
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('first-run wizard', () => {
  it('opens on the theme step and drives the window theme', async () => {
    const user = userEvent.setup()
    installHost()
    render(<App />)

    // Step 1 needs no catalog data, so it is on screen immediately — that is the
    // whole point of starting discovery in the background.
    const group = await screen.findByTestId('onboarding-theme')
    expect(screen.getByTestId('onboarding-step-1')).toBeInTheDocument()
    expect(group.querySelectorAll('[role="radio"]')).toHaveLength(3)

    await user.click(screen.getByTestId('onboarding-theme-dark'))
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
    // Same preference the rest of the app reads, so 设置 → 外观 agrees.
    expect(window.localStorage.getItem('anvil.theme')).toBe('dark')
    expect(screen.getByTestId('onboarding-theme-dark')).toHaveAttribute(
      'data-selected',
      'true',
    )

    await user.click(screen.getByTestId('onboarding-theme-light'))
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('light'))
    expect(window.localStorage.getItem('anvil.theme')).toBe('light')

    await user.click(screen.getByTestId('onboarding-theme-system'))
    await waitFor(() =>
      expect(screen.getByTestId('onboarding-theme-system')).toHaveAttribute(
        'data-selected',
        'true',
      ),
    )
  })

  it('moves to the runtime step and can come back', async () => {
    const user = userEvent.setup()
    installHost()
    render(<App />)

    await user.click(await screen.findByTestId('onboarding-next'))
    expect(await screen.findByTestId('onboarding-step-2')).toBeInTheDocument()

    await user.click(screen.getByTestId('onboarding-back'))
    expect(await screen.findByTestId('onboarding-step-1')).toBeInTheDocument()
  })

  it('says so when nothing is installed, and can be dismissed', async () => {
    const user = userEvent.setup()
    installHost(catalog([
      { runtimeId: 'pi', displayName: 'Pi Agent', status: 'not_installed', version: null },
    ], []))
    render(<App />)

    await user.click(await screen.findByTestId('onboarding-next'))
    expect(await screen.findByText(/本机还没有可用的运行时/)).toBeInTheDocument()
    // No runtime to install from an empty prefix either: it is a row with a
    // button, and the finish button has nothing to enable.
    expect(screen.getByTestId('onboarding-install-pi')).toBeEnabled()
    expect(screen.getByTestId('onboarding-finish')).toBeDisabled()
    expect(screen.getByTestId('onboarding-skip')).toBeEnabled()
  })

  it('enables one model per checked runtime and asks nothing about models', async () => {
    const user = userEvent.setup()
    const calls = installHost(
      catalog(
        [
          {
            runtimeId: 'pi',
            displayName: 'Pi Agent',
            status: 'available',
            version: '1.5.0',
          },
        ],
        [PI_MODEL, { ...PI_MODEL, modelId: 'gpt-5', displayName: 'GPT-5', verified: false }],
      ),
    )
    render(<App />)

    await user.click(await screen.findByTestId('onboarding-next'))
    const row = await screen.findByTestId('onboarding-runtime-pi')
    // pi is the only one checked by default, and it already shows which model
    // it will use — without offering a list to choose from.
    expect(row).toHaveAttribute('data-checked', 'true')
    expect(within(row).getByTestId('onboarding-pick-pi')).toHaveTextContent(
      'DeepSeek V4.1 Flash',
    )
    expect(document.querySelectorAll('[data-testid^="onboarding-model-"]')).toHaveLength(0)

    await user.click(screen.getByTestId('onboarding-finish'))

    await waitFor(() =>
      expect(
        calls.filter((call) => call.command === 'catalog_set_model_enabled').map((c) => c.args),
      ).toEqual([{ runtimeId: 'pi', modelId: 'deepseek-v4', enabled: true }]),
    )
    // The verified one is promoted so the next launch has a known default.
    expect(
      calls.some(
        (call) =>
          call.command === 'catalog_set_default_model' &&
          call.args?.modelId === 'deepseek-v4',
      ),
    ).toBe(true)
    await waitFor(() => expect(screen.queryByTestId('onboarding')).toBeNull())
  })

  it('still finishes when the leading model was never verified', async () => {
    const user = userEvent.setup()
    const calls = installHost(
      catalog(
        [{ runtimeId: 'pi', displayName: 'Pi Agent', status: 'available', version: '1.5.0' }],
        [{ ...PI_MODEL, verified: false }],
      ),
    )
    render(<App />)

    await user.click(await screen.findByTestId('onboarding-next'))
    await user.click(screen.getByTestId('onboarding-finish'))

    // Enabling is what makes the composer work; the promotion is optional, so a
    // store that refuses an unverified default must not strand the user here.
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.command === 'catalog_set_model_enabled' &&
            call.args?.modelId === 'deepseek-v4',
        ),
      ).toBe(true),
    )
    await waitFor(() => expect(screen.queryByTestId('onboarding')).toBeNull())
  })
})
