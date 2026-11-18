import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import App from '../../App'
import { LanguageProvider, useI18n } from '../LanguageProvider'
import LanguageSettings from '../LanguageSettings'
import PagePlaceholder from '../../shell/PagePlaceholder'
import { WORKBENCH_ROUTE } from '../../shell/routes'
import {
  initialLanguageFromWindow,
  resolveStoredLanguage,
  systemLanguage,
  type Language,
  type LanguageEnvironment,
} from '../language'

function stubSystemLocale(languages: string[]) {
  Object.defineProperty(window.navigator, 'languages', { value: languages, configurable: true })
  Object.defineProperty(window.navigator, 'language', {
    value: languages[0] ?? '',
    configurable: true,
  })
}

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
  stubSystemLocale(['zh-CN'])
})

afterEach(cleanup)

describe('system locale', () => {
  it('reads the supported language out of the OS locale list', () => {
    stubSystemLocale(['en-GB', 'de-DE'])
    expect(systemLanguage()).toBe('en-US')
    stubSystemLocale(['zh-Hant-TW', 'en-US'])
    expect(systemLanguage()).toBe('zh-CN')
    // A locale this build does not translate reads English rather than Chinese.
    stubSystemLocale(['ja-JP'])
    expect(systemLanguage()).toBe('en-US')
    stubSystemLocale([])
    expect(systemLanguage()).toBe('zh-CN')
  })

  it('opens in English on an English system when nothing was chosen', async () => {
    stubSystemLocale(['en-US', 'en'])
    window.history.replaceState(null, '', '/settings')
    render(<App />)
    expect(await screen.findByRole('link', { name: 'Dashboard' })).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/[一-鿿]/)
    expect(initialLanguageFromWindow()).toBe('en-US')
  })

  it('opens in Chinese on a Chinese system', async () => {
    stubSystemLocale(['zh-CN'])
    window.history.replaceState(null, '', '/settings')
    render(<App />)
    expect(await screen.findByRole('link', { name: '工作台' })).toBeInTheDocument()
    expect(initialLanguageFromWindow()).toBe('zh-CN')
  })

  it('treats the default value as "not chosen" and a real pick as a choice', () => {
    // The host writes zh-CN for a profile that never picked anything, so the
    // bare default must not pin an English machine to Chinese.
    expect(resolveStoredLanguage('zh-CN')).toBeUndefined()
    window.localStorage.setItem('anvil.language.chosen', '1')
    window.localStorage.setItem('anvil.language', 'zh-CN')
    expect(resolveStoredLanguage('zh-CN')).toBe('zh-CN')
    // English never equals the built-in default, so it needs no marker.
    expect(resolveStoredLanguage('en-US')).toBe('en-US')
  })

  it('keeps a chosen language even when the system speaks another', () => {
    stubSystemLocale(['en-US'])
    window.localStorage.setItem('anvil.language.chosen', '1')
    window.localStorage.setItem('anvil.language', 'zh-CN')
    expect(initialLanguageFromWindow()).toBe('zh-CN')
  })

  it('remembers that a language was picked through the settings page', async () => {
    const user = userEvent.setup()
    stubSystemLocale(['en-US'])
    window.history.replaceState(null, '', '/settings')
    render(<App />)
    await user.click(await screen.findByRole('radio', { name: 'Chinese' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-CN'))
    expect(window.localStorage.getItem('anvil.language')).toBe('zh-CN')
    expect(window.localStorage.getItem('anvil.language.chosen')).toBe('1')
  })
})

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
