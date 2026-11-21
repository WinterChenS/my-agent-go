import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { QueuedMessages } from '../ChatPage'
import { LanguageProvider } from '../../../i18n/LanguageProvider'

afterEach(() => cleanup())

function wrap(node: React.ReactNode) {
  return render(<LanguageProvider>{node}</LanguageProvider>)
}

const items = [
  { id: 'a', text: 'first queued message', refs: [] },
  { id: 'b', text: 'second queued message', refs: [] },
]

function mount(overrides: Partial<React.ComponentProps<typeof QueuedMessages>> = {}) {
  return wrap(
    <QueuedMessages
      items={items}
      running
      onSteer={() => undefined}
      onEdit={() => undefined}
      onDrop={() => undefined}
      onMove={() => undefined}
      {...overrides}
    />,
  )
}

describe('the queued-message list', () => {
  it('is one compact card per message with icon actions only', () => {
    mount()
    const rows = screen.getAllByTestId('chat-queue-item')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('first queued message')

    // A single line of text, not a labelled button row: the actions are icons
    // with accessible names, which is what keeps the row as tall as one line.
    const steer = screen.getAllByTestId('chat-queue-steer')[0]
    expect(steer.getAttribute('aria-label')).toBe('立即发送')
    expect(steer.textContent).toBe('')
    expect(screen.getAllByTestId('chat-queue-edit')[0].getAttribute('aria-label')).toBe('修改')
    expect(screen.getAllByTestId('chat-queue-drop')[0].getAttribute('aria-label')).toBe('撤回')
    // No header line: the count belongs to the control, not above the field.
    expect(screen.queryByText(/待发送/)).toBeNull()
  })

  it('shows the attachments a queued message carries', () => {
    mount({
      items: [
        {
          id: 'c',
          text: 'look at this',
          refs: [
            { kind: 'file', path: 'D:/work/report.md', label: 'report.md' },
            { kind: 'skill', path: 'skill:pdf', label: 'pdf' },
          ],
        },
      ],
    })
    const refs = screen.getAllByTestId('chat-queue-ref')
    expect(refs).toHaveLength(2)
    expect(refs[0].textContent).toContain('report.md')
    expect(refs[0].getAttribute('title')).toBe('D:/work/report.md')
    expect(refs[1].textContent).toContain('pdf')
  })

  it('offers a grip so the send order can be dragged', () => {
    mount()
    const row = screen.getAllByTestId('chat-queue-item')[0]
    expect(row.getAttribute('draggable')).toBe('true')
  })

  it('cannot send into a turn that is not running', () => {
    mount({ running: false })
    expect((screen.getAllByTestId('chat-queue-steer')[0] as HTMLButtonElement).disabled).toBe(true)
  })

  it('asks for a reorder with the dragged id and the row it was dropped on', () => {
    const onMove = vi.fn()
    mount({ onMove })
    const [first, second] = screen.getAllByTestId('chat-queue-item')

    const data: Record<string, string> = {}
    fireEvent.dragStart(first, {
      dataTransfer: {
        setData: (type: string, value: string) => {
          data[type] = value
        },
        effectAllowed: '',
      },
    })
    fireEvent.dragOver(second, { dataTransfer: { dropEffect: '' } })
    fireEvent.drop(second, {
      dataTransfer: {
        getData: (type: string) => data[type] ?? '',
        dropEffect: '',
      },
    })

    expect(onMove).toHaveBeenCalledWith('a', 'b')
  })

  it('runs the action the clicked icon stands for', () => {
    const onSteer = vi.fn()
    const onEdit = vi.fn()
    const onDrop = vi.fn()
    mount({ onSteer, onEdit, onDrop })

    fireEvent.click(screen.getAllByTestId('chat-queue-steer')[1])
    fireEvent.click(screen.getAllByTestId('chat-queue-edit')[0])
    fireEvent.click(screen.getAllByTestId('chat-queue-drop')[0])

    expect(onSteer).toHaveBeenCalledWith('b')
    expect(onEdit).toHaveBeenCalledWith('a')
    expect(onDrop).toHaveBeenCalledWith('a')
  })
})
