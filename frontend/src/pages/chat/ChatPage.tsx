import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
  type SetStateAction,
} from 'react'
import { Link } from 'react-router-dom'
import {
  buildTurnParts,
  printedNotices,
  useChatSession,
  withStoredAnswer,
  type AgentChoice,
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
  SubAgentButton,
  SubAgentDrawer,
  SubAgentList,
  mergeSubAgents,
} from './SubAgents'
import {
  describeSessionError,
  browserAction,
  browserClose,
  browserBounds,
  browserOpen,
  loadSessionEvents,
  THINKING_LEVELS,
  type ApprovalDecision,
  type ApprovalRequest,
  type ContextEntry,
  type ContextRef,
  type PermissionMode,
  type PiImage,
  type SessionEvent,
  type ThinkingLevel,
  type UiAnswer,
  type UiRequest,
} from '../../chat/client'
import { ExtensionUiCard } from './ExtensionUiCard'
import { FilePreviewOverlay } from './FilePreviewOverlay'
import ChatHistoryPanel from './ChatHistoryPanel'
import { imageSrc } from '../../chat/image'
import { useWorkspace } from '../../workspace/WorkspaceProvider'
import { pickWorkspace, pickWorkspaceFiles } from '../../workspace/client'
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
  /** Queue a message for the turn that is already running. */
  onQueue: (text: string, context: ContextRef[]) => void
  onStop: () => void
  /** Re-scan the machine for models. */
  onSync: () => void
  syncing: boolean
  running: boolean
}

/** A message the reader wrote while a turn was still running. */
export interface QueuedTurn {
  id: string
  text: string
  refs: ContextRef[]
}

/** How many trailing messages a compaction leaves verbatim. */
// The last exchange and the answer in progress; everything older is folded.
const COMPACT_KEEP_LAST = 2

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

