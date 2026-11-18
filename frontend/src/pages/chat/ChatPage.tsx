import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from 'react'
import { Link } from 'react-router-dom'
import {
  buildTurnParts,
  useChatSession,
  withStoredAnswer,
  type AgentChoice,
  type LiveSubAgent,
  type ModelChoice,
  type TurnFailure,
  type TurnPart,
} from '../../chat/useChatSession'
import { turnUsage, type TurnUsage } from '../../chat/usage'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import SyncButton from '../../shell/SyncButton'
import { ROUTES_BY_ID } from '../../shell/routes'
import { PREF_SHOW_TIMELINE, useBoolPreference, useSendShortcut } from '../../preferences/flags'
import { useCatalog } from '../../catalog/useCatalog'
import {
  installPiSubagent,
  piSubagentStatus,
  type CatalogSkill,
  type PiSubagentStatus,
} from '../../catalog/client'
import ChatDialogs, { type ChatDialogKind, type ExportRequest } from './ChatDialogs'
import {
  describeSessionError,
  loadSessionEvents,
  THINKING_LEVELS,
  type ApprovalDecision,
  type ApprovalRequest,
  type ContextEntry,
  type ContextRef,
  type PermissionMode,
  type SessionEvent,
  type ThinkingLevel,
} from '../../chat/client'
import ChatHistoryPanel from './ChatHistoryPanel'
import { useWorkspace } from '../../workspace/WorkspaceProvider'
import { pickWorkspace, pickWorkspaceFile } from '../../workspace/client'
import ChatRail, { type RailTab } from './ChatRail'
import { sampleChat, useChatSample, type ChatProse, type ChatTool, type SampleMessage } from './sampleData'
import { useChatHistory } from '../../chat/ChatHistoryProvider'
import { Markdown } from './Markdown'
import {
  chipEditPosition,
  InlineDraft,
  insertSkill,
  skillAtCaret,
  skillRanges,
  withoutRange,
} from './composerText'
import './chat.css'

type Attachment = {
  id: string
  name: string
  kind: 'file' | 'dir' | 'skill'
  /** Absolute path the host resolves; absent for sample rows. */
  path?: string
  size?: string
}

/** One row of the `@` picker: a project file or an installed skill. */
type MentionHit = {
  id: string
  name: string
  kind: Attachment['kind']
  path: string
  detail: string
  bytes: number
}

/**
 * What opened the context picker: `@` lists project files and skills, the "+"
 * drawer's Skill entry narrows `@` to skills only, and `/` is a skill palette
 * that never mixes in files.
 */
type MentionMode = 'all' | 'skill' | 'slash'

/** What the composer may do when a runtime is actually connected. */
interface LiveComposer {
  models: ModelChoice[]
  model: ModelChoice | null
  /** Saved runtime+model bindings; the first entry is the derived default. */
  agents: AgentChoice[]
  onModel: (choice: ModelChoice) => void
  onSend: (text: string, context: ContextRef[]) => void
  onStop: () => void
  /** Re-scan the machine for models. */
  onSync: () => void
  syncing: boolean
  running: boolean
}

/** How many trailing messages a compaction leaves verbatim. */
const COMPACT_KEEP_LAST = 6

/** Persisted trust level for the composer's permission picker. */
const PERMISSION_KEY = 'anvil.chat.permission'

/** Persisted thinking level for the composer's picker. */
const THINKING_KEY = 'anvil.chat.thinking'

/**
 * How each level reads in the picker.
 *
 * Deliberately not translated: these are pi's own `--thinking` values, and a
 * picker that says 「思考 · 高」 while the command says `--thinking high` makes
 * the one thing the control exists for — knowing what the runtime was told —
 * harder to check.
 */
const THINKING_LABELS: Record<ThinkingLevel, string> = {
  off: 'Off',
  minimal: 'Minimal',
  low: 'Low',
  medium: 'Medium',
  high: 'High',
  xhigh: 'Extra high',
  max: 'Max',
}

/** The level stored last time, if it is still one pi accepts. */
function storedThinking(): ThinkingLevel {
  try {
    const saved = window.localStorage.getItem(THINKING_KEY)
    if (saved && (THINKING_LEVELS as readonly string[]).includes(saved)) {
      return saved as ThinkingLevel
    }
  } catch {
    // A locked-down webview must not break the page over a preference.
  }
  return 'medium'
}

/** `runtime::model` is unique even when two runtimes publish the same id. */
function modelKey(choice: ModelChoice): string {
  return `${choice.runtimeId}::${choice.modelId}`
}

function basename(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean)
  return parts.length > 0 ? parts[parts.length - 1] : ''
}

function useNarrow() {
  const [narrow, setNarrow] = useState(() => {
    if (typeof window.matchMedia !== 'function') return false
    return window.matchMedia('(max-width: 900px)').matches
  })
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const query = window.matchMedia('(max-width: 900px)')
    const update = () => setNarrow(query.matches)
    update()
    query.addEventListener?.('change', update)
    return () => query.removeEventListener?.('change', update)
  }, [])
  return narrow
}

/** Split a ledger by run, keeping ledger order inside each run. */
function groupByRun(events: SessionEvent[]): Record<string, SessionEvent[]> {
  const grouped: Record<string, SessionEvent[]> = {}
  for (const event of events) (grouped[event.runId] ??= []).push(event)
  return grouped
}

/** Slice a ledger into per-run parts, keeping ledger order inside each run. */
function partsByRun(events: SessionEvent[]): Record<string, TurnPart[]> {
  // One pass to group, one short build per run: the ledger interleaves runs,
  // and re-scanning all of it for every run turned a settled session with many
  // turns into an O(runs x events) walk on each reload.
  const result: Record<string, TurnPart[]> = {}
  for (const [run, runEvents] of Object.entries(groupByRun(events))) {
    result[run] = buildTurnParts(runEvents, run)
  }
  return result
}

/**
 * Reasoning, tool calls and their results belong to the reply they produced.
 *
 * The completed ledger is read once when a turn settles, so every earlier
 * answer keeps its own ordered blocks and its own token count; while a turn
 * streams, the same shape is built from the live poll. Building both from
 * `buildTurnParts` is what keeps the transcript from re-arranging itself when
 * the answer lands, and it is why a second reasoning burst after a tool call
 * shows up as its own block instead of being folded into the first one.
 */
