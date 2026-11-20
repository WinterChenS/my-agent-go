// One ledger list shared by the sidebar and the chat page.
//
// The history panel lives in the shell sidebar, but the conversation it lists
// belongs to the chat page. Two independent reads would drift — after a send
// the sidebar would still show the list it loaded at startup — and clicking a
// row in one place would not move the other. So the shell owns the list, and
// the chat page asks it which session is open.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useNavigate } from 'react-router-dom'
import {
  archiveSession,
  compactSession,
  deleteSession,
  forkSession,
  listProjects,
  listSessions,
  pinSession,
  renameSession,
  type ProjectSummary,
  type SessionSummary,
} from './client'
import { isTauriHost } from '../host/tauri'
import { useI18n } from '../i18n/LanguageProvider'
import { useChatSample } from '../pages/chat/sampleData'

export interface ChatHistory {
  sessions: SessionSummary[]
  projects: ProjectSummary[]
  /** The session the chat page is showing; `null` means a new one. */
  activeId: string | null
  /** True once the first read finished, so the panel can tell empty from loading. */
  ready: boolean
  error: string | null
  /**
   * One line about the last action: what it did, or why it could not.
   *
   * Every row action used to end in silence — a request that failed, a menu
   * that closed, nothing on screen. The panel now always has an answer to
   * "did that work?".
   */
  notice: string | null
  /** Whether the notice reports a success or a refusal. */
  noticeTone: 'ok' | 'error'
  dismissNotice: () => void
  /** Archived sessions are hidden unless the panel asks for them. */
  showArchived: boolean
  setShowArchived: (next: boolean) => void
  refresh: () => Promise<void>
  /** Record which session the chat page moved to (no navigation). */
  setActive: (id: string | null) => void
  /**
   * Ask for a blank conversation. A counter rather than `activeId === null`:
   * a null active id also happens for one render while a new session is being
   * created, and treating that as "start over" would wipe the turn in flight.
   */
  newSeq: number
  startNew: () => void
  /** Open a session: switch to the chat route and show it. */
  open: (id: string) => void
  rename: (id: string, title: string) => Promise<void>
  setPinned: (id: string, pinned: boolean) => Promise<void>
  setArchived: (id: string, archived: boolean) => Promise<void>
  remove: (id: string) => Promise<void>
  fork: (id: string) => Promise<string | null>
  compact: (id: string, keepLast: number) => Promise<void>
}

const HistoryContext = createContext<ChatHistory | null>(null)

/** How many rows the panel shows; the ledger pages beyond this on demand. */
const HISTORY_LIMIT = 50

/**
 * The session the page was last showing.
 *
 * The chat page is one route among many: leaving it unmounts the conversation,
 * and coming back used to start from a blank page even though nothing had
 * closed the conversation. Remembering the id makes the return land where the
 * reader left, including on a run that is still going.
 */
const ACTIVE_KEY = 'anvil.chat.session'

function readActive(): string | null {
  try {
    return window.localStorage.getItem(ACTIVE_KEY)
  } catch {
    return null
  }
}

function writeActive(id: string | null) {
  try {
    if (id) window.localStorage.setItem(ACTIVE_KEY, id)
    else window.localStorage.removeItem(ACTIVE_KEY)
  } catch {
    // A locked-down webview is not a reason to lose the session on screen.
  }
}

