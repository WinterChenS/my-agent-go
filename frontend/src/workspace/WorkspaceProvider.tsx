// The directory the next conversation runs in.
//
// One value, shared by the chat page (which passes it to `session_open`), the
// composer (which shows what it is) and the project dialog (which changes it).
// It is per window rather than per session: opening an old conversation also
// adopts that conversation's project, so "continue where I was" and "start
// something new here" agree.

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
import { isTauriHost, reportHostLog } from '../host/tauri'
import {
  createWorkspace,
  ensureWorkspace,
  loadWorkspaceDefaults,
  pickWorkspace,
  pickWorkspaceFile,
} from './client'

export interface Workspace {
  /** Absolute path, or `null` before the host answers / outside the desktop. */
  path: string | null
  /** The last path component, for buttons and headings. */
  name: string
  /** `<home>/Anvil` unless the user chose another default — where sessions live. */
  defaultPath: string | null
  /** `<home>/Anvil/Projects` — where projects made here are created. */
  projectsRoot: string | null
  ready: boolean
  error: string | null
  /** True when the current path is the default workspace. */
  isDefault: boolean
  /** Validate and switch to a directory; resolves to the canonical path. */
  choose: (path: string) => Promise<string>
  /** Open the native folder picker and switch to what it returns. */
  browse: () => Promise<string | null>
  /** Open the native file picker and switch to the file's parent directory. */
  pickFile: () => Promise<string | null>
  /** Create a project folder under the projects root and switch to it. */
  create: (name: string) => Promise<string>
  /** Make a directory the default workspace and switch to it. */
  setDefault: (path: string) => Promise<string>
  /** Follow a conversation's own project without re-validating it. */
  adopt: (path: string | null) => void
  /** Back to the default workspace. */
  reset: () => Promise<void>
  /** Re-read the error state after a dialog showed it. */
  clearError: () => void
}

const WorkspaceContext = createContext<Workspace | null>(null)

/** The chosen directory survives reloads and route changes. */
const WORKSPACE_KEY = 'anvil.chat.workspace'
/** The default can be changed by the user; kept separately from the choice. */
const DEFAULT_KEY = 'anvil.chat.defaultWorkspace'

function readStored(): string | null {
  try {
    return window.localStorage.getItem(WORKSPACE_KEY)
  } catch {
    return null
  }
}

function writeStored(path: string | null) {
  try {
    if (path) window.localStorage.setItem(WORKSPACE_KEY, path)
    else window.localStorage.removeItem(WORKSPACE_KEY)
  } catch {
    // A locked-down webview must not break the page over a preference.
  }
}

function readStoredDefault(): string | null {
  try {
    return window.localStorage.getItem(DEFAULT_KEY)
  } catch {
    return null
  }
}

function writeStoredDefault(path: string | null) {
  try {
    if (path) window.localStorage.setItem(DEFAULT_KEY, path)
    else window.localStorage.removeItem(DEFAULT_KEY)
  } catch {
    // Same as above: preferences are best-effort.
  }
}

/** Last path component of a Windows or POSIX path. */
export function workspaceName(path: string | null): string {
  if (!path) return ''
  const trimmed = path.replace(/[\\/]+$/, '')
  const cut = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  return cut === -1 ? trimmed : trimmed.slice(cut + 1)
}

