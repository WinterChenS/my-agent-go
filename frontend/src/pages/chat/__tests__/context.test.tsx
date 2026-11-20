import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import { ContextRing, parseWindowSize } from '../ChatPage'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}
const wrap = (node: React.ReactNode) => render(<LanguageProvider environment={environment}>{node}</LanguageProvider>)

describe('the context window indicator', () => {
  it('reads the labels pi/catalog actually use', () => {
    expect(parseWindowSize('400K')).toBe(400_000)
    expect(parseWindowSize('1M')).toBe(1_000_000)
    expect(parseWindowSize('128000')).toBe(128_000)
    expect(parseWindowSize('unknown')).toBe(0)
    expect(parseWindowSize(undefined)).toBe(0)
  })

  it('draws the occupancy at the right arc and says the numbers', () => {
    wrap(<ContextRing windowSize={200_000} used={60_000} percent={30} />)
    const ring = screen.getByTestId('chat-context')
    expect(ring.getAttribute('data-percent')).toBe('30.0')
    expect(ring.textContent).toContain('200K')
    expect(ring.textContent).toContain('30%')
    const arc = ring.querySelectorAll('circle')[1]
    const dash = Number(String(arc.getAttribute('stroke-dasharray')).split(' ')[0])
    expect(dash).toBeCloseTo(2 * Math.PI * 13 * 0.3, 1)
  })

  it('shows zero, not a made-up number, before a session has stats', () => {
    wrap(<ContextRing windowSize={400_000} used={null} percent={null} />)
    const ring = screen.getByTestId('chat-context')
    expect(ring.getAttribute('data-percent')).toBe('0.0')
    expect(ring.title).toContain('400K')
  })

  it('switches to the new model window when the caller passes one', () => {
    const { rerender } = wrap(<ContextRing windowSize={400_000} used={100_000} percent={25} />)
    expect(screen.getByTestId('chat-context').textContent).toContain('400K')
    rerender(
      <LanguageProvider environment={environment}>
        <ContextRing windowSize={1_000_000} used={100_000} percent={10} />
      </LanguageProvider>,
    )
    const ring = screen.getByTestId('chat-context')
    expect(ring.textContent).toContain('1M')
    expect(ring.getAttribute('data-percent')).toBe('10.0')
  })
})
