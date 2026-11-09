import { useMemo, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { WORKBENCH_ROUTE } from '../../shell/routes'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import { sampleWorkbench, type SampleWorkbench } from './sampleData'
import './workbench.css'

export function useWorkbenchSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/workbench') return true
  return new URLSearchParams(location.search).get('sample') === 'workbench'
}

function greetingKey(now = new Date()): MessageKey {
  const hour = now.getHours()
  if (hour < 12) return 'workbench.greeting.morning'
  if (hour < 18) return 'workbench.greeting.afternoon'
  return 'workbench.greeting.evening'
}

function ActionButton({
  labelKey,
  sample,
  onAction,
  primary = false,
  small = false,
}: {
  labelKey: MessageKey
  sample: boolean
  onAction: (message: string) => void
  primary?: boolean
  small?: boolean
}) {
  const { t } = useI18n()
  const className = `btn${primary ? ' btn-primary' : ''}${small ? ' btn-sm' : ''}`
  if (!sample) {
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
      title={t('workbench.action.sampleNotice')}
      onClick={() => onAction(t('workbench.action.sampleNotice'))}
    >
      {t(labelKey)}
    </button>
  )
}

function WorkbenchStats({ sample }: { sample: SampleWorkbench | null }) {
  const { t } = useI18n()
  const labelKeys: Record<SampleWorkbench['stats'][number]['key'], MessageKey> = {
    sessions: 'workbench.stats.sessions',
    toolCalls: 'workbench.stats.toolCalls',
    tokens: 'workbench.stats.tokens',
    pending: 'workbench.stats.pending',
  }
  return (
    <section className="wb-stats" aria-labelledby="wb-stats-title">
      <h2 id="wb-stats-title" className="sr-only">
        {t('workbench.stats.title')}
      </h2>
      <div className="wb-stat-grid">
        {Object.entries(labelKeys).map(([key, labelKey]) => {
          const stat = sample?.stats.find((entry) => entry.key === key)
          return (
            <div className="wb-stat" key={key} data-stat={key}>
              <div className="wb-stat-label">{t(labelKey)}</div>
              <div className="wb-stat-value">{stat?.value ?? '0'}</div>
              <div className="wb-stat-sub">{stat ? t(stat.sub as MessageKey) : t('workbench.stats.emptySub')}</div>
            </div>
          )
        })}
      </div>
    </section>
  )
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

function activityLevel(count: number): number {
  if (!count) return 0
  if (count <= 3) return 1
  if (count <= 6) return 2
  if (count <= 10) return 3
  return 4
}

function formatDay(date: Date, language: string): string {
  return new Intl.DateTimeFormat(language, { year: 'numeric', month: 'long', day: 'numeric' }).format(date)
}

function ActivityHeatmap({ sample }: { sample: SampleWorkbench | null }) {
  const { t, language } = useI18n()
  const [weeks, setWeeks] = useState(53)
  const cells = useMemo(() => {
    if (!sample) return []
    const today = new Date()
    today.setHours(12, 0, 0, 0)
    const end = new Date(today)
    end.setDate(end.getDate() + (6 - end.getDay()))
    const start = new Date(end)
    start.setDate(start.getDate() - weeks * 7 + 1)
    const output: Array<{ date: Date; count: number; future: boolean }> = []
    for (let week = 0; week < weeks; week += 1) {
      const weekStart = new Date(start)
      weekStart.setDate(start.getDate() + week * 7)
      for (let day = 0; day < 7; day += 1) {
        const date = new Date(weekStart)
        date.setDate(weekStart.getDate() + day)
        const future = date > today
        output.push({ date, future, count: future ? 0 : dayCount(date) })
      }
    }
    return output
  }, [sample, weeks])

  const monthLabels = useMemo(() => {
    if (!sample) return []
    const labels: Array<{ column: number; label: string }> = []
    let previous = ''
    cells.forEach((cell, index) => {
      if (index % 7 !== 0) return
      const month = new Intl.DateTimeFormat(language, { month: 'short' }).format(cell.date)
      if (month !== previous) {
        labels.push({ column: Math.floor(index / 7) + 1, label: month })
        previous = month
      }
    })
    return labels
  }, [cells, language, sample])

  const stat = sample?.activity
  return (
    <section className="wb-card wb-activity" aria-labelledby="wb-activity-title">
      <div className="wb-card-head">
        <h3 id="wb-activity-title">{t('workbench.activity.title')}</h3>
        <span className="wb-card-sub">{t('workbench.activity.subtitle')}</span>
        <div className="wb-card-actions">
          <select
            className="wb-select"
            aria-label={t('workbench.activity.title')}
            value={weeks}
            disabled={!sample}
            onChange={(event) => setWeeks(Number(event.target.value))}
          >
            <option value={12}>{t('workbench.activity.range12')}</option>
            <option value={26}>{t('workbench.activity.range26')}</option>
            <option value={53}>{t('workbench.activity.range53')}</option>
          </select>
        </div>
      </div>
      {!sample ? (
        <div className="wb-card-body">
          <p className="wb-empty">{t('workbench.activity.empty')}</p>
        </div>
      ) : (
        <div className="wb-card-body">
          <div className="wb-activity-summary">
            <div>
              <b>
                {t(weeks === 53 ? 'workbench.activity.summaryYear' : 'workbench.activity.summaryWeeks', {
                  count: stat?.completed ?? '0',
                  weeks,
                })}
              </b>
              <span>{t('workbench.activity.foot')}</span>
            </div>
            <div className="wb-activity-metrics">
              <span>
                <b>{stat?.activeDays ?? '0'}</b>
                <small>{t('workbench.activity.activeDays')}</small>
              </span>
              <span>
                <b>{stat?.completed ?? '0'}</b>
                <small>{t('workbench.activity.completed')}</small>
              </span>
              <span>
                <b>{stat?.streak ?? '0'}</b>
                <small>{t('workbench.activity.streak')}</small>
              </span>
            </div>
          </div>
          <div className="wb-activity-scroll">
            <div className="wb-activity-chart" style={{ ['--wb-activity-weeks' as string]: weeks }}>
              <div className="wb-activity-months" aria-hidden="true">
                {monthLabels.map((marker) => (
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
                  {cells.map((cell) => {
                    const day = formatDay(cell.date, language)
                    return (
                      <button
                        key={cell.date.toISOString()}
                        type="button"
                        role="gridcell"
                        tabIndex={cell.future ? -1 : 0}
                        className={`wb-activity-cell${cell.future ? ' is-future' : ''}`}
                        data-level={activityLevel(cell.count)}
                        aria-label={t('workbench.activity.cell', { date: day, count: cell.count })}
                        title={t('workbench.activity.cell', { date: day, count: cell.count })}
                      />
                    )
                  })}
                </div>
              </div>
            </div>
          </div>
          <div className="wb-activity-foot">
            <span>{t('workbench.activity.foot')}</span>
            <span className="wb-activity-legend">
              <span>{t('workbench.activity.less')}</span>
              {[0, 1, 2, 3, 4].map((level) => (
                <i key={level} data-level={level} />
              ))}
              <span>{t('workbench.activity.more')}</span>
            </span>
          </div>
        </div>
      )}
    </section>
  )
}

function CardHeader({
  id,
  titleKey,
  subtitleKey,
  actionKey,
  sample,
  onAction,
}: {
  id: string
  titleKey: MessageKey
  subtitleKey?: MessageKey
  actionKey?: MessageKey
  sample: SampleWorkbench | null
  onAction: (message: string) => void
}) {
  const { t } = useI18n()
  return (
    <div className="wb-card-head">
      <h3 id={id}>{t(titleKey)}</h3>
      {subtitleKey ? <span className="wb-card-sub">{t(subtitleKey)}</span> : null}
      {actionKey ? (
        <div className="wb-card-actions">
          <ActionButton labelKey={actionKey} sample={Boolean(sample)} onAction={onAction} small />
        </div>
      ) : null}
    </div>
  )
}

function ChatsCard({ sample, onAction }: { sample: SampleWorkbench | null; onAction: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-chats-title">
      <CardHeader
        id="wb-chats-title"
        titleKey="workbench.chats.title"
        subtitleKey="workbench.chats.subtitle"
        actionKey="workbench.action.allChats"
        sample={sample}
        onAction={onAction}
      />
      {!sample ? (
        <div className="wb-card-body">
          <p className="wb-empty">{t('workbench.chats.empty')}</p>
        </div>
      ) : (
        <ul className="wb-list">
          {sample.chats.map((chat) => (
            <li className="wb-row" key={chat.title}>
              <span className="wb-row-icon" aria-hidden="true">
                ◌
              </span>
              <div className="wb-row-main">
                <div className="wb-row-title">
                  {chat.title}
                  {chat.status ? <span className="wb-badge wb-badge-warn">{t('workbench.status.pending')}</span> : null}
                </div>
                <div className="wb-row-sub">{chat.meta}</div>
              </div>
              <ActionButton labelKey="workbench.action.continue" sample onAction={onAction} small />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function ArtifactsCard({ sample, onAction }: { sample: SampleWorkbench | null; onAction: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-artifacts-title">
      <CardHeader
        id="wb-artifacts-title"
        titleKey="workbench.artifacts.title"
        subtitleKey={sample ? 'workbench.artifacts.subtitle' : undefined}
        actionKey="workbench.action.all"
        sample={sample}
        onAction={onAction}
      />
      {!sample ? (
        <div className="wb-card-body">
          <p className="wb-empty">{t('workbench.artifacts.empty')}</p>
        </div>
      ) : (
        <ul className="wb-list">
          {sample.artifacts.map((artifact) => (
            <li className="wb-row" key={artifact.name}>
              <span className="wb-file-ext" aria-hidden="true">
                {artifact.extension}
              </span>
              <div className="wb-row-main">
                <div className="wb-row-title">{artifact.name}</div>
                <div className="wb-row-sub">{artifact.meta}</div>
              </div>
              <span className={`wb-badge${artifact.status === 'published' ? ' wb-badge-success' : ''}`}>
                {t(artifact.status === 'published' ? 'workbench.status.published' : 'workbench.status.draft')}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function AgentsCard({ sample, onAction }: { sample: SampleWorkbench | null; onAction: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-agents-title">
      <CardHeader
        id="wb-agents-title"
        titleKey="workbench.agents.title"
        actionKey="workbench.action.manage"
        sample={sample}
        onAction={onAction}
      />
      {!sample ? (
        <div className="wb-card-body">
          <p className="wb-empty">{t('workbench.agents.empty')}</p>
        </div>
      ) : (
        <ul className="wb-list">
          {sample.agents.map((agent) => (
            <li className="wb-row" key={agent.name}>
              <span className="wb-agent-badge" aria-hidden="true">
                {agent.initial}
              </span>
              <div className="wb-row-main">
                <div className="wb-row-title">{agent.name}</div>
                <div className="wb-row-sub">{agent.meta}</div>
              </div>
              <span className="wb-badge wb-badge-success">{t('workbench.agents.ready')}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function SchedulesCard({ sample, onAction }: { sample: SampleWorkbench | null; onAction: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-schedules-title">
      <CardHeader
        id="wb-schedules-title"
        titleKey="workbench.schedules.title"
        actionKey="workbench.action.all"
        sample={sample}
        onAction={onAction}
      />
      {!sample ? (
        <div className="wb-card-body">
          <p className="wb-empty">{t('workbench.schedules.empty')}</p>
        </div>
      ) : (
        <div className="wb-card-body">
          <div className="wb-timeline">
            {sample.schedules.map((schedule) => (
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

function StatusCard({ sample }: { sample: SampleWorkbench | null }) {
  const { t } = useI18n()
  return (
    <section className="wb-card" aria-labelledby="wb-status-title">
      <div className="wb-card-head">
        <h3 id="wb-status-title">{t('workbench.status.title')}</h3>
      </div>
      <div className="wb-card-body">
        {!sample ? (
          <p className="wb-empty">{t('workbench.status.empty')}</p>
        ) : (
          <div className="wb-status-list">
            {sample.status.map((row) => (
              <div className="wb-status-row" key={row.labelKey}>
                <span className="wb-live-dot" aria-hidden="true" />
                <span>{t(row.labelKey)}</span>
                <span className="wb-status-value">{row.value}</span>
              </div>
            ))}
            <div className="wb-hairline" />
            <div className="wb-status-row">
              <span>{t('workbench.status.quota')}</span>
              <span className="wb-status-value">{sample.quota}</span>
            </div>
          </div>
        )}
      </div>
    </section>
  )
}

export default function WorkbenchPage() {
  const { t } = useI18n()
  const sample = useWorkbenchSample()
  const data = sample ? sampleWorkbench : null
  const [notice, setNotice] = useState('')
  const greeting = t(greetingKey())
  const greetingText = data ? `${t('workbench.greeting.afternoon')}，${data.greetingName}` : greeting

  return (
    <div className="wb-page" data-testid="workbench-page" data-workbench-mode={sample ? 'sample' : 'normal'}>
      {sample ? (
        <div className="wb-sample-banner" role="status" data-testid="sample-banner">
          {t('workbench.sampleBanner')}
        </div>
      ) : null}
      <header className="wb-page-head">
        <div>
          <h1>{greetingText}</h1>
          <p className="wb-page-desc">{t(sample ? 'workbench.subtitle.sample' : 'workbench.subtitle.empty')}</p>
        </div>
        <div className="wb-page-actions">
          <ActionButton labelKey="workbench.action.configureAgent" sample={sample} onAction={setNotice} />
          <ActionButton labelKey="workbench.action.newChat" sample={sample} onAction={setNotice} primary />
        </div>
      </header>
      {notice ? (
        <p className="wb-notice" role="status" data-testid="workbench-notice">
          {notice}
        </p>
      ) : null}

      <WorkbenchStats sample={data} />
      <ActivityHeatmap sample={data} />

      <div className="wb-split">
        <div className="wb-stack">
          <ChatsCard sample={data} onAction={setNotice} />
          <ArtifactsCard sample={data} onAction={setNotice} />
        </div>
        <div className="wb-stack">
          <AgentsCard sample={data} onAction={setNotice} />
          <SchedulesCard sample={data} onAction={setNotice} />
          <StatusCard sample={data} />
        </div>
      </div>
      <span className="wb-tech-marker" data-testid="page-marker">
        {WORKBENCH_ROUTE.marker}
      </span>
    </div>
  )
}
