import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { ThemeProvider, useTheme } from './ThemeProvider'
import type { ThemeEnvironment, ThemeMode } from './theme'

function fakeEnvironment(initial: unknown, systemDark: boolean) {
  let stored = initial
  let dark = systemDark
  const listeners = new Set<(value: boolean) => void>()
  const applies: Array<{ resolved: string; mode: string }> = []
  const saves: ThemeMode[] = []
  const environment: ThemeEnvironment = {
    storage: {
      load: async () => stored,
      save: async (mode) => {
        stored = mode
        saves.push(mode)
      },
    },
    systemDark: () => dark,
    subscribeSystem: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    apply: (resolved, mode) => {
      applies.push({ resolved, mode })
    },
  }
  return {
    environment,
    saves,
    applies,
    stored: () => stored,
    setSystem(value: boolean) {
      dark = value
      for (const listener of listeners) listener(value)
    },
  }
}

function Probe() {
  const theme = useTheme()
  return (
    <div>
      <span data-testid="mode">{theme.mode}</span>
      <span data-testid="resolved">{theme.resolved}</span>
      {theme.notice ? <span data-testid="notice">{theme.notice}</span> : null}
      <button onClick={() => theme.setMode('light')}>light</button>
      <button onClick={() => theme.setMode('dark')}>dark</button>
      <button onClick={() => theme.setMode('system')}>system</button>
    </div>
  )
}

function renderTheme(env: ThemeEnvironment) {
  return render(
    <ThemeProvider environment={env}>
      <Probe />
    </ThemeProvider>,
  )
}

afterEach(cleanup)

describe('theme preference state', () => {
  it('defaults to system without writing a dirty first-launch value', async () => {
    const fake = fakeEnvironment(undefined, false)
    renderTheme(fake.environment)
    await waitFor(() => expect(screen.getByTestId('mode')).toHaveTextContent('system'))
    expect(screen.getByTestId('resolved')).toHaveTextContent('light')
    expect(fake.saves).toEqual([])
  })

  it('keeps a manual mode when the system theme changes', async () => {
    const user = userEvent.setup()
    const fake = fakeEnvironment('light', false)
    renderTheme(fake.environment)
    await waitFor(() => expect(screen.getByTestId('mode')).toHaveTextContent('light'))

    await user.click(screen.getByRole('button', { name: 'dark' }))
    expect(screen.getByTestId('resolved')).toHaveTextContent('dark')
    fake.setSystem(false)
    expect(screen.getByTestId('resolved')).toHaveTextContent('dark')
    fake.setSystem(true)
    expect(screen.getByTestId('resolved')).toHaveTextContent('dark')
    expect(fake.saves).toEqual(['dark'])
  })

  it('aligns with the current system value immediately when switching to system', async () => {
    const user = userEvent.setup()
    const fake = fakeEnvironment('dark', false)
    renderTheme(fake.environment)
    await waitFor(() => expect(screen.getByTestId('mode')).toHaveTextContent('dark'))

    fake.setSystem(true)
    await user.click(screen.getByRole('button', { name: 'system' }))
    await waitFor(() => expect(screen.getByTestId('resolved')).toHaveTextContent('dark'))
    expect(screen.getByTestId('mode')).toHaveTextContent('system')
  })

  it('persists a manual mode and restores it on remount', async () => {
    const user = userEvent.setup()
    const fake = fakeEnvironment('system', false)
    const first = renderTheme(fake.environment)
    await user.click(screen.getByRole('button', { name: 'dark' }))
    expect(fake.stored()).toBe('dark')
    first.unmount()

    render(
      <ThemeProvider environment={fake.environment}>
        <Probe />
      </ThemeProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('mode')).toHaveTextContent('dark'))
    expect(screen.getByTestId('resolved')).toHaveTextContent('dark')
  })

  it('falls back to system with a diagnosable notice for corrupt storage', async () => {
    const fake = fakeEnvironment('purple', false)
    renderTheme(fake.environment)
    await waitFor(() => expect(screen.getByTestId('mode')).toHaveTextContent('system'))
    expect(screen.getByTestId('notice')).toHaveTextContent('invalid')
    expect(fake.saves).toEqual([])
  })

  it('applies every resolved change to the environment', async () => {
    const fake = fakeEnvironment('system', false)
    renderTheme(fake.environment)
    await waitFor(() => expect(fake.applies).toContainEqual({ resolved: 'light', mode: 'system' }))
    fake.setSystem(true)
    await waitFor(() => expect(fake.applies).toContainEqual({ resolved: 'dark', mode: 'system' }))
  })
})
