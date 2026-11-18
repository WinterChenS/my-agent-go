// Token accounting for one turn.
//
// The runtime reports usage in its own shape, and the shapes disagree on what
// "input" means: pi sends `{ input, output, cacheRead, cacheWrite }` where
// `input` excludes the cached prompt, while OpenAI-compatible payloads send
// `prompt_tokens` / `input_tokens` that already include the cached part. Reading
// a fixed field name produced the old display: input showed a number (it had
// fallen back to `totalTokens`) and output was always zero because no runtime
// sends `outputTokens`.
//
// A turn is also more than one model call: every tool call is followed by
// another request whose prompt repeats the conversation. The turn's usage is the
// sum of those requests, which is what the provider billed. Codex is the
// exception — its thread notification carries a running total for the whole
// thread, so those reports are marked `scope: "thread"` and read as snapshots
// instead of being added up.

import type { SessionEvent } from './client'

/** Tokens a turn used, as the runtime counted them. */
export interface TurnUsage {
  /** Prompt tokens processed, cached reads included. */
  input: number
  /** Tokens the model wrote. */
  output: number
  /** The part of `input` served from the prompt cache. */
  cached: number
  /** How many usage reports contributed; 0 means "the runtime said nothing". */
  reports: number
}

type Bag = Record<string, unknown>

function asNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  // Some runtimes serialise counts as strings.
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return null
}

function at(bag: Bag, path: string): unknown {
  let current: unknown = bag
  for (const step of path.split('.')) {
    if (typeof current !== 'object' || current === null) return undefined
    current = (current as Bag)[step]
  }
  return current
}

/** First numeric value among `paths`, in order of preference. */
function first(bag: Bag, paths: string[]): number | null {
  for (const path of paths) {
    const value = asNumber(at(bag, path))
    if (value !== null) return value
  }
  return null
}

/** Field names that already include the cached prompt in their total. */
const INCLUSIVE_INPUT = [
  'promptTokens',
  'prompt_tokens',
  'inputTokens',
  'prompt_tokens_details',
  'input_tokens_details',
]

/**
 * Normalise one runtime usage object.
 *
 * Returns `null` when the object carries no token counts at all, so a stray
 * `{}` or a cost-only payload does not become a zero row.
 */
export function readUsage(raw: unknown): Omit<TurnUsage, 'reports'> | null {
  if (typeof raw !== 'object' || raw === null) return null
  const bag = raw as Bag

  const base = first(bag, [
    'inputTokens',
    'input_tokens',
    'promptTokens',
    'prompt_tokens',
    'input',
    'prompt',
  ])
  const output = first(bag, [
    'outputTokens',
    'output_tokens',
    'completionTokens',
    'completion_tokens',
    'output',
    'completion',
  ])
  const cached =
    first(bag, [
      'cacheRead',
      'cache_read',
      'cache_read_input_tokens',
      'cachedInputTokens',
      'cached_input_tokens',
      'prompt_tokens_details.cached_tokens',
      'input_tokens_details.cached_tokens',
    ]) ?? 0
  const cacheWrite =
    first(bag, ['cacheWrite', 'cache_write', 'cache_creation_input_tokens']) ?? 0

  if (base === null && output === null && cached === 0 && cacheWrite === 0) return null

  // `input` / `input_tokens` are exclusive only when the runtime also reports a
  // cache counter (pi, Anthropic); the OpenAI-style aggregate has no sibling
  // cache field and must not be padded a second time.
  const inclusive = INCLUSIVE_INPUT.some((path) => at(bag, path) !== undefined)
  const input = (base ?? 0) + (inclusive ? 0 : cached + cacheWrite)

  return { input, output: output ?? 0, cached }
}

/**
 * Sum a turn's usage reports.
 *
 * `scope: "thread"` reports are running totals for the whole thread, so only the
 * last one is meaningful and adding two of them would double count the earlier
 * turns. Every other report is one model request and is added up.
 */
export function turnUsage(events: SessionEvent[]): TurnUsage | null {
  let input = 0
  let output = 0
  let cached = 0
  let reports = 0
  let snapshot: Omit<TurnUsage, 'reports'> | null = null

  for (const event of events) {
    if (event.kind !== 'usage') continue
    const usage = readUsage(event.payload.usage ?? event.payload)
    if (!usage) continue
    if (event.payload.scope === 'thread') {
      snapshot = usage
      continue
    }
    input += usage.input
    output += usage.output
    cached += usage.cached
    reports += 1
  }

  if (reports > 0) return { input, output, cached, reports }
  // Only thread snapshots arrived: the latest one is the whole thread's total.
  if (snapshot) return { ...snapshot, reports: 1 }
  return null
}
