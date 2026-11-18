import { describe, expect, it } from 'vitest'
import type { SessionEvent } from '../client'
import { buildTurnParts, withStoredAnswer, type TurnPart } from '../useChatSession'

function textPart(text: string): TurnPart {
  return { kind: 'text', key: 'run:1', text }
}

describe('settled answer reconciliation', () => {
  // pi auto-retries a provider call. The retried turn emits a SECOND batch of
  // text_delta with different chunk boundaries than the abandoned first batch.
  // We append both, so the streamed bubble is the answer woven into itself. The
  // stored message (pi's `finalText` from turn_end) is the single correct copy.
  const correct = '我亲自跑的验收结果'
  const polluted = '我亲自亲自跑的验收跑的验收结果结果'

  it('replaces a duplicated stream with the single stored answer', () => {
    const parts = withStoredAnswer([textPart(polluted)], correct)
    expect(parts).toHaveLength(1)
    expect(parts[0]).toMatchObject({ kind: 'text', text: correct })
  })

  it('replaces a path that pi replayed with overlapping chunks', () => {
    const correctPath = 'C:/Users/Administrator/Pictures/email_sender/'
    const pollutedPath = 'C:/C:/Users/Administrator/Pictures/Users/Administrator/Pictures/email_sender/email_sender/'
    const parts = withStoredAnswer([textPart(pollutedPath)], correctPath)
    expect(parts[0]).toMatchObject({ text: correctPath })
  })

  it('keeps the prefix-completion behaviour for a genuinely shorter stream', () => {
    // Normal case: the runtime reported fewer deltas than the stored message.
    const parts = withStoredAnswer([textPart('我亲自跑的')], '我亲自跑的验收结果')
    expect(parts[0]).toMatchObject({ text: '我亲自跑的验收结果' })
  })

  it('leaves an exact match untouched', () => {
    const parts = withStoredAnswer([textPart(correct)], correct)
    expect(parts[0]).toMatchObject({ text: correct })
  })

  it('preserves thinking/tool blocks around a duplicated answer', () => {
    const parts: TurnPart[] = [
      { kind: 'thinking', key: 'run:0', text: '先看一下目录' },
      textPart(polluted),
      {
        kind: 'tool',
        key: 'run:2',
        tool: { id: 't', name: 'ls', args: '', ok: true, output: 'email_sender' },
      },
    ]
    const result = withStoredAnswer(parts, correct)
    expect(result).toHaveLength(3)
    expect(result[0]).toMatchObject({ kind: 'thinking', text: '先看一下目录' })
    expect(result[1]).toMatchObject({ kind: 'text', text: correct })
    expect(result[2]).toMatchObject({ kind: 'tool' })
  })

  // REAL AGENTIC TURN: the ledger interleaves several DISTINCT text blocks with
  // thinking/tool cards — an opening aside, a mid-turn note, the final answer.
  // finalText is ONLY the last block. The old logic concatenated all text
  // parts, saw finalText as a subsequence, moved it to the first text position
  // and deleted the rest — shoving every thinking/tool card below the prose
  // and scrambling the transcript. Reconciliation must touch only the tail.
  it('keeps every intermediate text and tool block in place (agentic turn)', () => {
    const aside = "I'll take a look at both files."
    const finalAnswer = '两个文件都看过了。\n\n## 1. 照片\n\n...表格...'
    const parts: TurnPart[] = [
      textPart(aside),
      { kind: 'tool', key: 'run:2', tool: { id: 't1', name: 'bash', args: 'ls', ok: true, output: 'a.png' } },
      { kind: 'thinking', key: 'run:3', text: 'Extract text from docx.' },
      { kind: 'tool', key: 'run:4', tool: { id: 't2', name: 'read', args: 'a.docx', ok: true, output: '...' } },
      textPart(finalAnswer),
    ]
    const result = withStoredAnswer(parts, finalAnswer)
    expect(result).toHaveLength(5)
    // Nothing moved: aside stays first, final answer stays last, cards in between.
    expect(result[0]).toMatchObject({ kind: 'text', text: aside })
    expect(result[1]).toMatchObject({ kind: 'tool' })
    expect(result[2]).toMatchObject({ kind: 'thinking' })
    expect(result[3]).toMatchObject({ kind: 'tool' })
    expect(result[4]).toMatchObject({ kind: 'text', text: finalAnswer })
  })

  it('repairs a replayed final block without disturbing earlier prose', () => {
    const aside = '子代理已完成。我自己复验一遍：'
    const pollutedTail = '我亲自亲自跑的验收跑的验收结果结果'
    const parts: TurnPart[] = [
      textPart(aside),
      { kind: 'tool', key: 'run:2', tool: { id: 't', name: 'bash', args: '', ok: true, output: 'x' } },
      textPart(pollutedTail),
    ]
    const result = withStoredAnswer(parts, correct)
    expect(result).toHaveLength(3)
    expect(result[0]).toMatchObject({ kind: 'text', text: aside })
    expect(result[2]).toMatchObject({ kind: 'text', text: correct })
  })
})

describe('streaming through a pi auto-retry', () => {
  const ev = (seq: number, kind: SessionEvent['kind'], payload: Record<string, unknown>): SessionEvent => ({
    sessionId: 's',
    runId: 'r',
    seq,
    kind,
    at: '2026-11-17T00:00:00Z',
    payload,
  })
  const delta = (seq: number, text: string) => ev(seq, 'text_delta', { delta: text })

  // The abandoned attempt streams "我亲自跑的验", pi emits auto_retry, then the
  // retry streams the whole answer cleanly. The reader must see one answer, not
  // the abandoned fragment stitched onto the front of the retry.
  it('drops the abandoned attempt text at auto_retry', () => {
    const events = [
      delta(1, '我亲自'),
      delta(2, '跑的验'),
      ev(3, 'unknown', { type: 'auto_retry', attempt: 1, maxAttempts: 3, message: 'timeout' }),
      delta(4, '我亲自'),
      delta(5, '跑的验收结果'),
    ]
    const text = buildTurnParts(events, 'r')
      .filter((part) => part.kind === 'text')
      .map((part) => (part.kind === 'text' ? part.text : ''))
      .join('')
    expect(text).toBe('我亲自跑的验收结果')
  })

  it('also drops reasoning from the abandoned attempt', () => {
    const events = [
      ev(1, 'thinking', { delta: '第一次尝试的思路' }),
      ev(2, 'unknown', { type: 'auto_retry', attempt: 1, maxAttempts: 3, message: 'timeout' }),
      ev(3, 'thinking', { delta: '重试后的思路' }),
      delta(4, '答案'),
    ]
    const parts = buildTurnParts(events, 'r')
    expect(parts).toHaveLength(2)
    expect(parts[0]).toMatchObject({ kind: 'thinking', text: '重试后的思路' })
    expect(parts[1]).toMatchObject({ kind: 'text', text: '答案' })
  })
})