/** A byte count as the reader thinks of it. */
function prettySize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`
  return `${bytes} B`
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
function partsByRun(events: SessionEvent[], shown: Set<string> = new Set()): Record<string, TurnPart[]> {
  // One pass to group, one short build per run: the ledger interleaves runs,
  // and re-scanning all of it for every run turned a settled session with many
  // turns into an O(runs x events) walk on each reload.
  //
  // `shown` is carried across the runs on purpose: a notice the runtime repeats
  // every turn prints once, in the turn that first carried it.
  const result: Record<string, TurnPart[]> = {}
  for (const [run, runEvents] of Object.entries(groupByRun(events))) {
    result[run] = buildTurnParts(runEvents, run, shown)
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
): { parts: Record<string, TurnPart[]>; usage: Record<string, TurnUsage>; shownNotices: Set<string> } {
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
  // What the turns above have already shown. The run in flight is left out on
  // purpose: its own notice must print while it streams, not only once it
  // settles. Skipping it here also keeps the live turn from printing a line the
  // settled transcript already carries.
  const shownNotices = useMemo(() => {
    const inFlight = turnEvents[0]?.runId
    return printedNotices(
      inFlight ? ledger.filter((event) => event.runId !== inFlight) : ledger,
    )
  }, [ledger, turnEvents])
  const live = useMemo(
    () => partsByRun(turnEvents, new Set(shownNotices)),
    [turnEvents, shownNotices],
  )
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

  return { parts, usage, shownNotices }
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

export function TurnPartsList({
  parts,
  live = false,
  sources = 0,
  onCite,
  onAnswerUi,
}: {
  parts: TurnPart[]
  live?: boolean
  sources?: number
  onCite?: () => void
  onAnswerUi?: (request: UiRequest, answer: UiAnswer) => void
}) {
  const { t } = useI18n()
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
          // Nothing in the transcript. A delegation is the parent's own work,
          // but the child's words are not the parent's answer: printing even
          // one row here made a reply read as if it had stopped mid-sentence
          // and started again. The composer's control and its panel carry the
          // whole story.
          return null
        }
        if (part.kind === 'steer') {
          return (
            <div className="chat-steer-note" key={part.key} data-testid="chat-steer-note">
              <span>{t('chat.subagent.steerLabel')}</span>
              <p>{part.text}</p>
            </div>
          )
        }
        if (part.kind === 'extension_ui') {
          // The runtime is blocked on this answer; the card is how it gets one.
          return (
            <ExtensionUiCard
              key={part.key}
              request={part.request}
              answer={part.answer}
              onAnswer={onAnswerUi}
            />
          )
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
  // The file the reader asked to look at; one overlay serves both entry points.
  const [previewPath, setPreviewPath] = useState<string | null>(null)
  // The address the right pane's browser holds. The page lives in a native
  // webview the host places over the pane, so the app only remembers where it
  // is and asks for it.
  const [browserUrl, setBrowserUrl] = useState('')
  /**
   * The ring that marks the turn the rail jumped to.
   *
   * It needs a real run on both sides. Messages copied into a branch carry no
   * run id, and `null === undefined` was true, so every message in a forked
   * conversation was ringed at once — a page of boxes.
   */
  const focusRing = (runId: string | null | undefined) =>
    focusedRun !== null && runId != null && focusedRun === runId ? ' is-focused' : ''
  const [dialog, setDialog] = useState<ChatDialogKind>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [notice, setNotice] = useState('')
  const [attachments, setAttachments] = useState<Attachment[]>(sample ? sampleChat.attachments : [])
  const [mentionOpen, setMentionOpen] = useState(false)
  const [attachmentOpen, setAttachmentOpen] = useState(false)
  // Sub-agents: the composer-row control counts them, the floating panel shows
  // one. Archived ids are per conversation and only hide rows — nothing about
  // the ledger changes.
  const [subAgentOpen, setSubAgentOpen] = useState(false)
  const [subAgentActive, setSubAgentActive] = useState<string | null>(null)
  const [archivedAgents, setArchivedAgents] = useState<string[]>([])
  // Messages written while a turn is streaming. They are not sent until the
  // reader says "now" (steer) or the turn ends.
  const [queue, setQueue] = useState<QueuedTurn[]>([])
  /** A queued message handed back to the composer for editing. */
  const [draftRequest, setDraftRequest] = useState<{ id: number; text: string } | null>(null)
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
  //
  // The run id is the precise key, but it is not always paintable: while a turn
  // is starting, the bubble on screen is the optimistic message, which has no
  // run id yet (the host only returns it when the turn ends). The rail, on the
  // other hand, already read the stored row — so a click there used to answer
  // "找不到该轮对话" about a turn the reader was watching. Hence the fallback by
  // turn index, which the row prints anyway.
  const jumpToTurn = (runId: string, turn?: number) => {
    const thread = document.querySelector('[data-testid="chat-thread"]')
    const rows = thread
      ? Array.from(thread.querySelectorAll<HTMLElement>('[data-run]'))
      : []
    let target = runId ? rows.find((node) => node.dataset.run === runId) : undefined
    if (!target && turn && turn > 0) {
      const bubbles = thread
        ? Array.from(thread.querySelectorAll<HTMLElement>('[data-testid="chat-message-user"]'))
        : []
      target = bubbles[turn - 1]
    }
    if (!target) {
      showNotice(t('chat.rail.turnMissing'))
      return
    }
    // `scrollIntoView` is absent in the test DOM and in some webviews; the
    // highlight still lands, so a missing scroll must not abort the jump.
    target.scrollIntoView?.({ block: 'center' })
    const run = target.dataset.run
    if (!run) {
      // The bubble has no run id yet (the turn has not been written back), so
      // the class is the only handle on it. React drops it on the next render,
      // which is what the timer would have done anyway.
      target.classList.add('is-focused')
      window.setTimeout(() => target.classList.remove('is-focused'), 2400)
      return
    }
    setFocusedRun(run)
    window.setTimeout(() => setFocusedRun((current) => (current === run ? null : current)), 2400)
  }

  // A `[n]` marker is a real link to the sources pane: the number in the text
  // and the number in the rail are the same number, from the same read.
  const showSource = useCallback(() => {
    setRailOpen(true)
    setRailTab('sources')
  }, [])

  // A question the runtime asked on its own. The run is blocked until the
  // answer is back, so it travels straight to the host instead of queueing
  // behind the turn that is waiting for it.
  const answerUi = useCallback(
    (request: UiRequest, answer: UiAnswer) => {
      void live.answerUi(request, answer)
    },
    [live],
  )

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
  // A conversation that does not exist yet. Nothing has been said, so there is
  // no transcript to scroll: the page becomes a welcome screen — where it will
  // run, then the field — instead of a large empty column with a field pinned
  // to the bottom.
  const blankPage = liveReady && liveSession === null
  // Who is speaking. A session opened with a saved agent answers under that
  // agent's own name; a session running a plain model answers as the runtime
  // ("pi"). The session row remembers the agent *slug* it was opened with, so a
  // conversation resumed days later still shows the right name. The built-in
  // default agent is derived from the default model and has no persona of its
  // own, so it reads as "pi" like any other model choice.
  const sessionAgent = useMemo(() => {
    const slug = liveSession?.agentId
    if (!slug) return null
    const agent = live.agents.find((item) => item.slug === slug)
    return agent && !agent.isDefault ? agent : null
  }, [liveSession?.agentId, live.agents])
  const speakerName = sessionAgent?.name || liveSession?.runtimeId || ''
  // The ring follows the model the next turn will use, including an agent
  // choice (an agent is a model plus its settings). pi's own number, when a
  // session has one, supplies the occupancy.
  const pickedModel = pickedAgent
    ? live.agents.find((agent) => agent.id === pickedAgent)
    : undefined
  // Models do not carry a window label; saved agents do. The runtime's own
  // number wins once a session has been measured, but a blank page still needs
  // to say what window the next turn will have.
  const windowAgent =
    pickedModel ?? (pickedAgent ? undefined : live.agents.find((agent) => agent.isDefault))
  const contextWindow = live.context?.contextWindow || parseWindowSize(windowAgent?.contextWindow)
  const contextUsed = live.context?.tokens ?? null
  // A steer is stored as a user message *and* as an event of the running turn.
  // The event is what the transcript renders (it sits between the steps it
  // interrupted); printing the message as well would show it twice.
  const liveMessages = (live.current?.messages ?? []).filter(
    (message) => !(message.meta as { steer?: boolean } | undefined)?.steer,
  )
  // The stored ledger and the streaming timeline are two different render
  // paths. When a turn ends the host writes its assistant message before the
  // front-end clears `running`; without this guard both paths draw the *same*
  // run (one "settled", one "正在生成") for the whole await window. Own the
  // running run exclusively in the streaming bubble, exactly like a steer.
  // A steer may add a second run to the same stream, so every run id in the
  // live events counts as "being streamed".
  const streamingRunIds = live.running
    ? new Set(live.turnEvents.map((event) => event.runId).filter((id): id is string => Boolean(id)))
    : new Set<string>()
  const visibleMessages = liveMessages.filter(
    (message) =>
      !(message.role === 'assistant' && message.runId && streamingRunIds.has(message.runId)),
  )
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
  const { parts: partsForRun, usage: usageByRun, shownNotices } = useRunDetail(
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
  // pass gives the thinking/tool stack the transcript should show. Notices the
  // settled turns above already printed are skipped here too.
  // What each turn wrote, so the file list can sit under its own answer.
  const filesByRun = useMemo(() => {
    const map: Record<string, { path: string; relative: string; bytes: number }[]> = {}
    for (const file of live.rail?.artifacts ?? []) {
      ;(map[file.runId] ??= []).push({ path: file.path, relative: file.relative, bytes: file.bytes })
    }
    return map
  }, [live.rail])
  const liveParts = useMemo(
    () => buildTurnParts(live.turnEvents, undefined, new Set(shownNotices)),
    [live.turnEvents, shownNotices],
  )

  // Which agents this conversation has delegated to. The rail holds the stored
  // list; the live turn is fresher for one that is still talking, so the two are
  // merged by the runtime's own id.
  const liveAgents = useMemo(
    () =>
      liveParts
        .filter((part): part is Extract<TurnPart, { kind: 'agent' }> => part.kind === 'agent')
        .map((part) => part.agent),
    [liveParts],
  )
  const subAgents = useMemo(
    () => mergeSubAgents(live.rail?.subagents, liveAgents),
    [live.rail, liveAgents],
  )
  const visibleAgents = useMemo(
    () => subAgents.filter((agent) => !archivedAgents.includes(agent.id)),
    [subAgents, archivedAgents],
  )
  // The panel opens for any agent a row points at, including an archived one:
  // archiving hides a row from the list, it does not delete the transcript.
  const activeAgent = subAgents.find((agent) => agent.id === subAgentActive) ?? null

  // Archive is a view decision, kept per conversation: hiding a finished child
  // must survive a reload without touching the ledger.
  useEffect(() => {
    if (!currentId) {
      setArchivedAgents([])
      return
    }
    try {
      const raw = window.localStorage.getItem(`anvil.chat.subagents.archived.${currentId}`)
      setArchivedAgents(raw ? (JSON.parse(raw) as string[]) : [])
    } catch {
      setArchivedAgents([])
    }
  }, [currentId])

  const archiveSubAgent = (id: string) => {
    setArchivedAgents((previous) => {
      const next = previous.includes(id) ? previous : [...previous, id]
      try {
        if (currentId) {
          window.localStorage.setItem(
            `anvil.chat.subagents.archived.${currentId}`,
            JSON.stringify(next),
          )
        }
      } catch {
        // A locked-down webview keeps the row; nothing else breaks.
      }
      return next
    })
    setSubAgentActive((open) => (open === id ? null : open))
    showNotice(t('chat.subagent.panel.archived'))
  }

  const openSubAgent = (id: string) => {
    setSubAgentActive(id)
    setSubAgentOpen(false)
  }

  // ---- queued messages ----------------------------------------------------
  const enqueue = (text: string, context: ContextRef[]) => {
    const prompt = text.trim()
    if (!prompt) return
    setQueue((previous) => [
      ...previous,
      { id: `q-${Date.now()}-${previous.length}`, text: prompt, refs: context },
    ])
    showNotice(t('chat.queue.queued'))
  }

  const steerQueued = (id: string) => {
    const item = queue.find((entry) => entry.id === id)
    if (!item) return
    void live.steer(item.text).then((accepted) => {
      if (!accepted) {
        showNotice(t('chat.queue.steerFailed'))
        return
      }
      setQueue((previous) => previous.filter((entry) => entry.id !== id))
      showNotice(t('chat.queue.steered'))
    })
  }

  const editQueued = (id: string) => {
    const item = queue.find((entry) => entry.id === id)
    if (!item) return
    setQueue((previous) => previous.filter((entry) => entry.id !== id))
    setDraftRequest({ id: Date.now(), text: item.text })
    showNotice(t('chat.queue.editing'))
  }

  const dropQueued = (id: string) => {
    setQueue((previous) => previous.filter((entry) => entry.id !== id))
  }

  // Dragging a queued row moves it in the send order; the head goes out first
  // when the turn ends, so this is how the reader chooses what goes next.
  const moveQueued = (from: string, to: string) => {
    setQueue((previous) => {
      const start = previous.findIndex((entry) => entry.id === from)
      const target = previous.findIndex((entry) => entry.id === to)
      if (start < 0 || target < 0 || start === target) return previous
      const next = [...previous]
      const [moved] = next.splice(start, 1)
      next.splice(target, 0, moved)
      return next
    })
  }

  // A queue is a promise about the *next* turn: when this one ends, the head
  // goes out. Refs rather than dependencies so the effect fires on the running
  // flag alone instead of on every keystroke that re-creates a callback.
  const queueRef = useRef<QueuedTurn[]>([])
  queueRef.current = queue
  const sendRef = useRef(live.send)
  sendRef.current = live.send
  const sawRun = useRef(false)
  useEffect(() => {
    if (live.running) {
      sawRun.current = true
      return
    }
    if (!sawRun.current) return
    sawRun.current = false
    const next = queueRef.current[0]
    if (!next) return
    setQueue((previous) => previous.slice(1))
    void sendRef.current(next.text, next.refs)
  }, [live.running])

  // A different conversation has its own queue, its own agents and its own
  // drafts; carrying them across would send text into the wrong thread.
  useEffect(() => {
    setQueue([])
    setSubAgentActive(null)
    setSubAgentOpen(false)
  }, [currentId])

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

  // The context ring compacts the *open* session, not a history row. It is
  // offered only when pi is idle and there is a ledger to compress; while a
  // turn runs the ring stays a read-only gauge.
  const [compactingContext, setCompactingContext] = useState(false)
  const compactLiveContext = () => {
    if (!liveSession || live.running || compactingContext) return
    setCompactingContext(true)
    void live
      .compact(liveSession.id, COMPACT_KEEP_LAST)
      .then(() => {
        setNotice(t('chat.live.compacted'))
        return live.refreshContext(liveSession.id)
      })
      .finally(() => setCompactingContext(false))
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
                <span>{speakerName}</span>
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
        // Kept as a screen-reader-only status: the yellow banner used to stay
        // on screen forever after one action. Announce it, but do not paint it.
        <div className="chat-notice is-muted" data-testid="chat-notice" role="status">
          {notice}
        </div>
      ) : null}

      <div className={`chat-layout${railOpen ? ' rail-open' : ''}`} data-testid="chat-layout">
        <div className={`chat-thread-col${blankPage ? ' is-blank' : ''}`}>
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
                {visibleMessages.map((message) =>
                  message.role === 'user' ? (
                    <article
                      className={`chat-msg chat-msg-user${focusRing(message.runId)}`}
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
                      className={`chat-msg${focusRing(message.runId)}`}
                      data-testid="chat-message-agent"
                      data-run={message.runId}
                      key={message.id}
                    >
                      <div className="chat-agent-head">
                        <span className="chat-agent-badge" data-testid="chat-speaker-badge">
                          {speakerName.slice(0, 1)}
                        </span>
                        <span className="chat-agent-name" data-testid="chat-speaker-name">
                          {speakerName}
                        </span>
                      </div>
                      {message.runId && partsWithAnswer[message.runId]?.length ? (
                        <TurnPartsList
                          parts={partsWithAnswer[message.runId]}
                          sources={live.rail?.sources.length ?? 0}
                          onCite={showSource}
                          onAnswerUi={answerUi}
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
                      {message.runId && filesByRun[message.runId]?.length ? (
                        <div className="chat-msg-files" data-testid="chat-message-files">
                          {filesByRun[message.runId].map((file) => (
                            <button
                              key={file.path}
                              type="button"
                              className="chat-file-chip"
                              data-testid="chat-message-file"
                              title={file.path}
                              onClick={() => setPreviewPath(file.path)}
                            >
                              <Icon name="file" size={11} />
                              <span className="chat-file-chip-name">{file.relative}</span>
                              <small>{prettySize(file.bytes)}</small>
                            </button>
                          ))}
                        </div>
                      ) : null}
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
                  agentLabel={speakerName}
                  sources={live.rail?.sources.length ?? 0}
                  onCite={showSource}
                  onAnswerUi={answerUi}
                  onApprove={(decision) => void live.approve(decision)}
                  onRetry={() => void live.retry()}
                />
              </div>
            ) : blankPage ? null : (
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
                    onQueue: enqueue,
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
            onTrust={() => {
              // Focus goes back to the permission select, not to whatever was
              // last clicked: the dialog is reachable from that control only.
              dialogTrigger.current = document.querySelector<HTMLElement>(
                '[data-testid="chat-permission-select"]',
              )
              setDialog('trust')
            }}
            thinking={thinking}
            setThinking={setThinking}
            onNotice={showNotice}
            onProject={(trigger) => openDialog('project', trigger)}
            workspaceLabel={
              workspace.isDefault ? t('workspace.defaultName') : workspace.name
            }
            showWorkspace={sample || blankPage}
            onMention={liveReady ? live.mention : undefined}
            skills={catalog.snapshot?.skills ?? []}
            runtimeId={live.current?.session.runtimeId ?? live.model?.runtimeId ?? ''}
            projectAside={
              sample ? undefined : (
                <div className="chat-subagent-slot">
                  <SubAgentButton
                    agents={visibleAgents}
                    open={subAgentOpen}
                    onToggle={() => setSubAgentOpen((open) => !open)}
                  />
                  {subAgentOpen ? (
                    <SubAgentList
                      agents={visibleAgents}
                      activeId={subAgentActive}
                      onOpen={openSubAgent}
                      onArchive={archiveSubAgent}
                      onClose={() => setSubAgentOpen(false)}
                    />
                  ) : null}
                </div>
              )
            }
            queueAside={
              sample ? undefined : (
                <QueuedMessages
                  items={queue}
                  running={live.running}
                  onSteer={steerQueued}
                  onEdit={editQueued}
                  onDrop={dropQueued}
                  onMove={moveQueued}
                />
              )
            }
            draftRequest={draftRequest}
            contextAside={
              <ContextRing
                windowSize={contextWindow}
                used={contextUsed}
                percent={live.context?.percent ?? null}
                canCompact={Boolean(liveSession) && !live.running}
                compacting={compactingContext}
                onCompact={compactLiveContext}
              />
            }
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
        <ChatRail
          sample={sample}
          live={{
            view: live.rail,
            running: live.running,
            onTurn: jumpToTurn,
            onPreview: setPreviewPath,
            browser: {
              url: browserUrl,
              open: (url) => {
                setBrowserUrl(url)
                void browserOpen(url).catch((cause: unknown) =>
                  showNotice(cause instanceof Error ? cause.message : String(cause)),
                )
              },
              close: () => {
                setBrowserUrl('')
                void browserClose().catch(() => undefined)
              },
              action: (method) => void browserAction(method).catch(() => undefined),
              // A placement hint, not something the reader asked for: a host
              // that cannot place it (no desktop, a closed window) must not
              // raise into the page.
              bounds: (rect) => void browserBounds(rect).catch(() => undefined),
            },
          }}
          open={railOpen}
          tab={railTab}
          onTab={setRailTab}
          onNotice={showNotice}
          onClose={() => {
            setRailOpen(false)
            railToggle.current?.focus()
          }}
        />
        {sample ? null : (
          <SubAgentDrawer
            agent={activeAgent}
            onClose={() => setSubAgentActive(null)}
            onArchive={archiveSubAgent}
            onReturn={(runId, turn) => {
              setSubAgentActive(null)
              jumpToTurn(runId, turn)
            }}
          />
        )}
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
                      onUnarchive: (id) => void history.setArchived(id, false),
                      notice: history.notice,
                      showArchived: history.showArchived,
                      onToggleArchived: () => history.setShowArchived(!history.showArchived),
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
        onConfirmTrust={() => setPermission('full')}
      />

      {previewPath && liveSession ? (
        <FilePreviewOverlay
          sessionId={liveSession.id}
          path={previewPath}
          onClose={() => setPreviewPath(null)}
        />
      ) : null}
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

/** `400K`, `1M`, `128000` → tokens. An unparseable label is not a number. */
export function parseWindowSize(label: string | undefined): number {
  if (!label) return 0
  const match = /^\s*([\d.]+)\s*([kKmM]?)\s*$/.exec(label)
  if (!match) return 0
  const value = Number(match[1])
  if (!Number.isFinite(value) || value <= 0) return 0
  const scale = match[2].toLowerCase() === 'k' ? 1_000 : match[2].toLowerCase() === 'm' ? 1_000_000 : 1
  return Math.round(value * scale)
}

/** How full the model's context window is; the number comes from pi when the
 * session has one, otherwise from the model's advertised window. Hovering opens
 * a themed card with the numbers, and — when idle on a real session — a compact
 * action. While a turn runs the ring is read-only. */
export function ContextRing({
  windowSize,
  used,
  percent,
  canCompact = false,
  compacting = false,
  onCompact,
}: {
  windowSize: number
  used: number | null
  percent: number | null
  /** Only an idle, real session can be compacted. */
  canCompact?: boolean
  compacting?: boolean
  onCompact?: () => void
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const ratio = percent ?? (used !== null && windowSize > 0 ? (used * 100) / windowSize : 0)
  const shown = Math.max(0, Math.min(100, ratio))
  const circumference = 2 * Math.PI * 13
  const label = windowSize > 0 ? (windowSize >= 1_000_000 ? `${windowSize / 1_000_000}M` : `${Math.round(windowSize / 1_000)}K`) : '—'
  const detail =
    used === null
      ? t('chat.context.modelOnly', { window: label })
      : t('chat.context.used', { used: used.toLocaleString(), window: label })
  const tooltip = `${t('chat.context.label', { window: label, percent: Math.round(shown) })} · ${detail}`
  return (
    <span className="chat-context-wrap" onMouseEnter={() => setOpen(true)} onMouseLeave={() => setOpen(false)}>
      <span
        className="chat-context"
        data-testid="chat-context"
        data-percent={shown.toFixed(1)}
        title={tooltip}
        aria-label={tooltip}
        role="img"
        tabIndex={0}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
      >
        <svg width="18" height="18" viewBox="0 0 32 32" aria-hidden="true">
          <circle cx="16" cy="16" r="13" fill="none" stroke="var(--border-strong)" strokeWidth="3" />
          <circle
            cx="16"
            cy="16"
            r="13"
            fill="none"
            stroke="var(--accent)"
            strokeWidth="3"
            strokeLinecap="round"
            strokeDasharray={`${(shown / 100) * circumference} ${circumference}`}
            transform="rotate(-90 16 16)"
          />
        </svg>
      </span>
      {open ? (
        <span className="chat-context-card" role="tooltip" data-testid="chat-context-card">
          <span className="chat-context-card-head">
            <Icon name="sparkle" size={12} />
            <b>{t('chat.context.label', { window: label, percent: Math.round(shown) })}</b>
          </span>
          <span className="chat-context-card-row">{detail}</span>
          {canCompact ? (
            <button
              type="button"
              className="chat-context-compact"
              data-testid="chat-context-compact"
              disabled={compacting}
              onClick={() => {
                onCompact?.()
                setOpen(false)
              }}
            >
              <Icon name="file" size={12} />
              {compacting ? '…' : t('chat.context.compact')}
            </button>
          ) : null}
        </span>
      ) : null}
    </span>
  )
}

/**
 * Messages written while a turn was still streaming.
 *
 * Above the field, not inside it: the draft below is the *next* thing the
 * reader is writing, and the queue is what they already wrote. Sending one into
 * the live run is a deliberate click — pi folds it in at its next step.
 */
export function QueuedMessages({
  items,
  running,
  onSteer,
  onEdit,
  onDrop,
  onMove,
}: {
  items: QueuedTurn[]
  running: boolean
  onSteer: (id: string) => void
  onEdit: (id: string) => void
  onDrop: (id: string) => void
  /** Reorder by dragging: `from` lands where `to` was. */
  onMove: (from: string, to: string) => void
}) {
  const { t } = useI18n()
  const [dragging, setDragging] = useState<string | null>(null)
  if (items.length === 0) return null
  return (
    <div className="chat-queue" data-testid="chat-queue">
      <ul className="chat-queue-list" title={t('chat.queue.hint')}>
        {items.map((item) => (
          <li
            key={item.id}
            className={`chat-queue-item${dragging === item.id ? ' is-dragging' : ''}`}
            data-testid="chat-queue-item"
            draggable
            onDragStart={(event) => {
              setDragging(item.id)
              event.dataTransfer.effectAllowed = 'move'
              // A drag needs a payload before the browser will start it.
              event.dataTransfer.setData('text/plain', item.id)
            }}
            onDragOver={(event) => {
              event.preventDefault()
              event.dataTransfer.dropEffect = 'move'
            }}
            onDrop={(event) => {
              event.preventDefault()
              const from = event.dataTransfer.getData('text/plain') || dragging
              if (from && from !== item.id) onMove(from, item.id)
              setDragging(null)
            }}
            onDragEnd={() => setDragging(null)}
          >
            <span className="chat-queue-grip" aria-hidden="true">
              <Icon name="grip" size={13} />
            </span>
            <p title={item.text}>{item.text}</p>
            <div className="chat-queue-actions">
              <button
                type="button"
                className="chat-queue-btn"
                data-testid="chat-queue-steer"
                disabled={!running}
                aria-label={t('chat.queue.steer')}
                title={running ? t('chat.queue.steer') : t('chat.queue.steerFailed')}
                onClick={() => onSteer(item.id)}
              >
                <Icon name="send" size={12} />
              </button>
              <button
                type="button"
                className="chat-queue-btn"
                data-testid="chat-queue-edit"
                aria-label={t('chat.queue.edit')}
                title={t('chat.queue.edit')}
                onClick={() => onEdit(item.id)}
              >
                <Icon name="pen" size={12} />
              </button>
              <button
                type="button"
                className="chat-queue-btn"
                data-testid="chat-queue-drop"
                aria-label={t('chat.queue.remove')}
                title={t('chat.queue.remove')}
                onClick={() => onDrop(item.id)}
              >
                <Icon name="trash" size={12} />
              </button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  )
}

type MenuOption = {
  value: string
  label: string
  detail?: string
  /** Only this row is the red "完全访问" warning — never the whole menu. */
  danger?: boolean
  disabled?: boolean
}
type MenuGroup = { label?: string; options: MenuOption[] }

/**
 * The composer's selects were native <select> elements. WebView2 paints their
 * popup with the OS palette (white strips in dark mode), so they could never
 * match the theme; tinting the danger one red also tinted every option. This is
 * a DOM listbox: themed in both modes, keyboard-driven, and only the one
 * dangerous row — plus its trigger once chosen — wears red.
 */
function ComposerMenu({
  testId,
  menuTestId,
  label,
  tip,
  icon,
  value,
  groups,
  disabled,
  danger,
  onPick,
  width = 320,
}: {
  testId: string
  menuTestId: string
  label: string
  tip?: string
  icon: string
  value: string
  groups: MenuGroup[]
  disabled?: boolean
  danger?: boolean
  onPick: (value: string) => void
  width?: number
}) {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ left: number; bottom: number } | null>(null)
  const [active, setActive] = useState(0)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const popRef = useRef<HTMLDivElement>(null)

  const options = groups.flatMap((group) => group.options)
  const currentLabel = options.find((option) => option.value === value)?.label ?? value

  const place = () => {
    const rect = triggerRef.current?.getBoundingClientRect()
    if (!rect) return
    setPos({
      left: Math.min(Math.max(8, rect.left), Math.max(8, window.innerWidth - width - 8)),
      bottom: window.innerHeight - rect.top + 8,
    })
  }
  const show = () => {
    if (disabled) return
    place()
    setActive(Math.max(0, options.findIndex((option) => option.value === value)))
    setOpen(true)
  }
  const hide = (focus = false) => {
    setOpen(false)
    if (focus) triggerRef.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    const onOutside = (event: Event) => {
      const node = event.target as Node | null
      if (!node) return
      const path = typeof event.composedPath === 'function' ? event.composedPath() : []
      if (triggerRef.current && (triggerRef.current.contains(node) || path.includes(triggerRef.current))) return
      if (popRef.current && (popRef.current.contains(node) || path.includes(popRef.current))) return
      setOpen(false)
    }
    const onMove = () => place()
    document.addEventListener('pointerdown', onOutside, true)
    document.addEventListener('mousedown', onOutside, true)
    window.addEventListener('resize', onMove)
    window.addEventListener('scroll', onMove, true)
    return () => {
      document.removeEventListener('pointerdown', onOutside, true)
      document.removeEventListener('mousedown', onOutside, true)
      window.removeEventListener('resize', onMove)
      window.removeEventListener('scroll', onMove, true)
    }
  }, [open, width])

  // Roving focus: exactly one row is tabbable, and it follows the arrow keys.
  useEffect(() => {
    if (!open) return
    popRef.current?.querySelectorAll<HTMLElement>('[data-menu-option]')[active]?.focus()
  }, [open, active])

  const pick = (option: MenuOption) => {
    if (option.disabled) return
    hide()
    onPick(option.value)
  }
  const onTriggerKey = (event: KeyboardEvent) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp' || event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      if (!open) show()
    } else if (event.key === 'Escape' && open) {
      event.preventDefault()
      hide()
    }
  }
  const onMenuKey = (event: KeyboardEvent) => {
    const enabled = options.map((option, index) => ({ option, index })).filter((row) => !row.option.disabled)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const direction = event.key === 'ArrowDown' ? 1 : -1
      const at = enabled.findIndex((row) => row.index === active)
      const next = enabled[(at + direction + enabled.length) % enabled.length]
      if (next) setActive(next.index)
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      const next = event.key === 'Home' ? enabled[0] : enabled[enabled.length - 1]
      if (next) setActive(next.index)
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      if (options[active]) pick(options[active])
    } else if (event.key === 'Escape') {
      event.preventDefault()
      hide(true)
    } else if (event.key === 'Tab') {
      hide()
    }
  }

  let index = -1
  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`chat-ctl chat-menu-trigger${danger ? ' is-danger' : ''}${open ? ' is-open' : ''}`}
        data-testid={testId}
        data-value={value}
        aria-label={label}
        title={tip ?? label}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => (open ? hide() : show())}
        onKeyDown={onTriggerKey}
      >
        <Icon name={icon} size={13} />
        <span className="chat-menu-value">{currentLabel}</span>
        <Icon name="chevDown" size={11} />
      </button>
      {open && pos ? (
        <div
          ref={popRef}
          className="chat-menu-pop"
          data-testid={menuTestId}
          role="listbox"
          aria-label={label}
          style={{ left: pos.left, bottom: pos.bottom, width }}
          onKeyDown={onMenuKey}
        >
          {groups.map((group, groupIndex) => (
            <div className="chat-menu-group" key={groupIndex}>
              {group.label ? <div className="chat-menu-group-label">{group.label}</div> : null}
              {group.options.map((option) => {
                index += 1
                const row = index
                const selected = option.value === value
                return (
                  <button
                    key={`${option.value}:${row}`}
                    type="button"
                    role="option"
                    data-menu-option
                    data-testid={`${menuTestId}-option`}
                    data-value={option.value}
                    aria-selected={selected}
                    disabled={option.disabled}
                    className={`chat-menu-option${selected ? ' is-selected' : ''}${option.danger ? ' is-danger' : ''}`}
                    tabIndex={row === active ? 0 : -1}
                    onMouseEnter={() => setActive(row)}
                    onClick={() => pick(option)}
                  >
                    <span className="chat-menu-option-main">
                      <span className="chat-menu-option-label">{option.label}</span>
                      {option.detail ? <span className="chat-menu-option-detail">{option.detail}</span> : null}
                    </span>
                    {selected ? <Icon name="check" size={13} /> : null}
                  </button>
                )
              })}
            </div>
          ))}
        </div>
      ) : null}
    </>
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
  onTrust,
  thinking,
  setThinking,
  onNotice,
  onProject,
  workspaceLabel,
  showWorkspace,
  onMention,
  skills,
  runtimeId,
  projectAside,
  queueAside,
  draftRequest,
  contextAside,
}: {
  sample: boolean
  live: LiveComposer | null
  language: string
  pickedAgent: string
  setPickedAgent: (id: string) => void
  attachments: Attachment[]
  setAttachments: Dispatch<SetStateAction<Attachment[]>>
  mentionOpen: boolean
  setMentionOpen: (open: boolean) => void
  attachmentOpen: boolean
  setAttachmentOpen: (open: boolean) => void
  /** Trust level the next turn runs with; see `PermissionMode`. */
  permission: PermissionMode
  setPermission: (next: PermissionMode) => void
  /** Ask the page for the confirmation a switch to `full` requires. */
  onTrust: () => void
  /** Thinking level the next turn runs with; see `ThinkingLevel`. */
  thinking: ThinkingLevel
  setThinking: (next: ThinkingLevel) => void
  onNotice: (message: string) => void
  onProject: (trigger: HTMLElement) => void
  /** Directory the next conversation runs in, shown on the project chip. */
  workspaceLabel: string
  /** The workspace chip only belongs on the blank page; hide it once a turn starts. */
  showWorkspace: boolean
  /** Ask the host which files this project offers for `@`. */
  onMention?: (query: string) => Promise<ContextEntry[]>
  /** Installed skills from the live catalog, filtered to this session's runtime. */
  skills: CatalogSkill[]
  runtimeId: string
  /** The sub-agent control, shown next to the workspace chip. */
  projectAside?: ReactNode
  /** Messages queued behind the running turn, shown above the field. */
  queueAside?: ReactNode
  /** A queued message handed back for editing; the id makes each handoff fire once. */
  draftRequest?: { id: number; text: string } | null
  /** Context-window occupancy, below the field. */
  contextAside?: ReactNode
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

  // A queued message handed back for editing lands in the field, caret at the
  // end, ready to be re-sent. Keyed by the handoff id so re-renders do not keep
  // overwriting whatever the reader has typed since.
  useEffect(() => {
    if (!draftRequest) return
    setText(draftRequest.text)
    caretTarget.current = draftRequest.text.length
  }, [draftRequest])

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
    if (!live || !text.trim()) return
    // A turn that is still streaming does not refuse the message: it goes to
    // the queue above the field, where the reader can send it into the live
    // run, edit it, or withdraw it before it is ever sent.
    if (live.running) {
      live.onQueue(text, refs(text))
    } else {
      live.onSend(text, refs(text))
    }
    setText('')
    // The chips were the turn's context, and that turn has been handed to the
    // host: leaving them above an empty field reads as "these will go with the
    // next message too", which is not what happened. The transcript keeps the
    // record (each user message prints its own chips) and the rail lists them.
    setAttachments([])
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

  /** Adds picked paths as context chips; the host reads them on send. */
  const attachPicked = (paths: string[], kind: 'file' | 'dir') => {
    if (paths.length === 0) return
    setAttachments((current) => {
      const next = [...current]
      for (const path of paths) {
        const name = path.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || path
        const index = next.findIndex((item) => item.path === path)
        const chip = { id: `picked:${path}`, name, kind, path }
        // Re-picking a path that is already attached must not create a second
        // chip, and must not drop the size a mention already knew about.
        if (index >= 0) next[index] = { ...next[index], ...chip, size: next[index].size }
        else next.push(chip)
      }
      return next
    })
  }

  /** The drawer's file entry: native multi-select picker, then attach them all. */
  const addFilesFromPicker = async () => {
    setAttachmentOpen(false)
    try {
      const picked = await pickWorkspaceFiles()
      if (picked.length > 0) {
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
        attachPicked([picked], 'dir')
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
  const selectedModel = live?.model ? modelKey(live.model) : ''

  // The model list is the same one the native select grouped: saved agents that
  // bind an enabled model first, then every enabled model. `agent:` values keep
  // the persona binding the picker used to carry.
  const usableAgents = live
    ? live.agents.filter((agent) =>
        live.models.some(
          (choice) => choice.runtimeId === agent.runtimeId && choice.modelId === agent.modelId,
        ),
      )
    : []
  const modelGroups: MenuGroup[] = sample
    ? [{ options: [{ value: sampleChat.model, label: sampleChat.model }] }]
    : live && live.models.length > 0
      ? [
          ...(usableAgents.length > 0
            ? [
                {
                  label: t('chat.composer.agents'),
                  options: usableAgents.map((agent) => ({
                    value: `agent:${agent.id}`,
                    label: agent.isDefault ? t('chat.composer.defaultAgent') : agent.name,
                    detail: `${agent.runtimeId} · ${agent.modelId}`,
                  })),
                },
              ]
            : []),
          {
            label: t('chat.composer.models'),
            options: live.models.map((choice) => ({
              value: modelKey(choice),
              label: `${choice.runtimeId} · ${choice.modelId}`,
              detail: choice.displayName,
            })),
          },
        ]
      : [
          {
            options: [
              {
                value: '',
                label: live ? t('chat.live.noModel') : t('chat.composer.normalPlaceholder'),
                disabled: true,
              },
            ],
          },
        ]
  const modelValue = sample
    ? sampleChat.model
    : pickedAgent
      ? `agent:${pickedAgent}`
      : selectedModel
  const pickModel = (value: string) => {
    if (value.startsWith('agent:')) {
      const agent = live?.agents.find((item) => item.id === value.slice(6))
      const choice = agent
        ? live?.models.find(
            (item) => item.runtimeId === agent.runtimeId && item.modelId === agent.modelId,
          )
        : undefined
      if (!agent || !choice) return
      setPickedAgent(agent.id)
      live?.onModel(choice)
      return
    }
    setPickedAgent('')
    const choice = live?.models.find((item) => modelKey(item) === value)
    if (choice) live?.onModel(choice)
  }

  const thinkingGroups: MenuGroup[] = [
    { options: THINKING_LEVELS.map((level) => ({ value: level, label: THINKING_LABELS[level] })) },
  ]
  const thinkingValue = sample ? sampleChat.thinkingLevel : thinking
  const permissionGroups: MenuGroup[] = [
    {
      options: [
        { value: 'readonly', label: t('chat.composer.permission.readonly') },
        { value: 'medium', label: t('chat.composer.permission.medium') },
        { value: 'high', label: t('chat.composer.permission.high') },
        { value: 'full', label: t('chat.composer.permission.full'), danger: true },
      ],
    },
  ]

  return (
    <div className="chat-composer-wrap">
      {showWorkspace || projectAside ? (
        <div className="chat-composer-project-row">
          {showWorkspace ? (
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
          ) : null}
          {projectAside}
        </div>
      ) : null}
      {queueAside}
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
            <div className="chat-pick-head">
              <Icon name={mentionMode === 'slash' ? 'puzzle' : 'sparkle'} size={13} />
              <b>
                {mentionMode === 'slash'
                  ? t('chat.composer.skillPicker')
                  : t('chat.composer.mention')}
              </b>
              <span className="chat-pick-hint">{t('chat.composer.pickerHint')}</span>
            </div>
            {mentionHits.length === 0 ? (
              <span className="chat-mention-empty" data-testid="chat-mention-empty">
                {mentionMode === 'slash'
                  ? t('chat.composer.skillEmpty')
                  : t('chat.composer.mentionEmpty')}
              </span>
            ) : (
              <div className="chat-pick-items">
                {mentionHits.map((hit, index) => (
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
                    <span className={`chat-pick-icon is-${hit.kind}`}>
                      <Icon name={hit.kind === 'skill' ? 'puzzle' : hit.kind === 'dir' ? 'folder' : 'file'} size={13} />
                    </span>
                    <span className="chat-mention-name">{hit.name}</span>
                    <small>{hit.detail}</small>
                  </button>
                ))}
              </div>
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
            <div className="chat-pick-head">
              <Icon name="plus" size={13} />
              <b>{t('chat.composer.addContext')}</b>
            </div>
            <div className="chat-attachment-group">
              <div className="chat-pick-group-label">{t('chat.composer.attachLocal')}</div>
              <button
                type="button"
                role="menuitem"
                data-testid="chat-add-file"
                disabled={!enabled || sample}
                title={sample ? t('chat.action.unavailable') : t('chat.composer.chooseFilesDesc')}
                onClick={() => void addFilesFromPicker()}
              >
                <span className="chat-pick-icon is-file"><Icon name="file" size={14} /></span>
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
                <span className="chat-pick-icon is-dir"><Icon name="folder" size={14} /></span>
                <span>
                  <b>{t('chat.composer.chooseDir')}</b>
                  <small>{t('chat.composer.chooseDirDesc')}</small>
                </span>
              </button>
            </div>
            <div className="chat-attachment-group">
              <div className="chat-pick-group-label">{t('chat.composer.skill')}</div>
              <button
                type="button"
                role="menuitem"
                data-testid="chat-add-skill"
                disabled={!enabled || sample}
                title={sample ? t('chat.action.unavailable') : t('chat.composer.skillDesc')}
                onClick={addSkill}
              >
                <span className="chat-pick-icon is-skill"><Icon name="puzzle" size={14} /></span>
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
          <ComposerMenu
            testId="chat-model-select"
            menuTestId="chat-model-menu"
            label={t('chat.composer.model')}
            icon="sparkle"
            value={modelValue}
            groups={modelGroups}
            disabled={!enabled}
            onPick={pickModel}
          />
          {live ? (
            <SyncButton
              label={t('chat.live.syncModels')}
              busy={live.syncing}
              onClick={live.onSync}
              testId="chat-models-sync"
            />
          ) : null}
          <ComposerMenu
            testId="chat-thinking-select"
            menuTestId="chat-thinking-menu"
            label="Thinking effort"
            icon="brain"
            value={thinkingValue}
            groups={thinkingGroups}
            disabled={!enabled}
            width={180}
            onPick={(value) => setThinking(value as ThinkingLevel)}
          />
          {/* A trusted workspace is the one mode that stops asking, so it is
              also the one mode that reads as a warning: red trigger once chosen,
              and the switch only happens after the reader confirms in a dialog.
              Only the "full" option is red — the other three stay neutral. */}
          <ComposerMenu
            testId="chat-permission-select"
            menuTestId="chat-permission-menu"
            label={t('chat.composer.permission')}
            tip={t('chat.composer.permission.hint')}
            icon="shield"
            value={permission}
            groups={permissionGroups}
            disabled={!enabled}
            danger={permission === 'full'}
            width={220}
            onPick={(next) => {
              if (next === 'full' && permission !== 'full') {
                onTrust()
                return
              }
              setPermission(next as PermissionMode)
            }}
          />
          {contextAside}
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
              title={
                sendShortcut === 'enter'
                  ? t('chat.composer.sendTip.enter')
                  : t('chat.composer.sendTip.ctrlEnter')
              }
              disabled={!enabled || !text.trim()}
            >
              <Icon name="send" size={14} />
            </button>
          </div>
        </div>
      </form>
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
  agentLabel,
  sources,
  onCite,
  onAnswerUi,
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
  /** Who answers this turn: a saved agent's name, or the runtime ("pi"). */
  agentLabel: string
  sources: number
  onCite: () => void
  onAnswerUi?: (request: UiRequest, answer: UiAnswer) => void
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
            <span className="chat-agent-badge" data-testid="chat-speaker-badge">
              {agentLabel.slice(0, 1)}
            </span>
            <span className="chat-agent-name" data-testid="chat-speaker-name">
              {agentLabel}
            </span>
            {/* No "generating" badge next to the name: the typing dots under
                the last block already say the turn is live. */}
          </div>
          {parts.length > 0 ? (
            <TurnPartsList
              parts={parts}
              live
              sources={sources}
              onCite={onCite}
              onAnswerUi={onAnswerUi}
            />
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
function LiveToolCard({
  tool,
}: {
  tool: { id?: string; name: string; args: string; ok: boolean | null; output: string; images?: PiImage[] }
}) {
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
      {/* Pictures are the tool's answer, not debug detail: show them in place
          even while the text output is folded away. */}
      {tool.images && tool.images.length > 0 ? (
        <div className="chat-tool-images" data-testid="chat-tool-images">
          {tool.images.map((image, index) => (
            <figure key={`${tool.id}-image-${index}`}>
              <img src={imageSrc(image)} alt={t('chat.tool.imageAlt')} />
              <figcaption>{t('chat.tool.imageCaption', { tool: tool.name })}</figcaption>
            </figure>
          ))}
        </div>
      ) : null}
      {open ? <pre className="chat-tool-body">{tool.output || tool.args}</pre> : null}
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

