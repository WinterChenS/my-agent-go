import { useEffect, useMemo, useRef, useState, type FocusEvent, type MouseEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { WORKBENCH_ROUTE } from '../../shell/routes'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import {
  formatBytes,
  formatCount,
  formatDay,
  formatTokens,
  hasWorkbenchHost,
  loadWorkbenchOverview,
  monthOf,
  relativeTime,
  type WorkbenchOverview,
  type WorkbenchWeeks,
} from './client'
import { sampleWorkbench } from './sampleData'
import './workbench.css'

export function useWorkbenchSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/workbench') return true
  return new URLSearchParams(location.search).get('sample') === 'workbench'
}

/** `09:00` in the viewer's clock, from a UTC ledger stamp. */
function clockOf(stamp: string): string {
  const time = Date.parse(stamp)
  if (!Number.isFinite(time)) return '—'
  return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(new Date(time))
}

function scheduleStatus(
  row: { lastStatus: string; enabled: boolean },
  t: (key: MessageKey) => string,
): string {
  if (row.lastStatus === 'running') return t('workbench.schedules.running')
  if (row.lastStatus === 'success') return t('workbench.schedules.success')
  if (row.lastStatus === 'failed' || row.lastStatus === 'unknown') return t('workbench.schedules.failed')
  return row.enabled ? t('workbench.schedules.never') : t('workbench.schedules.paused')
}

type StatKey = 'sessions' | 'toolCalls' | 'tokens' | 'pending'
type ButtonMode = 'sample' | 'live' | 'unavailable'

interface StatView {
  key: StatKey
  value: string
  sub: string
}

interface CellView {
  date: string
  count: number
  level: number
  future: boolean
  label: string
  detail: string
}

interface ActivityView {
  cells: CellView[]
  weeks: number
  total: string
  activeDays: string
  streak: string
  monthLabels: Array<{ column: number; label: string }>
}

interface ChatView {
  id: string
  title: string
  meta: string
  badge?: string
}

interface ArtifactView {
  name: string
  ext: string
  meta: string
  badge: string
  tone?: 'success'
}

interface AgentView {
  initial: string
  name: string
  meta: string
  ready: boolean
  label: string
}

interface ScheduleView {
  time: string
  title: string
  sub: string
  state: 'done' | 'now' | 'upcoming'
}

interface StatusView {
  label: string
  value: string
  live: boolean
}

/** Everything the cards render. Both the sample fixtures and the real ledger
 *  are mapped into this shape, so no card can accidentally render a fixture
 *  when the real entry is open. */
interface WorkbenchView {
  subtitle: string
  greetingName?: string
  stats: StatView[]
  activity: ActivityView
  chats: ChatView[]
  artifacts: ArtifactView[]
  agents: AgentView[]
  schedules: ScheduleView[]
  status: StatusView[]
  quota: string
  empty: {
    chats: string
    artifacts: string
    agents: string
    schedules: string
    status: string
    activity: string
  }
  foot: string
  artifactSubtitle: string
  sourceNote: string
}

interface Actions {
  mode: ButtonMode
  notice: (message: string) => void
  go: (path: string) => void
  newChat: () => void
  resume: (id: string) => void
}

const STAT_LABELS: Record<StatKey, MessageKey> = {
  sessions: 'workbench.stats.sessions',
  toolCalls: 'workbench.stats.toolCalls',
  tokens: 'workbench.stats.tokens',
  pending: 'workbench.stats.pending',
}

function greetingKey(now = new Date()): MessageKey {
  const hour = now.getHours()
  if (hour < 12) return 'workbench.greeting.morning'
  if (hour < 18) return 'workbench.greeting.afternoon'
  return 'workbench.greeting.evening'
}

function activityLevel(count: number): number {
  if (!count) return 0
  if (count <= 3) return 1
  if (count <= 6) return 2
  if (count <= 10) return 3
  return 4
}

function dayCount(date: Date): number {
  const seed = date.getFullYear() * 19 + (date.getMonth() + 1) * 37 + date.getDate() * 53
  const weekday = date.getDay()
  if (seed % 7 === 0 || (weekday === 0 && seed % 3 === 0)) return 0
  let base = (seed % 9) + 1
  if (weekday === 0 || weekday === 6) base = Math.max(1, Math.floor(base * 0.55))
  if (seed % 13 === 0) base += 8
  return base
}

