import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
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

  it('draws the occupancy at the right arc and hides the numbers in the tooltip', () => {
    wrap(<ContextRing windowSize={200_000} used={60_000} percent={30} />)
    const ring = screen.getByTestId('chat-context')
    expect(ring.getAttribute('data-percent')).toBe('30.0')
    // A bare ring: the numbers are only on hover, never printed in the field.
    expect(ring.textContent).toBe('')
    expect(ring.title).toContain('200K')
    expect(ring.title).toContain('30%')
    expect(ring.title).toContain('60,000')
    expect(ring.getAttribute('role')).toBe('img')
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
    expect(screen.getByTestId('chat-context').title).toContain('400K')
    rerender(
      <LanguageProvider environment={environment}>
        <ContextRing windowSize={1_000_000} used={100_000} percent={10} />
      </LanguageProvider>,
    )
    const ring = screen.getByTestId('chat-context')
    expect(ring.title).toContain('1M')
    expect(ring.getAttribute('data-percent')).toBe('10.0')
  })

  it('opens a themed card on hover with the numbers, not an OS tooltip', () => {
    vi.useFakeTimers()
    wrap(<ContextRing windowSize={400_000} used={100_000} percent={25} />)
    expect(screen.queryByTestId('chat-context-card')).toBeNull()
    fireEvent.mouseEnter(screen.getByTestId('chat-context').parentElement!)
    const card = screen.getByTestId('chat-context-card')
    expect(card.getAttribute('role')).toBe('tooltip')
    expect(card.textContent).toContain('400K')
    expect(card.textContent).toContain('25%')
    expect(card.textContent).toContain('100,000')
    fireEvent.mouseLeave(screen.getByTestId('chat-context').parentElement!)
    // The card closes after a short grace period so the pointer can cross the
    // gap between the ring and the card to reach its compact button.
    act(() => {
      vi.advanceTimersByTime(250)
    })
    expect(screen.queryByTestId('chat-context-card')).toBeNull()
    vi.useRealTimers()
  })

  it('offers compaction only when idle, and calls back on click', () => {
    const onCompact = vi.fn()
    // Running (canCompact=false): a gauge only, no action in the card.
    const running = wrap(<ContextRing windowSize={400_000} used={100_000} percent={25} />)
    fireEvent.mouseEnter(screen.getByTestId('chat-context').parentElement!)
    expect(screen.queryByTestId('chat-context-compact')).toBeNull()
    running.unmount()

    wrap(
      <ContextRing
        windowSize={400_000}
        used={100_000}
        percent={25}
        canCompact
        onCompact={onCompact}
      />,
    )
    fireEvent.mouseEnter(screen.getByTestId('chat-context').parentElement!)
    const button = screen.getByTestId('chat-context-compact')
    expect(button.textContent).toContain('压缩上下文')
    fireEvent.click(button)
    expect(onCompact).toHaveBeenCalledTimes(1)
    // The card closes after the action so it cannot be clicked twice.
    expect(screen.queryByTestId('chat-context-compact')).toBeNull()
  })
})
