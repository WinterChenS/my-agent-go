import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import App from '../../App'

beforeEach(() => {
  window.history.replaceState(null, '', '/')
})

afterEach(cleanup)

describe('history navigation', () => {
  it('supports browser history back without a persistent back button', async () => {
    const user = userEvent.setup()
    render(<App />)
    expect(screen.queryByRole('button', { name: '返回上一页' })).toBeNull()

    await user.click(screen.getByRole('link', { name: '对话' }))
    expect(screen.getByTestId('chat-page')).toBeInTheDocument()

    window.history.back()
    await waitFor(() => expect(screen.getByTestId('workbench-page')).toBeInTheDocument())
  })

  it('supports the Alt+Left history shortcut without a visible back button', async () => {
    const user = userEvent.setup()
    render(<App />)
    await user.click(screen.getByRole('link', { name: '对话' }))
    expect(screen.getByTestId('chat-page')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'ArrowLeft', altKey: true })
    await waitFor(() => expect(screen.getByTestId('workbench-page')).toBeInTheDocument())
  })
})
