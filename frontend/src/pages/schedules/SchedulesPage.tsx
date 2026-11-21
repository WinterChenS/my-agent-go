import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { useCatalog } from '../../catalog/useCatalog'
import { listenHost } from '../../host/tauri'
import NewScheduleDialog from './NewScheduleDialog'
import { sampleScheduleStats, sampleSchedules, sampleTrend, useSchedulesSample, type RunLabel, type ScheduleState } from './sampleData'
import {
  SCHEDULE_EVENT,
  formatDuration,
  hasScheduleHost,
  loadSchedules,
  runScheduleNow,
  toggleSchedule,
  type ScheduleRecord,
  type ScheduleView,
} from './client'
import './schedules.css'

const RUN_LABEL_KEYS: Record<RunLabel, MessageKey> = {
  success: 'schedules.run.success',
  warn: 'schedules.run.warn',
  partial: 'schedules.run.partial',
  skipped: 'schedules.run.skipped',
  never: 'schedules.run.never',
}

const OUTPUT_KEYS: Record<string, MessageKey> = {
  library: 'schedules.form.output.library',
  inline: 'schedules.form.output.inline',
  mail: 'schedules.form.output.mail',
}

const PERMISSION_KEYS: Record<string, MessageKey> = {
  readonly: 'schedules.permission.readonly',
  medium: 'schedules.permission.medium',
  high: 'schedules.permission.high',
  full: 'schedules.permission.full',
  builtin: 'schedules.permission.builtin',
}

type StatusFilter = 'all' | ScheduleState | 'failed'

interface RunView {
  time: string
  label: RunLabel
  duration: string
}

interface TaskView {
  id: string
  name: string
  meta: string
  badgeChar: string
  cron: string
  when: string
  agent: string
  model: string
  state: ScheduleState
  permissionKey: string
  prompt: string
  output: string
  lastLabel: RunLabel
  lastDuration: string
  runs: RunView[]
  pendingNote?: string
  record?: ScheduleRecord
}

interface StatView {
  key: 'enabled' | 'today' | 'success' | 'duration'
  labelKey: MessageKey
  value: string
  sub: string
}

/** `11/23 09:00`, in the viewer's locale, from a UTC stamp. */
function formatStamp(stamp: string, language: string): string {
  const time = Date.parse(stamp)
  if (!Number.isFinite(time)) return ''
  return new Intl.DateTimeFormat(language, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(time))
}

function labelOf(state: string): RunLabel {
  switch (state) {
    case 'success':
      return 'success'
    case 'failed':
      return 'warn'
    case 'unknown':
      return 'partial'
    case 'skipped':
      return 'skipped'
    default:
      return 'never'
  }
}