function monthLabels(cells: CellView[], language: string): Array<{ column: number; label: string }> {
  const labels: Array<{ column: number; label: string }> = []
  let previous = ''
  cells.forEach((cell, index) => {
    if (index % 7 !== 0) return
    const month = monthOf(cell.date, language)
    if (month !== previous) {
      labels.push({ column: Math.floor(index / 7) + 1, label: month })
      previous = month
    }
  })
  return labels
}

/** Design-only fixtures, isolated behind `/?sample=workbench`. */
function sampleView(
  weeks: WorkbenchWeeks,
  language: string,
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
): WorkbenchView {
  const today = new Date()
  today.setHours(12, 0, 0, 0)
  const end = new Date(today)
  end.setDate(end.getDate() + (6 - end.getDay()))
  const start = new Date(end)
  start.setDate(start.getDate() - weeks * 7 + 1)
  const cells: CellView[] = []
  for (let week = 0; week < weeks; week += 1) {
    const weekStart = new Date(start)
    weekStart.setDate(start.getDate() + week * 7)
    for (let day = 0; day < 7; day += 1) {
      const date = new Date(weekStart)
      date.setDate(weekStart.getDate() + day)
      const future = date > today
      const dateKey = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
      const count = future ? 0 : dayCount(date)
      cells.push({
        date: dateKey,
        count,
        level: activityLevel(count),
        future,
        label: `${formatDay(dateKey, language)} · ${count}`,
        detail: '',
      })
    }
  }
  return {
    subtitle: '',
    greetingName: sampleWorkbench.greetingName,
    stats: sampleWorkbench.stats.map((stat) => ({
      key: stat.key,
      value: stat.value,
      sub: t(stat.sub as MessageKey),
    })),
    activity: {
      cells,
      weeks,
      total: sampleWorkbench.activity.completed,
      activeDays: sampleWorkbench.activity.activeDays,
      streak: sampleWorkbench.activity.streak,
      monthLabels: monthLabels(cells, language),
    },
    chats: sampleWorkbench.chats.map((chat) => ({
      id: '',
      title: chat.title,
      meta: chat.meta,
      badge: chat.status ? 'pending' : undefined,
    })),
    artifacts: sampleWorkbench.artifacts.map((artifact) => ({
      name: artifact.name,
      ext: artifact.extension,
      meta: artifact.meta,
      badge: artifact.status,
      tone: artifact.status === 'published' ? 'success' : undefined,
    })),
    agents: sampleWorkbench.agents.map((agent) => ({
      initial: agent.initial,
      name: agent.name,
      meta: agent.meta,
      ready: true,
      label: '',
    })),
    schedules: sampleWorkbench.schedules.map((schedule) => ({ ...schedule })),
    status: sampleWorkbench.status.map((row) => ({ label: '', value: row.value, live: true })),
    quota: sampleWorkbench.quota,
    empty: { chats: '', artifacts: '', agents: '', schedules: '', status: '', activity: '' },
    foot: '',
    artifactSubtitle: '',
    sourceNote: '',
  }
}