export function ChatHistoryProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate()
  const sample = useChatSample()
  const { t } = useI18n()
  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [activeId, setActiveId] = useState<string | null>(() => readActive())
  const [newSeq, setNewSeq] = useState(0)
  const [ready, setReady] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [noticeTone, setNoticeTone] = useState<'ok' | 'error'>('ok')
  const [showArchived, setShowArchived] = useState(false)
  const mounted = useRef(true)
  const activeRef = useRef<string | null>(null)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  // A confirmation is an answer, not a banner: it goes away on its own.
  useEffect(() => {
    if (!notice) return
    const timer = window.setTimeout(() => setNotice(null), 6000)
    return () => window.clearTimeout(timer)
  }, [notice])

  const dismissNotice = useCallback(() => setNotice(null), [])

  const setActive = useCallback((id: string | null) => {
    activeRef.current = id
    setActiveId(id)
    writeActive(id)
  }, [])

  const refresh = useCallback(async () => {
    if (!isTauriHost() || sample) {
      setReady(true)
      return
    }
    try {
      // `includeArchived` is what makes the archive action reversible: the rows
      // come back when the panel asks for them.
      const [page, index] = await Promise.all([
        listSessions({ limit: HISTORY_LIMIT, includeArchived: showArchived }),
        listProjects(),
      ])
      if (!mounted.current) return
      setSessions(page.sessions)
      setProjects(index)
      setError(null)
      // A session deleted elsewhere must not stay the active one. Only decide
      // that from a complete list: an old conversation beyond the first page is
      // missing from `sessions` without having been deleted, and dropping it
      // would blank the page the reader just came back to.
      const complete = page.sessions.length < HISTORY_LIMIT
      if (
        complete &&
        activeRef.current &&
        !page.sessions.some((row) => row.id === activeRef.current)
      ) {
        setActive(null)
      }
    } catch (cause) {
      if (mounted.current) setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (mounted.current) setReady(true)
    }
  }, [sample, setActive, showArchived])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const startNew = useCallback(() => {
    setActive(null)
    setNewSeq((current) => current + 1)
  }, [setActive])

  const open = useCallback(
    (id: string) => {
      setActive(id)
      navigate('/chat')
    },
    [navigate, setActive],
  )

  /**
   * Run one row action and say what happened.
   *
   * Failures used to disappear: `finally` refreshed the list and the rejected
   * promise went nowhere, so a refused rename looked exactly like a menu that
   * did nothing. The list is refreshed either way, and the reason is put on
   * screen.
   */
  const run = useCallback(
    async <T,>(action: () => Promise<T>, done: string | ((value: T) => string)) => {
      try {
        const value = await action()
        if (mounted.current) {
          setNoticeTone('ok')
          setNotice(typeof done === 'function' ? done(value) : done)
        }
      } catch (cause) {
        const reason = cause instanceof Error ? cause.message : String(cause)
        if (mounted.current) {
          setNoticeTone('error')
          setNotice(t('chat.history.failed', { reason }))
        }
      } finally {
        await refresh()
      }
    },
    [refresh, t],
  )

  const rename = useCallback(
    (id: string, title: string) =>
      run(() => renameSession(id, title), t('chat.history.renamed')),
    [run, t],
  )

  const setPinned = useCallback(
    (id: string, pinned: boolean) =>
      run(() => pinSession(id, pinned), pinned ? t('chat.history.pinned') : t('chat.history.unpinned')),
    [run, t],
  )

  const setArchived = useCallback(
    (id: string, archived: boolean) =>
      run(
        async () => {
          await archiveSession(id, archived)
          if (archived && activeRef.current === id) setActive(null)
        },
        archived ? t('chat.history.archived') : t('chat.history.unarchived'),
      ),
    [run, setActive, t],
  )

  const remove = useCallback(
    (id: string) =>
      run(
        async () => {
          await deleteSession(id)
          if (activeRef.current === id) setActive(null)
        },
        t('chat.history.deleted'),
      ),
    [run, setActive, t],
  )

  const fork = useCallback(
    async (id: string) => {
      let created: string | null = null
      await run(async () => {
        const branch = await forkSession(id)
        created = branch.id
        open(branch.id)
      }, t('chat.history.forked'))
      return created
    },
    [open, run, t],
  )

  const compact = useCallback(
    (id: string, keepLast: number) =>
      run(
        () => compactSession(id, keepLast),
        (report) =>
          t('chat.history.compacted', { folded: report.collapsed, kept: report.kept }),
      ),
    [run, t],
  )

  const value = useMemo<ChatHistory>(
    () => ({
      sessions,
      projects,
      activeId,
      newSeq,
      ready,
      error,
      notice,
      noticeTone,
      dismissNotice,
      showArchived,
      setShowArchived,
      refresh,
      setActive,
      startNew,
      open,
      rename,
      setPinned,
      setArchived,
      remove,
      fork,
      compact,
    }),
    [
      sessions,
      projects,
      activeId,
      newSeq,
      ready,
      error,
      notice,
      noticeTone,
      dismissNotice,
      showArchived,
      setShowArchived,
      refresh,
      setActive,
      startNew,
      open,
      rename,
      setPinned,
      setArchived,
      remove,
      fork,
      compact,
    ],
  )

  return <HistoryContext.Provider value={value}>{children}</HistoryContext.Provider>
}

/**
 * The shared history. Outside the provider (unit tests of one page) it degrades
 * to an empty, inert list instead of throwing: a missing sidebar must not take
 * the chat down.
 */
export function useChatHistory(): ChatHistory {
  const value = useContext(HistoryContext)
  if (value) return value
  return EMPTY_HISTORY
}

const EMPTY_HISTORY: ChatHistory = {
  sessions: [],
  projects: [],
  activeId: null,
  newSeq: 0,
  ready: true,
  error: null,
  notice: null,
  noticeTone: 'ok',
  dismissNotice: () => undefined,
  showArchived: false,
  setShowArchived: () => undefined,
  refresh: async () => undefined,
  setActive: () => undefined,
  startNew: () => undefined,
  open: () => undefined,
  rename: async () => undefined,
  setPinned: async () => undefined,
  setArchived: async () => undefined,
  remove: async () => undefined,
  fork: async () => null,
  compact: async () => undefined,
}
