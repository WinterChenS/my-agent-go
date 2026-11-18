import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'

function runtime(runtimeId: string, displayName: string, status: string) {
  return {
    id: `${runtimeId}-install`,
    runtimeId,
    displayName,
    version: '1.0.0',
    installPath: `C:/tools/${runtimeId}`,
    installSource: 'path',
    configPath: null,
    status,
    capabilities: {
      session: 'supported',
      models: 'supported',
      skills: 'supported',
      tools: 'supported',
      mcp: 'supported',
      customModels: 'supported',
      streaming: 'supported',
      approvals: 'supported',
    },
    health: { checkedAt: '2026-11-16T00:00:00Z', latencyMs: 12, detail: null },
    lastSeen: '2026-11-16T00:00:00Z',
    firstSeen: '2026-11-16T00:00:00Z',
    snapshotState: 'fresh',
    adapterRegistered: true,
  }
}

function model(runtimeId: string, modelId: string, isDefault = false) {
  return {
    runtimeId,
    modelId,
    displayName: modelId,
    origin: 'discovered',
    protocol: 'openai',
    endpoint: 'http://127.0.0.1:3425/v1',
    verified: true,
    verifiedAt: '2026-11-16T00:00:00Z',
    isDefault,
    enabled: true,
    updatedAt: '2026-11-16T00:00:00Z',
    hasCredential: false,
  }
}

// `pi` is first in the strip even though the snapshot lists codex first.
function snapshot() {
  return {
    runtimes: [
      runtime('codex', 'Codex', 'available'),
      runtime('opencode', 'OpenCode', 'not_installed'),
      runtime('pi', 'Pi', 'available'),
    ],
    models: [
      model('codex', 'gpt-6.1-sol'),
      model('pi', 'auto/gpt-5.6-sol', true),
      model('pi', 'auto/glm-5.3-flash'),
    ],
    agents: [],
    skills: [],
    tools: [],
    mcp: [],
    syncs: [],
    adapterFailures: [],
    scannedAt: '2026-11-16T00:00:00Z',
  }
}

function installHost() {
  const calls: Array<{ command: string; args?: Record<string, unknown> }> = []
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      if (
        command === 'catalog_snapshot' ||
        command === 'catalog_refresh' ||
        command === 'catalog_sync' ||
        command === 'catalog_sync_inventory'
      ) {
        return snapshot()
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  const syncs = () =>
    calls
      .filter((call) => call.command === 'catalog_sync')
      .map((call) => String((call.args as { runtimeId?: string } | undefined)?.runtimeId))
  return { calls, syncs }
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('models page syncs one runtime at a time', () => {
  it('opens on the pi tab and only syncs the tab being looked at', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/settings#models']}>
        <AppRoutes />
      </MemoryRouter>,
    )

    await screen.findByTestId('catalog-model-tabs')
    const piTab = screen.getByTestId('catalog-model-tab-pi')
    const codexTab = screen.getByTestId('catalog-model-tab-codex')
    expect(piTab).toHaveAttribute('aria-selected', 'true')
    expect(codexTab).toHaveAttribute('aria-selected', 'false')
    // A runtime that is not installed gets no tab.
    expect(screen.queryByTestId('catalog-model-tab-opencode')).toBeNull()

    // Only the open tab's models are listed, and only it was synced.
    expect(await screen.findByTestId('catalog-model-pi:auto/gpt-5.6-sol')).toBeInTheDocument()
    expect(screen.queryByTestId('catalog-model-codex:gpt-6.1-sol')).toBeNull()
    await waitFor(() => expect(host.syncs()).toEqual(['pi']))

    await userEvent.click(codexTab)
    expect(await screen.findByTestId('catalog-model-codex:gpt-6.1-sol')).toBeInTheDocument()
    expect(screen.queryByTestId('catalog-model-pi:auto/gpt-5.6-sol')).toBeNull()
    await waitFor(() => expect(host.syncs()).toEqual(['pi', 'codex']))

    // Going back to a tab already read in this visit does not read it again.
    await userEvent.click(piTab)
    await screen.findByTestId('catalog-model-pi:auto/gpt-5.6-sol')
    expect(host.syncs()).toEqual(['pi', 'codex'])
  })

  it('syncs the open runtime from the sync button', async () => {
    const host = installHost()
    render(
      <MemoryRouter initialEntries={['/settings#models']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await screen.findByTestId('catalog-model-tabs')
    await waitFor(() => expect(host.syncs()).toEqual(['pi']))

    await userEvent.click(screen.getByTestId('catalog-models-sync'))
    await waitFor(() => expect(host.syncs()).toEqual(['pi', 'pi']))
  })
})
