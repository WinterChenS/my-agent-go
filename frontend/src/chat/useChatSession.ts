// The chat page's live data path.
//
// One rule shapes this hook: **the ledger is the truth**. A send writes the
// user message, then every runtime event, then the final assistant message, so
// the UI never has to hold a conversation in memory. While a turn runs the hook
// polls the ledger and merges text deltas into a single streaming bubble; when
// the turn ends it reloads the session, which replaces the optimistic rows with
// the stored ones.

import { useCallback, useEffect, useRef, useState } from 'react'
import {
  CatalogUnavailable,
  deleteCatalogAgent,
  loadCatalog,
  refreshCatalog,
  type CatalogAgent,
  type CatalogModel,
} from '../catalog/client'
import { cacheUsable, readCachedSnapshot, writeCachedSnapshot } from '../catalog/cache'
import { onCatalogChanged } from '../catalog/notify'
import { isTauriHost } from '../host/tauri'
import {
  abortTurn,
  approveRequest,
  archiveSession,
  compactSession,
  deleteSession,
  describeSessionError,
  exportConversation,
  forkSession,
  latestSeq,
  listContext,
  loadRail,
  listProjects,
  listSessions,
  loadSession,
  loadSessionEvents,
  openSession,
  pinSession,
  renameSession,
  sendTurn,
  SessionUnavailable,
  sessionContext,
  steerTurn,
  type ApprovalDecision,
  type ApprovalRequest,
  type ContextEntry,
  type ContextUsage,
  type PiImage,
  type ContextRef,
  type ExportReport,
  type PermissionMode,
  type ProjectSummary,
  type RailView,
  type SessionDetail,
  type SessionEvent,
  type SessionRun,
  type SessionSummary,
  type ThinkingLevel,
} from './client'

/** How often the running turn re-reads the ledger. */
const POLL_INTERVAL_MS = 120

export type TurnFailureKind = 'failed' | 'error'

/** Why the last turn ended without a reply, for the page's state card. */
export interface TurnFailure {
  kind: TurnFailureKind
  message: string
}

export type ChatStatus = 'unavailable' | 'loading' | 'ready' | 'error'

export interface ModelChoice {
  runtimeId: string
  modelId: string
  displayName: string
  verified: boolean
  isDefault: boolean
}

/** A saved runtime+model binding. Picking one is picking a model plus two
 * decisions already made. */
export interface AgentChoice {
  id: string
  name: string
  runtimeId: string
  modelId: string
  isDefault: boolean
  /** Preset window label from the saved agent (`400K`, `1M`, …). */
  contextWindow: string
}

/** One tool call and, when it finished, its result. */
export interface LiveTool {
  id: string
  name: string
  args: string
  ok: boolean | null
  output: string
  /** Pictures the tool returned; drawn where the tool ran. */
  images?: PiImage[]
}

/** One piece of a delegated agent's own transcript, in order. */
export type SubAgentStep =
  | { kind: 'text'; text: string; channel: 'answer' | 'thinking' }
  | { kind: 'tool'; name: string; args: string; ok: boolean | null; output: string; images?: PiImage[] }
  | { kind: 'image'; image: PiImage }

export type SubAgentState =
  | 'pending'
  | 'running'
  | 'waiting'
  | 'completed'
  | 'failed'
  | 'interrupted'

/**
 * A delegated child agent, folded from its `sub_agent` events.
 *
 * One card per agent, not per event: a child reports its status, its streamed
 * answer, its tool calls and its final answer as separate rows, and this is
 * what puts them back together in the order they arrived.
 */
export interface LiveSubAgent {
  id: string
  name: string
  model: string
  task: string
  status: SubAgentState
  parentId: string
  step: number | null
  steps: SubAgentStep[]
  result: string
  error: string
  usage: Record<string, unknown> | null
}

export interface ChatSessionView {
  status: ChatStatus
  error: string | null
  models: ModelChoice[]
  model: ModelChoice | null
  chooseModel: (choice: ModelChoice) => void
  /** Saved bindings plus the derived default agent. */
  agents: AgentChoice[]
  deleteAgent: (id: string) => Promise<void>
  /** A manual rescan is in flight (the picker's spinning control). */
  syncing: boolean
  /** Re-scan the machine and rebuild the picker from the result. */
  syncModels: () => Promise<void>
  sessions: SessionSummary[]
  /** Directories the history panel groups the sessions by. */
  projects: ProjectSummary[]
  current: SessionDetail | null
  /** Text of the turn in flight, merged from text deltas. */
  /** Every event of the turn in flight, in ledger order. */
  turnEvents: SessionEvent[]
  /** The approval card waiting for an answer, if any. */
  pending: ApprovalRequest | null
  /** The right rail's four panes, re-read after every turn. */
  rail: RailView | null
  /** pi's own context-window estimate for the open session; null when unknown. */
  context: ContextUsage | null
  running: boolean
  /** Why the last turn ended without a reply; null while running or clean. */
  turnFailure: TurnFailure | null
  /** When the running turn started, for the visible waiting clock. */
  turnStartedAt: number | null
  /** Re-send the last prompt after a failure. */
  retry: () => Promise<string | null>
  select: (sessionId: string) => Promise<void>
  startNew: () => void
  send: (text: string, context?: ContextRef[]) => Promise<string | null>
  /**
   * Write a message into the turn that is streaming.
   *
   * Returns `false` when nothing was live to steer; the caller keeps the text.
   */
  steer: (text: string) => Promise<boolean>
  /** Answer the pending approval card. */
  approve: (decision: ApprovalDecision) => Promise<boolean>
  /** Files and directories the composer may reference in this project. */
  mention: (query: string) => Promise<ContextEntry[]>
  /** Write the conversation out; `overwrite` is the caller's explicit yes. */
  exportSession: (
    format: 'html' | 'markdown' | 'json',
    includeTools: boolean,
    overwrite?: boolean,
  ) => Promise<ExportReport>
  abort: () => Promise<boolean>
  refresh: () => Promise<void>
  rename: (sessionId: string, title: string) => Promise<void>
  setPinned: (sessionId: string, pinned: boolean) => Promise<void>
  setArchived: (sessionId: string, archived: boolean) => Promise<void>
  remove: (sessionId: string) => Promise<void>
  /** Branch a session at its last message and open the branch. */
  fork: (sessionId: string) => Promise<string | null>
  /** Fold all but the last `keepLast` messages into one summary row. */
  compact: (sessionId: string, keepLast: number) => Promise<void>
  /** Re-read pi's context estimate after an external change (e.g. compaction). */
  refreshContext: (sessionId: string) => Promise<void>
}

