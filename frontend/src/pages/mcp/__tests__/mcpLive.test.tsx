import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import type { TauriInternals } from '../../../host/tauri'

const snapshot = {
  runtimes: [
    {
      id: 'pi:1',
      runtimeId: 'pi',
      displayName: 'Pi Agent',
      version: '1.5.0',
      installPath: 'C:/bin/pi.cmd',
      installSource: 'path',
      configPath: null,
      status: 'available',
      capabilities: {
        models: 'supported',
        customModels: 'supported',
        skills: 'supported',
        tools: 'supported',
        mcp: 'supported',
        sessions: 'supported',
      },
      health: { checkedAt: '2026-11-21T00:00:00Z', latencyMs: 1, detail: null },
      lastSeen: '2026-11-21T00:00:00Z',
      firstSeen: '2026-11-21T00:00:00Z',
      snapshotState: 'fresh',
      adapterRegistered: true,
    },
  ],
  agents: [],
  models: [],
  skills: [],
  tools: [],
  mcp: [],
  syncs: [],
  adapterFailures: [],
  scannedAt: '2026-11-21T00:00:00Z',
}

function installHost() {
  const calls: string[] = []
  const internals = {
    invoke: async (command: string) => {
      calls.push(command)
      if (command === 'catalog_snapshot' || command === 'catalog_refresh' || command === 'catalog_sync_inventory' || command === 'catalog_add_mcp') return snapshot
      if (command === 'catalog_test_mcp') return { ok: true, tools: ['read_file'], detail: null }
      if (command === 'catalog_probe_mcp') return { ok: true, tools: [], detail: null }
      if (command === 'session_list') return { sessions: [], total: 0 }
      if (command === 'session_projects') return []
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
})

afterEach(() => {
  cleanup()
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
})

describe('mcp live actions', () => {
  it('tests and saves a declaration through the host', async () => {
    const calls = installHost()
    render(
      <MemoryRouter initialEntries={['/mcp']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    const add = await waitFor(() => {
      const button = screen.getByRole('button', { name: '添加服务' })
      expect(button).not.toBeDisabled()
      return button
    })
    await userEvent.click(add)
    const dialog = await screen.findByRole('dialog', { name: '添加 MCP 服务' })
    expect(within(dialog).getByLabelText('运行器')).toHaveValue('pi')
    await userEvent.type(within(dialog).getByLabelText('服务名称'), 'demo')
    await userEvent.type(within(dialog).getByLabelText('启动命令或地址'), 'node demo.mjs')

    await userEvent.click(within(dialog).getByRole('button', { name: '测试连接' }))
    await waitFor(() => expect(screen.getByTestId('mcp-test-result')).toHaveTextContent('连接成功，发现 1 个工具'))
    expect(calls).toContain('catalog_test_mcp')

    await userEvent.click(within(dialog).getByRole('button', { name: '添加' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(calls).toContain('catalog_add_mcp')
    expect(screen.getByTestId('mcp-notice')).toHaveTextContent('已添加服务并重新同步配置。')
  })

  it('reconnects through a real sync command', async () => {
    const calls = installHost()
    render(
      <MemoryRouter initialEntries={['/mcp']}>
        <AppRoutes />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByTestId('mcp-catalog')).toBeInTheDocument())
    await userEvent.click(screen.getByTestId('mcp-reconnect'))
    await waitFor(() => expect(calls).toContain('catalog_sync_inventory'))
    expect(screen.getByTestId('mcp-notice')).toHaveTextContent('已重新同步并探测全部 MCP 服务。')
  })
})
