import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ContextRing } from '../ChatPage'
import { LanguageProvider } from '../../../i18n/LanguageProvider'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

function mount(overrides: Partial<React.ComponentProps<typeof ContextRing>> = {}) {
  return render(
    <LanguageProvider>
      <ContextRing windowSize={100_000} used={42_000} percent={42} canCompact {...overrides} />
    </LanguageProvider>,
  )
}

describe('the context ring hover card', () => {
  it('opens on hover and exposes the compact action', () => {
    mount()
    fireEvent.mouseEnter(screen.getByTestId('chat-context-wrap'))
    expect(screen.getByTestId('chat-context-card')).toBeInTheDocument()
    expect(screen.getByTestId('chat-context-compact')).toBeInTheDocument()
  })

  it('stays open across the gap when the pointer leaves the ring briefly', () => {
    vi.useFakeTimers()
    mount()
    const wrap = screen.getByTestId('chat-context-wrap')
    fireEvent.mouseEnter(wrap)
    // Pointer crosses the 10px gap: leaving the ring starts a grace period.
    fireEvent.mouseLeave(wrap)
    act(() => {
      vi.advanceTimersByTime(80)
    })
    expect(screen.getByTestId('chat-context-card')).toBeInTheDocument()
    // Entering the card cancels the pending close, so its button is reachable.
    fireEvent.mouseEnter(screen.getByTestId('chat-context-card'))
    act(() => {
      vi.advanceTimersByTime(500)
    })
    expect(screen.getByTestId('chat-context-compact')).toBeInTheDocument()
  })

  it('closes after the grace period when the pointer really leaves', () => {
    vi.useFakeTimers()
    mount()
    const wrap = screen.getByTestId('chat-context-wrap')
    fireEvent.mouseEnter(wrap)
    fireEvent.mouseLeave(wrap)
    act(() => {
      vi.advanceTimersByTime(250)
    })
    expect(screen.queryByTestId('chat-context-card')).toBeNull()
  })

  it('calls compact when the button is clicked and closes at once', () => {
    vi.useFakeTimers()
    const onCompact = vi.fn()
    mount({ onCompact })
    fireEvent.mouseEnter(screen.getByTestId('chat-context-wrap'))
    fireEvent.click(screen.getByTestId('chat-context-compact'))
    expect(onCompact).toHaveBeenCalledOnce()
    expect(screen.queryByTestId('chat-context-card')).toBeNull()
  })
})