function toChoices(models: CatalogModel[], keep: string[]): ModelChoice[] {
  // Two filters, for two different reasons: the runtime has to be installed and
  // usable, and the user has to have turned the model on. Discovery finds every
  // model an installed runtime can reach, which is not a list anyone wants in a
  // chat picker.
  return models
    .filter((model) => keep.includes(model.runtimeId) && model.enabled)
    .sort((left, right) =>
      left.runtimeId === right.runtimeId
        ? left.modelId.localeCompare(right.modelId)
        : left.runtimeId.localeCompare(right.runtimeId),
    )
    .map((model) => ({
      runtimeId: model.runtimeId,
      modelId: model.modelId,
      displayName: model.displayName || model.modelId,
      verified: model.verified,
      isDefault: model.isDefault,
    }))
}

function toAgentChoices(agents: CatalogAgent[] | undefined): AgentChoice[] {
  return (agents ?? []).map((agent) => ({
    id: agent.id,
    name: agent.name,
    runtimeId: agent.runtimeId,
    modelId: agent.modelId,
    isDefault: agent.isDefault,
    contextWindow: agent.contextWindow,
  }))
}

/** Prefer an explicit default, then a verified model, then anything. */
function pickModel(choices: ModelChoice[]): ModelChoice | null {
  return (
    choices.find((choice) => choice.isDefault) ??
    choices.find((choice) => choice.verified) ??
    choices[0] ??
    null
  )
}

/** The last approval request that has not been answered yet. */
function lastPendingApproval(events: SessionEvent[]): ApprovalRequest | null {
  const answered = new Set(
    events
      .filter((event) => event.kind === 'approval_resolved')
      .map((event) => String(event.payload.id ?? '')),
  )
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index]
    if (event.kind !== 'approval_request') continue
    const payload = event.payload as unknown as ApprovalRequest
    if (!payload || answered.has(payload.id)) return null
    return payload
  }
  return null
}

/**
 * One block of a turn, in the order the runtime produced it.
 *
 * The turn used to be rendered as three separate piles — all reasoning in one
 * disclosure, all answers in one bubble, all tool cards in one list at the end
 * of the thread — which is not what happened. A run reasons, calls a tool,
 * reads the result, reasons again; the transcript should say that. Grouping the
 * ledger into ordered parts is what makes that renderable.
 */
export type TurnPart =
  | { kind: 'thinking'; key: string; text: string }
  | { kind: 'tool'; key: string; tool: LiveTool }
  | { kind: 'agent'; key: string; agent: LiveSubAgent }
  /** A message the reader injected while this turn was already running. */
  | { kind: 'steer'; key: string; text: string }
  | { kind: 'text'; key: string; text: string }

/** A child agent before any of its own events have arrived. */
function emptySubAgent(id: string): LiveSubAgent {
  return {
    id,
    name: '',
    model: '',
    task: '',
    status: 'running',
    parentId: '',
    step: null,
    steps: [],
    result: '',
    error: '',
    usage: null,
  }
}

function asText(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function stringifyValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  return typeof value === 'string' ? value : JSON.stringify(value)
}

/** Convert a snapshot's `steps` payload into the card's own vocabulary. */
/** Pictures on a payload, validated before they reach an `<img src>`. */
function imagesOf(item: Record<string, unknown>): PiImage[] {
  const raw = item.images
  if (!Array.isArray(raw)) return []
  return raw
    .filter((entry): entry is Record<string, unknown> => Boolean(entry) && typeof entry === 'object')
    .map((entry) => ({ data: asText(entry.data), mimeType: asText(entry.mimeType) || 'image/png' }))
    .filter((image) => image.data !== '')
}

function toSubAgentSteps(raw: unknown): SubAgentStep[] {
  if (!Array.isArray(raw)) return []
  const steps: SubAgentStep[] = []
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue
    const item = entry as Record<string, unknown>
    if (item.kind === 'text') {
      const text = asText(item.text)
      if (text === '') continue
      // The host marks a child's reasoning with `channel: "thinking"`; dropping
      // it would print reasoning and answer as one paragraph.
      const channel = item.channel === 'thinking' ? 'thinking' : 'answer'
      steps.push({ kind: 'text', text, channel })
      continue
    }
    if (item.kind === 'tool') {
      steps.push({
        kind: 'tool',
        name: asText(item.name) || 'tool',
        args: stringifyValue(item.args),
        ok: null,
        output: '',
        images: imagesOf(item),
      })
      continue
    }
    if (item.kind === 'image') {
      const image = item.image as PiImage | undefined
      if (image?.data) steps.push({ kind: 'image', image })
      continue
    }
    if (item.kind === 'tool_result') {
      const name = asText(item.name) || 'tool'
      const open = [...steps]
        .reverse()
        .find((step) => step.kind === 'tool' && step.ok === null && step.name === name)
      if (open && open.kind === 'tool') {
        open.ok = item.ok === true
        open.output = stringifyValue(item.output)
        open.images = imagesOf(item)
      } else {
        steps.push({
          kind: 'tool',
          name,
          args: '',
          ok: item.ok === true,
          output: stringifyValue(item.output),
          images: imagesOf(item),
        })
      }
    }
  }
  return steps
}

/**
 * Fold one `sub_agent` row into the agent's card.
 *
 * Two runtimes, two shapes, one card: codex streams increments (append text,
 * open a tool, close it), pi sends a cumulative snapshot and a `final` copy of
 * each answer. Handling both here keeps the renderer unaware of which runtime
 * produced the delegation.
 */
