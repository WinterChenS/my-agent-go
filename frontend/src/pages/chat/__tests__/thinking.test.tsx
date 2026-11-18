import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import type { SessionEvent } from '../../../chat/client'
import { ThinkingDisclosure } from '../ChatPage'
import { buildTurnParts } from '../../../chat/useChatSession'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function wrap(node: ReactNode) {
  return <LanguageProvider environment={environment}>{node}</LanguageProvider>
}

function delta(seq: number, runId: string, text: string): SessionEvent {
  return {
    sessionId: 'pi-1',
    runId,
    seq,
    kind: 'thinking',
    at: '2026-11-15T00:00:00Z',
    payload: { delta: text },
  }
}

describe('reasoning disclosure', () => {
  it('groups deltas by run in ledger order and skips empty ones', () => {
    const events: SessionEvent[] = [
      delta(1, 'run-a', '先'),
      delta(2, 'run-b', '另一轮'),
      delta(3, 'run-a', '读文件'),
      { ...delta(4, 'run-a', ''), kind: 'text_delta' },
      delta(5, 'run-a', ''),
    ]
    const text = (run: string) =>
      buildTurnParts(events, run)
        .map((part) => (part.kind === 'thinking' ? part.text : ''))
        .join('')
    expect(text('run-a')).toBe('先读文件')
    expect(text('run-b')).toBe('另一轮')
  })

  it('keeps reasoning and tool calls in the order the run produced them', () => {
    // A run reasons, calls a tool, reads the result, then reasons again. The
    // old rendering poured both reasoning bursts into one disclosure above the
    // answer and listed the tool below it, which is not what happened.
    const events: SessionEvent[] = [
      delta(1, 'run-a', '先看目录'),
      {
        sessionId: 'pi-1',
        runId: 'run-a',
        seq: 2,
        kind: 'tool_call',
        at: '2026-11-15T00:00:01Z',
        payload: { tool: 'command', command: 'ls' },
      },
      {
        sessionId: 'pi-1',
        runId: 'run-a',
        seq: 3,
        kind: 'tool_result',
        at: '2026-11-15T00:00:02Z',
        payload: { ok: true, output: 'a.txt' },
      },
      delta(4, 'run-a', '只有一个文件'),
      delta(5, 'run-b', '别的轮次'),
    ]
    const parts = buildTurnParts(events, 'run-a')
    expect(parts.map((part) => part.kind)).toEqual(['thinking', 'tool', 'thinking'])
    expect(parts[0].kind === 'thinking' && parts[0].text).toBe('先看目录')
    expect(parts[1].kind === 'tool' && parts[1].tool.output).toBe('a.txt')
    expect(parts[1].kind === 'tool' && parts[1].tool.ok).toBe(true)
    expect(parts[2].kind === 'thinking' && parts[2].text).toBe('只有一个文件')
    // A run's parts never absorb another run's reasoning.
    expect(JSON.stringify(parts)).not.toContain('别的轮次')
  })

  it('is open while reasoning streams and collapses once the answer lands', async () => {
    const { rerender } = render(wrap(<ThinkingDisclosure text="先读文件" running />))
    const disclosure = screen.getByTestId('chat-live-thinking')
    expect(disclosure).toHaveAttribute('data-open', 'true')
    expect(disclosure).toHaveAttribute('open')
    expect(screen.getByText('正在思考 · 4 字')).toBeInTheDocument()
    expect(disclosure.querySelector('.chat-live-dot')).not.toBeNull()

    rerender(wrap(<ThinkingDisclosure text="先读文件" running={false} />))
    expect(screen.getByTestId('chat-live-thinking')).toHaveAttribute('data-open', 'false')
    expect(screen.getByText('思考过程 · 4 字')).toBeInTheDocument()
  })

  it('lets an explicit click win over the streaming default', async () => {
    const user = userEvent.setup()
    render(wrap(<ThinkingDisclosure text="推理中" running />))
    expect(screen.getByTestId('chat-live-thinking')).toHaveAttribute('data-open', 'true')
    await user.click(screen.getByText('正在思考 · 3 字'))
    expect(screen.getByTestId('chat-live-thinking')).toHaveAttribute('data-open', 'false')
    await user.click(screen.getByText('正在思考 · 3 字'))
    expect(screen.getByTestId('chat-live-thinking')).toHaveAttribute('data-open', 'true')
  })

  it('renders nothing when the run produced no reasoning', () => {
    render(wrap(<ThinkingDisclosure text="" />))
    expect(screen.queryByTestId('chat-live-thinking')).toBeNull()
  })
})
