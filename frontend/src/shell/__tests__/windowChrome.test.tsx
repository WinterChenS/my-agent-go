import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../i18n/LanguageProvider'
import type { TauriInternals } from '../../host/tauri'
import WindowChrome, { windowChromeKind } from '../WindowChrome'

interface HostCall {
  command: string
  args?: Record<string, unknown>
}

function installHost() {
  const calls: HostCall[] = []
  const internals = {
    invoke: async (command: string, args?: Record<string, unknown>) => {
      calls.push({ command, args })
      return null
    },
  }
  ;(window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__ =
    internals as unknown as TauriInternals
  return calls
}

function clearHost() {
  delete (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__
  delete document.documentElement.dataset.windowChrome
}

function setChrome(kind: string) {
  document.documentElement.dataset.windowChrome = kind
}

function renderChrome() {
  return render(
    <LanguageProvider>
      <WindowChrome />
    </LanguageProvider>,
  )
}

afterEach(() => {
  cleanup()
  clearHost()
  document.documentElement.lang = 'zh-CN'
})

describe('window chrome', () => {
  it('draws nothing on Windows, where the system title bar exists', () => {
    setChrome('windows')
    const { container } = renderChrome()
    expect(windowChromeKind()).toBe('windows')
    expect(container.querySelector('.window-drag-strip')).toBeNull()
    expect(container.querySelector('.window-controls')).toBeNull()
  })

  it('draws nothing when the platform was not injected (plain browser or tests)', () => {
    const { container } = renderChrome()
    expect(windowChromeKind()).toBe('')
    expect(container.querySelector('.window-drag-strip')).toBeNull()
  })

  it('reserves a drag strip but no buttons on macOS, which keeps its traffic lights', () => {
    setChrome('macos')
    const { container } = renderChrome()
    expect(container.querySelector('.window-drag-strip')).toHaveAttribute(
      'data-tauri-drag-region',
    )
    expect(container.querySelector('.window-controls')).toBeNull()
    expect(screen.queryByRole('button', { name: '最小化' })).toBeNull()
  })

  it('draws the three window buttons on Linux, which runs borderless', async () => {
    const calls = installHost()
    const user = userEvent.setup()
    setChrome('linux')
    const { container } = renderChrome()
    expect(container.querySelector('.window-drag-strip')).toBeTruthy()
    expect(screen.getByRole('group', { name: '窗口控制' })).toBeTruthy()

    await user.click(screen.getByRole('button', { name: '最小化' }))
    await user.click(screen.getByRole('button', { name: '最大化' }))
    await user.click(screen.getByRole('button', { name: '关闭' }))

    const commands = calls.map((call) => call.command)
    expect(commands).toEqual([
      'plugin:window|minimize',
      'plugin:window|toggle_maximize',
      'plugin:window|close',
    ])
    // No label argument: Tauri resolves the calling window itself.
    for (const call of calls) expect(call.args).toBeUndefined()
  })

  it('does not fail loudly when the window command is unavailable', async () => {
    const user = userEvent.setup()
    setChrome('linux')
    // No host installed at all: the click must not throw into the page.
    renderChrome()
    await user.click(screen.getByRole('button', { name: '关闭' }))
    expect(screen.getByRole('button', { name: '关闭' })).toBeTruthy()
  })
})