/** Directory that contains `path`, for the file picker (Windows or POSIX). */
export function parentPath(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, '')
  const cut = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  if (cut <= 0) return trimmed
  // Keep the drive root (`C:\`) and UNC share intact.
  if (cut === 2 && trimmed[1] === ':') return trimmed.slice(0, cut + 1)
  return trimmed.slice(0, cut)
}

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const [path, setPath] = useState<string | null>(null)
  const [defaultPath, setDefaultPath] = useState<string | null>(null)
  const [projectsRoot, setProjectsRoot] = useState<string | null>(null)
  const [ready, setReady] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const apply = useCallback((next: string | null) => {
    setError(null)
    setPath(next)
    writeStored(next)
  }, [])

  // Read the defaults once. A remembered directory that has since been deleted
  // (or moved) falls back to the default instead of leaving the page pointing at
  // something that no longer exists. A user-chosen default wins over the host's
  // `<home>/Anvil` and is validated the same way.
  useEffect(() => {
    if (!isTauriHost()) {
      setReady(true)
      return
    }
    let cancelled = false
    void loadWorkspaceDefaults()
      .then(async (defaults) => {
        if (cancelled) return
        // A host that answers something else is not a host with directories:
        // leaving `path` null keeps the page honest instead of inventing one.
        if (!defaults || typeof defaults.defaultWorkspace !== 'string') {
          throw new Error('the host did not report a workspace')
        }
        setProjectsRoot(defaults.projectsRoot)

        let effectiveDefault = defaults.defaultWorkspace
        const storedDefault = readStoredDefault()
        if (storedDefault) {
          try {
            effectiveDefault = await ensureWorkspace(storedDefault)
          } catch {
            if (!cancelled) writeStoredDefault(null)
          }
        }
        if (cancelled) return
        setDefaultPath(effectiveDefault)

        const stored = readStored()
        if (!stored) {
          setPath(effectiveDefault)
          return
        }
        try {
          const valid = await ensureWorkspace(stored)
          if (!cancelled) setPath(valid)
        } catch {
          if (!cancelled) {
            // Drop the stale value instead of rewriting the default: "no stored
            // choice" and "the default" are the same state, and keeping the old
            // path around would retry it on every reload.
            writeStored(null)
            setPath(effectiveDefault)
            setError('the remembered workspace is gone; switched back to the default')
          }
        }
      })
      .catch((cause) => {
        if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause))
      })
      .finally(() => {
        if (!cancelled) setReady(true)
      })
    return () => {
      cancelled = true
    }
  }, [apply])

  const choose = useCallback(
    async (next: string) => {
      reportHostLog(`workspace: choose ${next}`)
      const valid = await ensureWorkspace(next)
      if (mounted.current) apply(valid)
      return valid
    },
    [apply],
  )

  const browse = useCallback(async () => {
    setError(null)
    reportHostLog('workspace: browse')
    const picked = await pickWorkspace()
    if (!picked) return null
    // Canonicalise the picker's raw path so it matches ledger group keys.
    const valid = await ensureWorkspace(picked)
    if (mounted.current) apply(valid)
    return valid
  }, [apply])

  const pickFile = useCallback(async () => {
    setError(null)
    reportHostLog('workspace: pick file')
    const picked = await pickWorkspaceFile()
    if (!picked) return null
    const valid = await ensureWorkspace(parentPath(picked))
    if (mounted.current) apply(valid)
    return valid
  }, [apply])

  const create = useCallback(
    async (name: string) => {
      setError(null)
      reportHostLog(`workspace: create ${name}`)
      const created = await createWorkspace(name)
      if (mounted.current) apply(created)
      return created
    },
    [apply],
  )

  const setDefault = useCallback(
    async (next: string) => {
      reportHostLog(`workspace: set default ${next}`)
      const valid = await ensureWorkspace(next)
      if (!mounted.current) return valid
      writeStoredDefault(valid)
      setDefaultPath(valid)
      apply(valid)
      return valid
    },
    [apply],
  )

  const adopt = useCallback(
    (next: string | null) => {
      if (!next) return
      if (next === path) return
      apply(next)
    },
    [apply, path],
  )

  const reset = useCallback(async () => {
    if (defaultPath) apply(defaultPath)
  }, [apply, defaultPath])

  const clearError = useCallback(() => setError(null), [])

  const value = useMemo<Workspace>(
    () => ({
      path,
      name: workspaceName(path),
      defaultPath,
      projectsRoot,
      ready,
      error,
      isDefault: Boolean(path && defaultPath && path === defaultPath),
      choose,
      browse,
      pickFile,
      create,
      setDefault,
      adopt,
      reset,
      clearError,
    }),
    [path, defaultPath, projectsRoot, ready, error, choose, browse, pickFile, create, setDefault, adopt, reset, clearError],
  )

  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>
}

/** The shared workspace; inert (no host) outside the desktop app. */
export function useWorkspace(): Workspace {
  return useContext(WorkspaceContext) ?? IDLE_WORKSPACE
}

const IDLE_WORKSPACE: Workspace = {
  path: null,
  name: '',
  defaultPath: null,
  projectsRoot: null,
  ready: true,
  error: null,
  isDefault: false,
  choose: async () => '',
  browse: async () => null,
  pickFile: async () => null,
  create: async () => '',
  setDefault: async () => '',
  adopt: () => undefined,
  reset: async () => undefined,
  clearError: () => undefined,
}
