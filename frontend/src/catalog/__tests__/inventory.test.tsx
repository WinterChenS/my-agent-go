import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../App'
import type { TauriInternals } from '../../host/tauri'

const SKILLS = [
  {
    runtimeId: 'pi',
    name: 'code-review',
    description: '按 diff 出具风险分级',
    root: 'C:/skills',
    path: 'C:/skills/code-review',
    state: 'valid',
    reason: null,
    updatedAt: '2026-11-15T00:00:00Z',
  },
  {
    runtimeId: 'codex',
    name: 'dashi-ppt',
    description: '生成设计稿 PPT',
    root: 'C:/skills',
    path: 'C:/skills/dashi-ppt',
    state: 'valid',
    reason: null,
    updatedAt: '2026-11-15T00:00:00Z',
  },
]

const MCP = [
  {
    runtimeId: 'pi',
    name: 'box',
    transport: 'http',
    command: null,
    args: [],
    url: 'https://mcp.example.test/mcp',
    envKeys: [],
    headerKeys: [],
    source: '~/.pi/agent/mcp.json',
    state: 'declared',
    probedAt: null,
  },
  {
    runtimeId: 'codex',
    name: 'pencil',
    transport: 'stdio',
    command: 'D:/tools/pencil.exe',
    args: ['--app', 'desktop'],
    url: null,
    envKeys: [],
    headerKeys: [],
    source: '~/.codex/config.toml',
    state: 'declared',
    probedAt: null,
  },
]

function snapshot(extraSkill = false) {
  return {
    runtimes: [],
    models: [],
    agents: [],
    skills: extraSkill
      ? [...SKILLS, { ...SKILLS[0], name: 'new-after-sync', path: 'C:/skills/new-after-sync' }]
      : SKILLS,
    tools: [],
    mcp: MCP,
    syncs: [],
    adapterFailures: [],
    scannedAt: '2026-11-15T00:00:00Z',
  }
}

function installHost() {
  const calls: string[] = []
  // The page syncs inventory once by itself right after mount, so the extra
  // skill belongs to the *second* read: the one the manual button asks for.
  let syncs = 0
  const internals = {
    invoke: async (command: string) => {
      calls.push(command)
      if (command === 'catalog_sync_inventory') {
        syncs += 1
        return snapshot(syncs > 1)
      }
      if (command === 'catalog_snapshot' || command === 'catalog_refresh') {
        return snapshot(false)
      }
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('synced inventory is visible and syncable', () => {
  it('renders the real skill rows on the skills page instead of the placeholder', async () => {
    installHost()
    renderAt('/skills')
    const grid = await screen.findByTestId('skills-grid')
    expect(grid).toHaveTextContent('code-review')
    expect(grid).toHaveTextContent('dashi-ppt')
    expect(screen.queryByTestId('skill-empty-list')).toBeNull()
    expect(screen.getAllByTestId('skill-item')).toHaveLength(2)

    const search = screen.getByTestId('skill-search')
    expect(search).not.toBeDisabled()
    await userEvent.type(search, 'dashi')
    expect(screen.getAllByTestId('skill-item')).toHaveLength(1)
    expect(screen.getByTestId('skills-grid')).toHaveTextContent('dashi-ppt')
  })

  it('manual sync calls the cheap inventory command and refreshes the rows', async () => {
    const calls = installHost()
    renderAt('/skills')
    await screen.findByTestId('skills-grid')
    await userEvent.click(screen.getByTestId('catalog-skills-sync'))
    await waitFor(() => expect(calls).toContain('catalog_sync_inventory'))
    expect(await screen.findByText('new-after-sync')).toBeTruthy()
    expect(screen.getByTestId('catalog-auto-sync')).toHaveTextContent('自动同步已开启')
  })

  it('renders real MCP declarations and a usable search', async () => {
    installHost()
    renderAt('/mcp')
    const cards = await screen.findAllByTestId('mcp-card')
    expect(cards).toHaveLength(2)
    expect(cards[0]).toHaveTextContent('box')
    expect(cards[1]).toHaveTextContent('pencil')
    expect(screen.getByTestId('mcp-stat-connected')).toHaveTextContent('0')
    expect(screen.getByTestId('mcp-search')).not.toBeDisabled()
    expect(screen.getByTestId('catalog-mcp-sync')).toBeTruthy()
  })

  it('auto sync runs when the window regains focus while the setting is on', async () => {
    const calls = installHost()
    renderAt('/skills')
    await screen.findByTestId('skills-grid')
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(calls).toContain('catalog_sync_inventory'))
  })

  it('the settings switches persist both preferences', async () => {
    installHost()
    renderAt('/settings#general')
    const timeline = await screen.findByTestId('settings-show-timeline')
    const autoSync = screen.getByTestId('settings-auto-sync')
    expect(timeline).toHaveAttribute('aria-checked', 'false')
    expect(autoSync).toHaveAttribute('aria-checked', 'true')

    await userEvent.click(timeline)
    await userEvent.click(autoSync)
    expect(window.localStorage.getItem('anvil.chat.showTimeline')).toBe('1')
    expect(window.localStorage.getItem('anvil.catalog.autoSync')).toBe('0')
    expect(screen.getByTestId('settings-show-timeline')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('settings-auto-sync')).toHaveAttribute('aria-checked', 'false')

    // The composer's send key is a preference too: Enter by default.
    const shortcut = screen.getByTestId('settings-send-shortcut') as HTMLSelectElement
    expect(shortcut.value).toBe('enter')
    await userEvent.selectOptions(shortcut, 'ctrl-enter')
    expect(window.localStorage.getItem('anvil.chat.sendShortcut')).toBe('ctrl-enter')
    expect((screen.getByTestId('settings-send-shortcut') as HTMLSelectElement).value).toBe(
      'ctrl-enter',
    )
  })
})
