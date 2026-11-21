// Typed access to the workbench's real data.
//
// The desktop host joins the session ledger, the persona catalog and the files
// sessions wrote. A plain browser has none of those, so the caller checks
// `isTauriHost()` and keeps the honest empty state instead of inventing rows.

import { invokeHost, isTauriHost } from '../../host/tauri'

export interface WorkbenchStats {
  sessionsThisWeek: number
  sessionsDelta: number
  activeSessions: number
  toolCalls: number
  toolResults: number
  toolFailures: number
  /** null when no tool reported an outcome. */
  toolSuccessRate: number | null
  tokens: number
  tokensInput: number
  tokensOutput: number
  tokensReported: boolean
  pendingApprovals: number
}

export interface ActivityCell {
  date: string
  count: number
  runs: number
  tools: number
  future: boolean
}

export interface WorkbenchActivity {
  cells: ActivityCell[]
  total: number
  activeDays: number
  longestStreak: number
}

export interface WorkbenchChat {
  id: string
  title: string
  agent: string
  model: string
  updatedAt: string
  turns: number
  messages: number
  pending: number
  status: string
  preview: string
}

export interface WorkbenchArtifact {
  name: string
  relative: string
  path: string
  ext: string
  bytes: number
  session: string
  sessionId: string
  at: string
}

export interface WorkbenchAgent {
  id: string
  name: string
  initial: string
  model: string
  runtimeId: string
  skills: number
  runs: number
  enabled: boolean
  isDefault: boolean
}

export interface WorkbenchStatus {
  runtimeReady: number
  runtimeTotal: number
  runtimeLabel: string
  mcpConnected: number
  mcpTotal: number
  connectorsAvailable: boolean
  quotaAvailable: boolean
}

export interface WorkbenchSources {
  ledger: boolean
  catalog: boolean
  filesystem: boolean
  schedules: boolean
  connectors: boolean
  quota: boolean
}

export interface WorkbenchOverview {
  today: string
  weeks: number
  gridStart: string
  gridEnd: string
  stats: WorkbenchStats
  activity: WorkbenchActivity
  chats: WorkbenchChat[]
  artifacts: WorkbenchArtifact[]
  agents: WorkbenchAgent[]
  status: WorkbenchStatus
  sources: WorkbenchSources
}

export type WorkbenchWeeks = 12 | 26 | 53

export class WorkbenchUnavailable extends Error {
  constructor() {
    super('the workbench needs the desktop host')
    this.name = 'WorkbenchUnavailable'
  }
}

/** Whether this window can actually answer the command. */
export function hasWorkbenchHost(): boolean {
  return isTauriHost()
}

/**
 * Read one overview. `utcOffsetMinutes` is `-Date#getTimezoneOffset()`:
 * the ledger writes UTC while the heatmap is a local calendar.
 */
export function loadWorkbenchOverview(weeks: WorkbenchWeeks = 53): Promise<WorkbenchOverview> {
  if (!isTauriHost()) return Promise.reject(new WorkbenchUnavailable())
  return invokeHost<WorkbenchOverview>('workbench_overview', {
    weeks,
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
  })
}

export function formatCount(value: number, language: string): string {
  return new Intl.NumberFormat(language).format(value)
}

/** Compact but honest: 1.2M means 1,200,000, not an estimate. */
export function formatTokens(value: number, language: string): string {
  if (value >= 1_000_000) {
    return `${new Intl.NumberFormat(language, { maximumFractionDigits: 1 }).format(value / 1_000_000)}M`
  }
  if (value >= 1_000) {
    return `${new Intl.NumberFormat(language, { maximumFractionDigits: 1 }).format(value / 1_000)}K`
  }
  return formatCount(value, language)
}

export function formatBytes(value: number, language: string): string {
  if (value >= 1024 * 1024) {
    return `${new Intl.NumberFormat(language, { maximumFractionDigits: 1 }).format(value / 1024 / 1024)} MB`
  }
  if (value >= 1024) {
    return `${new Intl.NumberFormat(language, { maximumFractionDigits: 1 }).format(value / 1024)} KB`
  }
  return `${formatCount(value, language)} B`
}

/** Local relative time; `Intl` produces the right wording in both languages. */
export function relativeTime(stamp: string, language: string): string {
  const time = Date.parse(stamp)
  if (!Number.isFinite(time)) return ''
  const seconds = Math.round((time - Date.now()) / 1000)
  const absolute = Math.abs(seconds)
  const relative = new Intl.RelativeTimeFormat(language, { numeric: 'auto' })
  if (absolute < 60) return relative.format(seconds, 'second')
  if (absolute < 3600) return relative.format(Math.round(seconds / 60), 'minute')
  if (absolute < 86_400) return relative.format(Math.round(seconds / 3600), 'hour')
  return relative.format(Math.round(seconds / 86_400), 'day')
}

/** A `YYYY-MM-DD` cell date, formatted without a timezone shift. */
export function formatDay(date: string, language: string): string {
  const parsed = new Date(`${date}T12:00:00`)
  if (Number.isNaN(parsed.getTime())) return date
  return new Intl.DateTimeFormat(language, { year: 'numeric', month: 'long', day: 'numeric' }).format(parsed)
}

export function monthOf(date: string, language: string): string {
  const parsed = new Date(`${date}T12:00:00`)
  if (Number.isNaN(parsed.getTime())) return ''
  return new Intl.DateTimeFormat(language, { month: 'short' }).format(parsed)
}