/** The real entry: one host response, no estimate and no fixture. */
function liveView(
  overview: WorkbenchOverview,
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
  language: string,
): WorkbenchView {
  const { stats, activity, status, sources } = overview
  const cells: CellView[] = activity.cells.map((cell) => ({
    date: cell.date,
    count: cell.count,
    level: activityLevel(cell.count),
    future: cell.future,
    label: t('workbench.activity.cell', {
      date: formatDay(cell.date, language),
      count: formatCount(cell.count, language),
    }),
    detail: t('workbench.activity.cellBreakdown', { runs: cell.runs, tools: cell.tools }),
  }))
  const count = (value: number) => formatCount(value, language)
  const signed = `${stats.sessionsDelta >= 0 ? '+' : ''}${count(stats.sessionsDelta)}`
  return {
    subtitle: t('workbench.subtitle.live', {
      sessions: count(stats.sessionsThisWeek),
      tools: count(stats.toolCalls),
      active: count(stats.activeSessions),
    }),
    stats: [
      {
        key: 'sessions',
        value: count(stats.sessionsThisWeek),
        sub: t('workbench.stats.sessionsDeltaLive', { delta: signed }),
      },
      {
        key: 'toolCalls',
        value: count(stats.toolCalls),
        sub:
          stats.toolSuccessRate === null
            ? t('workbench.stats.successRateMissing')
            : t('workbench.stats.successRateLive', { rate: stats.toolSuccessRate.toFixed(1) }),
      },
      {
        key: 'tokens',
        value: stats.tokensReported ? formatTokens(stats.tokens, language) : '—',
        sub: stats.tokensReported
          ? t('workbench.stats.tokensSubLive', {
              input: formatTokens(stats.tokensInput, language),
              output: formatTokens(stats.tokensOutput, language),
            })
          : t('workbench.stats.tokensMissing'),
      },
      {
        key: 'pending',
        value: count(stats.pendingApprovals),
        sub: t('workbench.stats.pendingSubLive'),
      },
    ],
    activity: {
      cells,
      weeks: overview.weeks,
      total: count(activity.total),
      activeDays: count(activity.activeDays),
      streak: count(activity.longestStreak),
      monthLabels: monthLabels(cells, language),
    },
    chats: overview.chats.map((chat) => ({
      id: chat.id,
      title: chat.title || t('workbench.chats.untitled'),
      meta: t('workbench.chats.meta', {
        agent: chat.agent || chat.model || '—',
        time: relativeTime(chat.updatedAt, language) || chat.updatedAt,
        turns: count(chat.turns),
      }),
      badge:
        chat.pending > 0
          ? t('workbench.status.pending')
          : chat.status === 'running'
            ? t('workbench.chats.running')
            : undefined,
    })),
    artifacts: overview.artifacts.map((artifact) => ({
      name: artifact.name,
      ext: artifact.ext.toUpperCase() || 'FILE',
      meta: t('workbench.artifacts.meta', {
        session: artifact.session || '—',
        size: formatBytes(artifact.bytes, language),
        time: relativeTime(artifact.at, language) || artifact.at,
      }),
      badge: t('workbench.status.localFile'),
    })),
    agents: overview.agents.map((agent) => ({
      initial: agent.initial || 'A',
      name: agent.name,
      meta: t('workbench.agents.meta', {
        model: agent.model || '—',
        skills: count(agent.skills),
        runs: count(agent.runs),
      }),
      ready: agent.enabled,
      label: agent.enabled ? t('workbench.agents.ready') : t('workbench.agents.disabled'),
    })),
    schedules: overview.schedules.map((row) => ({
      time: clockOf(row.nextRunAt || row.lastRunAt),
      title: row.name,
      sub: t('workbench.schedules.meta', {
        agent: row.agent || '—',
        status: scheduleStatus(row, t),
      }),
      state: row.enabled ? 'upcoming' : 'done',
    })),
    status: [
      {
        label: t('workbench.status.runtime'),
        value: status.runtimeTotal
          ? t('workbench.status.runtimeValue', {
              label: status.runtimeLabel || '—',
              ready: count(status.runtimeReady),
              total: count(status.runtimeTotal),
            })
          : t('workbench.status.noRuntime'),
        live: sources.catalog && status.runtimeReady > 0,
      },
      {
        label: t('workbench.status.mcp'),
        value: t('workbench.status.mcpValue', {
          connected: count(status.mcpConnected),
          total: count(status.mcpTotal),
        }),
        live: sources.catalog && status.mcpConnected > 0,
      },
      {
        label: t('workbench.status.connectors'),
        value: sources.connectors
          ? t('workbench.status.connectorsValue', { count: count(status.mcpTotal) })
          : t('workbench.status.unavailable'),
        live: sources.connectors && status.mcpTotal > 0,
      },
    ],
    quota: sources.quota ? '—' : t('workbench.status.quotaUnavailable'),
    empty: {
      chats: t('workbench.chats.emptyLive'),
      artifacts: t('workbench.artifacts.emptyLive'),
      agents: t('workbench.agents.emptyLive'),
      schedules: t('workbench.schedules.unavailable'),
      status: t('workbench.status.empty'),
      activity: t('workbench.activity.emptyLive'),
    },
    foot: t('workbench.activity.footLive'),
    artifactSubtitle: t('workbench.artifacts.subtitleLive', { count: count(overview.artifacts.length) }),
    sourceNote: t('workbench.sources.note'),
  }
}

