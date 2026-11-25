// The chat pane has two ways to show a page, and only one of them involves the
// host: a native child webview on Windows/macOS, a frame on Linux (where Tauri
// cannot place a second webview at all — see `browserPane.ts`).
//
// These tests pin the frame path, because that one is the easy thing to break
// silently: an iframe that is never rendered, or a `bounds` report that still
// fires and ends up moving a native view that does not exist.

import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RailView } from '../../../chat/client'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import ChatRail, { type RailLive } from '../ChatRail'

const invokeHost = vi.fn(async (_command: string, _args?: unknown) => undefined)

vi.mock('../../../host/tauri', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../host/tauri')>()
  return { ...actual, invokeHost: (command: string, args?: unknown) => invokeHost(command, args) }
})

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function emptyView(): RailView {
  return {
    turns: [],
    exec: [],
    artifacts: [],
    sources: [],
    references: [],
    subagents: [],
    browser: { state: 'unavailable', reason: '' },
    outsidePaths: 0,
    empty: false,
  }
}

function renderPane(url: string, bounds = vi.fn()) {
  const live: RailLive = {
    view: emptyView(),
    running: false,
    browserUrl: url,
    browser: { open: vi.fn(), close: vi.fn(), action: vi.fn(), bounds },
    onTurn: () => undefined,
  }
  render(
    <LanguageProvider environment={environment}>
      <ChatRail
        sample={false}
        live={live}
        open
        tab="browser"
        onTab={() => undefined}
        onClose={() => undefined}
        onNotice={() => undefined}
        onResize={() => undefined}
        width={420}
      />
    </LanguageProvider>,
  )
  return bounds
}

beforeEach(() => {
  invokeHost.mockClear()
  delete document.documentElement.dataset.browserPane
})

afterEach(cleanup)

describe('chat pane browser', () => {
  it('uses the native view when the host can place one', () => {
    const bounds = renderPane('https://example.com/report')
    // No frame on this path: the native layer is drawn over the pane.
    expect(screen.queryByTestId('chat-rail-browser-page')).toBeNull()
    expect(screen.queryByTestId('chat-browser-framed-note')).toBeNull()
    expect(screen.getByTestId('chat-browser-reload')).toBeInTheDocument()
    // The pane reports where it is, timestamp and size included.
    expect(bounds).toHaveBeenCalled()
  })

  it('embeds the page in a frame when the host cannot place a view', async () => {
    document.documentElement.dataset.browserPane = 'iframe'
    const bounds = renderPane('https://example.com/report')

    const frame = screen.getByTestId('chat-rail-browser-page')
    expect(frame.tagName).toBe('IFRAME')
    expect(frame).toHaveAttribute('src', 'https://example.com/report')
    // A frame cannot be driven by the host, so its controls are not offered and
    // no placement is reported (there is nothing to place).
    expect(screen.queryByTestId('chat-browser-reload')).toBeNull()
    expect(screen.queryByTestId('chat-browser-back')).toBeNull()
    expect(bounds).not.toHaveBeenCalled()
    expect(screen.getByTestId('chat-browser-framed-note')).toHaveTextContent('内嵌框架')

    // The one way out of a page that refuses to be embedded.
    await userEvent.click(screen.getByTestId('chat-browser-external'))
    expect(invokeHost).toHaveBeenCalledWith('open_external', { url: 'https://example.com/report' })
  })

  it('states why a framed page can stay blank instead of showing nothing', () => {
    document.documentElement.dataset.browserPane = 'iframe'
    renderPane('')
    // No address yet: the pane keeps its empty state, and the frame note still
    // explains the platform instead of leaving a silent gap.
    expect(screen.getByTestId('chat-rail-browser-unavailable')).toBeInTheDocument()
    expect(screen.getByTestId('chat-browser-framed-note')).toBeInTheDocument()
    expect(screen.queryByTestId('chat-rail-browser-page')).toBeNull()
  })
})