function useRunDetail(
  sessionId: string | undefined,
  running: boolean,
  turnEvents: SessionEvent[],
): { parts: Record<string, TurnPart[]>; usage: Record<string, TurnUsage> } {
  const [ledger, setLedger] = useState<SessionEvent[]>([])
  useEffect(() => {
    // Read the ledger even while a turn is running. Skipping it meant that
    // coming back to a session mid-turn showed only the turn in flight: the
    // earlier answers lost the reasoning and tool cards they had while the page
    // was open, because nothing was loaded until the run ended.
    if (!sessionId) {
      setLedger([])
      return
    }
    let cancelled = false
    void loadSessionEvents(sessionId, 0)
      .then((events) => {
        if (!cancelled) setLedger(events)
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [sessionId, running])

  const stored = useMemo(() => partsByRun(ledger), [ledger])
  const live = useMemo(() => partsByRun(turnEvents), [turnEvents])
  const parts = useMemo(() => {
    const merged: Record<string, TurnPart[]> = { ...stored }
    for (const [run, runParts] of Object.entries(live)) {
      // A live prefix must never replace a fuller ledger snapshot. Ties go to
      // the live copy, which carries the tool results the reload may not have
      // stored yet.
      if (runParts.length >= (merged[run]?.length ?? 0)) merged[run] = runParts
    }
    return merged
  }, [stored, live])

  // Per-turn tokens. The ledger is authoritative once the turn is written; the
  // run in flight is summed from the events polled so far, so the number grows
  // while the runtime works instead of appearing only at the end.
  const usage = useMemo(() => {
    const result: Record<string, TurnUsage> = {}
    for (const [run, runEvents] of Object.entries(groupByRun(ledger))) {
      const total = turnUsage(runEvents)
      if (total) result[run] = total
    }
    // The live poll is fresher than the ledger read for the run in flight.
    for (const [run, runEvents] of Object.entries(groupByRun(turnEvents))) {
      const total = turnUsage(runEvents)
      if (total) result[run] = total
    }
    return result
  }, [ledger, turnEvents])

  return { parts, usage }
}

/**
 * Open while reasoning is still streaming (and kept at its tail), collapsed
 * once the answer has landed. An explicit click always wins over the default.
 */
export function ThinkingDisclosure({ text, running = false }: { text: string; running?: boolean }) {
  const { t } = useI18n()
  const [override, setOverride] = useState<boolean | null>(null)
  const bodyRef = useRef<HTMLDivElement>(null)
  const open = override ?? running
  useEffect(() => {
    if (running && open && bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [text, running, open])
  if (!text) return null
  return (
    <details
      className={`chat-live-thinking${running ? ' is-live' : ''}`}
      data-testid="chat-live-thinking"
      data-open={open ? 'true' : 'false'}
      open={open}
    >
      <summary
        onClick={(event) => {
          // Drive `open` from React only: a native toggle would fight the
          // streaming state on the next render.
          event.preventDefault()
          setOverride(!(override ?? running))
        }}
      >
        <Icon name="sparkle" size={11} />
        <span>
          {running
            ? t('chat.live.thinkingLive', { count: text.length })
            : t('chat.live.thinking', { count: text.length })}
        </span>
        {running ? <i className="chat-live-dot" aria-hidden="true" /> : null}
      </summary>
      <div className="chat-live-thinking-body" ref={bodyRef}>
        {text}
      </div>
    </details>
  )
}

/**
 * What the runtime did, in the order it did it.
 *
 * One disclosure per reasoning burst, a card per tool call — so a run that
 * reasons, calls a tool and reasons again reads as exactly that, instead of all
 * its thinking being poured into a single box above the answer.
 */
/** Token counts are read by people: group them like every other number here. */
const tokenFormat = new Intl.NumberFormat('en-US')

/**
 * One turn's tokens, as the runtime counted them.
 *
 * `input` includes cached prompt reads (each model call in the turn re-sends the
 * conversation), so the cached part is called out separately instead of being
 * hidden inside a number that looks too large for one question.
 */
function UsageLine({ usage }: { usage: TurnUsage }) {
  const { t } = useI18n()
  const input = tokenFormat.format(usage.input)
  const output = tokenFormat.format(usage.output)
  return (
    <span data-testid="chat-usage">
      {usage.cached > 0
        ? t('chat.usage.cached', { input, output, cached: tokenFormat.format(usage.cached) })
        : t('chat.usage.line', { input, output })}
    </span>
  )
}

function TurnPartsList({
  parts,
  live = false,
  sources = 0,
  onCite,
}: {
  parts: TurnPart[]
  live?: boolean
  sources?: number
  onCite?: () => void
}) {
  if (parts.length === 0) return null
  const last = parts[parts.length - 1]
  return (
    <div className="chat-parts" data-testid="chat-turn-parts">
      {parts.map((part) => {
        if (part.kind === 'thinking') {
          // Only the tail of the reasoning is still being written; earlier
          // bursts collapse as soon as a tool call or the answer follows them.
          return <ThinkingDisclosure key={part.key} text={part.text} running={live && part === last} />
        }
        if (part.kind === 'tool') {
          return <LiveToolCard key={part.key} tool={part.tool} />
        }
        if (part.kind === 'agent') {
          return <SubAgentCard key={part.key} agent={part.agent} sources={sources} onCite={onCite} />
        }
        // The answer is a part like any other: when the runtime reasons or runs
        // a tool again, that text stays where it was written instead of being
        // hoisted into one block at the end of the turn.
        return (
          <div className="chat-prose chat-live-text chat-markdown" key={part.key} data-testid="chat-live-text">
            <Markdown text={part.text} sources={sources} onCite={onCite ?? (() => undefined)} />
            {live && part === last ? <span className="chat-caret" aria-hidden="true" /> : null}
          </div>
        )
      })}
    </div>
  )
}

export default function ChatPage() {
  const { t, language } = useI18n()
  const sample = useChatSample()
  // Off by default: a plain question/answer turn has no execution steps worth
  // a card. Settings → General can turn it back on for tool-heavy runs.
  const [showTimeline] = useBoolPreference(PREF_SHOW_TIMELINE, false)
  // The agent the composer picked. Declared before the hook so the hook can
  // pass it to `session_open` and the host can apply the saved persona.
  const [pickedAgent, setPickedAgent] = useState(() =>
    new URLSearchParams(window.location.search).get('agent') ?? '',
  )
  // The directory the next conversation runs in. A new session is created on
  // the first send, so the workspace is read then (through a ref) rather than
  // being frozen when the page mounted.
  const workspace = useWorkspace()
  // Trust level for the next turn. `full` means "I trust this workspace": a
  // reference outside it is read without a card. Remembered across reloads so
  // the answer to "do you trust this folder" is not asked again every launch.
  const [permission, setPermission] = useState<PermissionMode>(() => {
    try {
      const saved = window.localStorage.getItem(PERMISSION_KEY)
      if (saved === 'readonly' || saved === 'medium' || saved === 'high' || saved === 'full') {
        return saved
      }
    } catch {
      // A locked-down webview must not break the page over a preference.
    }
    return 'medium'
  })
  useEffect(() => {
    try {
      window.localStorage.setItem(PERMISSION_KEY, permission)
    } catch {
      // Same as above: best effort only.
    }
  }, [permission])
  // How hard the runtime should think. Travels with each turn rather than being
  // stored on the session: "think harder about this one" is a decision about the
  // question, and the picker must not need a new conversation to take effect.
  const [thinking, setThinking] = useState<ThinkingLevel>(storedThinking)
  useEffect(() => {
    try {
      window.localStorage.setItem(THINKING_KEY, thinking)
    } catch {
      // Best effort only, same as the permission picker.
    }
  }, [thinking])
  const live = useChatSession({
    enabled: !sample,
    agentId: pickedAgent || undefined,
    cwd: workspace.path ?? '',
    permission,
    thinking,
  })
  // The catalog is already loaded for the composer's model picker; its skill
  // rows are the same ones the host resolves when a turn is sent.
  const catalog = useCatalog(!sample)
  const history = useChatHistory()
  const narrow = useNarrow()
  // The rail starts closed on purpose: it is reference material for the turn,
  // not part of the conversation, and an always-open panel took a third of the
  // window before the reader asked for it. The header toggle is the only way in.
  const [railOpen, setRailOpen] = useState(false)
  const [railTab, setRailTab] = useState<RailTab>('browser')
  // The turn the rail sent the reader back to; highlighted briefly so the jump
  // is visible instead of leaving them to guess which bubble moved.
  const [focusedRun, setFocusedRun] = useState<string | null>(null)
  const [dialog, setDialog] = useState<ChatDialogKind>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [notice, setNotice] = useState('')
  const [attachments, setAttachments] = useState<Attachment[]>(sample ? sampleChat.attachments : [])
  const [mentionOpen, setMentionOpen] = useState(false)
  const [attachmentOpen, setAttachmentOpen] = useState(false)
  const dialogTrigger = useRef<HTMLElement | null>(null)
  const railToggle = useRef<HTMLButtonElement>(null)
  const historyToggle = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    // Narrowing the window turns the rail into a drawer over the thread, so it
    // closes; widening never re-opens it — the toggle stays the reader's call.
    if (narrow) setRailOpen(false)
  }, [narrow])

  // One value decides which conversation is on screen: the shared history's
  // `activeId`. The sidebar sets it, the page follows; a turn that creates a
  // session publishes its id back so the list can highlight it.
  const currentId = live.current?.session.id ?? null
  // Publishing is one-way and one-shot per id. Without the memo, pressing "new
  // conversation" put the old id straight back: the sidebar cleared `activeId`,
  // but this effect still saw `currentId` (the page had not unmounted it yet)
  // and re-published it, so the select effect loaded the old session again and
  // the "new" page showed the previous conversation.
  const publishedId = useRef<string | null>(null)
  // Opening an old conversation also says which project the reader is in: the
  // next conversation should start where that one ran, not wherever the window
  // happened to point before.
  const adoptWorkspace = workspace.adopt
  const sessionProject = live.current?.session.projectPath ?? null
  useEffect(() => {
    if (sample || !sessionProject) return
    adoptWorkspace(sessionProject)
  }, [sample, sessionProject, adoptWorkspace])

  useEffect(() => {
    if (sample) return
    if (!currentId || currentId === publishedId.current) return
    publishedId.current = currentId
    if (currentId !== history.activeId) history.setActive(currentId)
  }, [sample, currentId, history])

  useEffect(() => {
    if (sample) return
    if (!history.activeId || history.activeId === currentId) return
    void live.select(history.activeId)
  }, [sample, history.activeId, currentId, live.select])

  // A running turn exists only in the ledger until the sidebar asks for the
  // list again. `session_open` writes the row immediately, but the panel kept
  // the list it read at startup, so a long run looked like it had never
  // produced a conversation. Refresh when the turn starts and keep a slow tick
  // while it runs so the row and its preview track the work.
  const refreshHistory = history.refresh
  useEffect(() => {
    if (sample || !live.running) return
    void refreshHistory()
    const timer = window.setInterval(() => void refreshHistory(), 3000)
    return () => window.clearInterval(timer)
  }, [sample, live.running, refreshHistory])

  // "New conversation" from the sidebar is a counted request, not just a null
  // id: the id is briefly null while a new session is being created, and that
  // is not a reason to drop the turn in flight.
  const seenNew = useRef(history.newSeq)
  useEffect(() => {
    if (sample || history.newSeq === seenNew.current) return
    seenNew.current = history.newSeq
    live.startNew()
  }, [sample, history.newSeq, live.startNew])

  useEffect(() => {
    if (!narrow || !railOpen) return
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      setRailOpen(false)
      window.setTimeout(() => railToggle.current?.focus(), 0)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [narrow, railOpen])

  useEffect(() => {
    if (!historyOpen) return
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      setHistoryOpen(false)
      window.setTimeout(() => historyToggle.current?.focus(), 0)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [historyOpen])

  const showNotice = (message: string) => setNotice(message)

  // Jumping from a rail row to the turn it came from. The highlight is cleared
  // by a timer, so a second jump does not leave two rows lit.
  const jumpToTurn = (runId: string) => {
    const thread = document.querySelector('[data-testid="chat-thread"]')
    const target = thread
      ? Array.from(thread.querySelectorAll<HTMLElement>('[data-run]')).find(
          (node) => node.dataset.run === runId,
        )
      : undefined
    if (!target) {
      showNotice(t('chat.rail.turnMissing'))
      return
    }
    // `scrollIntoView` is absent in the test DOM and in some webviews; the
    // highlight still lands, so a missing scroll must not abort the jump.
    target.scrollIntoView?.({ block: 'center' })
    setFocusedRun(runId)
    window.setTimeout(() => setFocusedRun((current) => (current === runId ? null : current)), 2400)
  }

  // A `[n]` marker is a real link to the sources pane: the number in the text
  // and the number in the rail are the same number, from the same read.
  const showSource = useCallback(() => {
    setRailOpen(true)
    setRailTab('sources')
  }, [])

  // One export, with the overwrite confirm the store requires: the host refuses
  // to replace a file until the person says so, and the dialog stays open with
  // the reason when it fails.
  const runExport = async ({ format, includeTools }: ExportRequest) => {
    const done = (path: string) => ({ ok: true, message: t('chat.export.saved', { path }) })
    try {
      const report = await live.exportSession(format, includeTools)
      return done(report.path)
    } catch (cause) {
      const message = describeSessionError(cause)
      if (!message.includes('already exists')) return { ok: false, message }
      const confirmed =
        typeof window.confirm === 'function' ? window.confirm(t('chat.export.overwrite')) : false
      if (!confirmed) return { ok: false, message: t('chat.export.cancelled') }
      try {
        const report = await live.exportSession(format, includeTools, true)
        return done(report.path)
      } catch (retry) {
        return { ok: false, message: describeSessionError(retry) }
      }
    }
  }

  // Live mode is the real desktop path: the ledger holds the conversation.
  // Sample mode keeps the prototype data and every control inert.
  // The composer waits for the model list: a chat with no model can only fail.
  const liveReady = !sample && live.status === 'ready' && live.models.length > 0
  const liveSession = live.current?.session ?? null
  const liveMessages = live.current?.messages ?? []
  // The runtime the next turn will run on. pi delegates only through its
  // `subagent` extension, so this is also the condition for the install notice.
  const activeRuntime = liveSession?.runtimeId ?? live.model?.runtimeId ?? ''

  // One check per runtime selection, not per render: the answer is a filesystem
  // fact and the host command reads it fresh anyway. A failure (plain browser,
  // jsdom) hides the notice instead of showing a broken one.
  const [subagent, setSubagent] = useState<PiSubagentStatus | null>(null)
  const [subagentBusy, setSubagentBusy] = useState(false)
  useEffect(() => {
    if (sample || activeRuntime !== 'pi') {
      setSubagent(null)
      return
    }
    let cancelled = false
    piSubagentStatus()
      .then((next) => {
        if (!cancelled) setSubagent(next)
      })
      .catch(() => {
        if (!cancelled) setSubagent(null)
      })
    return () => {
      cancelled = true
    }
  }, [sample, activeRuntime])

  const installSubagent = () => {
    if (subagentBusy) return
    setSubagentBusy(true)
    void installPiSubagent()
      .then((report) => {
        setSubagent(report.status)
        setNotice(t('chat.subagent.installed', { count: report.status.agents }))
      })
      .catch((cause) => {
        setNotice(t('chat.subagent.installFailed', { message: describeSessionError(cause) }))
      })
      .finally(() => setSubagentBusy(false))
  }
  const { parts: partsForRun, usage: usageByRun } = useRunDetail(
    liveSession?.id,
    live.running,
    live.turnEvents,
  )
  // A settled turn's stored message is the full answer; the deltas are usually
  // the same text. Folding them here keeps the timeline blocks in order without
  // printing the answer twice.
  const partsWithAnswer = useMemo(() => {
    const merged: Record<string, TurnPart[]> = { ...partsForRun }
    for (const message of liveMessages) {
      if (message.role !== 'assistant' || !message.runId) continue
      const parts = merged[message.runId]
      if (parts) merged[message.runId] = withStoredAnswer(parts, message.text)
    }
    return merged
  }, [partsForRun, liveMessages])
  // The turn in flight: its events are already only this turn's, so one flat
  // pass gives the thinking/tool stack the transcript should show.
  const liveParts = useMemo(() => buildTurnParts(live.turnEvents), [live.turnEvents])

  const sendLive = (text: string, context: ContextRef[]) => {
    // `send` resolves with the failure message (when there is one) instead of
    // rejecting, so a failed turn never becomes an unhandled rejection.
    void live.send(text, context).then((failure) => {
      if (failure) setNotice(t('chat.live.failed', { message: failure }))
      // The ledger gained a session and a turn: the sidebar list is stale until
      // it re-reads, and a list that lags a send looks like lost history.
      void history.refresh()
    })
  }

  const stopLive = () => {
    void live.abort().then((stopped) => {
      setNotice(stopped ? t('chat.live.stopped') : t('chat.live.nothingRunning'))
    })
  }

  const openHistorySession = (id: string) => {
    history.open(id)
    setHistoryOpen(false)
  }

  // A session is opened on one runtime+model pair; switching the picker cannot
  // retarget a conversation the runtime already holds. Start a new one, and say
  // so, instead of letting the next turn silently use the old pair.
  const chooseModel = (choice: ModelChoice) => {
    live.chooseModel(choice)
    if (live.current && !live.running) {
      live.startNew()
      history.startNew()
      setNotice(t('chat.live.modelChanged'))
    }
  }

  const renameHistorySession = (id: string, title: string) => {
    void history.rename(id, title)
  }

  const deleteHistorySession = (id: string) => {
    void history.remove(id)
  }

  const forkHistorySession = (id: string) => {
    void history.fork(id).then((branch) => {
      if (branch) setNotice(t('chat.live.forked'))
    })
  }

  const compactHistorySession = (id: string) => {
    void history.compact(id, COMPACT_KEEP_LAST).then(() => setNotice(t('chat.live.compacted')))
  }

  const openDialog = (kind: ChatDialogKind, trigger: HTMLElement) => {
    dialogTrigger.current = trigger
    setDialog(kind)
  }
  const closeDialog = () => {
    setDialog(null)
    const trigger = dialogTrigger.current
    window.setTimeout(() => trigger?.focus(), 0)
  }

  const openRail = (tab: RailTab) => {
    setRailTab(tab)
    setRailOpen(true)
  }

  return (
    <section className="chat-page" data-testid="chat-page" data-sample={sample ? 'true' : 'false'}>
      <span className="sr-only" id="route-marker" data-testid="page-marker">
        {ROUTES_BY_ID.get('chat')?.marker}
      </span>
      <header className="chat-header">
        <Link className="chat-iconbtn chat-back" to="/" aria-label={t('chat.back')} title={t('chat.back')}>
          <Icon name="arrowLeft" size={16} />
        </Link>
        <div className="chat-head-main">
          <h1 className="chat-title">
            {/* A title only claims a conversation when there is one to name. */}
            {sample
              ? sampleChat.title
              : liveSession
                ? liveSession.title
                : live.status === 'ready'
                  ? t('chat.live.title')
                  : t('chat.normal.title')}
          </h1>
          <div className="chat-subline">
            {sample ? (
              <>
                <span className="chat-project-mark">
                  <Icon name="folder" size={10} />
                  {sampleChat.project}
                </span>
                <span className="sep">/</span>
                <span>{sampleChat.agent}</span>
                <span className="sep">/</span>
                <span className="mono">{sampleChat.model}</span>
                <span className="sep">/</span>
                <span className="mono">{THINKING_LABELS[sampleChat.thinkingLevel]}</span>
                <span className="sep">/</span>
                <span className="mono">{t(`chat.composer.permission.${sampleChat.permission}` as MessageKey)}</span>
              </>
            ) : liveSession ? (
              <>
                <span className="chat-project-mark">
                  <Icon name="folder" size={10} />
                  {basename(liveSession.projectPath) || t('chat.live.localProject')}
                </span>
                <span className="sep">/</span>
                <span>{liveSession.runtimeId}</span>
                <span className="sep">/</span>
                <span className="mono">{liveSession.model}</span>
                <span className="sep">/</span>
                <span className="mono">{liveSession.status}</span>
              </>
            ) : (
              <span>{live.status === 'unavailable' ? t('chat.normal.subtitle') : t('chat.live.title')}</span>
            )}
          </div>
        </div>
        <div className="chat-head-actions">
          <button
            type="button"
            className="chat-iconbtn chat-history-toggle"
            aria-label={t('chat.history.title')}
            aria-expanded={historyOpen}
            onClick={() => setHistoryOpen(true)}
            ref={historyToggle}
          >
            <Icon name="layers" size={15} />
          </button>
          {sample ? (
            <>
              <button
                type="button"
                className="chat-badge chat-thil-trigger"
                onClick={(event) => openDialog('thil', event.currentTarget)}
              >
                <Icon name="message" size={11} />
                <span>{t('chat.badge.clarification')}</span>
              </button>
              <span className="chat-badge chat-tone-warn">
                <i className="chat-dot" aria-hidden="true" />
                {t('chat.badge.pendingAuth')}
              </span>
            </>
          ) : null}
          {/* No language switch here: the UI follows the system locale and the
              choice lives in 设置 → 语言. A one-click toggle in the page header
              was easy to hit by accident and made the header look like a demo. */}
          {/* Exporting needs a real conversation: the prototype still opens the
              dialog, and the live page writes the file through the host. */}
          <button
            type="button"
            className="chat-iconbtn chat-export"
            aria-label={t('chat.export')}
            title={sample || liveSession ? t('chat.export') : t('chat.action.unavailable')}
            disabled={!sample && !liveSession}
            onClick={(event) => openDialog('export', event.currentTarget)}
          >
            <Icon name="download" size={15} />
            <span className="chat-export-label">{t('chat.export')}</span>
          </button>
          <button
            type="button"
            className={`chat-iconbtn${railOpen ? ' is-on' : ''}`}
            data-testid="chat-rail-toggle"
            aria-label={t('chat.rail.toggle')}
            aria-expanded={railOpen}
            aria-controls="chat-rail"
            onClick={() => setRailOpen((open) => !open)}
            ref={railToggle}
          >
            <Icon name="panel" size={16} />
          </button>
          <button
            type="button"
            className="chat-iconbtn chat-more"
            aria-label={t('chat.more')}
            title={sample ? t('chat.thread.title') : t('chat.action.unavailable')}
            disabled={!sample}
            onClick={(event) => openDialog('thread', event.currentTarget)}
          >
            <Icon name="more" size={16} />
          </button>
        </div>
      </header>

      {sample ? (
        <div className="chat-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('chat.sampleBanner')}
        </div>
      ) : null}
      {notice ? (
        <div className="chat-notice" data-testid="chat-notice" role="status">
          {notice}
        </div>
      ) : null}

      <div className={`chat-layout${railOpen ? ' rail-open' : ''}`} data-testid="chat-layout">
        <div className="chat-thread-col">
          <div className="chat-thread" data-testid="chat-thread">
            {sample ? (
              <div className="chat-thread-inner" data-testid="chat-message-list" aria-label={sampleChat.title}>
                {sampleChat.messages.map((message) => (
                  <MessageView
                    key={message.id}
                    message={message}
                    onOpenRail={openRail}
                    onDialog={(kind, trigger) => openDialog(kind, trigger)}
                    onNotice={showNotice}
                  />
                ))}
              </div>
            ) : live.status === 'loading' ? (
              <div className="chat-empty" data-testid="chat-loading" role="status">
                <span className="chat-empty-icon">
                  <Icon name="refresh" size={22} />
                </span>
                <h2>{t('chat.live.loading')}</h2>
              </div>
            ) : live.status === 'error' ? (
              <div className="chat-empty" data-testid="chat-error" role="alert">
                <span className="chat-empty-icon">
                  <Icon name="alert" size={22} />
                </span>
                <h2>{t('chat.live.emptyTitle')}</h2>
                <p>{t('chat.live.error', { message: live.error ?? '' })}</p>
              </div>
            ) : live.status === 'ready' && live.models.length === 0 ? (
              // A ready page with no model is not "connecting": the scan has
              // finished and nothing is enabled. Saying otherwise leaves the
              // reader watching a spinner that will never end.
              <div className="chat-empty" data-testid="chat-connecting" role="status">
                <span className="chat-empty-icon">
                  <Icon name="brain" size={22} />
                </span>
                <h2>{t('chat.live.noModel')}</h2>
                {/* Straight to the section that fixes it: landing on the
                    general page left the user hunting for the model list. */}
                <Link to="/settings#models" className="chat-btn chat-btn-sm">
                  {t('chat.live.openSettings')}
                </Link>
              </div>
            ) : live.status === 'unavailable' ? (
              <div className="chat-empty" data-testid="chat-empty" role="status">
                <span className="chat-empty-icon">
                  <Icon name="message" size={22} />
                </span>
                <h2>{t('chat.empty.title')}</h2>
                <p>{t('chat.empty.body')}</p>
              </div>
            ) : liveSession ? (
              <div className="chat-thread-inner" data-testid="chat-message-list" aria-label={liveSession.title}>
                {liveMessages.map((message) =>
                  message.role === 'user' ? (
                    <article
                      className={`chat-msg chat-msg-user${focusedRun === message.runId ? ' is-focused' : ''}`}
                      data-testid="chat-message-user"
                      data-run={message.runId}
                      key={message.id}
                    >
                      <ContextChips meta={message.meta} />
                      <div className="chat-bubble">{message.text}</div>
                    </article>
                  ) : message.meta && (message.meta as { compacted?: boolean }).compacted ? (
                    <section
                      className="chat-state-card"
                      data-testid="chat-message-compacted"
                      role="status"
                      aria-label={t('chat.live.compacted')}
                      key={message.id}
                    >
                      <span className="chat-state-icon">
                        <Icon name="archive" size={14} />
                      </span>
                      <div className="chat-state-copy">
                        <b>{t('chat.live.compacted')}</b>
                        <span>{message.text}</span>
                      </div>
                    </section>
                  ) : (
                    <article
                      className={`chat-msg${focusedRun === message.runId ? ' is-focused' : ''}`}
                      data-testid="chat-message-agent"
                      data-run={message.runId}
                      key={message.id}
                    >
                      <div className="chat-agent-head">
                        <span className="chat-agent-badge">{liveSession.runtimeId.slice(0, 1)}</span>
                        <span className="chat-agent-name">{liveSession.runtimeId}</span>
                        <span className="chat-agent-model">{liveSession.model}</span>
                      </div>
                      {message.runId && partsWithAnswer[message.runId]?.length ? (
                        <TurnPartsList
                          parts={partsWithAnswer[message.runId]}
                          sources={live.rail?.sources.length ?? 0}
                          onCite={showSource}
                        />
                      ) : null}
                      {/* The parts already carry the answer when the ledger has
                          its text deltas; printing the stored message as well
                          would show the same words twice. The message is the
                          fallback for a turn whose deltas never landed. */}
                      {message.runId && partsWithAnswer[message.runId]?.some((part) => part.kind === 'text') ? null : (
                        <div className="chat-prose chat-live-text chat-markdown">
                          <Markdown
                            text={message.text}
                            sources={live.rail?.sources.length ?? 0}
                            onCite={showSource}
                          />
                        </div>
                      )}
                      {/* The count stays under the answer after the turn is
                          written; it used to exist only in the streaming bubble
                          and vanished the moment the reply landed. */}
                      {message.runId && usageByRun[message.runId] ? (
                        <div className="chat-msg-meta">
                          <UsageLine usage={usageByRun[message.runId]} />
                        </div>
                      ) : null}
                    </article>
                  ),
                )}
                <LiveTurnTimeline
                  showTimeline={showTimeline}
                  events={live.turnEvents}
                  parts={liveParts}
                  pending={live.pending}
                  running={live.running}
                  startedAt={live.turnStartedAt}
                  failure={live.turnFailure}
                  runtimeId={liveSession.runtimeId}
                  model={liveSession.model}
                  sources={live.rail?.sources.length ?? 0}
                  onCite={showSource}
                  onApprove={(decision) => void live.approve(decision)}
                  onRetry={() => void live.retry()}
                />
              </div>
            ) : (
              <div className="chat-empty" data-testid="chat-empty" role="status">
                <span className="chat-empty-icon">
                  <Icon name="message" size={22} />
                </span>
                <h2>{t('chat.live.emptyTitle')}</h2>
                <p>{t('chat.live.emptyBody')}</p>
              </div>
            )}
          </div>

          {!sample && activeRuntime === 'pi' && subagent && !subagent.installed ? (
            <div
              className="chat-subagent-notice"
              data-testid="chat-subagent-notice"
              role="status"
            >
              <span className="chat-subagent-notice-icon">
                <Icon name="puzzle" size={14} />
              </span>
              <div className="chat-subagent-notice-copy">
                <b>{t('chat.subagent.missing')}</b>
                <small title={subagent.detail ?? subagent.extensionDir}>
                  {subagent.canInstall
                    ? t('chat.subagent.detail', { dir: subagent.extensionDir })
                    : t('chat.subagent.noSource')}
                </small>
              </div>
              <button
                type="button"
                className="chat-btn chat-btn-sm chat-subagent-notice-btn"
                data-testid="chat-subagent-install"
                disabled={!subagent.canInstall || subagentBusy}
                title={subagent.canInstall ? subagent.source : subagent.detail}
                onClick={installSubagent}
              >
                <Icon name="download" size={12} />
                {subagentBusy ? t('chat.subagent.installing') : t('chat.subagent.install')}
              </button>
            </div>
          ) : null}

          <Composer
            sample={sample}
            live={
              liveReady
                ? {
                    models: live.models,
                    model: live.model,
                    agents: live.agents,
                    onModel: chooseModel,
                    onSend: sendLive,
                    onStop: stopLive,
                    onSync: () => void live.syncModels(),
                    syncing: live.syncing,
                    running: live.running,
                  }
                : null
            }
            language={language}
            pickedAgent={pickedAgent}
            setPickedAgent={setPickedAgent}
            attachments={attachments}
            setAttachments={setAttachments}
            mentionOpen={mentionOpen}
            setMentionOpen={setMentionOpen}
            attachmentOpen={attachmentOpen}
            setAttachmentOpen={setAttachmentOpen}
            permission={permission}
            setPermission={setPermission}
            thinking={thinking}
            setThinking={setThinking}
            onNotice={showNotice}
            onProject={(trigger) => openDialog('project', trigger)}
            workspaceLabel={
              workspace.isDefault ? t('workspace.defaultName') : workspace.name
            }
            onMention={liveReady ? live.mention : undefined}
            skills={catalog.snapshot?.skills ?? []}
            runtimeId={live.current?.session.runtimeId ?? live.model?.runtimeId ?? ''}
          />
        </div>

        {railOpen ? (
          <button
            type="button"
            className="chat-rail-scrim"
            aria-label={t('chat.rail.closeScrim')}
            tabIndex={-1}
            onClick={() => {
              setRailOpen(false)
              railToggle.current?.focus()
            }}
          />
        ) : null}
        <ChatRail sample={sample} live={{ view: live.rail, running: live.running, onTurn: jumpToTurn }} open={railOpen} tab={railTab} onTab={setRailTab} onClose={() => {
          setRailOpen(false)
          railToggle.current?.focus()
        }} onNotice={showNotice} />
      </div>

      {historyOpen ? (
        <div className="chat-drawer-backdrop" onMouseDown={(event) => event.target === event.currentTarget && setHistoryOpen(false)}>
          <div className="chat-history-drawer" role="dialog" aria-modal="true" aria-label={t('chat.history.title')}>
            <ChatHistoryPanel
              sample={sample}
              live={
                sample
                  ? undefined
                  : {
                      sessions: history.sessions,
                      projects: history.projects,
                      activeId: history.activeId,
                      onOpen: openHistorySession,
                      onNew: () => {
                        history.startNew()
                        live.startNew()
                        setHistoryOpen(false)
                      },
                      onRename: renameHistorySession,
                      onPin: (id) => {
                        const session = history.sessions.find((item) => item.id === id)
                        void history.setPinned(id, !(session?.pinned ?? false))
                      },
                      onArchive: (id) => void history.setArchived(id, true),
                      onDelete: deleteHistorySession,
                      onFork: forkHistorySession,
                      onCompact: compactHistorySession,
                    }
              }
              variant="drawer"
              onClose={() => {
                setHistoryOpen(false)
                historyToggle.current?.focus()
              }}
            />
          </div>
        </div>
      ) : null}

      <ChatDialogs
        kind={dialog}
        sample={sample}
        onClose={closeDialog}
        onNotice={showNotice}
        onExport={liveSession ? runExport : undefined}
      />
    </section>
  )
}

function MessageView({
  message,
  onOpenRail,
  onDialog,
  onNotice,
}: {
  message: SampleMessage
  onOpenRail: (tab: RailTab) => void
  onDialog: (kind: ChatDialogKind, trigger: HTMLElement) => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  if (message.kind === 'user') {
    return (
      <article className="chat-msg chat-msg-user" data-testid="chat-message-user">
        <div className="chat-bubble">{message.text}</div>
        <div className="chat-msg-meta">
          <span>{message.author}</span>
          <span>·</span>
          <span>{message.time}</span>
        </div>
      </article>
    )
  }
  if (message.kind === 'clarification') {
    return (
      <article className="chat-msg" data-testid="chat-message-clarification">
        <section className="chat-inline-clarification" role="status" aria-label={t('chat.clarification.kicker')}>
          <div className="chat-inline-mark">
            <Icon name="message" size={15} />
          </div>
          <div className="chat-inline-main">
            <div className="chat-inline-kicker">{t('chat.clarification.kicker')}</div>
            <h3>{message.title}</h3>
            <p>{message.body}</p>
            <div className="chat-inline-note">
              <Icon name="shield" size={11} />
              {message.note}
            </div>
          </div>
          <button
            type="button"
            className="chat-btn chat-btn-sm"
            onClick={(event) => onDialog('thil', event.currentTarget)}
          >
            {t('chat.clarification.view')}
          </button>
        </section>
      </article>
    )
  }
  if (message.kind === 'state') {
    const actionKeys: Record<NonNullable<Extract<SampleMessage, { kind: 'state' }>['actions']>[number], MessageKey> = {
      detail: 'chat.state.detail',
      retry: 'chat.state.retry',
      login: 'chat.state.login',
      compress: 'chat.state.compress',
      branch: 'chat.state.branch',
      continue: 'chat.state.continue',
    }
    return (
      <section
        className={`chat-state-card chat-tone-${message.tone}`}
        role={message.role}
        aria-label={message.title}
        data-testid="chat-state-card"
      >
        <span className="chat-state-icon">
          <Icon name={message.tone === 'danger' ? 'alert' : message.tone === 'warn' ? 'pause' : 'refresh'} size={14} />
        </span>
        <div className="chat-state-copy">
          <b>{message.title}</b>
          <span>{message.body}</span>
        </div>
        {message.actions?.map((action) => (
          <button key={action} type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.sample.notice'))}>
            {t(actionKeys[action])}
          </button>
        ))}
      </section>
    )
  }

  return (
    <article className="chat-msg" data-testid="chat-message-agent">
      <div className="chat-agent-head">
        <span className="chat-agent-badge">{message.agent.slice(0, 1)}</span>
        <span className="chat-agent-name">{message.agent}</span>
        <span className="chat-agent-model">{message.model}</span>
        {message.badge === 'generating' ? (
          <span className="chat-badge">
            <i className="chat-dot" aria-hidden="true" />
            {t('chat.status.generating')}
          </span>
        ) : null}
        {message.badge === 'failed' ? <span className="chat-badge chat-tone-danger">{t('chat.tool.failed')}</span> : null}
      </div>

      {message.thinking ? (
        <ThinkingBlock title={message.thinking.title} steps={message.thinking.steps} initialOpen={message.thinking.open} />
      ) : null}

      {message.tools?.map((tool) => <ToolBlock key={tool.name} tool={tool} />)}

      {message.prose ? <Prose prose={message.prose} /> : null}

      {message.meta ? (
        <div className="chat-msg-meta">
          <span className="mono">{message.meta.tokens}</span>
          <span>·</span>
          <span>{message.meta.time}</span>
          <span className="chat-msg-actions">
            <button type="button" className="chat-iconbtn" aria-label={t('chat.message.copy')} onClick={() => onNotice(t('chat.notice.copy'))}>
              <Icon name="copy" size={13} />
            </button>
            <button
              type="button"
              className="chat-iconbtn"
              aria-label={t('chat.message.regenerate')}
              onClick={() => onNotice(t('chat.sample.notice'))}
            >
              <Icon name="refresh" size={13} />
            </button>
          </span>
        </div>
      ) : null}

      {message.artifacts ? (
        <button type="button" className="chat-turn-artifacts" data-testid="chat-turn-artifacts" onClick={() => onOpenRail('artifacts')}>
          <span className="chat-turn-artifacts-icon">
            <Icon name="file" size={13} />
          </span>
          <span>
            <b>{t('chat.message.artifacts', { count: message.artifacts.count })}</b>
            <small>{message.artifacts.files}</small>
          </span>
          <span className="chat-turn-artifacts-open">
            {t('chat.message.openArtifacts')} <Icon name="chevron" size={11} />
          </span>
        </button>
      ) : null}

      {message.confirm ? <ConfirmCard onNotice={onNotice} /> : null}

      {message.stream ? (
        <div className="chat-stream-status" role="status">
          <span className="chat-live-dot" aria-hidden="true" />
          <span>{t('chat.status.paused', { tokens: '412' })}</span>
          <span className="chat-caret" aria-hidden="true" />
        </div>
      ) : null}
    </article>
  )
}

function ThinkingBlock({ title, steps, initialOpen = false }: { title: string; steps: string[]; initialOpen?: boolean }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(initialOpen)
  return (
    <section className={`chat-thinking${open ? ' is-open' : ''}`} data-testid="chat-thinking" aria-expanded={open}>
      <button type="button" className="chat-thinking-head" aria-label={t('chat.thinking.toggle')} onClick={() => setOpen((value) => !value)}>
        <Icon name="brain" size={14} />
        <span>{title}</span>
        <span className="chat-thinking-chevron" aria-hidden="true">
          <Icon name="chevron" size={12} />
        </span>
      </button>
      {open ? (
        <div className="chat-thinking-body">
          {steps.map((step, index) => (
            <div className="chat-thinking-step" key={step}>
              <span className="chat-thinking-num">{index + 1}</span>
              <span>{step}</span>
            </div>
          ))}
        </div>
      ) : null}
    </section>
  )
}

function ToolBlock({ tool }: { tool: ChatTool }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  return (
    <section className={`chat-tool${open ? ' is-open' : ''}`} data-testid="chat-tool" aria-expanded={open}>
      <button type="button" className="chat-tool-row" aria-label={t('chat.tool.toggle')} onClick={() => setOpen((value) => !value)}>
        <span className="chat-tool-icon">
          <Icon name={tool.icon} size={12} />
        </span>
        <span className="chat-tool-name">{tool.name}</span>
        <span className="chat-tool-arg">{tool.arg}</span>
        {tool.status === 'pending' ? <span className="chat-badge chat-tone-warn">{t('chat.tool.pending')}</span> : null}
        <span className="chat-tool-duration">{tool.duration}</span>
        <span className="chat-tool-caret" aria-hidden="true">
          <Icon name="chevron" size={12} />
        </span>
      </button>
      {open ? <pre className="chat-tool-body">{tool.body}</pre> : null}
    </section>
  )
}

function Prose({ prose }: { prose: ChatProse }) {
  const { t } = useI18n()
  return (
    <div className="chat-prose">
      <p>{prose.intro}</p>
      {prose.bullets ? (
        <ul>
          {prose.bullets.map((bullet) => (
            <li key={bullet.lead}>
              <strong>{bullet.lead}</strong>
              {bullet.text ? `：${bullet.text}` : null}
              {bullet.code ? <code>{bullet.code}</code> : null}
              {bullet.cite ? (
                <button type="button" className="chat-cite" aria-label={t('chat.message.cite', { num: bullet.cite })}>
                  {bullet.cite}
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {prose.closing ? <p>{prose.closing}</p> : null}
    </div>
  )
}

function ConfirmCard({ onNotice }: { onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className="chat-confirm" role="alert" aria-label={t('chat.confirm.title')}>
      <div className="chat-confirm-head">
        <Icon name="shield" size={14} />
        <span>{t('chat.confirm.title')}</span>
        <span className="chat-badge chat-tone-warn">{t('chat.confirm.risk')}</span>
      </div>
      <div className="chat-confirm-body">
        <p>{t('chat.confirm.desc')}</p>
        <pre className="chat-confirm-code">{`write  docs/migration/v1-to-v2-plan.md   3.4 KB
scope docs/**                               允许
副作用 新增文件（无覆盖）`}</pre>
      </div>
      <div className="chat-confirm-foot">
        <span>{t('chat.confirm.hint')}</span>
        <button type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.notice.approval'))}>
          {t('chat.confirm.deny')}
        </button>
        <button type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.notice.approval'))}>
          {t('chat.confirm.always')}
        </button>
        <button type="button" className="chat-btn chat-btn-sm chat-btn-primary" onClick={() => onNotice(t('chat.notice.approval'))}>
          {t('chat.confirm.once')}
        </button>
      </div>
    </section>
  )
}

function Composer({
  sample,
  live,
  language,
  pickedAgent,
  setPickedAgent,
  attachments,
  setAttachments,
  mentionOpen,
  setMentionOpen,
  attachmentOpen,
  setAttachmentOpen,
  permission,
  setPermission,
  thinking,
  setThinking,
  onNotice,
  onProject,
  workspaceLabel,
  onMention,
  skills,
  runtimeId,
}: {
  sample: boolean
  live: LiveComposer | null
  language: string
  pickedAgent: string
  setPickedAgent: (id: string) => void
  attachments: Attachment[]
  setAttachments: (next: Attachment[]) => void
  mentionOpen: boolean
  setMentionOpen: (open: boolean) => void
  attachmentOpen: boolean
  setAttachmentOpen: (open: boolean) => void
  /** Trust level the next turn runs with; see `PermissionMode`. */
  permission: PermissionMode
  setPermission: (next: PermissionMode) => void
  /** Thinking level the next turn runs with; see `ThinkingLevel`. */
  thinking: ThinkingLevel
  setThinking: (next: ThinkingLevel) => void
  onNotice: (message: string) => void
  onProject: (trigger: HTMLElement) => void
  /** Directory the next conversation runs in, shown on the project chip. */
  workspaceLabel: string
  /** Ask the host which files this project offers for `@`. */
  onMention?: (query: string) => Promise<ContextEntry[]>
  /** Installed skills from the live catalog, filtered to this session's runtime. */
  skills: CatalogSkill[]
  runtimeId: string
}) {
  const { t } = useI18n()
  const enabled = sample || live !== null
  // Enter or Ctrl/Cmd+Enter sends — the reader picks in settings. Read here, in
  // the composer, so the setting takes effect in an already-open conversation.
  const [sendShortcut] = useSendShortcut()
  const [text, setText] = useState(sample ? sampleChat.composerDraft : '')
  const [mentionHits, setMentionHits] = useState<MentionHit[]>([])
  const [mentionMode, setMentionMode] = useState<MentionMode>('all')
  /** Keyboard cursor: arrows move it, Tab/Enter accept it, the wheel scrolls. */
  const [mentionIndex, setMentionIndex] = useState(0)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const mirrorRef = useRef<HTMLDivElement>(null)
  const mentionListRef = useRef<HTMLDivElement>(null)
  const attachmentDrawerRef = useRef<HTMLDivElement>(null)
  const composerPlusRef = useRef<HTMLButtonElement>(null)
  /** Where the caret must land after React rewrites the draft. */
  const caretTarget = useRef<number | null>(null)

  // The "+" drawer is a popover: pressing anywhere outside it (or on the button
  // that opened it) dismisses it. Both pointerdown and mousedown are watched in
  // the capture phase because WebView2 does not always deliver pointer events
  // for every target, and composedPath covers reparented nodes.
  useEffect(() => {
    if (!attachmentOpen) return
    const onOutside = (event: Event) => {
      const node = event.target as Node | null
      if (!node) return
      const path = typeof event.composedPath === 'function' ? event.composedPath() : []
      const drawer = attachmentDrawerRef.current
      const plus = composerPlusRef.current
      if (drawer && (drawer.contains(node) || path.includes(drawer))) return
      if (plus && (plus.contains(node) || path.includes(plus))) return
      setAttachmentOpen(false)
    }
    document.addEventListener('pointerdown', onOutside, true)
    document.addEventListener('mousedown', onOutside, true)
    return () => {
      document.removeEventListener('pointerdown', onOutside, true)
      document.removeEventListener('mousedown', onOutside, true)
    }
  }, [attachmentOpen, setAttachmentOpen])

  // A chip is just `@name` inside the draft; the catalogue decides which names
  // count. When two runtimes ship the same name, the running one wins.
  const skillPaths = useMemo(() => {
    const map = new Map<string, string>()
    const preferred = new Set<string>()
    for (const skill of skills) {
      if (skill.state === 'broken') continue
      if (skill.runtimeId === runtimeId) {
        map.set(skill.name, skill.path)
        preferred.add(skill.name)
      } else if (!preferred.has(skill.name) && !map.has(skill.name)) {
        map.set(skill.name, skill.path)
      }
    }
    return map
  }, [skills, runtimeId])
  const pathOf = useCallback((name: string) => skillPaths.get(name), [skillPaths])

  // React rewrites the textarea value, which parks the caret at the end.
  // A pick must leave it right after the inserted chip instead.
  useLayoutEffect(() => {
    const target = caretTarget.current
    if (target === null) return
    caretTarget.current = null
    const field = textareaRef.current
    if (!field) return
    field.focus()
    field.setSelectionRange(target, target)
  }, [text])

  // Keep the highlighted row inside the popover. A 60-row skill list would
  // otherwise scroll the cursor out of view while arrowing through it.
  useEffect(() => {
    if (!mentionOpen) return
    const active = mentionListRef.current?.querySelector<HTMLElement>('[aria-selected="true"]')
    active?.scrollIntoView?.({ block: 'nearest' })
  }, [mentionOpen, mentionIndex, mentionHits])
  // Which agent the picker shows. Choosing a model clears it: the control's
  // value has to be one of its own options, and the two groups overlap in the
  // runtime+model they name.

  // The references the user actually attached become the turn's context.
  // Inline skills are parsed out of the draft: the chip is only a decoration
  // over the `@name` that already lives in the text. Sample rows have no path,
  // so they never reach the host.
  const refs = (value: string): ContextRef[] => {
    const out: ContextRef[] = []
    const seen = new Set<string>()
    for (const range of skillRanges(value, pathOf)) {
      if (seen.has(range.path)) continue
      seen.add(range.path)
      out.push({ kind: 'skill', path: range.path, label: range.name })
    }
    for (const attachment of attachments) {
      if (!attachment.path || attachment.kind === 'skill' || seen.has(attachment.path)) continue
      seen.add(attachment.path)
      out.push({ kind: attachment.kind, path: attachment.path })
    }
    return out
  }

  const submit = (event?: FormEvent) => {
    event?.preventDefault()
    if (sample) {
      onNotice(t('chat.notice.send'))
      return
    }
    if (!live || live.running || !text.trim()) return
    live.onSend(text, refs(text))
    setText('')
    caretTarget.current = 0
    closeMentions()
  }

  // `@` at the end of the draft opens the picker; everything after it is the
  // query, so typing `@src/ma` narrows the list without leaving the field.
  const atQuery = (value: string): string | null => {
    const match = /(?:^|\s)@([^\s]*)$/.exec(value)
    return match ? match[1] : null
  }

  // `/` is the skill palette. It only triggers at a word boundary, so a path
  // like `src/main.rs` or a date like `11/16` is left alone.
  const slashQuery = (value: string): string | null => {
    const match = /(?:^|\s)\/([^\s]*)$/.exec(value)
    return match ? match[1] : null
  }

  /** Every usable skill the catalog discovered. Nothing is hidden behind a
   *  runtime filter or a hard cap: the typed query narrows the list, and the
   *  running runtime's skills sort first so the common case stays on top. */
  const skillHits = (query: string): MentionHit[] =>
    skills
      .filter(
        (skill) =>
          skill.state !== 'broken' &&
          `${skill.name} ${skill.description} ${skill.runtimeId}`.toLowerCase().includes(query.toLowerCase()),
      )
      .sort((left, right) => {
        const leftRank = left.runtimeId === runtimeId ? 0 : 1
        const rightRank = right.runtimeId === runtimeId ? 0 : 1
        return leftRank - rightRank || left.name.localeCompare(right.name)
      })
      .map((skill) => ({
        id: `skill:${skill.path}`,
        name: skill.name,
        kind: 'skill' as const,
        path: skill.path,
        detail: `${skill.runtimeId} · Skill`,
        bytes: 0,
      }))

  const closeMentions = () => {
    setMentionOpen(false)
    setMentionHits([])
    setMentionMode('all')
    setMentionIndex(0)
  }

  const refreshMentions = (value: string, mode: MentionMode = 'all') => {
    const query = mode === 'slash' ? slashQuery(value) : atQuery(value)
    const open = mode === 'slash' ? query !== null : query !== null || value.endsWith('@')
    if (!open) {
      closeMentions()
      return
    }
    setMentionOpen(true)
    setMentionMode(mode)
    const matchedSkills = skillHits(query ?? '')
    // `@` from the drawer and `/` are skill-only; only a plain `@` asks the
    // host for project files.
    if (mode !== 'all' || !onMention) {
      setMentionHits(matchedSkills)
      setMentionIndex(0)
      return
    }
    void onMention(query ?? '')
      .then((files) => {
        setMentionHits([
          ...matchedSkills,
          ...files.map<MentionHit>((file) => ({
            id: file.path,
            name: file.relative,
            kind: file.kind,
            path: file.path,
            detail: file.kind,
            bytes: file.bytes,
          })),
        ])
        setMentionIndex(0)
      })
      .catch(() => {
        setMentionHits(matchedSkills)
        setMentionIndex(0)
      })
  }

  /** The drawer's Skill entry: put `@` in the draft and show only skills. */
  const addSkill = () => {
    if (!enabled || sample) return
    const next = `${text}${text && !text.endsWith(' ') ? ' ' : ''}@`
    setText(next)
    setAttachmentOpen(false)
    refreshMentions(next, 'skill')
    textareaRef.current?.focus()
  }

  /** Adds one picked path as a context chip; the host reads it on send. */
  const attachPicked = (path: string, kind: 'file' | 'dir') => {
    const name = path.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || path
    setAttachments([
      ...attachments.filter((item) => item.path !== path),
      { id: `picked:${path}`, name, kind, path },
    ])
  }

  /** The drawer's file entry: native picker, then attach the chosen file. */
  const addFilesFromPicker = async () => {
    setAttachmentOpen(false)
    try {
      const picked = await pickWorkspaceFile()
      if (picked) {
        attachPicked(picked, 'file')
        textareaRef.current?.focus()
      }
    } catch (cause) {
      onNotice(cause instanceof Error ? cause.message : String(cause))
    }
  }

  /** The drawer's directory entry: attach a whole folder as context. */
  const addDirFromPicker = async () => {
    setAttachmentOpen(false)
    try {
      const picked = await pickWorkspace()
      if (picked) {
        attachPicked(picked, 'dir')
        textareaRef.current?.focus()
      }
    } catch (cause) {
      onNotice(cause instanceof Error ? cause.message : String(cause))
    }
  }

  /**
   * A skill becomes inline text (`@name `), not an attachment chip: it has to
   * sit where the caret was and read as part of the sentence. Files keep the
   * chip above the field, because a path is not something you type into prose.
   */
  const acceptHit = (hit: MentionHit) => {
    if (hit.kind === 'skill') {
      const { value, caret } = insertSkill(
        text,
        hit.name,
        mentionMode === 'slash' ? 'slash' : 'at',
      )
      setText(value)
      caretTarget.current = caret
      closeMentions()
      return
    }
    setAttachments([
      ...attachments.filter((item) => item.path !== hit.path),
      {
        id: hit.id,
        name: hit.name,
        kind: hit.kind,
        path: hit.path,
        size: hit.bytes ? `${Math.max(1, Math.round(hit.bytes / 1024))} KB` : undefined,
      },
    ])
    setText((current) => current.replace(/@[^\s]*$/, ''))
    closeMentions()
    textareaRef.current?.focus()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    // IME is sacred: while a candidate window is open, Enter belongs to the
    // input method, not to us. Chinese/Japanese users would otherwise send a
    // half-finished word.
    const composing = event.nativeEvent.isComposing || event.keyCode === 229
    const modifier = event.metaKey || event.ctrlKey
    const paletteOpen = mentionOpen && !sample && mentionHits.length > 0
    const wantsSend =
      !composing &&
      !paletteOpen &&
      event.key === 'Enter' &&
      (sendShortcut === 'enter' ? !modifier && !event.shiftKey : modifier)
    if (wantsSend) {
      event.preventDefault()
      submit()
      return
    }
    if (mentionOpen && !sample && mentionHits.length > 0) {
      const last = mentionHits.length - 1
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setMentionIndex((index) => (index >= last ? 0 : index + 1))
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setMentionIndex((index) => (index <= 0 ? last : index - 1))
        return
      }
      // Tab is the requested acceptance key. Enter does the same while the list
      // is open, so the palette never eats a newline the reader wanted.
      if (event.key === 'Tab' || event.key === 'Enter') {
        event.preventDefault()
        acceptHit(mentionHits[Math.min(mentionIndex, last)])
        return
      }
    }
    // A chip is one unit: Backspace inside or right after it removes the whole
    // `@name`, never a character of it. The textarea stays the single source of
    // truth, so this can never desync the mirror layer.
    if (!sample && (event.key === 'Backspace' || event.key === 'Delete')) {
      const field = textareaRef.current
      if (field && field.selectionStart === field.selectionEnd) {
        const hit = skillAtCaret(
          text,
          skillRanges(text, pathOf),
          field.selectionStart,
          event.key === 'Backspace' ? 'back' : 'forward',
        )
        if (hit) {
          event.preventDefault()
          const next = withoutRange(text, hit)
          setText(next.value)
          caretTarget.current = next.caret
          return
        }
      }
    }
    if (event.key === 'Escape') {
      setMentionOpen(false)
      setAttachmentOpen(false)
      closeMentions()
    }
  }


  const shown = enabled ? text : ''
  // The counter reads the same two sources the turn will: file chips above the
  // field and skills written into the draft. One skill mentioned twice is one
  // reference, exactly like the dedupe the host receives.
  const contextCount =
    attachments.filter((attachment) => Boolean(attachment.path) && attachment.kind !== 'skill').length +
    new Set(skillRanges(text, pathOf).map((range) => range.path)).size
  const selectedModel = live?.model ? modelKey(live.model) : ''

  return (
    <div className="chat-composer-wrap">
      <div className="chat-composer-project-row">
        <button
          type="button"
          className="chat-project-trigger"
          data-testid="chat-project-trigger"
          aria-haspopup="dialog"
          disabled={!enabled}
          title={enabled ? t('chat.composer.project.change') : t('chat.action.unavailable')}
          onClick={(event) => onProject(event.currentTarget)}
        >
          <Icon name="folder" size={12} />
          <span>{sample ? sampleChat.project : workspaceLabel || t('chat.composer.project.none')}</span>
          <Icon name="chevDown" size={11} />
        </button>
      </div>
      <form className="chat-composer" onSubmit={submit} aria-label={t('chat.composer.placeholder')}>
        <div className="chat-composer-field">
          {/* Paints the skill blocks behind the real characters. The textarea
              above keeps every native editing behaviour (caret, selection, IME)
              and the block adds no padding, so both layers wrap identically. */}
          <div className="chat-composer-mirror" aria-hidden="true" ref={mirrorRef}>
            <InlineDraft text={shown} pathOf={pathOf} />
          </div>
          <textarea
            ref={textareaRef}
            data-testid="chat-composer"
            rows={2}
            value={shown}
            disabled={!enabled}
            placeholder={enabled ? t('chat.composer.placeholder') : t('chat.composer.normalPlaceholder')}
            aria-label={t('chat.composer.placeholder')}
            aria-expanded={mentionOpen}
            aria-controls={mentionOpen ? 'chat-mention-list' : undefined}
            aria-activedescendant={
              mentionOpen && !sample && mentionHits.length > 0
                ? `chat-mention-${mentionIndex}`
                : undefined
            }
            onChange={(event) => {
              if (!enabled) return
              const value = event.target.value
              if (!sample) {
                // A chip must not be renameable: an edit that lands inside
                // `@name` would silently point the turn at another skill. Put
                // the untouched draft back and park the caret on the block.
                const blocked = chipEditPosition(text, value, skillRanges(text, pathOf))
                if (blocked !== null) {
                  event.target.value = text
                  event.target.setSelectionRange(blocked, blocked)
                  return
                }
              }
              setText(value)
              if (sample) {
                setMentionOpen(value.endsWith('@'))
                return
              }
              // A trailing `/word` is the skill palette; otherwise keep the
              // skill-only scope the drawer set until a selection clears it.
              const slash = slashQuery(value) !== null
              refreshMentions(value, slash ? 'slash' : mentionMode === 'skill' ? 'skill' : 'all')
            }}
            onScroll={(event) => {
              // The mirror only paints: it has to follow the field's scroll.
              if (mirrorRef.current) mirrorRef.current.scrollTop = event.currentTarget.scrollTop
            }}
            onKeyDown={onKeyDown}
          />
        </div>
        {attachments.length > 0 ? (
          <div className="chat-composer-attachments" data-testid="chat-attachments" aria-live="polite">
            {attachments.map((attachment) => (
              <span key={attachment.id} className={`chat-attachment${attachment.kind === 'skill' ? ' is-skill' : ''}`}>
                <Icon
                  name={attachment.kind === 'skill' ? 'puzzle' : attachment.kind === 'dir' ? 'folder' : 'file'}
                  size={12}
                />
                <b>{attachment.name}</b>
                <small>{attachment.kind === 'skill' ? 'Skill' : attachment.size}</small>
                <button
                  type="button"
                  aria-label={t('chat.composer.remove', { name: attachment.name })}
                  onClick={() => {
                    setAttachments(attachments.filter((item) => item.id !== attachment.id))
                    onNotice(t('chat.notice.attachment'))
                  }}
                >
                  <Icon name="x" size={11} />
                </button>
              </span>
            ))}
          </div>
        ) : null}
        {mentionOpen && !sample ? (
          <div
            id="chat-mention-list"
            ref={mentionListRef}
            className="chat-mention-list"
            role="listbox"
            data-mode={mentionMode}
            aria-label={
              mentionMode === 'slash' ? t('chat.composer.skillPicker') : t('chat.composer.mention')
            }
            onWheel={(event) => event.stopPropagation()}
          >
            {mentionHits.length === 0 ? (
              <span className="chat-mention-empty" data-testid="chat-mention-empty">
                {mentionMode === 'slash'
                  ? t('chat.composer.skillEmpty')
                  : t('chat.composer.mentionEmpty')}
              </span>
            ) : (
              mentionHits.map((hit, index) => (
                <button
                  key={hit.id}
                  id={`chat-mention-${index}`}
                  type="button"
                  role="option"
                  aria-selected={index === mentionIndex}
                  className={index === mentionIndex ? 'is-active' : undefined}
                  data-testid="chat-mention-entry"
                  data-mention-kind={hit.kind}
                  onMouseEnter={() => setMentionIndex(index)}
                  onClick={() => acceptHit(hit)}
                >
                  <span className="chat-mention-name">{hit.name}</span>
                  <small>{hit.detail}</small>
                </button>
              ))
            )}
          </div>
        ) : null}
        {mentionOpen && sample ? (
          <div className="chat-mention-list" role="listbox" aria-label={t('chat.composer.mention')}>
            {[sampleChat.attachments[0].name, sampleChat.agent].map((name) => (
              <button
                key={name}
                type="button"
                role="option"
                aria-selected="false"
                onClick={() => {
                  setText((current) => current.replace(/@$/, `@${name} `))
                  setMentionOpen(false)
                  onNotice(t('chat.notice.mention'))
                }}
              >
                {name}
              </button>
            ))}
          </div>
        ) : null}
        {attachmentOpen ? (
          <div
            ref={attachmentDrawerRef}
            className="chat-attachment-drawer"
            role="menu"
            aria-label={t('chat.composer.addContext')}
            data-testid="chat-attachment-drawer"
          >
            <div className="chat-attachment-group">
              <button
                type="button"
                role="menuitem"
                data-testid="chat-add-file"
                disabled={!enabled || sample}
                title={sample ? t('chat.action.unavailable') : t('chat.composer.chooseFilesDesc')}
                onClick={() => void addFilesFromPicker()}
              >
                <Icon name="file" size={14} />
                <span>
                  <b>{t('chat.composer.chooseFiles')}</b>
                  <small>{t('chat.composer.chooseFilesDesc')}</small>
                </span>
              </button>
              <button
                type="button"
                role="menuitem"
                data-testid="chat-add-dir"
                disabled={!enabled || sample}
                title={sample ? t('chat.action.unavailable') : t('chat.composer.chooseDirDesc')}
                onClick={() => void addDirFromPicker()}
              >
                <Icon name="folder" size={14} />
                <span>
                  <b>{t('chat.composer.chooseDir')}</b>
                  <small>{t('chat.composer.chooseDirDesc')}</small>
                </span>
              </button>
            </div>
            <div className="chat-attachment-group">
              <button
                type="button"
                role="menuitem"
                data-testid="chat-add-skill"
                disabled={!enabled || sample}
                title={sample ? t('chat.action.unavailable') : t('chat.composer.skillDesc')}
                onClick={addSkill}
              >
                <Icon name="puzzle" size={14} />
                <span>
                  <b>{t('chat.composer.skill')}</b>
                  <small>{t('chat.composer.skillDesc')}</small>
                </span>
              </button>
            </div>
          </div>
        ) : null}
        <div className="chat-composer-bar">
          <button
            ref={composerPlusRef}
            type="button"
            className="chat-composer-plus"
            data-testid="chat-composer-plus"
            aria-label={t('chat.composer.add')}
            aria-expanded={attachmentOpen}
            disabled={!enabled}
            onClick={() => setAttachmentOpen(!attachmentOpen)}
          >
            <Icon name="plus" size={14} />
          </button>
          <label className="chat-ctl" title={t('chat.composer.model')}>
            <Icon name="sparkle" size={13} />
            <select
              aria-label={t('chat.composer.model')}
              data-testid="chat-model-select"
              disabled={!enabled}
              value={sample ? undefined : pickedAgent ? `agent:${pickedAgent}` : selectedModel}
              onChange={(event) => {
                const value = event.target.value
                if (value.startsWith('agent:')) {
                  const agent = live?.agents.find((item) => item.id === value.slice(6))
                  const choice = agent
                    ? live?.models.find(
                        (item) =>
                          item.runtimeId === agent.runtimeId && item.modelId === agent.modelId,
                      )
                    : undefined
                  // An agent whose model is not enabled cannot be run: leave the
                  // picker where it was rather than show a pair that would fail.
                  if (!agent || !choice) return
                  setPickedAgent(agent.id)
                  live?.onModel(choice)
                  return
                }
                setPickedAgent('')
                const choice = live?.models.find((item) => modelKey(item) === value)
                if (choice) live?.onModel(choice)
              }}
            >
              {sample ? (
                <option>{sampleChat.model}</option>
              ) : live && live.models.length > 0 ? (
                <>
                  {/* An agent is a saved runtime+model pair; a model is the
                      pair chosen on the spot. Both end in the same place. */}
                  {live.agents.some((agent) =>
                    live.models.some(
                      (choice) =>
                        choice.runtimeId === agent.runtimeId && choice.modelId === agent.modelId,
                    ),
                  ) ? (
                    <optgroup label={t('chat.composer.agents')}>
                      {live.agents
                        .filter((agent) =>
                          live.models.some(
                            (choice) =>
                              choice.runtimeId === agent.runtimeId &&
                              choice.modelId === agent.modelId,
                          ),
                        )
                        .map((agent) => (
                        <option
                          key={agent.id}
                          value={`agent:${agent.id}`}
                          title={`${agent.runtimeId} · ${agent.modelId}`}
                        >
                          {agent.isDefault ? t('chat.composer.defaultAgent') : agent.name}
                        </option>
                      ))}
                    </optgroup>
                  ) : null}
                  <optgroup label={t('chat.composer.models')}>
                    {live.models.map((choice) => (
                      // The id is what the runtime is actually asked for, so it
                      // is what the composer shows; the display name is the
                      // tooltip.
                      <option
                        key={modelKey(choice)}
                        value={modelKey(choice)}
                        title={choice.displayName}
                      >
                        {choice.runtimeId} · {choice.modelId}
                      </option>
                    ))}
                  </optgroup>
                </>
              ) : (
                <option>{live ? t('chat.live.noModel') : t('chat.composer.normalPlaceholder')}</option>
              )}
            </select>
          </label>
          {live ? (
            <SyncButton
              label={t('chat.live.syncModels')}
              busy={live.syncing}
              onClick={live.onSync}
              testId="chat-models-sync"
            />
          ) : null}
          <label className="chat-ctl" title="Thinking">
            <Icon name="brain" size={13} />
            <select
              aria-label="Thinking effort"
              data-testid="chat-thinking-select"
              disabled={!enabled}
              value={sample ? sampleChat.thinkingLevel : thinking}
              onChange={(event) => setThinking(event.target.value as ThinkingLevel)}
            >
              {THINKING_LEVELS.map((level) => (
                <option key={level} value={level}>
                  {THINKING_LABELS[level]}
                </option>
              ))}
            </select>
          </label>
          <label className="chat-ctl" title={t('chat.composer.permission.hint')}>
            <Icon name="shield" size={13} />
            <select
              aria-label={t('chat.composer.permission')}
              data-testid="chat-permission-select"
              disabled={!enabled}
              value={permission}
              onChange={(event) => setPermission(event.target.value as PermissionMode)}
            >
              <option value="readonly">{t('chat.composer.permission.readonly')}</option>
              <option value="medium">{t('chat.composer.permission.medium')}</option>
              <option value="high">{t('chat.composer.permission.high')}</option>
              <option value="full">{t('chat.composer.permission.full')}</option>
            </select>
          </label>
          <button type="button" className="chat-ctl" disabled={!enabled} onClick={() => onNotice(t('chat.notice.attachment'))}>
            <Icon name="paperclip" size={13} />
            <span>{t('chat.composer.context', { count: contextCount })}</span>
          </button>
          <div className="chat-composer-actions">
            <button
              type="button"
              className="chat-btn chat-btn-sm"
              data-testid="chat-stop"
              disabled={!enabled || (live !== null && !live.running)}
              onClick={() => (live ? live.onStop() : onNotice(t('chat.notice.stop')))}
            >
              <Icon name="pause" size={12} />
              {t('chat.composer.stop')}
            </button>
            <button
              type="submit"
              className="chat-send"
              data-testid="chat-send"
              aria-label={t('chat.composer.send')}
              disabled={!enabled || (live !== null && (live.running || !text.trim()))}
            >
              <Icon name="send" size={14} />
            </button>
          </div>
        </div>
      </form>
      <div className="chat-composer-foot">
        <span>{sample ? t('chat.composer.pending') : live ? t('chat.live.hint') : t('chat.composer.normalHint')}</span>
        <span className="chat-kbdset">
          <span className="kbd">{navigator.platform.includes('Mac') ? '⌘' : 'Ctrl'}</span>
          <span className="kbd">↵</span> {t('chat.composer.sendHint')}
        </span>
        <span className="chat-kbdset">
          <span className="kbd">⇧</span>
          <span className="kbd">↵</span> {t('chat.composer.newlineHint')}
        </span>
      </div>
      <span className="sr-only">{language}</span>
    </div>
  )
}

/** The context chips a user turn carried, straight from the message meta. */
function ContextChips({ meta }: { meta?: Record<string, unknown> }) {
  const { t } = useI18n()
  const refs = Array.isArray(meta?.context)
    ? (meta?.context as { kind: string; path?: string; label?: string; truncated?: boolean; outsideRoot?: boolean }[])
    : []
  if (refs.length === 0) return null
  return (
    <div className="chat-context-chips" data-testid="chat-context-chips">
      {refs.map((ref, index) => (
        <span
          key={`${ref.path ?? index}`}
          className={`chat-context-chip${ref.outsideRoot ? ' is-outside' : ''}`}
          title={ref.path ?? ''}
        >
          <Icon name={ref.kind === 'skill' ? 'puzzle' : ref.kind === 'dir' ? 'folder' : 'file'} size={11} />
          <b>{ref.label ?? ref.path?.split(/[\\/]/).pop() ?? ref.kind}</b>
          {ref.truncated ? <small>{t('chat.context.truncated')}</small> : null}
          {ref.outsideRoot ? <small>{t('chat.context.outside')}</small> : null}
        </span>
      ))}
    </div>
  )
}

/**
 * The live turn's timeline, rendered with the prototype's thinking / tool /
 * confirmation / state vocabulary. Every stage is derived from an event the
 * runtime actually wrote to the ledger — no invented reasoning text.
 */
function LiveTurnTimeline({
  showTimeline,
  events,
  parts,
  pending,
  running,
  startedAt,
  failure,
  runtimeId,
  model,
  sources,
  onCite,
  onApprove,
  onRetry,
}: {
  showTimeline: boolean
  events: SessionEvent[]
  parts: TurnPart[]
  pending: ApprovalRequest | null
  running: boolean
  startedAt: number | null
  failure: TurnFailure | null
  runtimeId: string
  model: string
  sources: number
  onCite: () => void
  onApprove: (decision: ApprovalDecision) => void
  onRetry: () => void
}) {
  const { t } = useI18n()
  const [elapsed, setElapsed] = useState(0)

  useEffect(() => {
    if (!running || !startedAt) {
      setElapsed(0)
      return
    }
    const tick = () => setElapsed(Math.max(0, Math.floor((Date.now() - startedAt) / 1000)))
    tick()
    const timer = window.setInterval(tick, 1000)
    return () => window.clearInterval(timer)
  }, [running, startedAt])

  const stages: string[] = []
  const seen = new Set<string>()
  const add = (key: string, label: string) => {
    if (seen.has(key)) return
    seen.add(key)
    stages.push(label)
  }
  for (const event of events) {
    switch (event.kind) {
      // `session_started` / `turn_started` carry no information the reader does
      // not already have ("you pressed send"), so the timeline starts at the
      // first thing that actually happened.
      case 'context_attached': {
        const refs = event.payload.refs
        add('context', t('chat.live.stageContext', { count: Array.isArray(refs) ? refs.length : 0 }))
        break
      }
      case 'tool_call':
        add(`tool-${event.seq}`, t('chat.live.stageTool', { name: String(event.payload.tool ?? 'tool') }))
        break
      // One stage per delegated agent, so the timeline says "delegated to X"
      // instead of hiding a whole sub-agent behind the parent's tool calls.
      case 'sub_agent': {
        if (event.payload.phase !== 'started') break
        const id = String(event.payload.agentId ?? '')
        const name = String(event.payload.name ?? id)
        add(`agent-${id}`, t('chat.live.stageAgent', { name }))
        break
      }
      case 'approval_request':
        add('approval', t('chat.live.stageApproval'))
        break
      // Streaming text and a clean finish are the conversation itself, not
      // execution steps. Putting them here produced a "1 开始输出回复" card
      // after every single answer.
      case 'aborted':
        add('stopped', t('chat.live.stageStopped'))
        break
      case 'error':
        add('error', t('chat.live.stageError'))
        break
      default:
        break
    }
  }

  // Summed, not "the last usage event": a turn with tool calls is several model
  // requests, and each one is billed. The previous code read `inputTokens` and
  // fell back to `totalTokens` (so input looked plausible) while no runtime
  // sends `outputTokens` at all, which is why output always read zero.
  const usage = turnUsage(events)

  return (
    <>
      {/* Everything the runtime produced, in the order it produced it, then
          the answer. The same stack is what the settled message shows, so a
          turn does not re-arrange itself when the reply lands. */}
      {running ? (
        <article className="chat-msg" data-testid="chat-message-streaming">
          <div className="chat-agent-head">
            <span className="chat-agent-badge">{runtimeId.slice(0, 1)}</span>
            <span className="chat-agent-name">{runtimeId}</span>
            <span className="chat-agent-model">{model}</span>
            <span className="chat-badge">
              <i className="chat-dot" aria-hidden="true" />
              {t('chat.live.generating')}
            </span>
          </div>
          {parts.length > 0 ? (
            <TurnPartsList parts={parts} live sources={sources} onCite={onCite} />
          ) : null}
          {/* Nothing written yet, or the runtime is between steps: keep the
              three dots under the last block so the turn still reads as live. */}
          {parts.length === 0 || parts[parts.length - 1].kind !== 'text' ? (
            <div className="chat-stream-status" role="status" aria-label={t('chat.live.generating')}>
              <span className="chat-typing" aria-hidden="true">
                <i />
                <i />
                <i />
              </span>
            </div>
          ) : null}
          <div className="chat-msg-meta">
            <span>{t('chat.live.elapsed', { seconds: elapsed })}</span>
            {usage ? (
              <span>
                {' · '}
                <UsageLine usage={usage} />
              </span>
            ) : null}
          </div>
        </article>
      ) : null}
      {showTimeline && stages.length > 0 ? (
        <ThinkingBlock title={t('chat.live.timeline')} steps={stages} initialOpen={running} />
      ) : null}
      {/* A run that already ended must not keep its card: the host has dropped
          the question, so pressing "allow" would look like a dead button. */}
      {pending && running ? <ApprovalCard request={pending} onDecision={onApprove} /> : null}
      {failure ? (
        <section
          className="chat-state-card chat-tone-danger"
          role="alert"
          aria-label={t('chat.live.failure')}
          data-testid="chat-live-failure"
        >
          <span className="chat-state-icon">
            <Icon name="alert" size={14} />
          </span>
          <div className="chat-state-copy">
            <b>{t('chat.live.failure')}</b>
            <span>{failure.message}</span>
          </div>
          <button type="button" className="chat-btn chat-btn-sm" onClick={onRetry}>
            {t('chat.live.retry')}
          </button>
        </section>
      ) : null}
    </>
  )
}

/** One tool call of the live turn, collapsed until the user opens it. */
function LiveToolCard({ tool }: { tool: { name: string; args: string; ok: boolean | null; output: string } }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const status = tool.ok === null ? 'pending' : tool.ok ? 'ok' : 'failed'
  return (
    <section
      className={`chat-tool${open ? ' is-open' : ''}`}
      data-testid="chat-live-tool"
      data-status={status}
      aria-expanded={open}
    >
      <button type="button" className="chat-tool-row" aria-label={t('chat.tool.toggle')} onClick={() => setOpen(!open)}>
        <span className="chat-tool-icon">
          <Icon name={tool.name === 'command' ? 'terminal' : 'file'} size={12} />
        </span>
        <span className="chat-tool-name">{tool.name}</span>
        <span className="chat-tool-arg">{tool.args}</span>
        {status === 'pending' ? <span className="chat-badge chat-tone-warn">{t('chat.tool.pending')}</span> : null}
        {status === 'failed' ? <span className="chat-badge chat-tone-danger">{t('chat.tool.failed')}</span> : null}
        <span className="chat-tool-caret" aria-hidden="true">
          <Icon name="chevDown" size={10} />
        </span>
      </button>
      {open ? <pre className="chat-tool-body">{tool.output || tool.args}</pre> : null}
    </section>
  )
}

/**
 * A delegated agent's card.
 *
 * The runtime owns the delegation; this only shows what it reported. Collapsed
 * by default because a parent that spawns three children would otherwise push
 * its own answer off the screen — but the collapsed row still carries the two
 * things a reader needs at a glance: what state the child is in and what it was
 * asked to do.
 */
export function SubAgentCard({
  agent,
  sources = 0,
  onCite,
}: {
  agent: LiveSubAgent
  sources?: number
  onCite?: () => void
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const running =
    agent.status === 'running' || agent.status === 'pending' || agent.status === 'waiting'
  const tone =
    agent.status === 'failed'
      ? ' chat-tone-danger'
      : agent.status === 'completed'
        ? ' chat-tone-success'
        : running
          ? ' chat-tone-warn'
          : ''
  const tools = agent.steps.filter((step) => step.kind === 'tool').length
  const title = agent.name || t('chat.subagent.unnamed')

  return (
    <section
      className={`chat-subagent${open ? ' is-open' : ''}`}
      data-testid="chat-subagent"
      data-status={agent.status}
      data-agent={agent.id}
    >
      <button
        type="button"
        className="chat-subagent-row"
        aria-expanded={open}
        aria-label={t('chat.subagent.toggle')}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="chat-subagent-icon">
          <Icon name="layers" size={12} />
        </span>
        <span className="chat-subagent-name">{title}</span>
        {agent.step !== null ? (
          <span className="chat-subagent-step">{t('chat.subagent.step', { step: agent.step })}</span>
        ) : null}
        {agent.model ? <span className="chat-subagent-model">{agent.model}</span> : null}
        <span className={`chat-badge${tone}`}>{t(`chat.subagent.status.${agent.status}` as MessageKey)}</span>
        {tools > 0 ? (
          <span className="chat-subagent-count">{t('chat.subagent.tools', { count: tools })}</span>
        ) : null}
        <span className="chat-tool-caret" aria-hidden="true">
          <Icon name="chevDown" size={10} />
        </span>
      </button>
      {agent.task ? (
        <p className="chat-subagent-task">
          <span>{t('chat.subagent.task')}</span>
          {agent.task}
        </p>
      ) : null}
      {open ? (
        <div className="chat-subagent-body">
          {agent.steps.length === 0 ? (
            <p className="chat-subagent-empty">{t('chat.subagent.empty')}</p>
          ) : (
            agent.steps.map((step, index) =>
              step.kind === 'text' ? (
                <div
                  className={`chat-subagent-text${step.channel === 'thinking' ? ' is-thinking' : ''}`}
                  key={`${agent.id}-step-${index}`}
                >
                  {step.channel === 'thinking' ? (
                    <span className="chat-subagent-label">{t('chat.subagent.thinking')}</span>
                  ) : null}
                  {step.text}
                </div>
              ) : (
                <div
                  className="chat-subagent-tool"
                  key={`${agent.id}-step-${index}`}
                  data-ok={step.ok === null ? 'pending' : String(step.ok)}
                >
                  <span className="chat-subagent-tool-name">{step.name}</span>
                  {step.args ? <code>{step.args}</code> : null}
                  {step.output ? <pre>{step.output}</pre> : null}
                </div>
              ),
            )
          )}
        </div>
      ) : null}
      {agent.error ? <p className="chat-subagent-error">{agent.error}</p> : null}
      {agent.result && !agent.error ? (
        <div className="chat-subagent-result">
          <span className="chat-subagent-label">{t('chat.subagent.result')}</span>
          <Markdown text={agent.result} sources={sources} onCite={onCite} />
        </div>
      ) : null}
    </section>
  )
}

/**
 * The card a blocked run is waiting on.
 *
 * Closing it is a real answer (`cancel`): leaving a question answerable after
 * the user walked away would let a later click approve a path they never saw.
 */
function ApprovalCard({
  request,
  onDecision,
}: {
  request: { id: string; title: string; detail: string; kind: string }
  onDecision: (decision: 'allow' | 'deny' | 'cancel') => void
}) {
  const { t } = useI18n()
  return (
    <section className="chat-confirm" data-testid="chat-approval" role="alert" aria-label={request.title}>
      <div className="chat-confirm-head">
        <Icon name="shield" size={14} />
        <span>{request.title}</span>
        <span className="chat-badge chat-tone-warn">{request.kind}</span>
      </div>
      <div className="chat-confirm-body">
        <p>
          {t(
            request.kind === 'read_outside_project'
              ? 'chat.confirm.outside'
              : 'chat.confirm.desc',
          )}
        </p>
        <pre className="chat-confirm-code">{request.detail}</pre>
      </div>
      <div className="chat-confirm-foot">
        <span>{t('chat.confirm.hint')}</span>
        <button type="button" className="chat-btn chat-btn-sm" onClick={() => onDecision('deny')}>
          {t('chat.confirm.deny')}
        </button>
        <button type="button" className="chat-btn chat-btn-sm" onClick={() => onDecision('cancel')}>
          {t('chat.confirm.cancel')}
        </button>
        <button type="button" className="chat-btn chat-btn-sm chat-btn-primary" onClick={() => onDecision('allow')}>
          {t('chat.confirm.once')}
        </button>
      </div>
    </section>
  )
}

