// The first-run wizard is the first thing a new profile shows, so two things
// have to hold without a device: it offers the theme switch, and that switch is
// the app's own theme (not a private copy). The rest of its styling is checked
// against rendered contrast in .tmp/coordination/cdp-onboarding-theme-verify.mjs
// — jsdom has no colour pipeline, so this file deliberately stops at behaviour.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import App from '../../App'
import type { TauriInternals } from '../../host/tauri'

const EMPTY_CATALOG = {
  runtimes: [],
  models: [],
  agents: [],
  skills: [],
  tools: [],
  mcp: [],
  syncs: [],
  adapterFailures: [],
  scannedAt: '2026-11-18T00:00:00Z',
}

function installHost() {
  const calls: Array<{ command: string; args: unknown }> = []
  const internals = {
    invoke: async (command: string, args: unknown) => {
      calls.push({ command, args })
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') return EMPTY_CATALOG
      if (command === 'catalog_sync_inventory') return EMPTY_CATALOG
      return null
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
  it('offers the three theme modes and drives the window theme', async () => {
    const user = userEvent.setup()
    installHost()
    render(<App />)

    const group = await screen.findByTestId('onboarding-theme')
    expect(group).toBeInTheDocument()
    expect(group.querySelectorAll('[role="radio"]')).toHaveLength(3)

    await user.click(screen.getByTestId('onboarding-theme-dark'))
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
    // Same preference the rest of the app reads, so 设置 → 外观 agrees.
    expect(window.localStorage.getItem('anvil.theme')).toBe('dark')

    await user.click(screen.getByTestId('onboarding-theme-light'))
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('light'))
    expect(window.localStorage.getItem('anvil.theme')).toBe('light')
  })

  it('still lets the user set up runtimes when nothing is installed', async () => {
    installHost()
    render(<App />)

    // No runtimes on this profile: the wizard says so instead of showing an
    // empty checklist, and it can still be dismissed.
    expect(await screen.findByText(/本机还没有可用的运行时/)).toBeInTheDocument()
    expect(screen.getByTestId('onboarding-finish')).toBeDisabled()
    expect(screen.getByTestId('onboarding-skip')).toBeEnabled()
  })
})
