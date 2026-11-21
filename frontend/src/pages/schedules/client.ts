// Typed access to schedules, which are rows in the session ledger.
//
// A schedule is real work: the host's tick opens a session for the agent and
// sends the prompt. The view therefore carries the runs themselves, not just a
// status word, so the detail panel can show what actually happened at 09:00.

import { invokeHost, isTauriHost } from '../../host/tauri'

export interface ScheduleRecord {
  id: string
  name: string
  cron: string
  timeZone: string
  utcOffsetMinutes: number
  agentId: string
  agentName: string
  runtimeId: string
  model: string
  prompt: string
  permission: string
  outputTarget: string
  enabled: boolean
  lastRunAt: string
  lastStatus: string
  lastDurationMs: number
  nextRunAt: string
  consecutiveFailures: number
  createdAt: string
  updatedAt: string
}

export interface ScheduleRun {
  scheduleId: string
  scheduledAt: string
  state: string
  reason: string
  sessionId: string
  runId: string
  startedAt: string
  finishedAt: string
  durationMs: number
}

export interface ScheduleStats {
  total: number
  enabled: number
  running: number
  todayRuns: number
  weekRuns: number
  weekFailed: number
  successRate: number | null
  avgDurationMs: number | null
  nextRunAt: string | null
}

export interface ScheduleTrendPoint {
  date: string
  runs: number
  failed: number
}

export interface ScheduleView {
  schedules: ScheduleRecord[]
  runs: ScheduleRun[]
  stats: ScheduleStats
  trend: ScheduleTrendPoint[]
}

export interface ScheduleDraft {
  id?: string
  name: string
  cron: string
  timeZone: string
  utcOffsetMinutes: number
  agentId: string
  prompt: string
  permission: string
  outputTarget: string
  enabled: boolean
}

export interface RunNowReport {
  sessionId: string
  runId: string
  status: string
  error?: string | null
}

/** Emitted by the host after a fire, so an open page re-reads the ledger. */
export const SCHEDULE_EVENT = 'anvil://schedules-changed'

/** The offset the host needs to read cron on the viewer's wall clock. */
export function utcOffsetMinutes(): number {
  return -new Date().getTimezoneOffset()
}

export function localTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

export function hasScheduleHost(): boolean {
  return isTauriHost()
}

export async function loadSchedules(): Promise<ScheduleView> {
  return invokeHost<ScheduleView>('schedule_overview', {
    utcOffsetMinutes: utcOffsetMinutes(),
  })
}

export async function saveSchedule(draft: ScheduleDraft): Promise<ScheduleRecord> {
  return invokeHost<ScheduleRecord>('schedule_save', { draft })
}

export async function deleteSchedule(id: string): Promise<void> {
  await invokeHost('schedule_delete', { id })
}

export async function toggleSchedule(id: string, enabled: boolean): Promise<ScheduleRecord> {
  return invokeHost<ScheduleRecord>('schedule_toggle', { id, enabled })
}

export async function runScheduleNow(id: string): Promise<RunNowReport> {
  return invokeHost<RunNowReport>('schedule_run_now', { id })
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  if (minutes < 60) return rest === 0 ? `${minutes}m` : `${minutes}m${String(rest).padStart(2, '0')}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h${String(minutes % 60).padStart(2, '0')}m`
}
