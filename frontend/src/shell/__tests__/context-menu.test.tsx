import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import App from '../../App'

beforeEach(() => {
  window.history.replaceState(null, '', '/')
})

afterEach(cleanup)

// WebView2 draws its own menu when a contextmenu event goes unprevented, so
// "prevented" is the only observable that means "no menu" in jsdom.
describe('context menu guard', () => {
  it('takes the context menu away from the whole app surface', async () => {
    render(<App />)
    const page = screen.getByTestId('workbench-page')
    expect(fireEvent.contextMenu(page)).toBe(false)
    expect(fireEvent.contextMenu(document.body)).toBe(false)
  })

  it('does not leave a text field with a native menu either', async () => {
    window.history.replaceState(null, '', '/chat')
    render(<App />)
    const field = await screen.findByRole('textbox', { name: /输入|消息|prompt/i })
    expect(fireEvent.contextMenu(field)).toBe(false)
  })

  it('stops guarding once the shell is gone', () => {
    const { unmount } = render(<App />)
    unmount()
    expect(fireEvent.contextMenu(document.body)).toBe(true)
  })
})
