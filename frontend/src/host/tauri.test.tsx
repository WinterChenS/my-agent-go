import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ThemeProvider, useTheme } from '../preferences/ThemeProvider'
import {
  SYSTEM_THEME_EVENT,
  getHostPreferences,
  isTauriHost,
  listenHost,
  reportHostLog,
  setHostPreferences,
  type TauriInternals,
} from './tauri'
import {
  createHostLanguageEnvironment,
  createTauriLanguageEnvironment,
} from '../i18n/language'
import {
  createHostThemeEnvironment,
  createTauriThemeEnvironment,
} from '../preferences/theme'

interface HostCall {
  command: string
  args?: Record<string, unknown>
}

function installTauriMock() {
  const calls: HostCall[] = []
  const callbacks = new Map<number, (payload: unknown) => void>()
  let nextId = 1
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      if (command === 'get_preferences') {
        return { theme: 'dark', systemDark: true, resolved: 'dark', language: 'en-US' }
      }
      if (command === 'set_preferences') {
        return { theme: 'light', systemDark: true, resolved: 'light', language: 'en-US' }
      }
      if (command === 'plugin:event|listen') return 1
      return null
    },
    transformCallback: (callback: (payload: unknown) => void) => {
      const id = nextId++
      callbacks.set(id, callback)
      return id
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return {
    calls,
    emit(event: string, payload: unknown) {
      for (const [id, callback] of callbacks) {
        callback({ event, id, payload })
      }
    },
  }
}

function removeTauriMock() {
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
}

function jsonResponse(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

beforeEach(() => {
  removeTauriMock()
})

afterEach(() => {
  cleanup()
  removeTauriMock()
  vi.unstubAllGlobals()
})

describe('jsdom without the Tauri host', () => {
  it('reports no host and keeps the fetch fallbacks', async () => {
    expect(isTauriHost()).toBe(false)
    const fetchMock = vi.fn(async () =>
      jsonResponse({ theme: 'light', systemDark: false, resolved: 'light', language: 'zh-CN' }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const host = createHostThemeEnvironment()
    await expect(host.storage.load()).resolves.toBe('light')
    expect(fetchMock).toHaveBeenCalledWith('/__anvil/preferences', { cache: 'no-store' })

    reportHostLog('layout-probe: viewport=1x1')
    expect(fetchMock).toHaveBeenCalledWith('/__anvil/log', {
      method: 'POST',
      headers: { 'Content-Type': 'text/plain' },
      body: 'layout-probe: viewport=1x1',
      keepalive: true,
    })
  })

  it('keeps the browser language environment on fetch and localStorage', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ language: 'en-US' }))
    vi.stubGlobal('fetch', fetchMock)
    const language = createHostLanguageEnvironment()
    await expect(language.storage.load()).resolves.toBe('en-US')
    expect(fetchMock).toHaveBeenCalledWith('/__anvil/preferences', { cache: 'no-store' })
  })
})

describe('Tauri host bridge', () => {
  it('invokes get_preferences/set_preferences for preferences', async () => {
    const mock = installTauriMock()
    expect(isTauriHost()).toBe(true)

    await expect(getHostPreferences()).resolves.toEqual({
      theme: 'dark',
      systemDark: true,
      resolved: 'dark',
      language: 'en-US',
    })
    await setHostPreferences({ theme: 'light' })
    expect(mock.calls).toEqual([
      { command: 'get_preferences', args: undefined },
      { command: 'set_preferences', args: { theme: 'light' } },
    ])
  })

  it('reads and writes preferences through typed commands from the theme environment', async () => {
    const mock = installTauriMock()
    const host = createTauriThemeEnvironment()

    await expect(host.storage.load()).resolves.toBe('dark')
    expect(host.systemDark()).toBe(true)
    await host.storage.save('light')
    expect(mock.calls.find((call) => call.command === 'set_preferences')?.args).toEqual({
      theme: 'light',
    })
  })

  it('subscribes to the pushed system theme event', async () => {
    const mock = installTauriMock()
    const host = createTauriThemeEnvironment()
    const seen: boolean[] = []
    const unsubscribe = host.subscribeSystem((dark) => seen.push(dark))

    await waitFor(() =>
      expect(mock.calls.some((call) => call.command === 'plugin:event|listen')).toBe(true),
    )
    const listen = mock.calls.find((call) => call.command === 'plugin:event|listen')
    expect(listen?.args).toMatchObject({
      event: SYSTEM_THEME_EVENT,
      target: { kind: 'Any' },
    })
    expect(typeof listen?.args?.handler).toBe('number')

    mock.emit(SYSTEM_THEME_EVENT, { systemDark: false, resolved: 'light' })
    expect(seen).toEqual([false])
    expect(host.systemDark()).toBe(false)

    unsubscribe()
    await waitFor(() =>
      expect(mock.calls.some((call) => call.command === 'plugin:event|unlisten')).toBe(true),
    )
  })

  it('writes theme, i18n and probe lines through log_event', async () => {
    const mock = installTauriMock()
    const theme = createTauriThemeEnvironment()
    theme.apply('dark', 'system')
    const language = createTauriLanguageEnvironment()
    language.apply('en-US')
    reportHostLog('layout-probe: viewport=1180x820')

    const logged = mock.calls
      .filter((call) => call.command === 'log_event')
      .map((call) => String(call.args?.message))
    expect(logged[0]).toMatch(/^theme: mode=system resolved=dark bg=/)
    expect(logged[1]).toBe('i18n: language=en-US htmlLang=en-US')
    expect(logged[2]).toBe('layout-probe: viewport=1180x820')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(document.documentElement.lang).toBe('en-US')
  })

  it('drives ThemeProvider with the Tauri environment', async () => {
    const mock = installTauriMock()
    function Probe() {
      const theme = useTheme()
      return (
        <div>
          <span data-testid="mode">{theme.mode}</span>
          <span data-testid="resolved">{theme.resolved}</span>
          <button onClick={() => theme.setMode('light')}>light</button>
        </div>
      )
    }
    render(
      <ThemeProvider environment={createTauriThemeEnvironment()}>
        <Probe />
      </ThemeProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('mode')).toHaveTextContent('dark'))
    expect(screen.getByTestId('resolved')).toHaveTextContent('dark')
    fireEvent.click(screen.getByText('light'))
    await waitFor(() =>
      expect(mock.calls.some((call) => call.command === 'set_preferences')).toBe(true),
    )
  })

  it('listenHost rejects when the host has no event bridge', async () => {
    installTauriMock()
    const internals = (window as unknown as { __TAURI_INTERNALS__: Partial<TauriInternals> })
      .__TAURI_INTERNALS__
    delete internals.transformCallback
    await expect(listenHost('x', () => {})).rejects.toThrow('events')
  })
})
