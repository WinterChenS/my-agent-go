// Typed access to the phase-2 chat commands.
//
// The desktop host answers these; a plain browser (or jsdom) has no session
// ledger, so every call rejects with `SessionUnavailable` and the page keeps its
// honest "not connected" state instead of inventing messages.

import { invokeHost, isTauriHost } from '../host/tauri'

export type SessionStatus = 'idle' | 'running' | 'aborted' | 'failed' | 'archived'
export type SessionRole = 'system' | 'user' | 'assistant' | 'tool'
export type RunStatus = 'running' | 'completed' | 'aborted' | 'failed'
export type SessionEventKind =
  | 'session_started'
  | 'turn_started'
  | 'text_delta'
  | 'thinking'
  | 'message_completed'
  | 'tool_call'
  | 'tool_result'
  | 'approval_request'
  | 'approval_resolved'
  | 'usage'
  | 'error'
  | 'turn_completed'
  | 'aborted'
  | 'compacted'
  | 'context_attached'
  | 'sub_agent'
  | 'user_steer'
  | 'unknown'

export interface SessionSummary {
  id: string
  runtimeId: string
  title: string
  status: SessionStatus
  nativeId: string
  model: string
  projectPath: string
  /** Agent slug the session was opened with; empty when it runs a plain model. */
  agentId?: string
  /** Session this one was branched from; empty for a session started anew. */
  parentId: string
  /** Message `seq` in the parent the branch was taken at; `0` without one. */
  branchAt: number
  pinned: boolean
  archived: boolean
  messageCount: number
  createdAt: string
  updatedAt: string
  preview: string
}

/** One directory the history panel groups sessions by. */
export interface ProjectSummary {
  path: string
  name: string
  sessionCount: number
  lastUpdated: string
}

/** What kind of context an `@` reference points at. */
export type ContextKind = 'file' | 'dir' | 'skill'

export interface ContextRef {
  kind: ContextKind
  path: string
  label?: string
}

/** One entry in the composer's `@` list. */
export interface ContextEntry {
  path: string
  relative: string
  kind: ContextKind
  bytes: number
}

/** A reference after the host resolved it. */
export interface ResolvedRef {
  kind: ContextKind
  path: string
  label?: string
  bytes: number
  chars: number
  truncated: boolean
  outsideRoot: boolean
}

/** One approval question raised by a blocked run. */
export interface ApprovalRequest {
  id: string
  sessionId: string
  runId: string
  kind: string
  title: string
  detail: string
  createdAt: string
}

export type ApprovalDecision = 'allow' | 'deny' | 'cancel'

/** One row of the right rail's execution pane. */
export interface RailExec {
  turn: number
  runId: string
  tool: string
  label: string
  status: 'done' | 'failed' | 'running' | 'no_result'
  seq: number
  at: string
}

/** A file a turn wrote; the path is always inside the project. */
export interface RailArtifact {
  path: string
  relative: string
  bytes: number
  turn: number
  runId: string
  seq: number
  touches: number
  ext: string
}

/** A cited URL; `number` is the marker that appears in the transcript. */
export interface RailSource {
  number: number
  url: string
  host: string
  turn: number
  runId: string
  origin: string
}

/** A file, folder or skill the user attached to a turn. */
export interface RailReference {
  path: string
  label: string
  /** `file`, `dir`, `image` or `skill`. */
  kind: string
  turn: number
  runId: string
  bytes: number
  truncated: boolean
  outsideRoot: boolean
  touches: number
}

export interface RailTurn {
  turn: number
  runId: string
  status: string
  startedAt: string
  endedAt: string
  calls: number
}

export interface RailUsage {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  cost: number
  events: number
}

/** The local page the browser pane may render, and its sandbox. */
export interface RailPage {
  relative: string
  /** Empty string means: no scripts, no forms, no same-origin, no navigation. */
  sandbox: string
  html: string
  bytes: number
}

export interface RailBrowser {
  state: 'local' | 'unavailable'
  reason: string
  page?: RailPage
}

/** Everything the four rail panes render, for one session. */
/** One step of a child agent's own transcript. */
/** A picture a runtime put in a message, base64 as delivered by pi. */
export interface PiImage {
  data: string
  mimeType: string
}

export interface RailSubAgentStep {
  kind: 'text' | 'tool' | 'tool_result' | 'image' | string
  text?: string
  channel?: 'answer' | 'thinking' | string
  name?: string
  args?: unknown
  ok?: boolean | null
  output?: unknown
  /** Present when `kind === "image"`. */
  image?: PiImage
}

