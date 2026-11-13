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
  loadCatalog,
  refreshCatalog,
  type CatalogModel,
} from '../catalog/client'
import { isTauriHost } from '../host/tauri'
import {
  abortTurn,
  approveRequest,
  archiveSession,
  compactSession,
  deleteSession,
  describeSessionError,
  forkSession,
  latestSeq,
  listContext,
  listProjects,
  listSessions,
  loadSession,
  loadSessionEvents,
  mergeDeltaText,
  openSession,
  pinSession,
  renameSession,
  sendTurn,
  SessionUnavailable,
  type ApprovalDecision,
  type ApprovalRequest,
  type ContextEntry,
  type ContextRef,
  type ProjectSummary,
  type SessionDetail,
  type SessionEvent,
  type SessionSummary,
} from './client'

/** How often the running turn re-reads the ledger. */
const POLL_INTERVAL_MS = 120

export type ChatStatus = 'unavailable' | 'loading' | 'ready' | 'error'

export interface ModelChoice {
  runtimeId: string
  modelId: string
  displayName: string
  verified: boolean
  isDefault: boolean
}

/** One tool call and, when it finished, its result. */
export interface LiveTool {
  id: string
  name: string
  args: string
  ok: boolean | null
  output: string
}

export interface ChatSessionView {
  status: ChatStatus
  error: string | null
  models: ModelChoice[]
  model: ModelChoice | null
  chooseModel: (choice: ModelChoice) => void
  /** A manual rescan is in flight (the picker's spinning control). */
  syncing: boolean
  /** Re-scan the machine and rebuild the picker from the result. */
  syncModels: () => Promise<void>
  sessions: SessionSummary[]
  /** Directories the history panel groups the sessions by. */
  projects: ProjectSummary[]
  current: SessionDetail | null
  /** Text of the turn in flight, merged from text deltas. */
  streamText: string
  /** Every event of the turn in flight, in ledger order. */
  turnEvents: SessionEvent[]
  /** The approval card waiting for an answer, if any. */
  pending: ApprovalRequest | null
  /** Tool calls of the turn in flight, paired with their results. */
  tools: LiveTool[]
  running: boolean
  select: (sessionId: string) => Promise<void>
  startNew: () => void
  send: (text: string, context?: ContextRef[]) => Promise<string | null>
  /** Answer the pending approval card. */
  approve: (decision: ApprovalDecision) => Promise<boolean>
  /** Files and directories the composer may reference in this project. */
  mention: (query: string) => Promise<ContextEntry[]>
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
}

function toChoices(models: CatalogModel[], keep: string[]): ModelChoice[] {
  // The catalog's row order is whatever the adapters discovered in; sorting
  // makes the default pick — and the dropdown — the same on every launch.
  return models
    .filter((model) => keep.includes(model.runtimeId))
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
 * Pair `tool_call` with the `tool_result` that follows it.
 *
 * Adapters emit them in order and without a shared key, so the pairing is
 * positional: the Nth result belongs to the Nth call that has no result yet.
 */
function pairTools(events: SessionEvent[]): LiveTool[] {
  const tools: LiveTool[] = []
  for (const event of events) {
    if (event.kind === 'tool_call') {
      const args = event.payload.args ?? event.payload.command ?? ''
      tools.push({
        id: `${event.runId}:${event.seq}`,
        name: String(event.payload.tool ?? 'tool'),
        args: typeof args === 'string' ? args : JSON.stringify(args),
        ok: null,
        output: '',
      })
      continue
    }
    if (event.kind === 'tool_result') {
      const tool = [...tools].reverse().find((entry) => entry.ok === null)
      if (!tool) continue
      tool.ok = event.payload.ok === true
      const output = event.payload.output ?? event.payload.text ?? ''
      tool.output = typeof output === 'string' ? output : JSON.stringify(output)
    }
  }
  return tools
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms)
  })
}

