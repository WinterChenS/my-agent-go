import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import { WorkspaceWelcome } from '../ChatPage'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function mount(overrides: Partial<React.ComponentProps<typeof WorkspaceWelcome>> = {}) {
  return render(
    <LanguageProvider environment={environment}>
      <WorkspaceWelcome
        path={'C://work//mailer'}
        defaultPath={'C://Users//me//.anvil//workspace'}
        isDefault={false}
        recent={[]}
        onBrowse={() => undefined}
        onReset={() => undefined}
        onChoose={() => undefined}
        {...overrides}
      />
    </LanguageProvider>,
  )
}

describe('the blank-page workspace block', () => {
  it('names the directory this conversation will run in', () => {
    mount()
    const panel = screen.getByTestId('chat-workspace')
    expect(panel.textContent).toContain('mailer')
    expect(panel.textContent).toContain('C://work//mailer')
    // Not the default workspace, so the way back is offered.
    expect(screen.getByTestId('chat-workspace-reset')).toBeInTheDocument()
  })

  it('offers the default workspace tag and no reset when already there', () => {
    mount({ path: 'C://Users//me//.anvil//workspace', isDefault: true })
    expect(screen.getByTestId('chat-workspace').textContent).toContain('默认')
    expect(screen.queryByTestId('chat-workspace-reset')).toBeNull()
  })

  it('lists recent directories except the current one', () => {
    mount({
      recent: [
        { path: 'C://work//mailer', name: 'mailer', sessionCount: 2, lastUpdated: '' },
        { path: 'D://lab//parser', name: 'parser', sessionCount: 1, lastUpdated: '' },
      ],
    })
    const chips = screen.getAllByTestId('chat-workspace-recent')
    expect(chips).toHaveLength(1)
    expect(chips[0].textContent).toContain('parser')
  })

  it('switches the directory from a recent chip', () => {
    const onChoose = vi.fn()
    mount({
      recent: [{ path: 'D://lab//parser', name: 'parser', sessionCount: 1, lastUpdated: '' }],
      onChoose,
    })
    fireEvent.click(screen.getByTestId('chat-workspace-recent'))
    expect(onChoose).toHaveBeenCalledWith('D://lab//parser')
  })

  it('asks the native picker from the browse button', () => {
    const onBrowse = vi.fn()
    mount({ onBrowse })
    fireEvent.click(screen.getByTestId('chat-workspace-browse'))
    expect(onBrowse).toHaveBeenCalled()
  })
})