/** A delegated child agent, folded by the host from its observations. */
export interface RailSubAgent {
  id: string
  name: string
  task: string
  model: string
  status: 'pending' | 'running' | 'waiting' | 'completed' | 'failed' | 'interrupted' | string
  step?: number
  /** 1-based turn that delegated it. */
  turn: number
  runId: string
  steps: RailSubAgentStep[]
  result: string
  error: string
  usage?: Record<string, unknown>
}

export interface RailView {
  turns: RailTurn[]
  exec: RailExec[]
  artifacts: RailArtifact[]
  sources: RailSource[]
  /** Files, folders and skills the user attached, per turn. */
  references: RailReference[]
  /** Delegated child agents, one row per agent. */
  subagents: RailSubAgent[]
  usage?: RailUsage
  browser: RailBrowser
  /** Paths a turn named outside the project: counted, never listed. */
  outsidePaths: number
  empty: boolean
}

/** One written export. */
export interface ExportReport {
  path: string
  root: string
  fileName: string
  bytes: number
  sha256: string
  format: string
  messages: number
  runs: number
  toolRows: number
  toolsIncluded: boolean
}

/** What one compaction did. */
export interface CompactReport {
  collapsed: number
  kept: number
  summarySeq: number
}

export interface SessionMessage {
  id: string
  sessionId: string
  runId?: string
  role: SessionRole
  text: string
  seq: number
  createdAt: string
  meta?: Record<string, unknown>
}

export interface SessionRun {
  id: string
  sessionId: string
  status: RunStatus
  startedAt: string
  endedAt?: string
  error?: string
  prompt: string
}

export interface SessionDetail {
  session: SessionSummary
  messages: SessionMessage[]
  runs: SessionRun[]
}

export interface SessionPage {
  sessions: SessionSummary[]
  nextCursor?: string
  total: number
}

export interface SessionEvent {
  sessionId: string
  runId: string
  seq: number
  kind: SessionEventKind
  at: string
  payload: Record<string, unknown>
}

export interface SendReport {
  runId: string
  status: RunStatus
  error?: string
  message?: SessionMessage
  eventCount: number
}

/** Marker error so callers can tell "no host" from a real failure. */
export class SessionUnavailable extends Error {
  constructor() {
    super('the session ledger is only available inside the desktop host')
    this.name = 'SessionUnavailable'
  }
}

/**
 * The host reports failures as `{code, message}` JSON; anything else is passed
 * through unchanged so a transport error is never swallowed.
 */
export function describeSessionError(error: unknown): string {
  if (typeof error === 'string') {
    const start = error.indexOf('{')
    if (start >= 0) {
      try {
        const parsed = JSON.parse(error.slice(start)) as { code?: string; message?: string }
        if (parsed.message) return parsed.code ? `${parsed.code}: ${parsed.message}` : parsed.message
      } catch {
        // Not the host shape; keep the raw text.
      }
    }
    return error
  }
  if (error instanceof Error) return error.message
  return String(error)
}

function hostCall<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  if (!isTauriHost()) return Promise.reject(new SessionUnavailable())
  return invokeHost<T>(command, args)
}

export function listSessions(input?: {
  runtimeId?: string
  projectPath?: string
  includeArchived?: boolean
  search?: string
  limit?: number
  cursor?: string
}): Promise<SessionPage> {
  // One `query` argument, mirroring the host's `SessionQuery`: the same filters
  // travel together, and an absent field means "no filter" on both sides.
  return hostCall<SessionPage>('session_list', { query: input ?? {} })
}

/** The directories the history panel groups sessions by. */
export function listProjects(): Promise<ProjectSummary[]> {
  return hostCall<ProjectSummary[]>('session_projects')
}

/** Branch a session at one message; the branch is a new session. */
export function forkSession(sessionId: string, uptoSeq?: number): Promise<SessionSummary> {
  return hostCall<SessionSummary>('session_fork', { sessionId, uptoSeq })
}

/** Fold all but the last `keepLast` messages into one summary row. */
export function compactSession(sessionId: string, keepLast: number): Promise<CompactReport> {
  return hostCall<CompactReport>('session_compact', { sessionId, keepLast })
}

export function loadSession(sessionId: string): Promise<SessionDetail> {
  return hostCall<SessionDetail>('session_detail', { sessionId })
}

/** Incremental poll of the ledger; `afterSeq: 0` returns the whole stream. */
export function loadSessionEvents(sessionId: string, afterSeq = 0): Promise<SessionEvent[]> {
  return hostCall<SessionEvent[]>('session_events', { sessionId, afterSeq })
}

