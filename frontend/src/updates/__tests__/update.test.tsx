// The update card has one job that is easy to get wrong: never offer an install
// this build cannot perform. A `.deb` install is the case that matters — the
// button would download a package and then fail to place it, which looks like
// the feature is broken rather than like the platform has no in-place update.

import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LanguageProvider } from '../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../i18n/language'
import UpdateCard from '../UpdateCard'
import { checkForUpdate, installUpdate, resetUpdateStore, UPDATE_EVENT, type UpdateCheck } from '../store'

const invokeHost = vi.fn(async (_command: string, _args?: unknown): Promise<unknown> => undefined)
const listeners = new Map<string, (payload: unknown) => void>()

vi.mock('../../host/tauri', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../host/tauri')>()
  return {
    ...actual,
    invokeHost: (command: string, args?: unknown) => invokeHost(command, args),
    listenHost: async (event: string, handler: (payload: unknown) => void) => {
      listeners.set(event, handler)
      return () => listeners.delete(event)
    },
  }
})

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function renderCard() {
  return render(
    <LanguageProvider environment={environment}>
      <UpdateCard />
    </LanguageProvider>,
  )
}

function answer(check: UpdateCheck) {
  invokeHost.mockImplementation(async (command: string) => {
    if (command === 'app_update_check') return check
    return undefined
  })
}

const newer: UpdateCheck = {
  current: '0.1.17',
  available: {
    version: '0.1.18',
    notes: 'Fixes the Linux pane.',
    date: '2026-11-25 07:00:00.000 +00:00:00',
    target: 'windows-x86_64',
    size: null,
  },
  installable: true,
  reason: null,
  releaseUrl: 'https://github.com/WinterChenS/my-agent-go/releases/latest',
}

beforeEach(() => {
  invokeHost.mockReset()
  invokeHost.mockImplementation(async () => undefined)
  listeners.clear()
  resetUpdateStore()
})

afterEach(cleanup)

describe('app update', () => {
  it('reports the running version as the newest when nothing is published', async () => {
    answer({ ...newer, available: null })
    renderCard()
    expect(await screen.findByText(/已是最新版本（0.1.17）/)).toBeInTheDocument()
    expect(screen.queryByTestId('update-install')).toBeNull()
    // The version is shown next to the title, not only inside the sentence.
    expect(screen.getByText('v0.1.17')).toBeInTheDocument()
  })

  it('offers an in-place update when the build can replace itself', async () => {
    answer(newer)
    renderCard()
    const install = await screen.findByTestId('update-install')
    expect(screen.getByTestId('update-status')).toHaveTextContent('新版本 0.1.18 可用')
    expect(screen.queryByTestId('update-reason')).toBeNull()
    expect(install).toHaveTextContent('立即更新')
  })

  it('sends a package install to the release page instead of a broken button', async () => {
    answer({ ...newer, installable: false, reason: 'package' })
    renderCard()
    const download = await screen.findByTestId('update-download')
    // The reason explains *why*, which is the difference between "unsupported"
    // and "we forgot".
    expect(screen.getByTestId('update-reason')).toHaveTextContent('系统软件包')
    expect(screen.queryByTestId('update-install')).toBeNull()
    await userEvent.click(download)
    expect(invokeHost).toHaveBeenCalledWith('open_external', {
      url: 'https://github.com/WinterChenS/my-agent-go/releases/latest',
    })
  })

  it('moves through download progress driven by the host events', async () => {
    answer(newer)
    renderCard()
    await screen.findByTestId('update-install')
    // The install promise never resolves in production (the host restarts);
    // the test keeps it pending so the intermediate states are observable.
    invokeHost.mockImplementation(async (command: string) => {
      if (command === 'app_update_install') return new Promise(() => undefined)
      return newer
    })
    void installUpdate()
    await waitFor(() => expect(listeners.has(UPDATE_EVENT)).toBe(true))
    listeners.get(UPDATE_EVENT)?.({ phase: 'downloading', received: 512, total: 1024, rate: 100 })
    await waitFor(() =>
      expect(screen.getByTestId('update-status')).toHaveTextContent('正在下载… 50%（512 B / 1 KB）'),
    )
    listeners.get(UPDATE_EVENT)?.({ phase: 'installing', received: 1024, total: 1024, rate: null })
    await waitFor(() => expect(screen.getByTestId('update-status')).toHaveTextContent('正在安装'))
  })

  it('shows a failure and lets the reader retry', async () => {
    invokeHost.mockImplementation(async (command: string) => {
      if (command === 'app_update_check') throw new Error('could not reach the endpoint')
      return undefined
    })
    renderCard()
    expect(await screen.findByText('could not reach the endpoint')).toBeInTheDocument()
    const retry = screen.getByTestId('update-retry')
    expect(retry).toHaveTextContent('重试')
    answer({ ...newer, available: null })
    await userEvent.click(retry)
    await waitFor(() => expect(screen.getByTestId('update-status')).toHaveTextContent('已是最新版本'))
  })

  it('keeps a failed background check quiet', async () => {
    invokeHost.mockImplementation(async (command: string) => {
      if (command === 'app_update_check') throw new Error('offline')
      return undefined
    })
    // The sidebar badge reads the same store; a laptop that is offline must not
    // be turned into an error banner by a check nobody asked for.
    await checkForUpdate({ quiet: true })
    expect(screen.queryByText('offline')).toBeNull()
  })
})