export default function SchedulesPage() {
  const { t, language } = useI18n()
  const sample = useSchedulesSample()
  const host = hasScheduleHost()
  const catalog = useCatalog(!sample && host)
  const [search, setSearch] = useState('')
  const [agent, setAgent] = useState('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [selected, setSelected] = useState('')
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState(false)
  const [paused, setPaused] = useState<string[]>([])
  const [editing, setEditing] = useState<ScheduleRecord | null>(null)
  const [view, setView] = useState<ScheduleView | null>(null)
  const [loading, setLoading] = useState(false)
  const [failure, setFailure] = useState('')
  const [busy, setBusy] = useState('')
  const trigger = useRef<HTMLElement | null>(null)

  const reload = useCallback(async () => {
    if (sample || !host) return
    setLoading(true)
    try {
      const next = await loadSchedules()
      setView(next)
      setFailure('')
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }, [host, sample])

  useEffect(() => {
    if (sample || !host) return
    void reload()
    let dispose: (() => void) | void
    void listenHost(SCHEDULE_EVENT, () => {
      void reload()
    })
      .then((off) => {
        dispose = off
      })
      // A plain browser has no event bridge; the mount load already ran.
      .catch(() => {})
    return () => dispose?.()
  }, [host, reload, sample])

  // Sample rows and live rows become one shape here, so the table, filters and
  // detail panel have a single code path.
  const tasks: TaskView[] = useMemo(() => {
    if (sample) {
      return sampleSchedules.map((task) => ({
        id: task.id,
        name: task.name,
        meta: task.meta,
        badgeChar: task.badgeChar,
        cron: task.cron,
        when: task.when,
        agent: task.agent,
        model: task.model,
        state: task.state,
        permissionKey: task.permissionKey,
        prompt: task.prompt,
        output: task.output,
        lastLabel: task.lastLabel,
        lastDuration: task.lastDuration,
        runs: task.runs,
        pendingNote: task.pendingNote,
      }))
    }
    if (!view) return []
    return view.schedules.map((record) => {
      const runs = view.runs
        .filter((run) => run.scheduleId === record.id)
        .slice(0, 5)
        .map((run) => ({
          time: formatStamp(run.finishedAt || run.startedAt || run.scheduledAt, language),
          label: labelOf(run.state),
          duration: formatDuration(run.durationMs) || '—',
        }))
      const paused = !record.enabled
      return {
        id: record.id,
        name: record.name,
        meta: record.prompt.split('\n')[0].slice(0, 80),
        badgeChar: record.agentName.slice(0, 1) || '•',
        cron: record.cron,
        when: paused
          ? t('schedules.when.paused')
          : record.nextRunAt
            ? t('schedules.when.next', { when: formatStamp(record.nextRunAt, language) })
            : '',
        agent: record.agentName,
        model: record.model || '—',
        state: paused ? 'paused' : 'enabled',
        permissionKey: record.permission,
        prompt: record.prompt,
        output: record.outputTarget,
        lastLabel: record.lastStatus ? labelOf(record.lastStatus) : 'never',
        lastDuration: formatDuration(record.lastDurationMs) || '—',
        runs,
        record,
      }
    })
  }, [language, sample, t, view])

  const stats: StatView[] = useMemo(() => {
    if (sample) {
      return sampleScheduleStats.map((stat) => ({
        key: stat.key,
        labelKey: (
          stat.key === 'success' ? 'schedules.stats.success' : `schedules.stats.${stat.key}`
        ) as MessageKey,
        value: stat.value,
        sub: t(stat.subKey as MessageKey),
      }))
    }
    const live = view?.stats
    const paused = live ? live.total - live.enabled : 0
    return [
      {
        key: 'enabled',
        labelKey: 'schedules.stats.enabled',
        value: String(live?.enabled ?? 0),
        sub: live && live.total > 0
          ? t('schedules.stats.enabledSubLive', { total: live.total, paused })
          : t('schedules.stats.empty'),
      },
      {
        key: 'today',
        labelKey: 'schedules.stats.today',
        value: String(live?.todayRuns ?? 0),
        sub: !live || live.total === 0
          ? t('schedules.stats.noTasks')
          : live.weekRuns > 0
            ? t('schedules.stats.todaySubLive', { runs: live.weekRuns })
            : t('schedules.stats.noRuns'),
      },
      {
        key: 'success',
        labelKey: 'schedules.stats.successLive',
        value: live?.successRate != null ? `${live.successRate.toFixed(1)}%` : '—',
        sub: !live || live.total === 0
          ? t('schedules.stats.noTasks')
          : live.weekRuns > 0
            ? t('schedules.stats.successSubLive', { failed: live.weekFailed })
            : t('schedules.stats.noRuns'),
      },
      {
        key: 'duration',
        labelKey: 'schedules.stats.duration',
        value: live?.avgDurationMs != null ? formatDuration(live.avgDurationMs) : '—',
        sub: live?.avgDurationMs != null
          ? t('schedules.stats.durationSubLive')
          : live && live.total > 0
            ? t('schedules.stats.noRuns')
            : t('schedules.stats.noTasks'),
      },
    ]
  }, [sample, t, view])

  const agents = useMemo(() => {
    const names = new Set(tasks.map((task) => task.agent).filter(Boolean))
    return Array.from(names).sort()
  }, [tasks])

  const query = search.trim().toLowerCase()
  const filtered = tasks.filter((task) => {
    const matchesSearch = !query || `${task.name} ${task.agent} ${task.meta} ${task.prompt}`.toLowerCase().includes(query)
    const matchesAgent = agent === 'all' || task.agent === agent
    const matchesStatus =
      status === 'all' ||
      (status === 'failed' ? task.lastLabel !== 'success' && task.lastLabel !== 'never' : task.state === status)
    return matchesSearch && matchesAgent && matchesStatus
  })
  // Sample mode still flips locally so the design's toggle can be inspected;
  // live mode writes through the host and re-reads.
  const isPaused = (task: TaskView) => paused.includes(task.id) || task.state === 'paused'
  const current = filtered.find((task) => task.id === selected) ?? filtered[0] ?? null

  const announce = (message: string) => setNotice(message)
  const openDialog = (element: HTMLElement, record: ScheduleRecord | null) => {
    trigger.current = element
    setEditing(record)
    setDialog(true)
  }
  const closeDialog = () => {
    setDialog(false)
    setEditing(null)
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }

  const onToggle = async (task: TaskView) => {
    if (!task.record) {
      setPaused((list) => (list.includes(task.id) ? list.filter((id) => id !== task.id) : [...list, task.id]))
      announce(t('schedules.notice.toggle', { name: task.name }))
      return
    }
    setBusy(task.id)
    try {
      const next = await toggleSchedule(task.id, !task.record.enabled)
      announce(t('schedules.notice.toggleLive', {
        name: task.name,
        state: next.enabled ? t('schedules.state.enabled') : t('schedules.state.paused'),
      }))
      await reload()
    } catch (error) {
      announce(t('schedules.notice.saveFailed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy('')
    }
  }

  const onPauseAll = async () => {
    if (sample || !view) {
      announce(t('schedules.notice.pauseAll'))
      return
    }
    const enabled = view.schedules.filter((row) => row.enabled)
    if (enabled.length === 0) {
      announce(t('schedules.notice.pauseAllLive', { count: 0 }))
      return
    }
    try {
      for (const row of enabled) {
        await toggleSchedule(row.id, false)
      }
      announce(t('schedules.notice.pauseAllLive', { count: enabled.length }))
      await reload()
    } catch (error) {
      announce(t('schedules.notice.saveFailed', { message: error instanceof Error ? error.message : String(error) }))
    }
  }

  const onRunNow = async (task: TaskView) => {
    if (sample || !task.record) {
      announce(t('schedules.notice.run'))
      return
    }
    setBusy(task.id)
    try {
      const report = await runScheduleNow(task.id)
      announce(
        report.status === 'success'
          ? t('schedules.notice.runLive')
          : t('schedules.notice.runFailed', { message: report.error || report.status }),
      )
      await reload()
    } catch (error) {
      announce(t('schedules.notice.runFailed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy('')
    }
  }

  const onCopyCron = async (task: TaskView) => {
    try {
      await navigator.clipboard.writeText(task.cron)
      announce(sample ? t('schedules.notice.copy') : t('schedules.notice.copyLive'))
    } catch {
      announce(t('schedules.notice.copy'))
    }
  }

  const agentChoices = useMemo(() => {
    const rows = catalog.snapshot?.agents ?? []
    return rows
      .filter((row) => row.enabled)
      .map((row) => ({ id: row.id, name: row.name, model: row.modelId }))
  }, [catalog.snapshot])

  const trend = sample
    ? sampleTrend
    : (view?.trend ?? []).map((point) => ({
        label: point.date.slice(5).replace('-', '/'),
        planned: point.runs,
        manual: 0,
      }))
  const trendMax = Math.max(1, ...trend.map((column) => column.planned))

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
            data-testid="schedule-pause-all"
            disabled={!sample && !view}
            title={sample ? t('schedules.pauseAll') : t('schedules.pauseAll')}
            onClick={() => void onPauseAll()}
          >
            <Icon name="pause" size={14} />
            {t('schedules.pauseAll')}
          </button>
          <button
            type="button"
            className="sc-btn sc-btn-primary"
            data-testid="schedule-new"
            disabled={!sample && !host}
            title={sample || host ? t('schedules.new') : t('schedules.action.unavailable')}
            onClick={(event) => openDialog(event.currentTarget, null)}
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
      {failure ? <div className="sc-notice-bar" data-testid="schedule-error" role="alert">{failure}</div> : null}
      {notice ? <div className="sc-notice-bar" data-testid="schedule-notice" role="status">{notice}</div> : null}

      <section className="sc-stats" data-testid="schedule-stats" aria-label={t('schedules.title')}>
        {stats.map((stat) => (
          <div key={stat.key} className="sc-stat" data-testid={`schedule-stat-${stat.key}`}>
            <span>{t(stat.labelKey)}</span>
            <b>{stat.value}</b>
            <small>{stat.sub}</small>
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
            placeholder={t('schedules.searchPlaceholder')}
            aria-label={t('schedules.searchPlaceholder')}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        <select aria-label={t('schedules.filter.agent')} value={agent} onChange={(event) => setAgent(event.target.value)}>
          <option value="all">{t('schedules.filter.allAgents')}</option>
          {agents.map((value) => (
            <option key={value} value={value}>{value}</option>
          ))}
        </select>
        <select aria-label={t('schedules.filter.status')} value={status} onChange={(event) => setStatus(event.target.value as StatusFilter)}>
          <option value="all">{t('schedules.filter.allStates')}</option>
          <option value="enabled">{t('schedules.state.enabled')}</option>
          <option value="paused">{t('schedules.state.paused')}</option>
          <option value="failed">{t('schedules.state.failed')}</option>
        </select>
        <span className="sc-toolbar-spacer" />
        <span className="sc-muted">
          {loading ? t('schedules.loading') : view ? t('schedules.running', { count: view.stats.running }) : ''}
        </span>
      </div>

      <div className="sc-split">
        <section className="sc-panel" data-testid="schedule-list">
          <div className="sc-panel-head">
            <h2>{t('schedules.list.title')}</h2>
            <span>{t('schedules.list.sub')}</span>
          </div>
          {tasks.length > 0 ? (
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
                  {filtered.map((task) => (
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
                          <span className="sc-row-sub">{task.when}</span>
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
                          disabled={busy === task.id}
                          onClick={(event) => {
                            event.stopPropagation()
                            void onToggle(task)
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
                    <dt>{t('schedules.detail.permission')}</dt>
                    <dd>{t((current.permissionKey.startsWith('schedules.') ? current.permissionKey : PERMISSION_KEYS[current.permissionKey] ?? 'schedules.permission.medium') as MessageKey)}</dd>
                    <dt>{t('schedules.detail.timezone')}</dt>
                    <dd className="mono">
                      {current.record
                        ? `${current.record.timeZone || 'UTC'} · UTC${current.record.utcOffsetMinutes >= 0 ? '+' : ''}${current.record.utcOffsetMinutes / 60}`
                        : 'Asia/Shanghai'}
                    </dd>
                  </dl>
                  <div className="sc-block">
                    <div className="sc-label">{t('schedules.detail.prompt')}</div>
                    <div className="sc-prompt">{current.prompt}</div>
                  </div>
                  <div className="sc-block">
                    <div className="sc-label">{t('schedules.detail.output')}</div>
                    <div className="sc-tags">
                      <span className="sc-tag">
                        {current.output
                          ? OUTPUT_KEYS[current.output]
                            ? t(OUTPUT_KEYS[current.output])
                            : current.output
                          : t('schedules.detail.outputInline')}
                      </span>
                      {current.record ? null : <span className="sc-tag">{t('schedules.detail.mailNotify')}</span>}
                    </div>
                  </div>
                  <div className="sc-block">
                    <div className="sc-label">{t('schedules.detail.recentRuns')}</div>
                    <div data-testid="schedule-runs">
                      {current.runs.length > 0 ? (
                        current.runs.map((run, index) => (
                          <div key={`${run.time}-${run.label}-${index}`} className="sc-run-line">
                            <span className="sc-row-sub">{run.time}</span>
                            <span className={`sc-badge is-${run.label}`}><i aria-hidden="true" />{t(RUN_LABEL_KEYS[run.label])}</span>
                            <span className="sc-run-duration mono">{run.duration}</span>
                          </div>
                        ))
                      ) : (
                        <div className="sc-row-sub">{t('schedules.detail.neverRun')}</div>
                      )}
                    </div>
                  </div>
                  <div className="sc-detail-actions">
                    <button
                      type="button"
                      className="sc-btn sc-btn-sm"
                      disabled={busy === current.id}
                      onClick={() => void onRunNow(current)}
                    >
                      <Icon name="send" size={12} />
                      {t('schedules.detail.runNow')}
                    </button>
                    <button
                      type="button"
                      className="sc-btn sc-btn-sm sc-btn-ghost"
                      onClick={(event) => {
                        event.stopPropagation()
                        if (current.record) {
                          openDialog(event.currentTarget, current.record)
                        } else {
                          openDialog(event.currentTarget, null)
                        }
                      }}
                    >
                      <Icon name="settings" size={12} />
                      {t('schedules.detail.edit')}
                    </button>
                    <button type="button" className="sc-btn sc-btn-sm sc-btn-ghost sc-cron-copy" onClick={() => void onCopyCron(current)}>
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
            {trend.length > 0 && trendMax > 0 && trend.some((column) => column.planned > 0) ? (
              <div className="sc-trend">
                <div className="sc-chart" role="img" aria-label={t('schedules.trend.aria')}>
                  {trend.map((column) => (
                    <div key={column.label} className="sc-chart-col">
                      <b className="sc-seg-in" style={{ height: `${Math.max(6, Math.round((column.planned / trendMax) * 100))}%` }} />
                    </div>
                  ))}
                </div>
                <div className="sc-axis">
                  {trend.map((column, index) => (
                    <span key={column.label}>
                      {index === trend.length - 1 && !sample ? t('schedules.trend.today') : column.label}
                    </span>
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

      <NewScheduleDialog
        open={dialog}
        sample={sample}
        agents={agentChoices}
        initial={editing}
        onClose={closeDialog}
        onNotice={announce}
        onSaved={reload}
      />
    </section>
  )
}