export function openSession(input: {
  runtimeId: string
  model: string
  cwd?: string
  title?: string
  /** When set, the host opens the session with this agent's saved persona. */
  agentId?: string
}): Promise<SessionSummary> {
  return hostCall<SessionSummary>('session_open', {
    runtimeId: input.runtimeId,
    model: input.model,
    cwd: input.cwd,
    title: input.title,
    agentId: input.agentId,
  })
}

/**
 * How far the user trusts the workspace for one turn.
 *
 * `full` reads paths outside the project without raising an approval card; the
 * other modes keep the per-path question. It travels with the send rather than
 * being stored on the session, because "I trust this folder" is a decision about
 * what to run now, not a permanent property of the conversation.
 */
export type PermissionMode = 'readonly' | 'medium' | 'high' | 'full'

/**
 * How hard the runtime should think on this turn.
 *
 * These are pi's own `--thinking` values, in pi's order, and the composer shows
 * them verbatim: a level is a runtime flag, not product prose, so translating
 * it would make the picker disagree with the command it produces.
 */
export type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export const THINKING_LEVELS: readonly ThinkingLevel[] = [
  'off',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
]

export function sendTurn(
  sessionId: string,
  text: string,
  context: ContextRef[] = [],
  permission: PermissionMode = 'medium',
  thinking: ThinkingLevel | undefined = undefined,
): Promise<SendReport> {
  return hostCall<SendReport>('session_send', { sessionId, text, context, permission, thinking })
}

/** Answer one approval card; the waiting run continues or gives up. */
export function approveRequest(
  sessionId: string,
  approvalId: string,
  decision: ApprovalDecision,
): Promise<void> {
  return hostCall<void>('session_approve', { sessionId, approvalId, decision })
}

/** Write one conversation to the application's export folder. */
export function exportConversation(
  sessionId: string,
  format: 'html' | 'markdown' | 'json',
  options: { includeTools?: boolean; overwrite?: boolean } = {},
): Promise<ExportReport> {
  return hostCall<ExportReport>('session_export', {
    sessionId,
    format,
    includeTools: options.includeTools ?? false,
    overwrite: options.overwrite ?? false,
  })
}

/** The right rail's four panes, derived from the session's own ledger. */
export function loadRail(sessionId: string): Promise<RailView> {
  return hostCall<RailView>('session_rail', { sessionId })
}

/** Files and directories the composer may offer, confined to the project. */
export function listContext(
  sessionId: string,
  query = '',
  limit = 30,
): Promise<ContextEntry[]> {
  return hostCall<ContextEntry[]>('session_context_list', { sessionId, query, limit })
}

export function abortTurn(sessionId: string): Promise<boolean> {
  return hostCall<boolean>('session_abort', { sessionId })
}

/**
 * Write a message into the turn that is streaming right now.
 *
 * `false` means there was nothing live to steer, so the caller keeps the text
 * instead of losing it into a pipe nobody reads.
 */
/** The runtime's own estimate of how full this session's context is. */
export interface ContextUsage {
  tokens: number | null
  contextWindow: number
  percent: number | null
}

export function sessionContext(sessionId: string): Promise<ContextUsage | null> {
  return hostCall<ContextUsage | null>('session_context', { sessionId })
}

export function steerTurn(sessionId: string, text: string): Promise<boolean> {
  return hostCall<boolean>('session_steer', { sessionId, text })
}

export function renameSession(sessionId: string, title: string): Promise<void> {
  return hostCall<void>('session_rename', { sessionId, title })
}

export function pinSession(sessionId: string, pinned: boolean): Promise<void> {
  return hostCall<void>('session_pin', { sessionId, pinned })
}

export function archiveSession(sessionId: string, archived: boolean): Promise<void> {
  return hostCall<void>('session_archive', { sessionId, archived })
}

export function deleteSession(sessionId: string): Promise<void> {
  return hostCall<void>('session_delete', { sessionId })
}

/**
 * Text carried by a batch of events, in ledger order.
 *
 * Exported because the merge is the part worth testing: a poll that overlaps
 * with the previous one must not duplicate a token, and an event that is not a
 * delta must not contribute text.
 */
export function mergeDeltaText(events: SessionEvent[], previous: string): string {
  const added = events
    .filter((event) => event.kind === 'text_delta')
    .map((event) => (typeof event.payload.delta === 'string' ? event.payload.delta : ''))
    .join('')
  return previous + added
}

/** Highest sequence number in a batch, so the next poll resumes after it. */
export function latestSeq(events: SessionEvent[], fallback: number): number {
  return events.reduce((highest, event) => (event.seq > highest ? event.seq : highest), fallback)
}
