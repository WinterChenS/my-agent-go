// Typed access to the workspace commands.
//
// A "workspace" is the directory a conversation runs in: the host stores it as
// the session's `project_path`, and the history panel groups conversations by
// it. These commands only resolve and create directories; nothing is written to
// them until a turn actually edits a file.

import { invokeHost, isTauriHost } from '../host/tauri'

export interface WorkspaceDefaults {
  /** `<home>/Anvil`, created on first read. */
  defaultWorkspace: string
  /** `<home>/Anvil/Projects`, where projects made here are placed. */
  projectsRoot: string
}

function call<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  if (!isTauriHost()) return Promise.reject(new Error('the workspace commands need the desktop host'))
  return invokeHost<T>(command, args)
}

export function loadWorkspaceDefaults(): Promise<WorkspaceDefaults> {
  return call<WorkspaceDefaults>('workspace_defaults')
}

/** Canonical path of a directory the user picked, or a reason it cannot be used. */
export function ensureWorkspace(path: string): Promise<string> {
  return call<string>('workspace_ensure', { path })
}

/** Create one folder under the projects root and return its path. */
export function createWorkspace(name: string): Promise<string> {
  return call<string>('workspace_create', { name })
}

/** Native folder picker; `null` when the user cancelled. */
export function pickWorkspace(): Promise<string | null> {
  return call<string | null>('workspace_pick')
}

/** Native file picker; the file's parent directory becomes the workspace. */
export function pickWorkspaceFile(): Promise<string | null> {
  return call<string | null>('workspace_pick_file')
}

/**
 * Native *multi*-select file picker, for the composer's attachments.
 *
 * Resolves to every chosen path, and to an empty list when the dialog was
 * cancelled: a turn may legitimately attach several files, so one call has to
 * be able to return more than one path.
 */
export function pickWorkspaceFiles(): Promise<string[]> {
  return call<string[]>('workspace_pick_files')
}
