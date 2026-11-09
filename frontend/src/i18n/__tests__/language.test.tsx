import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import App from '../../App'
import { LanguageProvider, useI18n } from '../LanguageProvider'
import LanguageQuickSwitch from '../LanguageQuickSwitch'
import LanguageSettings from '../LanguageSettings'
import PagePlaceholder from '../../shell/PagePlaceholder'
import { WORKBENCH_ROUTE } from '../../shell/routes'
import type { Language, LanguageEnvironment } from '../language'

function fakeEnvironment(initial: unknown, start: Language = 'zh-CN') {
  let stored = initial
  const applied: Language[] = []
  const saves: Language[] = []
  const environment: LanguageEnvironment = {
    initialLanguage: () => start,
    storage: {
      load: async () => stored,
      save: async (language) => {
        stored = language
        saves.push(language)
      },
    },
    apply: (language) => {
      applied.push(language)
      document.documentElement.lang = language
    },
  }
  return { environment, saves, applied, stored: () => stored }
}

function Probe() {
  const { language, notice, setLanguage, t } = useI18n()
  return (
    <div>
      <span data-testid="language">{language}</span>
      <span data-testid="workbench">{t('nav.workbench')}</span>
      <span data-testid="crumb">{t('crumb.workspace')}</span>
      <span data-testid="dynamic" aria-label={t('topbar.search')}>
        {t('settings.language.status', { language })}
      </span>
      {notice ? <span data-testid="notice">{notice}</span> : null}
      <button onClick={() => setLanguage('en-US')}>to-en</button>
      <button onClick={() => setLanguage('zh-CN')}>to-zh</button>
    </div>
  )
}

function renderProbe(env: LanguageEnvironment) {
  return render(
    <LanguageProvider environment={env}>
      <Probe />
    </LanguageProvider>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
  document.title = ''
})

afterEach(cleanup)

describe('language state', () => {
  it('defaults to zh-CN without writing a dirty value', async () => {
    const fake = fakeEnvironment(undefined)
    renderProbe(fake.environment)
    await waitFor(() => expect(screen.getByTestId('language')).toHaveTextContent('zh-CN'))
    expect(screen.getByTestId('workbench')).toHaveTextContent('工作台')
    expect(fake.saves).toEqual([])
  })

  it('switches to English immediately and persists the choice', async () => {
    const user = userEvent.setup()
    const fake = fakeEnvironment('zh-CN')
    renderProbe(fake.environment)
    await user.click(screen.getByRole('button', { name: 'to-en' }))
    expect(screen.getByTestId('workbench')).toHaveTextContent('Dashboard')
    expect(screen.getByTestId('crumb')).toHaveTextContent('Workspace')
    expect(screen.getByTestId('dynamic')).toHaveAttribute('aria-label', 'Search')
    expect(fake.stored()).toBe('en-US')
    expect(fake.saves).toEqual(['en-US'])
  })

  it('restores the persisted language on remount', async () => {
    const fake = fakeEnvironment('en-US')
    renderProbe(fake.environment)
    await waitFor(() => expect(screen.getByTestId('language')).toHaveTextContent('en-US'))
    expect(screen.getByTestId('workbench')).toHaveTextContent('Dashboard')
  })

  it('falls back to zh-CN with a notice for an unknown value', async () => {
    const fake = fakeEnvironment('fr-FR')
    renderProbe(fake.environment)
    await waitFor(() => expect(screen.getByTestId('language')).toHaveTextContent('zh-CN'))
    expect(screen.getByTestId('notice')).toHaveTextContent('invalid')
    expect(fake.saves).toEqual([])
  })
})

describe('language UI', () => {
  it('updates the whole app, aria labels, document lang and title without reloading', async () => {
    const user = userEvent.setup()
    window.history.replaceState(null, '', '/settings')
    render(<App />)
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: '简体中文' })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(document.title).toBe('设置 · Anvil')

    await user.click(screen.getByRole('radio', { name: 'English' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(document.title).toBe('Settings · Anvil')
    expect(screen.getByRole('link', { name: 'Dashboard' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Search' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Dark' })).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/[一-鿿]/)

    await user.click(screen.getByRole('radio', { name: 'Chinese' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-CN'))
    expect(document.title).toBe('设置 · Anvil')
  })

  it('keeps user-provided paths unchanged while translating system copy', async () => {
    const fake = fakeEnvironment('en-US')
    render(
      <LanguageProvider environment={fake.environment}>
        <PagePlaceholder route={WORKBENCH_ROUTE} fallbackPath="/用户/项目" />
      </LanguageProvider>,
    )
    await waitFor(() => expect(screen.getByText('Unknown route /用户/项目')).toBeInTheDocument())
    expect(screen.getByText(/\/用户\/项目/)).toBeInTheDocument()
    expect(screen.getByText(/Route placeholder/)).toBeInTheDocument()
  })

  it('exposes the reusable quick switch for the chat header', async () => {
    const user = userEvent.setup()
    const fake = fakeEnvironment('zh-CN')
    render(
      <LanguageProvider environment={fake.environment}>
        <LanguageQuickSwitch />
      </LanguageProvider>,
    )
    const button = screen.getByRole('button', { name: '语言' })
    await user.click(button)
    expect(fake.stored()).toBe('en-US')
    expect(screen.getByRole('button', { name: 'Language' })).toBeInTheDocument()
  })

  it('offers an accessible language control and updates status', async () => {
    const user = userEvent.setup()
    const fake = fakeEnvironment('zh-CN')
    render(
      <LanguageProvider environment={fake.environment}>
        <LanguageSettings />
      </LanguageProvider>,
    )
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: '简体中文' })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(screen.getByRole('radiogroup', { name: '语言' })).toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: 'English' }))
    expect(screen.getByRole('radio', { name: 'English' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('language-status')).toHaveTextContent('Current language: English')
    expect(screen.queryByText(/[\u4e00-\u9fff]/)).toBeNull()
  })
})
