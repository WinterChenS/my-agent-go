import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../i18n/language'
import { ThemeProvider } from '../../preferences/ThemeProvider'
import type { ThemeEnvironment, ThemeMode } from '../../preferences/theme'
import ThemeSettings from './ThemeSettings'

const languageEnvironment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => {} },
  apply: () => {},
}

function environment(systemDark = false): ThemeEnvironment {
  let stored: unknown = undefined
  return {
    storage: {
      load: async () => stored,
      save: async (mode: ThemeMode) => {
        stored = mode
      },
    },
    systemDark: () => systemDark,
    subscribeSystem: () => () => {},
    apply: () => {},
  }
}

function renderSettings(env: ThemeEnvironment) {
  return render(
    <ThemeProvider environment={env}>
      <LanguageProvider environment={languageEnvironment}>
        <ThemeSettings />
      </LanguageProvider>
    </ThemeProvider>,
  )
}

afterEach(cleanup)

describe('theme settings control', () => {
  it('exposes an accessible three-state radiogroup', async () => {
    renderSettings(environment(false))
    await waitFor(() =>
      expect(screen.getByRole('radio', { name: '跟随系统' })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(screen.getByRole('radiogroup', { name: '主题' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: '浅色' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByRole('radio', { name: '深色' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByTestId('theme-status')).toHaveTextContent('浅色')
  })

  it('switches theme by click and exposes the resolved state', async () => {
    const user = userEvent.setup()
    renderSettings(environment(false))
    await user.click(screen.getByRole('radio', { name: '深色' }))
    expect(screen.getByRole('radio', { name: '深色' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('theme-status')).toHaveTextContent('深色')
  })

  it('supports keyboard selection and roving focus', async () => {
    const user = userEvent.setup()
    renderSettings(environment(false))
    const system = screen.getByRole('radio', { name: '跟随系统' })
    system.focus()
    await user.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: '浅色' }))
    expect(screen.getByRole('radio', { name: '浅色' })).toHaveAttribute('aria-checked', 'true')
    await user.keyboard(' ')
    expect(screen.getByRole('radio', { name: '浅色' })).toHaveAttribute('aria-checked', 'true')
  })
})