function ActionButton({
  labelKey,
  mode,
  onAction,
  primary = false,
  small = false,
}: {
  labelKey: MessageKey
  mode: ButtonMode
  onAction: () => void
  primary?: boolean
  small?: boolean
}) {
  const { t } = useI18n()
  const className = `btn${primary ? ' btn-primary' : ''}${small ? ' btn-sm' : ''}`
  if (mode === 'unavailable') {
    return (
      <button type="button" className={className} disabled title={t('workbench.action.unavailable')}>
        {t(labelKey)}
      </button>
    )
  }
  return (
    <button
      type="button"
      className={className}
      title={mode === 'sample' ? t('workbench.action.sampleNotice') : undefined}
      onClick={onAction}
    >
      {t(labelKey)}
    </button>
  )
}

/** One card action: sample mode only emits a notice, the real entry navigates. */
function CardAction({
  labelKey,
  path,
  actions,
}: {
  labelKey: MessageKey
  path: string
  actions: Actions
}) {
  const { t } = useI18n()
  return (
    <ActionButton
      labelKey={labelKey}
      mode={actions.mode}
      small
      onAction={() =>
        actions.mode === 'sample'
          ? actions.notice(t('workbench.action.sampleNotice'))
          : actions.go(path)
      }
    />
  )
}

function WorkbenchStats({ view }: { view: WorkbenchView | null }) {
  const { t } = useI18n()
  return (
    <section className="wb-stats" aria-labelledby="wb-stats-title">
      <h2 id="wb-stats-title" className="sr-only">
        {t('workbench.stats.title')}
      </h2>
      <div className="wb-stat-grid">
        {Object.entries(STAT_LABELS).map(([key, labelKey]) => {
          const stat = view?.stats.find((entry) => entry.key === key)
          return (
            <div className="wb-stat" key={key} data-stat={key} data-source={view ? 'live' : 'empty'}>
              <div className="wb-stat-label">{t(labelKey)}</div>
              <div className="wb-stat-value">{stat?.value ?? '0'}</div>
              <div className="wb-stat-sub">{stat ? stat.sub || t('workbench.stats.emptySub') : t('workbench.stats.emptySub')}</div>
            </div>
          )
        })}
      </div>
    </section>
  )
}

interface TooltipState {
  label: string
  detail: string
  left: number
  top: number
}