function applySubAgentEvent(agent: LiveSubAgent, payload: Record<string, unknown>) {
  const name = asText(payload.name)
  if (name !== '') agent.name = name
  const model = asText(payload.model)
  if (model !== '') agent.model = model
  const task = asText(payload.task)
  if (task !== '') agent.task = task
  const parent = asText(payload.parentId)
  if (parent !== '') agent.parentId = parent
  if (typeof payload.step === 'number') agent.step = payload.step
  const status = asText(payload.status)
  if (status !== '') agent.status = status as SubAgentState
  if (payload.usage && typeof payload.usage === 'object') {
    agent.usage = payload.usage as Record<string, unknown>
  }
  const result = asText(payload.result)
  if (result !== '') agent.result = result
  const error = asText(payload.error)
  if (error !== '') agent.error = error

  const channel: 'answer' | 'thinking' =
    asText(payload.channel) === 'thinking' ? 'thinking' : 'answer'
  const phase = asText(payload.phase)
  if (phase === 'text') {
    const text = asText(payload.text)
    if (text === '') return
    if (payload.final === true) {
      // The runtime sent the finished message after its deltas; replace the
      // tail so the answer is not printed twice.
      for (let index = agent.steps.length - 1; index >= 0; index -= 1) {
        const step = agent.steps[index]
        if (step.kind === 'text' && step.channel === channel) {
          agent.steps[index] = { kind: 'text', text, channel }
          return
        }
      }
    }
    const last = agent.steps[agent.steps.length - 1]
    if (last && last.kind === 'text' && last.channel === channel) {
      last.text += text
    } else {
      agent.steps.push({ kind: 'text', text, channel })
    }
    return
  }
  if (phase === 'tool') {
    const tool = (payload.tool ?? {}) as Record<string, unknown>
    agent.steps.push({
      kind: 'tool',
      name: asText(tool.name) || 'tool',
      args: stringifyValue(tool.args),
      ok: null,
      output: '',
    })
    return
  }
  if (phase === 'tool_result') {
    const tool = (payload.tool ?? {}) as Record<string, unknown>
    const name = asText(tool.name) || 'tool'
    const open = [...agent.steps]
      .reverse()
      .find((step) => step.kind === 'tool' && step.ok === null && step.name === name)
    if (open && open.kind === 'tool') {
      open.ok = tool.ok === true
      open.output = stringifyValue(tool.output)
    }
    return
  }
  if (phase === 'snapshot') {
    agent.steps = toSubAgentSteps(payload.steps)
  }
}

/**
 * Fold a run's events into chronological parts.
 *
 * Every kind of output is a part, and each part ends where the runtime starts
 * doing something else: reasoning closes when the answer begins, the answer
 * closes when a tool call interrupts it, and a second burst of reasoning after
 * that call opens a new part. The transcript therefore reads in the order it
 * happened — thinking, tool, text, thinking, tool, text — instead of one
 * disclosure above a single block of prose with every tool card at the end.
 *
 * Consecutive deltas of the same kind merge into one part, so a streamed answer
 * is not one part per token. `tool_result` pairs with the most recent call that
 * has none yet, because adapters emit the two without a shared key.
 */
export function buildTurnParts(events: SessionEvent[], runId?: string): TurnPart[] {
  const parts: TurnPart[] = []
  let reasoning = ''
  let reasoningKey = ''
  let answer = ''
  let answerKey = ''
  const flushReasoning = () => {
    if (reasoning.trim() !== '') {
      parts.push({ kind: 'thinking', key: reasoningKey, text: reasoning })
    }
    reasoning = ''
    reasoningKey = ''
  }
  const flushAnswer = () => {
    // Only whitespace between two steps is spacing, not an answer.
    if (answer.trim() !== '') {
      parts.push({ kind: 'text', key: answerKey, text: answer })
    }
    answer = ''
    answerKey = ''
  }

  const ordered = [...events].sort((left, right) => left.seq - right.seq)
  for (const event of ordered) {
    if (runId !== undefined && event.runId !== runId) continue
    if (event.kind === 'thinking') {
      const delta = event.payload.delta
      if (typeof delta !== 'string' || delta === '') continue
      // Reasoning that follows an answer starts a new block above it.
      flushAnswer()
      if (reasoningKey === '') reasoningKey = `${event.runId}:${event.seq}`
      reasoning += delta
      continue
    }
    if (event.kind === 'text_delta') {
      const delta = event.payload.delta
      if (typeof delta !== 'string' || delta === '') continue
      flushReasoning()
      if (answerKey === '') answerKey = `${event.runId}:${event.seq}`
      answer += delta
      continue
    }
    if (event.kind === 'tool_call') {
      flushReasoning()
      flushAnswer()
      const args = event.payload.args ?? event.payload.command ?? ''
      parts.push({
        kind: 'tool',
        key: `${event.runId}:${event.seq}`,
        tool: {
          id: `${event.runId}:${event.seq}`,
          name: String(event.payload.tool ?? 'tool'),
          args: typeof args === 'string' ? args : JSON.stringify(args),
          ok: null,
          output: '',
          images: [],
        },
      })
      continue
    }
    if (event.kind === 'tool_result') {
      const open = [...parts].reverse().find((part) => part.kind === 'tool' && part.tool.ok === null)
      if (!open || open.kind !== 'tool') continue
      open.tool.ok = event.payload.ok === true
      const output = event.payload.output ?? event.payload.text ?? ''
      open.tool.output = typeof output === 'string' ? output : JSON.stringify(output)
      const images = imagesOf(event.payload as Record<string, unknown>)
      if (images.length > 0) open.tool.images = images
      continue
    }
    if (event.kind === 'user_steer') {
      // A steer is not a new turn: it belongs in the transcript at the point
      // the reader sent it, between the work it interrupted.
      flushReasoning()
      flushAnswer()
      const text = event.payload.text
      if (typeof text === 'string' && text.trim() !== '') {
        parts.push({ kind: 'steer', key: `${event.runId}:steer:${event.seq}`, text })
      }
      continue
    }
    if (event.kind === 'sub_agent') {
      const id = typeof event.payload.agentId === 'string' ? event.payload.agentId : ''
      if (id === '') continue
      // A delegated agent is a block of the transcript in its own right: the
      // parent stops talking while it works.
      flushReasoning()
      flushAnswer()
      let part = parts.find(
        (candidate): candidate is Extract<TurnPart, { kind: 'agent' }> =>
          candidate.kind === 'agent' && candidate.agent.id === id,
      )
      if (!part) {
        part = { kind: 'agent', key: `${event.runId}:agent:${id}`, agent: emptySubAgent(id) }
        parts.push(part)
      }
      applySubAgentEvent(part.agent, event.payload)
      continue
    }
    if (event.kind === 'unknown' && event.payload.type === 'auto_retry') {
      // pi auto-retries a failed provider call. The abandoned attempt already
      // streamed its text (and reasoning) into these accumulators; the retry
      // re-streams the whole answer with different chunk boundaries. Keeping
      // both weaves the answer into itself — the duplicated-transcript bug.
      // Drop everything the failed attempt produced and start the retry clean.
      // The event is recorded in the ledger as `unknown` with this marker so a
      // replayed session is repaired the same way as a live stream.
      reasoning = ''
      reasoningKey = ''
      answer = ''
      answerKey = ''
      continue
    }
  }
  flushReasoning()
  flushAnswer()
  return parts
}

