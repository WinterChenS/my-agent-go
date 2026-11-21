// Typed access to the resource library.
//
// The library is not a table: it is the set of files sessions actually wrote
// inside their project directories, plus whatever the user imported. The host
// scans on read, so a file renamed or deleted outside the app is reflected on
// the next load instead of lingering as a stale row.

import { invokeHost, isTauriHost } from '../../host/tauri'

export type AssetKind = 'doc' | 'sheet' | 'page' | 'chart' | 'code' | 'other'

export interface LibraryAsset {
  id: string
  name: string
  path: string
  relative: string
  ext: string
  kind: AssetKind
  bytes: number
  session: string
  sessionId: string
  /** RFC 3339, from the file's modification time. */
  at: string
  /** `session` (a turn wrote it) or `library` (imported). */
  source: string
}

export interface LibraryStats {
  assets: number
  bytes: number
  weekAdded: number
  sessions: number
  imported: number
  byKind: Partial<Record<AssetKind, number>>
}

export interface LibraryView {
  assets: LibraryAsset[]
  stats: LibraryStats
  root: string
}

export interface ImportReport {
  imported: LibraryAsset[]
  skipped: string[]
  root: string
}

export function hasLibraryHost(): boolean {
  return isTauriHost()
}

export async function loadLibrary(): Promise<LibraryView> {
  return invokeHost<LibraryView>('library_overview')
}

export async function importLibrary(paths: string[]): Promise<ImportReport> {
  return invokeHost<ImportReport>('library_import', { paths })
}

export async function exportLibrary(paths: string[]): Promise<string> {
  return invokeHost<string>('library_export', { paths })
}

export async function previewAsset(path: string): Promise<string> {
  return invokeHost<string>('library_preview', { path })
}

export async function revealAsset(path: string): Promise<void> {
  await invokeHost('library_reveal', { path })
}

/** `18.4 KB`, `1.2 MB` — one decimal above a kilobyte. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  if (bytes < 1024) return `${Math.round(bytes)} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let index = 0
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024
    index += 1
  }
  return `${value >= 10 ? Math.round(value) : value.toFixed(1)} ${units[index]}`
}