function ActivityHeatmap({
  view,
  weeks,
  onWeeks,
  refreshing,
}: {
  view: WorkbenchView | null
  weeks: WorkbenchWeeks
  onWeeks: (weeks: WorkbenchWeeks) => void
  refreshing: boolean
}) {
  const { t } = useI18n()
  const cardRef = useRef<HTMLElement>(null)
  const [tooltip, setTooltip] = useState<TooltipState | null>(null)
  const show = (cell: CellView, event: MouseEvent<HTMLButtonElement> | FocusEvent<HTMLButtonElement>) => {
    const card = cardRef.current?.getBoundingClientRect()
    const rect = event.currentTarget.getBoundingClientRect()
    if (!card) return
    setTooltip({
      label: cell.label,
      detail: cell.detail,
      left: Math.max(12, rect.left - card.left - 80 + rect.width / 2),
      top: Math.max(48, rect.top - card.top - 38),
    })
  }
  const hide = () => setTooltip(null)

  return (
    <section className="wb-card wb-activity" aria-labelledby="wb-activity-title" ref={cardRef}>
      <div className="wb-card-head">
        <h3 id="wb-activity-title">{t('workbench.activity.title')}</h3>
        <span className="wb-card-sub">{t('workbench.activity.subtitle')}</span>
        <div className="wb-card-actions">
          {refreshing ? <span className="wb-refreshing" role="status">{t('workbench.refreshing')}</span> : null}
          <select
            className="wb-select"
            aria-label={t('workbench.activity.title')}
            value={weeks}
            disabled={!view}
            onChange={(event) => onWeeks(Number(event.target.value) as WorkbenchWeeks)}
          >
            <option value={12}>{t('workbench.activity.range12')}</option>
            <option value={26}>{t('workbench.activity.range26')}</option>
            <option value={53}>{t('workbench.activity.range53')}</option>
          </select>
        </div>
      </div>
      {!view ? (
        <div className="wb-card-body">
          <p className="wb-empty">{t('workbench.activity.empty')}</p>
        </div>
      ) : (
        <div className="wb-card-body">
          <div className="wb-activity-summary">
            <div>
              <b>
                {t(weeks === 53 ? 'workbench.activity.summaryYear' : 'workbench.activity.summaryWeeks', {
                  count: view.activity.total,
                  weeks,
                })}
              </b>
              <span>{view.foot || t('workbench.activity.foot')}</span>
            </div>
            <div className="wb-activity-metrics">
              <span>
                <b>{view.activity.activeDays}</b>
                <small>{t('workbench.activity.activeDays')}</small>
              </span>
              <span>
                <b>{view.activity.total}</b>
                <small>{t('workbench.activity.completed')}</small>
              </span>
              <span>
                <b>{view.activity.streak}</b>
                <small>{t('workbench.activity.streak')}</small>
              </span>
            </div>
          </div>
          <div className="wb-activity-scroll">
            <div className="wb-activity-chart" style={{ ['--wb-activity-weeks' as string]: weeks }}>
              <div className="wb-activity-months" aria-hidden="true">
                {view.activity.monthLabels.map((marker) => (
                  <span key={`${marker.column}-${marker.label}`} style={{ gridColumn: `${marker.column} / span 4` }}>
                    {marker.label}
                  </span>
                ))}
              </div>
              <div className="wb-activity-row">
                <div className="wb-activity-weekdays" aria-hidden="true">
                  <span>{t('workbench.activity.mon')}</span>
                  <span>{t('workbench.activity.wed')}</span>
                  <span>{t('workbench.activity.fri')}</span>
                </div>
                <div className="wb-activity-grid" role="grid" aria-label={t('workbench.activity.title')}>
                  {view.activity.cells.map((cell) => (
                    <button
                      key={cell.date}
                      type="button"
                      role="gridcell"
                      tabIndex={cell.future ? -1 : 0}
                      className={`wb-activity-cell${cell.future ? ' is-future' : ''}`}
                      data-level={cell.level}
                      data-date={cell.date}
                      data-count={cell.count}
                      aria-label={cell.label}
                      title={cell.label}
                      onMouseEnter={(event) => show(cell, event)}
                      onMouseLeave={hide}
                      onFocus={(event) => show(cell, event)}
                      onBlur={hide}
                    />
                  ))}
                </div>
              </div>
            </div>
          </div>
          <div className="wb-activity-foot">
            <span>{view.foot || t('workbench.activity.foot')}</span>
            <span className="wb-activity-legend">
              <span>{t('workbench.activity.less')}</span>
              {[0, 1, 2, 3, 4].map((level) => (
                <i key={level} data-level={level} />
              ))}
              <span>{t('workbench.activity.more')}</span>
            </span>
          </div>
          {tooltip ? (
            <div className="wb-activity-tooltip" role="tooltip" style={{ left: tooltip.left, top: tooltip.top }}>
              <b>{tooltip.label}</b>
              {tooltip.detail ? <small>{tooltip.detail}</small> : null}
            </div>
          ) : null}
        </div>
      )}
    </section>
  )
}