/** Whether every character of `needle` appears in `haystack` in order. */
function isSubsequence(needle: string, haystack: string): boolean {
  let cursor = 0
  for (let index = 0; index < haystack.length && cursor < needle.length; index += 1) {
    if (haystack[index] === needle[cursor]) cursor += 1
  }
  return cursor === needle.length
}

/**
 * Fold the authoritative stored answer into the streamed parts.
 *
 * The ledger keeps one message per turn next to the deltas that produced it.
 * Usually they agree, and an adapter that reports fewer deltas than the final
 * answer just needs its tail completed.
 *
 * A runtime can also emit the answer twice. Measured on pi 1.5.0: when a
 * provider call is auto-retried, the abandoned attempt and the retry stream
 * their text as two batches of `text_delta` with different chunk boundaries.
 * Appending both weaves the answer into itself, e.g. `C:/` + `C:/Users/...` +
 * `Users/.../email_sender/` + `email_sender/`, rendering a broken path. The
 * stored message is pi's `finalText` from `turn_end`: one clean copy.
 *
 * CRITICAL — `finalText` is ONLY the LAST answer. A real agentic turn streams
 * several distinct text blocks: an opening aside ("I'll take a look"), a
 * mid-turn note after a tool, and finally the answer, with thinking/tool cards
 * interleaved. On the real ledger the final block equals `finalText` exactly
 * while the earlier blocks are different prose. Concatenating every text part
 * and treating the result as one replayed answer is wrong: it made us move the
 * final answer up to the FIRST text block and delete the rest, which shoved all
 * thinking/tool cards below the prose and re-arranged the transcript.
 *
 * So reconciliation touches ONLY the last text part and never moves, merges or
 * drops any part. Earlier text, thinking, tool and agent parts stay exactly
 * where the turn produced them.
 */
export function withStoredAnswer(parts: TurnPart[], stored: string): TurnPart[] {
  if (stored.trim() === '') return parts
  const textIndexes = parts
    .map((part, index) => (part.kind === 'text' ? index : -1))
    .filter((index) => index >= 0)
  if (textIndexes.length === 0) return parts

  const last = textIndexes[textIndexes.length - 1]
  const streamed = (parts[last] as Extract<TurnPart, { kind: 'text' }>).text

  if (stored === streamed) return parts

  if (stored.startsWith(streamed)) {
    // Normal case: the runtime reported a shorter stream than the stored answer.
    // Complete the tail on the last text part only; earlier blocks are untouched.
    const rest = stored.slice(streamed.length)
    if (rest.trim() === '') return parts
    return parts.map((part, index) =>
      index === last && part.kind === 'text' ? { ...part, text: part.text + rest } : part,
    )
  }

  // Genuine replay: the last streamed block is LONGER than the clean answer yet
  // still contains its characters in order (a subsequence — chunks interleave,
  // no characters lost). Replace just that last block. A different final block
  // (streamed shorter, or not a subsequence) is left alone — never a replay.
  if (streamed.length > stored.length && isSubsequence(stored, streamed)) {
    return parts.map((part, index) =>
      index === last && part.kind === 'text' ? { ...part, text: stored } : part,
    )
  }
  return parts
}

/** The wall clock a run began, for the visible waiting clock. */
function startedAtFrom(run: SessionRun): number | null {
  const parsed = Date.parse(run.startedAt)
  return Number.isFinite(parsed) ? parsed : null
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms)
  })
}

/// How long the page waits for the first catalog scan before letting the user
/// drive (90 seconds in total: a cold scan probes every installed runtime).
const MODEL_WAIT_ATTEMPTS = 60
const MODEL_WAIT_INTERVAL_MS = 1500

