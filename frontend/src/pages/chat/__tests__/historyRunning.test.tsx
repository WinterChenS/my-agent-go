import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import ChatHistoryPanel from '../ChatHistoryPanel'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function row(id: string, title: string) {
  return {
    id,
    runtimeId: 'pi',
    title,
    status: 'idle' as const,
    nativeId: `native-${id}`,
    model: 'auto/gpt-5',
    projectPath: 'D:\\project',
    pinned: false,
    archived: false,
    messageCount: 2,
    parentId: '',
    branchAt: 0,
    createdAt: '2026-11-13T00:00:00Z',
    updatedAt: '2026-11-13T00:00:00Z',
    preview: '',
  }
}

function renderPanel(runningId: string | null) {
  return render(
    <LanguageProvider environment={environment}>
      <ChatHistoryPanel
        sample={false}
        live={{
          sessions: [row('s-1', '正在写的报告'), row('s-2', '另一段对话')],
          projects: [],
          activeId: 's-1',
          runningId,
          onOpen: vi.fn(),
          onNew: vi.fn(),
          onRename: vi.fn(),
          onPin: vi.fn(),
          onArchive: vi.fn(),
          onDelete: vi.fn(),
          onFork: vi.fn(),
          onCompact: vi.fn(),
        }}
      />
    </LanguageProvider>,
  )
}

describe('history panel running indicator', () => {
  it('spins only the row whose session is generating a reply', () => {
    renderPanel('s-2')
    expect(screen.getByTestId('chat-history-running-s-2')).toBeInTheDocument()
    expect(screen.queryByTestId('chat-history-running-s-1')).toBeNull()
  })

  it('shows no spinner when nothing is running', () => {
    renderPanel(null)
    expect(screen.queryByTestId(/chat-history-running-/)).toBeNull()
  })
})
