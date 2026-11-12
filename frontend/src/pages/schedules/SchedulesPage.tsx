import { useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import NewScheduleDialog from './NewScheduleDialog'
import {
  sampleScheduleStats,
  sampleSchedules,
  sampleTrend,
  scheduleAgents,
  useSchedulesSample,
  type RunLabel,
  type ScheduleState,
  type ScheduleTask,
} from './sampleData'
import './schedules.css'

const RUN_LABEL_KEYS: Record<RunLabel, MessageKey> = {
  success: 'schedules.run.success',
  warn: 'schedules.run.warn',
  partial: 'schedules.run.partial',
  skipped: 'schedules.run.skipped',
}

const STAT_LABEL_KEYS: Record<(typeof sampleScheduleStats)[number]['key'], MessageKey> = {
  enabled: 'schedules.stats.enabled',
  today: 'schedules.stats.today',
  success: 'schedules.stats.success',
  duration: 'schedules.stats.duration',
}

type StatusFilter = 'all' | ScheduleState | 'failed'

export default function SchedulesPage() {
  const { t } = useI18n()
  const sample = useSchedulesSample()
  const [search, setSearch] = useState('')
  const [agent, setAgent] = useState('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [selected, setSelected] = useState('standup')
  const [disabled, setDisabled] = useState<string[]>([])
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState(false)
  const trigger = useRef<HTMLElement | null>(null)

  const query = search.trim().toLowerCase()
  const tasks = sample
    ? sampleSchedules.filter((task) => {
        const matchesSearch = !query || `${task.name} ${task.agent} ${task.meta}`.toLowerCase().includes(query)
        const matchesAgent = agent === 'all' || task.agent === agent
        const matchesStatus =
          status === 'all' ||
          (status === 'failed' ? task.lastLabel !== 'success' : task.state === status)
        return matchesSearch && matchesAgent && matchesStatus
      })
    : []

  const current = sample ? tasks.find((task) => task.id === selected) ?? tasks[0] ?? null : null
  const isPaused = (task: ScheduleTask) => disabled.includes(task.id) || task.state === 'paused'

  const announce = (message: string) => setNotice(message)
  const openDialog = (element: HTMLElement) => {
    trigger.current = element
    setDialog(true)
  }
  const closeDialog = () => {
    setDialog(false)
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }

  return (
    <section className="sc-page" data-testid="schedules-page" data-sample={sample ? 'true' : 'false'}>
      <span className="sc-sr-only" id="route-marker" data-testid="page-marker">
        R0-10 schedules · /schedules
      </span>
      <div className="sc-page-head">
        <div>
          <h1>{t('schedules.title')}</h1>
          <p>{t('schedules.description')}</p>
        </div>
        <div className="sc-head-actions">
          <button
            type="button"
            className="sc-btn"
            disabled={!sample}
            title={sample ? t('schedules.pauseAll') : t('schedules.action.unavailable')}
            onClick={() => announce(t('schedules.notice.pauseAll'))}
          >
            <Icon name="pause" size={14} />
            {t('schedules.pauseAll')}
          </button>
          <button
            type="button"
            className="sc-btn sc-btn-primary"
            data-testid="schedule-new"
            disabled={!sample}
            title={sample ? t('schedules.new') : t('schedules.action.unavailable')}
            onClick={(event) => openDialog(event.currentTarget)}
          >
            <Icon name="plus" size={14} />
            {t('schedules.new')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="sc-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('schedules.sampleBanner')}
        </div>
      ) : null}
      {notice ? <div className="sc-notice-bar" data-testid="schedule-notice" role="status">{notice}</div> : null}

      <section className="sc-stats" data-testid="schedule-stats" aria-label={t('schedules.title')}>
        {sampleScheduleStats.map((stat) => (
          <div key={stat.key} className="sc-stat" data-testid={`schedule-stat-${stat.key}`}>
            <span>{t(STAT_LABEL_KEYS[stat.key])}</span>
            <b>{sample ? stat.value : stat.key === 'success' ? '—' : stat.key === 'duration' ? '—' : '0'}</b>
            <small>{sample ? t(stat.subKey as MessageKey) : t('schedules.stats.empty')}</small>
          </div>
        ))}
      </section>

      <div className="sc-toolbar">
        <label className="sc-search">
          <Icon name="search" size={12} />
          <input
            data-testid="schedule-search"
            type="search"
            value={search}
            disabled={!sample}
            placeholder={t('schedules.searchPlaceholder')}
            aria-label={t('schedules.searchPlaceholder')}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        <select aria-label={t('schedules.filter.agent')} value={agent} disabled={!sample} onChange={(event) => setAgent(event.target.value)}>
          <option value="all">{t('schedules.filter.allAgents')}</option>
          {scheduleAgents.map((value) => (
            <option key={value} value={value}>{value}</option>
          ))}
        </select>
        <select aria-label={t('schedules.filter.status')} value={status} disabled={!sample} onChange={(event) => setStatus(event.target.value as StatusFilter)}>
          <option value="all">{t('schedules.filter.allStates')}</option>
          <option value="enabled">{t('schedules.state.enabled')}</option>
          <option value="paused">{t('schedules.state.paused')}</option>
          <option value="failed">{t('schedules.state.failed')}</option>
        </select>
        <span className="sc-toolbar-spacer" />
        <span className="sc-muted">{t('schedules.concurrency')} <span className="mono">2</span> {t('schedules.queue')}</span>
      </div>

      <div className="sc-split">
        <section className="sc-panel" data-testid="schedule-list">
          <div className="sc-panel-head">
            <h2>{t('schedules.list.title')}</h2>
            <span>{t('schedules.list.sub')}</span>
          </div>
          {sample ? (
            <div className="sc-table-wrap">
              <table className="sc-table">
                <thead>
                  <tr>
                    <th className="sc-col-task">{t('schedules.table.task')}</th>
                    <th>{t('schedules.table.plan')}</th>
                    <th>{t('schedules.table.agent')}</th>
                    <th>{t('schedules.table.lastRun')}</th>
                    <th className="sc-col-switch">{t('schedules.table.enabled')}</th>
                  </tr>
                </thead>
                <tbody>
                  {tasks.map((task) => (
                    <tr
                      key={task.id}
                      data-testid="schedule-row"
                      data-schedule={task.id}
                      className={current?.id === task.id ? 'is-active' : ''}
                      onClick={() => setSelected(task.id)}
                    >
                      <td>
                        <div className="sc-row-title">
                          {task.name}
                          {task.pendingNote ? <span className="sc-badge is-accent">{task.pendingNote}</span> : null}
                        </div>
                        <div className="sc-row-sub">{task.meta}</div>
                      </td>
                      <td>
                        <div className="sc-cell">
                          <code className="sc-cron">{task.cron}</code>
                          <span className="sc-row-sub">{task.when.split(' · ')[0]}</span>
                        </div>
                      </td>
                      <td>
                        <div className="sc-cell">
                          <span>{task.agent}</span>
                          <span className="sc-row-sub mono">{task.model}</span>
                        </div>
                      </td>
                      <td>
                        <div className="sc-cell sc-result">
                          <span className={`sc-badge is-${task.lastLabel}`}><i aria-hidden="true" />{t(RUN_LABEL_KEYS[task.lastLabel])}</span>
                          <span className="mono sc-row-sub">{task.lastDuration}</span>
                        </div>
                      </td>
                      <td className="sc-col-switch">
                        <button
                          type="button"
                          className="sc-switch"
                          role="switch"
                          aria-checked={!isPaused(task)}
                          aria-label={`${t('schedules.table.enabled')} ${task.name}`}
                          onClick={(event) => {
                            event.stopPropagation()
                            setDisabled((list) => (list.includes(task.id) ? list.filter((id) => id !== task.id) : [...list, task.id]))
                            announce(t('schedules.notice.toggle', { name: task.name }))
                          }}
                        >
                          <span />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="sc-empty" data-testid="schedule-empty">
              <Icon name="clock" size={22} />
              <b>{t('schedules.empty')}</b>
              <span>{t('schedules.emptyBody')}</span>
            </div>
          )}
        </section>

        <div className="sc-stack">
          <section className="sc-panel" data-testid="schedule-detail">
            {current ? (
              <>
                <div className="sc-detail-head">
                  <span className="sc-agent-badge">{current.badgeChar}</span>
                  <div className="sc-detail-title">
                    <h2>{current.name}</h2>
                    <div className="sc-row-sub">{current.when}</div>
                  </div>
                  <span className={`sc-badge ${isPaused(current) ? 'is-warn' : 'is-success'}`}>
                    <i aria-hidden="true" />{isPaused(current) ? t('schedules.state.paused') : t('schedules.state.enabled')}
                  </span>
                </div>
                <div className="sc-detail-body">
                  <dl className="sc-dl">
                    <dt>{t('schedules.detail.agent')}</dt><dd>{current.agent}</dd>
                    <dt>{t('schedules.detail.model')}</dt><dd className="mono">{current.model}</dd>
                    <dt>{t('schedules.detail.permission')}</dt><dd>{t(current.permissionKey as MessageKey)}</dd>
                    <dt>{t('schedules.detail.timezone')}</dt><dd className="mono">Asia/Shanghai</dd>
                  </dl>
                  <div className="sc-block">
                    <div className="sc-label">{t('schedules.detail.prompt')}</div>
                    <div className="sc-prompt">{current.prompt}</div>
                  </div>
                  <div className="sc-block">
                    <div className="sc-label">{t('schedules.detail.output')}</div>
                    <div className="sc-tags"><span className="sc-tag">{current.output}</span><span className="sc-tag">{t('schedules.detail.mailNotify')}</span></div>
                  </div>
                  <div className="sc-block">
                    <div className="sc-label">{t('schedules.detail.recentRuns')}</div>
                    <div data-testid="schedule-runs">
                      {current.runs.map((run) => (
                        <div key={`${run.time}-${run.label}`} className="sc-run-line">
                          <span className="sc-row-sub">{run.time}</span>
                          <span className={`sc-badge is-${run.label}`}><i aria-hidden="true" />{t(RUN_LABEL_KEYS[run.label])}</span>
                          <span className="sc-run-duration mono">{run.duration}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                  <div className="sc-detail-actions">
                    <button type="button" className="sc-btn sc-btn-sm" onClick={() => announce(t('schedules.notice.run'))}>
                      <Icon name="send" size={12} />
                      {t('schedules.detail.runNow')}
                    </button>
                    <button type="button" className="sc-btn sc-btn-sm sc-btn-ghost" onClick={(event) => openDialog(event.currentTarget)}>
                      <Icon name="settings" size={12} />
                      {t('schedules.detail.edit')}
                    </button>
                    <button type="button" className="sc-btn sc-btn-sm sc-btn-ghost sc-cron-copy" onClick={() => announce(t('schedules.notice.copy'))}>
                      <Icon name="copy" size={12} />
                      cron
                    </button>
                  </div>
                </div>
              </>
            ) : (
              <div className="sc-empty" data-testid="schedule-detail-empty">
                <Icon name="clock" size={20} />
                <b>{t('schedules.detail.empty')}</b>
                <span>{t('schedules.detail.emptyBody')}</span>
              </div>
            )}
          </section>

          <section className="sc-panel" data-testid="schedule-trend">
            <div className="sc-panel-head">
              <h2>{t('schedules.trend.title')}</h2>
              <span>{t('schedules.trend.unit')}</span>
            </div>
            {sample ? (
              <div className="sc-trend">
                <div className="sc-chart" role="img" aria-label={t('schedules.trend.aria')}>
                  {sampleTrend.map((column) => (
                    <div key={column.label} className="sc-chart-col">
                      <b className="sc-seg-in" style={{ height: `${column.planned}%` }} />
                    </div>
                  ))}
                </div>
                <div className="sc-axis">
                  {sampleTrend.map((column) => (
                    <span key={column.label}>{column.label.startsWith('schedules.') ? t(column.label as MessageKey) : column.label}</span>
                  ))}
                </div>
                <div className="sc-hairline" />
                <div className="sc-legend">
                  <span><i className="sc-legend-in" />{t('schedules.trend.planned')}</span>
                  <span><i className="sc-legend-out" />{t('schedules.trend.manual')}</span>
                </div>
              </div>
            ) : (
              <div className="sc-empty">{t('schedules.trend.empty')}</div>
            )}
          </section>
        </div>
      </div>

      <NewScheduleDialog open={dialog} onClose={closeDialog} onNotice={announce} />
    </section>
  )
}