export function useChatSession({
  enabled = true,
  agentId = '',
  cwd = '',
  permission = 'medium',
  thinking = 'medium',
}: {
  enabled?: boolean
  agentId?: string
  cwd?: string
  permission?: PermissionMode
  thinking?: ThinkingLevel
} = {}): ChatSessionView {
  const available = enabled && isTauriHost()
  // A ref rather than a dependency of `send`: the picker can change while a
  // turn is in flight, and only the *next* send should adopt the new trust
  // level. Reading it at send time also avoids re-creating `send` on every
  // change, which would restart the memo chain feeding the composer.
  const permissionRef = useRef<PermissionMode>(permission)
  permissionRef.current = permission
  // Same rule for the thinking level: it travels with the next turn, so it must
  // not tear down `send` when the picker moves.
  const thinkingRef = useRef<ThinkingLevel>(thinking)
  thinkingRef.current = thinking
  // A ref rather than a dependency of `send`: the picker can change while a
  // turn is in flight, and only the *next new session* should adopt that agent.
  const agentIdRef = useRef(agentId)
  agentIdRef.current = agentId
  // The directory a *new* session runs in. A ref for the same reason as the
  // agent: switching workspace must not disturb a conversation already open,
  // and the next "new conversation" picks it up.
  const cwdRef = useRef(cwd)
  cwdRef.current = cwd
  const [status, setStatus] = useState<ChatStatus>(() => (available ? 'loading' : 'unavailable'))
  const [error, setError] = useState<string | null>(null)
  const [models, setModels] = useState<ModelChoice[]>([])
  const [agents, setAgents] = useState<AgentChoice[]>([])
  const [model, setModel] = useState<ModelChoice | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [current, setCurrent] = useState<SessionDetail | null>(null)
  const [turnEvents, setTurnEvents] = useState<SessionEvent[]>([])
  const [rail, setRail] = useState<RailView | null>(null)
  const [context, setContext] = useState<ContextUsage | null>(null)
  const [running, setRunning] = useState(false)
  const [turnFailure, setTurnFailure] = useState<TurnFailure | null>(null)
  const [turnStartedAt, setTurnStartedAt] = useState<number | null>(null)
  const pollStop = useRef(false)
  const mounted = useRef(true)
  /** Last ledger seq already rendered; resets when the session changes. */
  const eventCursor = useRef(0)
  /**
   * Run ids that already existed when this turn started.
   *
   * The poll is a loop, not a subscription: the ledger can hold events the
   * previous turn wrote after its last read (the turn ends, the loop stops).
   * Those rows used to be merged into the *next* turn's streaming bubble, so
   * the second answer opened with the first answer's words until the final
   * reload replaced them. Events from any run that existed before this turn
   * are history, not this turn's output.
   */
  const staleRuns = useRef<Set<string>>(new Set())
  const lastPrompt = useRef('')
  const sendRef = useRef<
    (text: string, context?: ContextRef[]) => Promise<string | null>
  >(async () => null)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      pollStop.current = true
    }
  }, [])

  // The list and the project index are read together: the panel groups the
  // rows by project, so a list without the index would render every session
  // twice (once grouped, once flat) or lose the counts.
  const reloadSessions = useCallback(async () => {
    const [page, index] = await Promise.all([listSessions({ limit: 50 }), listProjects()])
    if (!mounted.current) return
    setSessions(page.sessions)
    setProjects(index)
  }, [])

  const refresh = useCallback(async () => {
    if (!isTauriHost()) return
    try {
      await reloadSessions()
      const active = current?.session.id
      if (active) {
        const detail = await loadSession(active)
        if (mounted.current) setCurrent(detail)
      }
    } catch (cause) {
      if (mounted.current) setError(describeSessionError(cause))
    }
  }, [current, reloadSessions])

  /** Read the catalog once and adopt its models; `true` when there is one. */
  const readModels = useCallback(async (): Promise<boolean> => {
    // The catalog says which runtimes exist and which models they can use; a
    // chat cannot offer a model the machine does not have.
    const catalog = await loadCatalog()
    writeCachedSnapshot(catalog)
    const usable = catalog.runtimes
      .filter((runtime) => runtime.status === 'available')
      .map((runtime) => runtime.runtimeId)
    const choices = toChoices(catalog.models, usable)
    if (!mounted.current) return choices.length > 0
    // An empty probe result (a runtime that failed to spawn this launch) must
    // not wipe the verified choices already on screen from the cache.
    if (choices.length > 0) {
      setModels(choices)
      setAgents(toAgentChoices(catalog.agents))
      setModel((existing) => existing ?? pickModel(choices))
    }
    return choices.length > 0
  }, [])

  const boot = useCallback(async () => {
    // Sample mode never touches the host, so the prototype page keeps working
    // without a ledger and the acceptance run does not pay for an extra scan.
    if (!available) {
      setStatus('unavailable')
      return
    }
    setStatus('loading')
    setError(null)

    // The model list needs a catalog scan, which on this machine takes several
    // seconds the first time. History is a single indexed query, so it is read
    // first and the page becomes usable immediately; the composer waits for the
    // models instead of the whole page waiting for them.
    void (async () => {
      try {
        const [page, index] = await Promise.all([listSessions({ limit: 50 }), listProjects()])
        if (!mounted.current) return
        setSessions(page.sessions)
        setProjects(index)
        setStatus('ready')
      } catch (cause) {
        if (!mounted.current) return
        if (cause instanceof CatalogUnavailable || cause instanceof SessionUnavailable) {
          setStatus('unavailable')
          return
        }
        setStatus('error')
        setError(describeSessionError(cause))
      }
    })()

    // A returning window can open the composer from the last verified snapshot
    // the moment it paints; the host refresh below reconciles it in the
    // background. Only a genuinely cold start (no usable cache) has to wait on
    // the startup scan that probes every runtime.
    const cached = readCachedSnapshot()
    let cachedReady = false
    if (cacheUsable(cached) && cached && mounted.current) {
      const usable = cached.runtimes
        .filter((runtime) => runtime.status === 'available')
        .map((runtime) => runtime.runtimeId)
      const choices = toChoices(cached.models, usable)
      setModels(choices)
      setAgents(toAgentChoices(cached.agents))
      setModel((existing) => existing ?? pickModel(choices))
      cachedReady = true
    }

    try {
      if (await readModels()) return
      // A cold data directory has an empty catalog until the first scan (which
      // probes every installed runtime) finishes. Reading once and giving up
      // left the composer disabled forever, with the page saying "connecting"
      // and no retry: the user's only way out was to press sync. Wait for the
      // scan instead, with a bounded budget so a broken machine still settles.
      // With a usable cache the composer already works, so a failed background
      // refresh here does not turn into a blocking wait.
      if (cachedReady) return
      for (let attempt = 0; attempt < MODEL_WAIT_ATTEMPTS; attempt += 1) {
        await sleep(MODEL_WAIT_INTERVAL_MS)
        if (!mounted.current) return
        if (await readModels()) return
      }
    } catch (cause) {
      if (!mounted.current) return
      if (cause instanceof CatalogUnavailable || cause instanceof SessionUnavailable) return
      setError(describeSessionError(cause))
    }
  }, [available])

  useEffect(() => {
    void boot()
  }, [boot])

  // The first-run wizard turns models on *after* this page mounted, so the
  // composer would otherwise keep the empty list it read before setup finished
  // and wait out its discovery budget. Re-read whenever the catalog announces
  // a change instead of making the user reload the window.
  useEffect(() => {
    return onCatalogChanged(() => {
      void readModels()
    })
  }, [readModels])

  const syncModels = useCallback(async () => {
    if (!available) return
    setSyncing(true)
    setError(null)
    try {
      // A manual sync re-scans: a model installed after the startup scan is
      // exactly the case this button exists for, and only a rescan finds it.
      const catalog = await refreshCatalog()
      writeCachedSnapshot(catalog)
      const usable = catalog.runtimes
        .filter((runtime) => runtime.status === 'available')
        .map((runtime) => runtime.runtimeId)
      const choices = toChoices(catalog.models, usable)
      if (!mounted.current) return
      setModels(choices)
      setAgents(toAgentChoices(catalog.agents))
      // Keep the user's pick when it survived the rescan; otherwise fall back to
      // the same rule as the first load, so the picker never points at a model
      // that is gone.
      setModel((existing) => {
        const kept = existing
          ? choices.find(
              (choice) =>
                choice.runtimeId === existing.runtimeId && choice.modelId === existing.modelId,
            )
          : undefined
        return kept ?? pickModel(choices)
      })
    } catch (cause) {
      if (mounted.current) setError(describeSessionError(cause))
    } finally {
      if (mounted.current) setSyncing(false)
    }
  }, [available])

  const deleteAgent = useCallback(async (id: string) => {
    const snapshot = await deleteCatalogAgent(id)
    if (!mounted.current) return
    setAgents(toAgentChoices(snapshot.agents))
  }, [])

  // The rail is a second read of the same ledger. It is loaded after the
  // transcript so the page renders first, and a rail failure never blanks a
  // conversation that loaded fine.
  const refreshRail = useCallback(async (sessionId: string | null) => {
    if (!sessionId) {
      setRail(null)
      return
    }
    try {
      const view = await loadRail(sessionId)
      if (mounted.current) setRail(view)
    } catch {
      // Keep whatever is on screen. Blanking the pane on a transient read error
      // turned "the file list did not load yet" into "this conversation has no
      // data", which is a different and false claim.
    }
  }, [])

  // The number is a property of the session, not of the draft: it changes when
  // a turn lands or another conversation is opened, never while typing.
  const refreshContext = useCallback(async (sessionId: string) => {
    try {
      const usage = await sessionContext(sessionId)
      if (mounted.current) setContext(usage ?? null)
    } catch {
      if (mounted.current) setContext(null)
    }
  }, [])

  /**
   * Follow a run that is still going, after the page was left and came back.
   *
   * Leaving `/chat` unmounts this hook: the poll stops with the page, and the
   * host keeps writing the turn into the ledger. Returning used to show the
   * user message and nothing else, as if the run had never happened. This picks
   * the live run up from the ledger's tail and renders it until it ends.
   */
  const attach = useCallback(
    async (sessionId: string, run: SessionRun, runs: SessionRun[]) => {
      // Read the ledger once. The cursor starts at its end so history is not
      // replayed by the poll, and this run's own events are kept to rebuild the
      // half-finished turn. The same read tells us whether the run ended while
      // the page was away — its terminal event is already in this batch, and
      // waiting for a new one would wait forever.
      let since = 0
      let replay: SessionEvent[] = []
      let terminal: SessionEvent | null = null
      try {
        const ledger = await loadSessionEvents(sessionId, 0)
        since = latestSeq(ledger, 0)
        // Only this run: a session with earlier turns must not pour them into
        // the bubble the reader is coming back to.
        replay = ledger.filter((event) => event.runId === run.id)
        terminal =
          replay.find((event) => event.kind === 'turn_completed' || event.kind === 'aborted') ??
          null
      } catch {
        return
      }
      if (!mounted.current) return

      if (terminal) {
        // Finished between the detail read and this one: just show the result.
        try {
          const detail = await loadSession(sessionId)
          if (mounted.current) setCurrent(detail)
        } catch {
          // Keep what is on screen; a manual refresh retries.
        }
        await refreshRail(sessionId)
        return
      }

      eventCursor.current = since
      // Only this run's events belong to the live view; every other run in the
      // session is history and must not leak into it.
      staleRuns.current = new Set(runs.map((row) => row.id).filter((id) => id !== run.id))
      // Resume, do not restart. The turn kept going while the page was away:
      // its reasoning and tool calls are already in the ledger, and starting
      // from an empty bubble made a half-finished turn look like one that had
      // just begun — everything it had thought and run so far was gone.
      setTurnEvents(replay)
      setTurnFailure(null)
      setTurnStartedAt(startedAtFrom(run) ?? Date.now())
      setRunning(true)
      pollStop.current = false

      while (!pollStop.current) {
        try {
          const events = await loadSessionEvents(sessionId, since)
          if (events.length > 0) {
            since = latestSeq(events, since)
            eventCursor.current = since
            const fresh = events.filter(
              (event) => !event.runId || !staleRuns.current.has(event.runId),
            )
            if (fresh.length > 0 && mounted.current) {
              setTurnEvents((previous) => [...previous, ...fresh])
            }
            terminal =
              fresh.find(
                (event) => event.kind === 'turn_completed' || event.kind === 'aborted',
              ) ?? terminal
            if (terminal) break
          }
        } catch {
          // A failed poll is not a failed turn: the next tick retries.
        }
        await sleep(POLL_INTERVAL_MS)
      }

      const payload = (terminal?.payload ?? {}) as Record<string, unknown>
      const status = typeof payload.status === 'string' ? payload.status : 'completed'
      let failure: TurnFailure | null = null
      if (status === 'failed') {
        failure = { kind: 'failed', message: String(payload.error ?? 'the turn failed') }
      } else if (status === 'aborted') {
        failure = { kind: 'failed', message: 'the turn was stopped' }
      }

      try {
        const detail = await loadSession(sessionId)
        if (mounted.current) {
          // Same batch as `send`: the ledger with its assistant message and the
          // end of streaming must land together, never two frames apart.
          setCurrent(detail)
          setRunning(false)
          setTurnStartedAt(null)
          setTurnFailure(failure)
        }
      } catch {
        // Keep the streamed view; the next open re-reads it.
        if (mounted.current) {
          setRunning(false)
          setTurnStartedAt(null)
          setTurnFailure(failure)
        }
      }
      await reloadSessions().catch(() => undefined)
      await refreshRail(sessionId)
      void refreshContext(sessionId)
    },
    [reloadSessions, refreshRail],
  )

  const select = useCallback(
    async (sessionId: string) => {
      try {
        const detail = await loadSession(sessionId)
        if (!mounted.current) return
        setCurrent(detail)
        setTurnEvents([])
        setTurnFailure(null)
        setTurnStartedAt(null)
        eventCursor.current = 0
        // Opening a stored conversation starts no turn: every run in it is
        // history, so a new message must not replay this transcript.
        staleRuns.current = new Set(detail.runs.map((run) => run.id))
        setError(null)
        // A run still marked `running` is a turn the host is executing right
        // now — the page was simply not here to watch it. Follow it instead of
        // showing the conversation as if it had stopped after the prompt.
        const live = detail.runs.find((run) => run.status === 'running')
        if (live) void attach(sessionId, live, detail.runs)
      } catch (cause) {
        if (mounted.current) setError(describeSessionError(cause))
      }
      await refreshRail(sessionId)
      await refreshContext(sessionId)
    },
    [attach, refreshRail, refreshContext],
  )

  const startNew = useCallback(() => {
    setCurrent(null)
    setTurnEvents([])
    setRail(null)
    setContext(null)
    setError(null)
    setTurnFailure(null)
    setTurnStartedAt(null)
    eventCursor.current = 0
    staleRuns.current = new Set()
  }, [])

  const send = useCallback(
    async (text: string, context: ContextRef[] = []): Promise<string | null> => {
      const prompt = text.trim()
      if (!prompt) return null
      if (!isTauriHost()) return describeSessionError(new SessionUnavailable())

      let active = current
      try {
        if (!active) {
          const choice = model
          if (!choice) {
            const missing = 'no model is available; add one in settings first'
            setError(missing)
            return missing
          }
          const session = await openSession({
            runtimeId: choice.runtimeId,
            model: choice.modelId,
            agentId: agentIdRef.current || undefined,
            cwd: cwdRef.current || undefined,
          })
          active = { session, messages: [], runs: [] }
          if (!mounted.current) return null
          setCurrent(active)
        }
      } catch (cause) {
        const message = describeSessionError(cause)
        if (mounted.current) setError(message)
        return message
      }

      const sessionId = active.session.id
      // A turn only ever shows its own output. `active.runs` is the ledger as
      // it stood before this prompt, so any event carrying one of those ids is
      // leftover history from a poll that stopped early.
      staleRuns.current = new Set(active.runs.map((run) => run.id))
      lastPrompt.current = prompt
      const optimistic = {
        id: `pending-${sessionId}`,
        sessionId,
        role: 'user' as const,
        text: prompt,
        seq: Number.MAX_SAFE_INTEGER,
        createdAt: new Date().toISOString(),
        // The chips the composer showed belong on this bubble from the first
        // frame. They used to appear only when the turn finished and the ledger
        // was read back, so a two-minute analysis left the file list in the
        // composer's past and nowhere on screen. The host replaces this with the
        // resolved copy (bytes, truncation, outside-project) when it lands.
        ...(context.length > 0 ? { meta: { context } } : {}),
      }
      setCurrent((existing) =>
        existing ? { ...existing, messages: [...existing.messages, optimistic] } : existing,
      )
      setRunning(true)
      setTurnEvents([])
      setError(null)
      setTurnFailure(null)
      setTurnStartedAt(Date.now())
      pollStop.current = false

      // The ledger keeps every previous turn's events. Starting every turn at
      // seq 0 replayed the earlier turns into this one's stream, so a second
      // turn showed the first turn's words again. The cursor survives across
      // turns of the same session and is reset when the session changes.
      const poll = async () => {
        let since = eventCursor.current
        while (!pollStop.current) {
          try {
            const events = await loadSessionEvents(sessionId, since)
            if (events.length > 0) {
              since = latestSeq(events, since)
              eventCursor.current = since
              const fresh = events.filter(
                (event) => !event.runId || !staleRuns.current.has(event.runId),
              )
              if (fresh.length > 0 && mounted.current) {
                // The turn's parts and approval cards come from the same
                // stream; keeping the batch lets the page render them without
                // a second read and without re-deriving text.
                setTurnEvents((previous) => [...previous, ...fresh])
              }
            }
          } catch {
            // A failed poll is not a failed turn: the next tick retries and the
            // final reload shows whatever actually landed.
          }
          await sleep(POLL_INTERVAL_MS)
        }
      }
      // Advance the cursor past anything written after the last tick. The loop
      // exits as soon as the run resolves, which can leave the tail of the turn
      // unread; without this the next prompt would start with a backlog.
      const drain = async () => {
        for (let round = 0; round < 8; round += 1) {
          try {
            const events = await loadSessionEvents(sessionId, eventCursor.current)
            if (events.length === 0) return
            eventCursor.current = latestSeq(events, eventCursor.current)
          } catch {
            return
          }
        }
      }

      const polling = poll()

      // The host writes the user message — with its context — before it spawns
      // the runtime, so the pane can list the attachments within a moment of
      // sending. Filling it only at the end meant a long analysis sat next to
      // "尚未接入会话数据" for minutes, right after the reader handed over files.
      let warmStop = false
      const warmRail = (async () => {
        for (let round = 0; round < 20; round += 1) {
          await sleep(400)
          if (warmStop || !mounted.current) return
          try {
            const view = await loadRail(sessionId)
            if (warmStop || !mounted.current) return
            setRail(view)
            // A turn that carried nothing has nothing to wait for: one read
            // after the message lands is all it needs.
            if ((view.references?.length ?? 0) >= context.length) return
            if (context.length === 0 && round >= 1) return
          } catch {
            // The message may not be committed yet; the next round retries.
          }
        }
      })()

      const sendPromise = sendTurn(
        sessionId,
        prompt,
        context,
        permissionRef.current,
        thinkingRef.current,
      )
      let failure: string | null = null
      let outcome: TurnFailure | null = null
      try {
        // No page-level deadline. A turn ends when the runtime says so — a long
        // run that reads files, runs commands and reasons in between is work,
        // not a hang, and the page used to throw its reply away. Stopping is the
        // user's call (`abort`), and a runtime that dies returns a failure.
        const report = await sendPromise
        if (report?.status === 'failed') {
          failure = report.error ?? 'the turn failed'
          outcome = { kind: 'failed', message: failure }
        } else if (report?.status === 'aborted') {
          outcome = { kind: 'failed', message: 'the turn was stopped' }
        }
      } catch (cause) {
        failure = describeSessionError(cause)
        outcome = { kind: 'error', message: failure }
      } finally {
        pollStop.current = true
        warmStop = true
        await polling
        await drain()
        // Let the warm read finish before the authoritative one, so a slow tail
        // cannot overwrite the finished view with the mid-turn copy.
        await warmRail
      }

      try {
        const detail = await loadSession(sessionId)
        if (mounted.current) {
          // Land the finished ledger and the end of the streaming state in the
          // same batch. Clearing `running` only after the IPC reads below left
          // a frame where both the stored answer and the streaming bubble were
          // visible — the same run drawn twice.
          setCurrent(detail)
          setRunning(false)
          setTurnStartedAt(null)
          setTurnFailure(outcome)
        }
      } catch {
        // Keep the optimistic view; the next refresh will retry.
        if (mounted.current) {
          setRunning(false)
          setTurnStartedAt(null)
          setTurnFailure(outcome)
        }
      }
      await reloadSessions().catch(() => undefined)
      await refreshRail(sessionId)
      await refreshContext(sessionId)
      if (failure) setError(failure)
      return failure
    },
    [current, model, reloadSessions, refreshRail, refreshContext],
  )

  const retry = useCallback(() => sendRef.current(lastPrompt.current), [])
  sendRef.current = send

  const exportSession = useCallback(
    async (
      format: 'html' | 'markdown' | 'json',
      includeTools: boolean,
      overwrite = false,
    ): Promise<ExportReport> => {
      const sessionId = current?.session.id
      if (!sessionId) throw new Error('there is no conversation to export yet')
      return exportConversation(sessionId, format, { includeTools, overwrite })
    },
    [current],
  )

  const approve = useCallback(
    async (decision: ApprovalDecision) => {
      const sessionId = current?.session.id
      const pending = lastPendingApproval(turnEvents)
      if (!sessionId || !pending) return false
      try {
        await approveRequest(sessionId, pending.id, decision)
        return true
      } catch (cause) {
        if (mounted.current) setError(describeSessionError(cause))
        return false
      }
    },
    [current, turnEvents],
  )

  const mention = useCallback(
    async (query: string): Promise<ContextEntry[]> => {
      const sessionId = current?.session.id
      if (!sessionId) return []
      try {
        return await listContext(sessionId, query)
      } catch {
        return []
      }
    },
    [current],
  )

  const steer = useCallback(
    async (text: string) => {
      const sessionId = current?.session.id
      const prompt = text.trim()
      if (!sessionId || !prompt) return false
      try {
        return await steerTurn(sessionId, prompt)
      } catch (cause) {
        if (mounted.current) setError(describeSessionError(cause))
        return false
      }
    },
    [current],
  )

  const abort = useCallback(async () => {
    const sessionId = current?.session.id
    if (!sessionId) return false
    try {
      const stopped = await abortTurn(sessionId)
      await refreshRail(sessionId)
      await refreshContext(sessionId)
      return stopped
    } catch (cause) {
      if (mounted.current) setError(describeSessionError(cause))
      return false
    }
  }, [current, refreshRail])

  const rename = useCallback(
    async (sessionId: string, title: string) => {
      await renameSession(sessionId, title)
      await refresh()
    },
    [refresh],
  )

  const setPinned = useCallback(
    async (sessionId: string, pinned: boolean) => {
      await pinSession(sessionId, pinned)
      await refresh()
    },
    [refresh],
  )

  const setArchived = useCallback(
    async (sessionId: string, archived: boolean) => {
      await archiveSession(sessionId, archived)
      if (archived && current?.session.id === sessionId) setCurrent(null)
      await refresh()
    },
    [current, refresh],
  )

  const remove = useCallback(
    async (sessionId: string) => {
      await deleteSession(sessionId)
      if (current?.session.id === sessionId) setCurrent(null)
      await refresh()
    },
    [current, refresh],
  )

  const fork = useCallback(
    async (sessionId: string): Promise<string | null> => {
      try {
        const branch = await forkSession(sessionId)
        await reloadSessions()
        await select(branch.id)
        return branch.id
      } catch (cause) {
        if (mounted.current) setError(describeSessionError(cause))
        return null
      }
    },
    [reloadSessions, select],
  )

  const compact = useCallback(
    async (sessionId: string, keepLast: number) => {
      try {
        await compactSession(sessionId, keepLast)
        const detail = await loadSession(sessionId)
        if (mounted.current) setCurrent(detail)
        await reloadSessions()
      } catch (cause) {
        if (mounted.current) setError(describeSessionError(cause))
      }
    },
    [reloadSessions],
  )

  const pending = lastPendingApproval(turnEvents)

  return {
    status,
    error,
    models,
    model,
    chooseModel: setModel,
    agents,
    deleteAgent,
    syncing,
    syncModels,
    sessions,
    projects,
    current,
    turnEvents,
    pending,
    rail,
    context,
    running,
    turnFailure,
    turnStartedAt,
    retry,
    select,
    startNew,
    send,
    steer,
    approve,
    mention,
    exportSession,
    abort,
    refresh,
    rename,
    setPinned,
    setArchived,
    remove,
    fork,
    compact,
    refreshContext,
  }
}