export function useChatSession({ enabled = true }: { enabled?: boolean } = {}): ChatSessionView {
  const available = enabled && isTauriHost()
  const [status, setStatus] = useState<ChatStatus>(() => (available ? 'loading' : 'unavailable'))
  const [error, setError] = useState<string | null>(null)
  const [models, setModels] = useState<ModelChoice[]>([])
  const [model, setModel] = useState<ModelChoice | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [current, setCurrent] = useState<SessionDetail | null>(null)
  const [streamText, setStreamText] = useState('')
  const [turnEvents, setTurnEvents] = useState<SessionEvent[]>([])
  const [running, setRunning] = useState(false)
  const pollStop = useRef(false)
  const mounted = useRef(true)

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

    try {
      // The catalog says which runtimes exist and which models they can use; a
      // chat cannot offer a model the machine does not have.
      const catalog = await loadCatalog()
      const usable = catalog.runtimes
        .filter((runtime) => runtime.status === 'available')
        .map((runtime) => runtime.runtimeId)
      const choices = toChoices(catalog.models, usable)
      if (!mounted.current) return
      setModels(choices)
      setModel((existing) => existing ?? pickModel(choices))
    } catch (cause) {
      if (!mounted.current) return
      if (cause instanceof CatalogUnavailable || cause instanceof SessionUnavailable) return
      setError(describeSessionError(cause))
    }
  }, [available])

  useEffect(() => {
    void boot()
  }, [boot])

  const syncModels = useCallback(async () => {
    if (!available) return
    setSyncing(true)
    setError(null)
    try {
      // A manual sync re-scans: a model installed after the startup scan is
      // exactly the case this button exists for, and only a rescan finds it.
      const catalog = await refreshCatalog()
      const usable = catalog.runtimes
        .filter((runtime) => runtime.status === 'available')
        .map((runtime) => runtime.runtimeId)
      const choices = toChoices(catalog.models, usable)
      if (!mounted.current) return
      setModels(choices)
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

  const select = useCallback(async (sessionId: string) => {
    try {
      const detail = await loadSession(sessionId)
      if (!mounted.current) return
      setCurrent(detail)
      setStreamText('')
      setError(null)
    } catch (cause) {
      if (mounted.current) setError(describeSessionError(cause))
    }
  }, [])

  const startNew = useCallback(() => {
    setCurrent(null)
    setStreamText('')
    setError(null)
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
          const session = await openSession({ runtimeId: choice.runtimeId, model: choice.modelId })
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
      const optimistic = {
        id: `pending-${sessionId}`,
        sessionId,
        role: 'user' as const,
        text: prompt,
        seq: Number.MAX_SAFE_INTEGER,
        createdAt: new Date().toISOString(),
      }
      setCurrent((existing) =>
        existing ? { ...existing, messages: [...existing.messages, optimistic] } : existing,
      )
      setRunning(true)
      setStreamText('')
      setTurnEvents([])
      setError(null)
      pollStop.current = false

      const poll = async () => {
        let since = 0
        while (!pollStop.current) {
          try {
            const events = await loadSessionEvents(sessionId, since)
            if (events.length > 0) {
              since = latestSeq(events, since)
              if (mounted.current) {
                setStreamText((previous) => mergeDeltaText(events, previous))
                // The turn's tool cards and approval cards come from the same
                // stream; keeping the batch lets the page render them without a
                // second read and without re-deriving text.
                setTurnEvents((previous) => [...previous, ...events])
              }
            }
          } catch {
            // A failed poll is not a failed turn: the next tick retries and the
            // final reload shows whatever actually landed.
          }
          await sleep(POLL_INTERVAL_MS)
        }
      }
      const polling = poll()

      let failure: string | null = null
      try {
        const report = await sendTurn(sessionId, prompt, context)
        if (report.status === 'failed') failure = report.error ?? 'the turn failed'
      } catch (cause) {
        failure = describeSessionError(cause)
      } finally {
        pollStop.current = true
        await polling
      }

      try {
        const detail = await loadSession(sessionId)
        if (mounted.current) {
          setCurrent(detail)
          setStreamText('')
        }
      } catch {
        // Keep the optimistic view; the next refresh will retry.
      }
      await reloadSessions().catch(() => undefined)
      if (mounted.current) setRunning(false)
      if (failure) setError(failure)
      return failure
    },
    [current, model, reloadSessions],
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

  const abort = useCallback(async () => {
    const sessionId = current?.session.id
    if (!sessionId) return false
    try {
      return await abortTurn(sessionId)
    } catch (cause) {
      if (mounted.current) setError(describeSessionError(cause))
      return false
    }
  }, [current])

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
  const tools = pairTools(turnEvents)

  return {
    status,
    error,
    models,
    model,
    chooseModel: setModel,
    syncing,
    syncModels,
    sessions,
    projects,
    current,
    streamText,
    turnEvents,
    pending,
    tools,
    running,
    select,
    startNew,
    send,
    approve,
    mention,
    abort,
    refresh,
    rename,
    setPinned,
    setArchived,
    remove,
    fork,
    compact,
  }
}
