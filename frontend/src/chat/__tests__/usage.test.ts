import { describe, expect, it } from 'vitest'
import type { SessionEvent } from '../client'
import { readUsage, turnUsage } from '../usage'

function usage(seq: number, payload: Record<string, unknown>): SessionEvent {
  return { sessionId: 'pi-1', runId: 'pi-1:r1', seq, kind: 'usage', at: '2026-11-16T00:00:00Z', payload }
}

describe('token accounting', () => {
  // The two reports pi wrote for one real turn: a first request before the tool
  // call, then a second one after it. `input` excludes the cached prompt.
  const piReports = [
    usage(15, { usage: { cacheRead: 0, cacheWrite: 0, input: 9023, output: 38, totalTokens: 9061 } }),
    usage(37, {
      usage: { cacheRead: 8960, cacheWrite: 0, input: 139, output: 78, reasoning: 58, totalTokens: 9177 },
    }),
  ]

  it('sums pi request reports and counts the cached prompt as input', () => {
    expect(turnUsage(piReports)).toEqual({
      // 9023 + (139 + 8960): the second request re-sent the conversation, and
      // 8960 of those tokens came from the cache.
      input: 18_122,
      output: 116,
      cached: 8960,
      reports: 2,
    })
  })

  it('does not pad an already inclusive prompt count', () => {
    // OpenAI-compatible: `prompt_tokens` already contains the cached part.
    expect(
      readUsage({
        prompt_tokens: 1200,
        completion_tokens: 80,
        total_tokens: 1280,
        prompt_tokens_details: { cached_tokens: 1000 },
      }),
    ).toEqual({ input: 1200, output: 80, cached: 1000 })
  })

  it('adds Anthropic cache fields, which input_tokens excludes', () => {
    expect(
      readUsage({
        input_tokens: 120,
        output_tokens: 30,
        cache_read_input_tokens: 900,
        cache_creation_input_tokens: 50,
      }),
    ).toEqual({ input: 1070, output: 30, cached: 900 })
  })

  it('accepts camelCase counters and numeric strings', () => {
    expect(readUsage({ inputTokens: '120', outputTokens: '7' })).toEqual({
      input: 120,
      output: 7,
      cached: 0,
    })
  })

  it('reads thread-scoped usage as a snapshot, not as a report to add up', () => {
    // Codex reports a running total for the whole thread; two of those added
    // together would double count the earlier turns.
    const events = [
      usage(1, { usage: { inputTokens: 100, outputTokens: 10 }, scope: 'thread' }),
      usage(2, { usage: { inputTokens: 300, outputTokens: 40 }, scope: 'thread' }),
    ]
    expect(turnUsage(events)).toEqual({ input: 300, output: 40, cached: 0, reports: 1 })
  })

  it('prefers the turn total when both scopes arrive', () => {
    const events = [
      usage(1, { usage: { inputTokens: 500, outputTokens: 60 }, scope: 'thread' }),
      usage(2, { usage: { inputTokens: 120, outputTokens: 20 }, scope: 'turn' }),
    ]
    expect(turnUsage(events)).toEqual({ input: 120, output: 20, cached: 0, reports: 1 })
  })

  it('reports nothing when the runtime sent no counters', () => {
    expect(turnUsage([usage(1, { usage: { cost: { total: 0 } } })])).toBeNull()
    expect(turnUsage([usage(1, {})])).toBeNull()
    expect(turnUsage([{ ...usage(2, {}), kind: 'text_delta' }])).toBeNull()
  })
})