function ChatsCard({ view, actions }: { view: WorkbenchView | null; actions: Actions }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-chats-title">
      <div className="wb-card-head">
        <h3 id="wb-chats-title">{t('workbench.chats.title')}</h3>
        <span className="wb-card-sub">{t('workbench.chats.subtitle')}</span>
        <div className="wb-card-actions">
          <CardAction labelKey="workbench.action.allChats" path="/chat" actions={actions} />
        </div>
      </div>
      {!view || view.chats.length === 0 ? (
        <div className="wb-card-body">
          <p className="wb-empty">{view ? view.empty.chats : t('workbench.chats.empty')}</p>
        </div>
      ) : (
        <ul className="wb-list">
          {view.chats.map((chat, index) => (
            <li className="wb-row" key={chat.id || `${chat.title}-${index}`}>
              <span className="wb-row-icon" aria-hidden="true">
                ◌
              </span>
              <div className="wb-row-main">
                <div className="wb-row-title">
                  {chat.title}
                  {chat.badge ? <span className="wb-badge wb-badge-warn">{chat.badge}</span> : null}
                </div>
                <div className="wb-row-sub">{chat.meta}</div>
              </div>
              <ActionButton
                labelKey="workbench.action.continue"
                mode={actions.mode}
                small
                onAction={() =>
                  actions.mode === 'sample'
                    ? actions.notice(t('workbench.action.sampleNotice'))
                    : actions.resume(chat.id)
                }
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function ArtifactsCard({ view, actions }: { view: WorkbenchView | null; actions: Actions }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-artifacts-title">
      <div className="wb-card-head">
        <h3 id="wb-artifacts-title">{t('workbench.artifacts.title')}</h3>
        {view ? (
          <span className="wb-card-sub">{view.artifactSubtitle || t('workbench.artifacts.subtitle')}</span>
        ) : null}
        <div className="wb-card-actions">
          <CardAction labelKey="workbench.action.all" path="/library" actions={actions} />
        </div>
      </div>
      {!view || view.artifacts.length === 0 ? (
        <div className="wb-card-body">
          <p className="wb-empty">{view ? view.empty.artifacts : t('workbench.artifacts.empty')}</p>
        </div>
      ) : (
        <ul className="wb-list">
          {view.artifacts.map((artifact, index) => (
            <li className="wb-row" key={`${artifact.name}-${index}`}>
              <span className="wb-file-ext" aria-hidden="true">
                {artifact.ext}
              </span>
              <div className="wb-row-main">
                <div className="wb-row-title">{artifact.name}</div>
                <div className="wb-row-sub">{artifact.meta}</div>
              </div>
              <span className={`wb-badge${artifact.tone === 'success' ? ' wb-badge-success' : ''}`}>
                {artifact.badge === 'published'
                  ? t('workbench.status.published')
                  : artifact.badge === 'draft'
                    ? t('workbench.status.draft')
                    : artifact.badge}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function AgentsCard({ view, actions }: { view: WorkbenchView | null; actions: Actions }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-agents-title">
      <div className="wb-card-head">
        <h3 id="wb-agents-title">{t('workbench.agents.title')}</h3>
        <div className="wb-card-actions">
          <CardAction labelKey="workbench.action.manage" path="/agents" actions={actions} />
        </div>
      </div>
      {!view || view.agents.length === 0 ? (
        <div className="wb-card-body">
          <p className="wb-empty">{view ? view.empty.agents : t('workbench.agents.empty')}</p>
        </div>
      ) : (
        <ul className="wb-list">
          {view.agents.map((agent) => (
            <li className="wb-row" key={agent.name}>
              <span className="wb-agent-badge" aria-hidden="true">
                {agent.initial}
              </span>
              <div className="wb-row-main">
                <div className="wb-row-title">{agent.name}</div>
                <div className="wb-row-sub">{agent.meta}</div>
              </div>
              <span className={`wb-badge${agent.ready ? ' wb-badge-success' : ''}`}>
                {agent.label || t('workbench.agents.ready')}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function SchedulesCard({ view, actions }: { view: WorkbenchView | null; actions: Actions }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-schedules-title">
      <div className="wb-card-head">
        <h3 id="wb-schedules-title">{t('workbench.schedules.title')}</h3>
        <div className="wb-card-actions">
          <CardAction labelKey="workbench.action.all" path="/schedules" actions={actions} />
        </div>
      </div>
      {!view || view.schedules.length === 0 ? (
        <div className="wb-card-body">
          <p className="wb-empty">{view ? view.empty.schedules : t('workbench.schedules.empty')}</p>
        </div>
      ) : (
        <div className="wb-card-body">
          <div className="wb-timeline">
            {view.schedules.map((schedule) => (
              <div className={`wb-timeline-item is-${schedule.state}`} key={schedule.time}>
                <div className="wb-timeline-time">{schedule.time}</div>
                <div>
                  <div className="wb-timeline-title">{schedule.title}</div>
                  <div className="wb-timeline-sub">{schedule.sub}</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}

function StatusCard({ view }: { view: WorkbenchView | null }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-status-title">
      <div className="wb-card-head">
        <h3 id="wb-status-title">{t('workbench.status.title')}</h3>
      </div>
      <div className="wb-card-body">
        {!view || view.status.length === 0 ? (
          <p className="wb-empty">{view ? view.empty.status : t('workbench.status.empty')}</p>
        ) : (
          <div className="wb-status-list">
            {view.status.map((row, index) => (
              <div className="wb-status-row" key={`${row.label}-${index}`}>
                <span className={`wb-live-dot${row.live ? '' : ' is-off'}`} aria-hidden="true" />
                <span>{row.label || t(sampleWorkbench.status[index]?.labelKey ?? 'workbench.status.runtime')}</span>
                <span className="wb-status-value">{row.value}</span>
              </div>
            ))}
            <div className="wb-hairline" />
            <div className="wb-status-row">
              <span>{t('workbench.status.quota')}</span>
              <span className="wb-status-value">{view.quota}</span>
            </div>
          </div>
        )}
      </div>
      {view?.sourceNote ? (
        <div className="wb-sources" data-testid="workbench-sources">
          <b>{t('workbench.sources.title')}</b>
          <span>{view.sourceNote}</span>
        </div>
      ) : null}
    </section>
  )
}

export default function WorkbenchPage() {
  const { t, language } = useI18n()
  const navigate = useNavigate()
  const sample = useWorkbenchSample()
  const host = hasWorkbenchHost()
  const [weeks, setWeeks] = useState<WorkbenchWeeks>(53)
  const [overview, setOverview] = useState<WorkbenchOverview | null>(null)
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (sample || !host) return
    let cancelled = false
    setLoading(true)
    setFailed('')
    loadWorkbenchOverview(weeks)
      .then((next) => {
        if (!cancelled) setOverview(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setOverview(null)
          setFailed(cause instanceof Error ? cause.message : String(cause))
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [sample, host, weeks])

  const view = useMemo(() => {
    if (sample) return sampleView(weeks, language, t)
    if (!overview) return null
    return liveView(overview, t, language)
  }, [sample, overview, weeks, language, t])

  const actions: Actions = {
    mode: sample ? 'sample' : view ? 'live' : 'unavailable',
    notice: setNotice,
    go: (path) => navigate(path),
    newChat: () => {
      try {
        window.localStorage.removeItem('anvil.chat.session')
      } catch {
        // Private browsing: navigating still opens a fresh composer.
      }
      navigate('/chat')
    },
    resume: (id) => {
      try {
        window.localStorage.setItem('anvil.chat.session', id)
      } catch {
        // The chat page falls back to its own session list.
      }
      navigate('/chat')
    },
  }

  const greeting = t(greetingKey())
  const greetingText = view?.greetingName ? `${t('workbench.greeting.afternoon')}，${view.greetingName}` : greeting
  const refreshing = host && !sample && loading && Boolean(overview)

  return (
    <div className="wb-page" data-testid="workbench-page" data-workbench-mode={sample ? 'sample' : view ? 'live' : 'empty'}>
      {sample ? (
        <div className="wb-sample-banner" role="status" data-testid="sample-banner">
          {t('workbench.sampleBanner')}
        </div>
      ) : null}
      <header className="wb-page-head">
        <div>
          <h1>{greetingText}</h1>
          <p className="wb-page-desc">
            {view ? view.subtitle || t('workbench.subtitle.sample') : t('workbench.subtitle.empty')}
          </p>
        </div>
        <div className="wb-page-actions">
          <ActionButton
            labelKey="workbench.action.configureAgent"
            mode={actions.mode}
            onAction={() =>
              actions.mode === 'sample'
                ? actions.notice(t('workbench.action.sampleNotice'))
                : actions.go('/agents')
            }
          />
          <ActionButton
            labelKey="workbench.action.newChat"
            mode={actions.mode}
            primary
            onAction={() =>
              actions.mode === 'sample' ? actions.notice(t('workbench.action.sampleNotice')) : actions.newChat()
            }
          />
        </div>
      </header>
      {notice ? (
        <p className="wb-notice" role="status" data-testid="workbench-notice">
          {notice}
        </p>
      ) : null}
      {host && !sample && loading && !overview ? (
        <p className="wb-notice" role="status" data-testid="workbench-loading">
          {t('workbench.loading')}
        </p>
      ) : null}
      {failed ? (
        <p className="wb-notice wb-notice-error" role="alert" data-testid="workbench-failed">
          {t('workbench.failed', { detail: failed })}
        </p>
      ) : null}

      <WorkbenchStats view={view} />
      <ActivityHeatmap
        view={view}
        weeks={weeks}
        onWeeks={setWeeks}
        refreshing={refreshing}
      />

      <div className="wb-split">
        <div className="wb-stack">
          <ChatsCard view={view} actions={actions} />
          <ArtifactsCard view={view} actions={actions} />
        </div>
        <div className="wb-stack">
          <AgentsCard view={view} actions={actions} />
          <SchedulesCard view={view} actions={actions} />
          <StatusCard view={view} />
        </div>
      </div>
      <span className="wb-tech-marker" data-testid="page-marker">
        {WORKBENCH_ROUTE.marker}
      </span>
    </div>
  )
}
