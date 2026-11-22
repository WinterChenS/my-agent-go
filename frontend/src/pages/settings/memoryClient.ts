// Host calls for 设置 → 记忆.
//
// Every mutation answers with the whole overview, so the page never merges a
// local guess with the host's counts: what it shows is what the ledger holds.
import { invokeHost } from '../../host/tauri'

export type MemoryKind = 'preference' | 'fact' | 'constraint' | 'workflow'
export type MemoryScope = 'user' | 'session'

export interface MemoryRow {
  id: string
  scope: MemoryScope
  sessionId: string
  sessionTitle: string
  agentId: string
  kind: MemoryKind
  text: string
  /** `manual` | `agent` | `session`. */
  source: string
  pinned: boolean
  enabled: boolean
  createdAt: string
  updatedAt: string
}

export interface MemorySettings {
  inject: boolean
  recordSessions: boolean
}

export interface MemoryOverview {
  settings: MemorySettings
  user: MemoryRow[]
  session: MemoryRow[]
  counts: { user: number; session: number; enabled: number }
  database: string
}

export interface MemoryDraft {
  scope?: MemoryScope
  sessionId?: string
  agentId?: string
  kind?: MemoryKind
  text: string
  source?: 'manual' | 'agent' | 'session'
  pinned?: boolean
}

export interface MemoryPatch {
  text?: string
  kind?: MemoryKind
  pinned?: boolean
  enabled?: boolean
}

export function loadMemories(query?: string): Promise<MemoryOverview> {
  return invokeHost<MemoryOverview>('memory_overview', { query })
}

export function saveMemory(memory: MemoryDraft, query?: string): Promise<MemoryOverview> {
  return invokeHost<MemoryOverview>('memory_save', { memory, query })
}

export function updateMemory(id: string, patch: MemoryPatch, query?: string): Promise<MemoryOverview> {
  return invokeHost<MemoryOverview>('memory_update', { id, patch, query })
}

export function deleteMemory(id: string, query?: string): Promise<MemoryOverview> {
  return invokeHost<MemoryOverview>('memory_delete', { id, query })
}

export function clearMemories(scope?: MemoryScope, query?: string): Promise<MemoryOverview> {
  return invokeHost<MemoryOverview>('memory_clear', { scope, query })
}

export function setMemorySwitches(
  next: { inject?: boolean; recordSessions?: boolean },
): Promise<MemoryOverview> {
  return invokeHost<MemoryOverview>('memory_set_switches', next)
}

/** A host error arrives as `{code,message}` JSON inside the rejection message. */
export function memoryError(error: unknown): string {
  const text = error instanceof Error ? error.message : String(error)
  const start = text.indexOf('{')
  if (start >= 0) {
    try {
      const parsed = JSON.parse(text.slice(start)) as { code?: string; message?: string }
      if (parsed.message) return parsed.code ? `${parsed.code}: ${parsed.message}` : parsed.message
    } catch {
      // Not the host shape; keep the raw text.
    }
  }
  return text
}
