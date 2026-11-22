import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import {
  deleteConnector,
  hasConnectorHost,
  loadConnectors,
  probeConnector,
  saveConnector,
  toggleConnector,
  type ConnectorDraft,
  type ConnectorOverview,
  type ConnectorProbeOutcome,
  type ConnectorRecord,
  type ConnectorType,
} from './client'
import { LiveConnectorDialog } from './LiveConnectorDialog'
import './connectors.css'

const TYPE_ICONS: Record<string, string> = {
  MAIL: 'mail',
  NOTION: 'layers',
  DINGTALK_DOC: 'file',
}

const STATUS_KEYS: Record<string, MessageKey> = {
  connected: 'connectors.status.connected',
  declared: 'connectors.status.unverified',
  // A failed probe is not the same as an expired grant: say what happened.
  failed: 'connectors.status.probeFailed',
  disabled: 'connectors.status.disabled',
}

const CAPABILITY_KEYS: Record<string, MessageKey> = {
  read: 'connectors.cap.read',
  search: 'connectors.cap.search',
  write: 'connectors.cap.write',
  send: 'connectors.cap.send',
  attachment: 'connectors.cap.attachment',
}

/** `09:41` today, `11-20` before that — compact but never ambiguous. */
function shortTime(stamp: string | null, language: string): string {
  if (!stamp) return ''
  const time = Date.parse(stamp)
  if (!Number.isFinite(time)) return ''
  const date = new Date(time)
  const sameDay = new Date().toDateString() === date.toDateString()
  return new Intl.DateTimeFormat(language, sameDay
    ? { hour: '2-digit', minute: '2-digit' }
    : { month: '2-digit', day: '2-digit' }).format(date)
}

function statusOf(connector: ConnectorRecord): string {
  if (!connector.enabled) return 'disabled'
  if (connector.state === 'connected') return 'connected'
  if (connector.state === 'failed') return 'failed'
  return 'declared'
}

/** The one line under the card title: the account this connector acts as. */
function accountOf(connector: ConnectorRecord): string {
  const config = connector.config
  if (connector.kind === 'MAIL') {
    return [config.email, config.imapHost].filter(Boolean).join(' · ')
  }
  if (connector.kind === 'NOTION') {
    return config.workspaceName || 'Notion'
  }
  return config.operatorId ? `operator · ${config.operatorId}` : '钉钉文档'
}

/** Connectors, once there is a host to read them from. */
export default function LiveConnectorsPage() {
  const { t, language } = useI18n()
  const host = hasConnectorHost()
  const [view, setView] = useState<ConnectorOverview | null>(null)
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState('all')
  const [notice, setNotice] = useState('')
  const [failure, setFailure] = useState('')
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [form, setForm] = useState<{ kind: string; connector: ConnectorRecord | null } | null>(null)
  const [detail, setDetail] = useState<ConnectorRecord | null>(null)
  const [probe, setProbe] = useState<ConnectorProbeOutcome | null>(null)
  const trigger = useRef<HTMLElement | null>(null)

  const reload = useCallback(async () => {
    if (!host) return
    setLoading(true)
    try {
      const next = await loadConnectors()
      setView(next)
      setFailure('')
      setDetail((current) =>
        current ? next.connectors.find((row) => row.id === current.id) ?? null : null,
      )
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }, [host])

  useEffect(() => {
    void reload()
  }, [reload])

  const connectors = view?.connectors ?? []
  const stats = view?.stats

  const cards = useMemo(() => {
    const query = search.trim().toLowerCase()
    return connectors.filter((connector) => {
      const state = statusOf(connector)
      if (status !== 'all' && status !== state) return false
      if (!query) return true
      return `${connector.name} ${connector.kindName} ${accountOf(connector)} ${connector.tools
        .map((tool) => tool.name)
        .join(' ')}`
        .toLowerCase()
        .includes(query)
    })
  }, [connectors, search, status])

  const latestProbe = useMemo(() => {
    const stamps = connectors
      .map((connector) => connector.probedAt)
      .filter((stamp): stamp is string => Boolean(stamp))
      .sort()
    return stamps.length > 0 ? stamps[stamps.length - 1] : null
  }, [connectors])

  const statCards = [
    {
      key: 'connected',
      value: String(stats?.connected ?? 0),
      sub:
        stats && stats.total > 0
          ? t('connectors.stats.connectedSubLive', { total: stats.total, failed: stats.failed })
          : t('connectors.stats.empty'),
    },
    {
      key: 'actions',
      value: String(stats?.tools ?? 0),
      sub: t('connectors.stats.actionsLiveSub'),
    },
    {
      key: 'calls',
      value: String(stats?.calls ?? 0),
      sub:
        stats && stats.calls > 0
          ? t('connectors.stats.callsSubLive', {
              failed: stats.failures,
              rate: ((1 - stats.failures / stats.calls) * 100).toFixed(1),
            })
          : t('connectors.stats.noCalls'),
    },
    {
      key: 'sync',
      value: latestProbe ? shortTime(latestProbe, language) : '—',
      sub: latestProbe ? t('connectors.stats.syncSubLive') : t('connectors.stats.empty'),
    },
  ]

  const policyRows = connectors.flatMap((connector) =>
    connector.tools.map((tool) => ({
      id: `${connector.id}-${tool.name}`,
      title: connector.name,
      tool: tool.name,
      write: tool.write,
      calls: tool.calls,
      failures: tool.failures,
    })),
  )

  const openForm = (element: HTMLElement | null, kind: string, connector: ConnectorRecord | null) => {
    trigger.current = element
    setProbe(null)
    setForm({ kind, connector })
  }

  const close = () => {
    setForm(null)
    setDetail(null)
    setProbe(null)
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }

  const runProbe = async (connector: ConnectorRecord) => {
    setBusy(connector.id)
    try {
      const outcome = await probeConnector(connector.id)
      setProbe(outcome)
      setNotice(
        outcome.ok
          ? t('connectors.notice.probed', { name: connector.name })
          : t('connectors.notice.probeFailed', { name: connector.name }),
      )
      await reload()
    } catch (error) {
      setNotice(t('connectors.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy('')
    }
  }

  const probeAll = async () => {
    if (!host || connectors.length === 0) {
      setNotice(t('connectors.notice.noConnectors'))
      return
    }
    let ok = 0
    let failed = 0
    for (const connector of connectors) {
      try {
        const outcome = await probeConnector(connector.id)
        if (outcome.ok) ok += 1
        else failed += 1
      } catch {
        failed += 1
      }
    }
    setNotice(t('connectors.notice.verifiedAll', { ok, failed }))
    await reload()
  }

  const onSave = async (draft: ConnectorDraft) => {
    const next = await saveConnector(draft)
    setView(next)
    close()
    setNotice(t('connectors.notice.saved', { name: draft.name }))
  }

  const onToggle = async (connector: ConnectorRecord) => {
    try {
      const next = await toggleConnector(connector.id, !connector.enabled)
      setView(next)
      setNotice(t(connector.enabled ? 'connectors.notice.disabled' : 'connectors.notice.enabled', { name: connector.name }))
    } catch (error) {
      setNotice(t('connectors.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    }
  }

  const onDelete = async (connector: ConnectorRecord) => {
    try {
      const next = await deleteConnector(connector.id)
      setView(next)
      close()
      setNotice(t('connectors.notice.deleted', { name: connector.name }))
    } catch (error) {
      setNotice(t('connectors.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    }
  }

  return (
    <section className="cx-page" data-testid="connectors-page" data-sample="false">
      <span className="cx-sr-only" id="route-marker" data-testid="page-marker">
        R0-09 connectors · /connectors
      </span>
      <div className="cx-page-head">
        <div>
          <h1>{t('connectors.title')}</h1>
          <p>{t('connectors.descriptionLive')}</p>
        </div>
        <div className="cx-head-actions">
          <button
            type="button"
            className="cx-btn"
            disabled={!host}
            title={host ? t('connectors.verify') : t('connectors.action.unavailable')}
            onClick={() => void probeAll()}
          >
            <Icon name="shield" size={14} />
            {t('connectors.verify')}
          </button>
          <button
            type="button"
            className="cx-btn cx-btn-primary"
            data-testid="connector-add"
            disabled={!host}
            title={host ? t('connectors.add') : t('connectors.action.unavailable')}
            onClick={(event) => openForm(event.currentTarget, view?.types[0]?.id ?? 'MAIL', null)}
          >
            <Icon name="plus" size={14} />
            {t('connectors.add')}
          </button>
        </div>
      </div>

      {failure ? <div className="cx-notice-bar" data-testid="connector-error" role="alert">{failure}</div> : null}
      {notice ? <div className="cx-notice-bar" data-testid="connector-notice" role="status">{notice}</div> : null}

      <section className="cx-stats" data-testid="connector-stats" aria-label={t('connectors.title')}>
        {statCards.map((stat) => (
          <div key={stat.key} className="cx-stat" data-testid={`connector-stat-${stat.key}`}>
            <span>{t(`connectors.stats.${stat.key}` as MessageKey)}</span>
            <b>{stat.value}</b>
            <small>{stat.sub}</small>
          </div>
        ))}
      </section>

      <div className="cx-toolbar">
        <label className="cx-search">
          <Icon name="search" size={12} />
          <input
            data-testid="connector-search"
            type="search"
            value={search}
            placeholder={t('connectors.searchPlaceholder')}
            aria-label={t('connectors.searchPlaceholder')}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        <select
          aria-label={t('connectors.filter.statusLabel')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
        >
          <option value="all">{t('connectors.filter.status')}</option>
          {(['connected', 'declared', 'failed', 'disabled'] as const).map((value) => (
            <option key={value} value={value}>{t(STATUS_KEYS[value])}</option>
          ))}
        </select>
        <span className="cx-toolbar-spacer" />
        <span className="cx-hint">
          {loading ? t('connectors.loading') : t('connectors.credentialsHint')}
        </span>
      </div>

      {connectors.length > 0 ? (
        cards.length > 0 ? (
          <div className="cx-grid" data-testid="connector-grid">
            {cards.map((connector) => {
              const state = statusOf(connector)
              return (
                <article
                  key={connector.id}
                  className="cx-card"
                  data-testid="connector-card"
                  data-connector={connector.id}
                >
                  <div className="cx-card-head">
                    <span className="cx-logo">
                      <Icon name={TYPE_ICONS[connector.kind] ?? 'plug'} size={16} />
                    </span>
                    <div className="cx-card-title">
                      <div className="cx-card-name-row">
                        <h3>{connector.name}</h3>
                        <span className={`cx-status is-${state}`}>
                          <i aria-hidden="true" />
                          {t(STATUS_KEYS[state])}
                        </span>
                      </div>
                      <div className="cx-card-desc">{connector.kindName}</div>
                    </div>
                  </div>
                  <div className="cx-card-body">
                    <div className="cx-account mono">{accountOf(connector)}</div>
                    <div className="cx-scopes">
                      {connector.capabilities.map((capability) => (
                        <span key={capability} className="cx-tag">
                          {t(CAPABILITY_KEYS[capability] ?? 'connectors.cap.read')}
                        </span>
                      ))}
                      <span className="cx-tag mono">{connector.tools.length} {t('connectors.toolsUnit')}</span>
                    </div>
                  </div>
                  <div className="cx-card-foot">
                    <span className={`cx-foot-copy mono${state === 'failed' ? ' is-warn' : ''}`}>
                      {connector.lastError && state === 'failed'
                        ? connector.lastError
                        : connector.calls > 0
                          ? t('connectors.foot.calls', { count: connector.calls, time: shortTime(connector.lastCallAt, language) })
                          : connector.probedAt
                            ? t('connectors.foot.probed', { time: shortTime(connector.probedAt, language) })
                            : t('connectors.foot.unprobed')}
                    </span>
                    <button
                      type="button"
                      className="cx-btn cx-btn-sm"
                      data-testid={`connector-action-${connector.id}`}
                      disabled={busy === connector.id}
                      onClick={(event) => {
                        trigger.current = event.currentTarget
                        setProbe(null)
                        setDetail(connector)
                      }}
                    >
                      {state === 'connected' ? t('connectors.action.configure') : t('connectors.action.verify')}
                    </button>
                  </div>
                </article>
              )
            })}
          </div>
        ) : (
          <div className="cx-empty">{t('connectors.noResults')}</div>
        )
      ) : (
        <>
          <div className="cx-empty" data-testid="connector-empty">
            <Icon name="plug" size={22} />
            <b>{t('connectors.empty')}</b>
            <span>{t('connectors.emptyBody')}</span>
          </div>
          {host && view ? (
            <div className="cx-type-strip" data-testid="connector-types">
              {view.types.map((kind) => (
                <article key={kind.id} className="cx-type-card">
                  <span className="cx-logo"><Icon name={TYPE_ICONS[kind.id] ?? 'plug'} size={16} /></span>
                  <div>
                    <h3>{kind.name}</h3>
                    <p>{kind.description}</p>
                    <div className="cx-scopes">
                      {kind.tools.map((tool) => (
                        <span key={tool.name} className="cx-tag mono">{tool.name}</span>
                      ))}
                    </div>
                  </div>
                  <button
                    type="button"
                    className="cx-btn cx-btn-sm"
                    data-testid={`connector-add-${kind.id}`}
                    onClick={(event) => openForm(event.currentTarget, kind.id, null)}
                  >
                    {t('connectors.addType')}
                  </button>
                </article>
              ))}
            </div>
          ) : null}
        </>
      )}

      <div className="cx-split">
        <section className="cx-panel" data-testid="connector-policy">
          <div className="cx-panel-head">
            <h2>{t('connectors.policy.title')}</h2>
            <span>{t('connectors.policy.sub')}</span>
          </div>
          {policyRows.length > 0 ? (
            <div className="cx-table-wrap">
              <table className="cx-table">
                <thead>
                  <tr>
                    <th>{t('connectors.policy.tool')}</th>
                    <th>{t('connectors.policy.kind')}</th>
                    <th className="cx-num">{t('connectors.policy.calls')}</th>
                    <th className="cx-num">{t('connectors.policy.failures')}</th>
                  </tr>
                </thead>
                <tbody>
                  {policyRows.map((row) => (
                    <tr key={row.id} data-testid="connector-policy-row">
                      <td>
                        <div className="cx-row-title">{row.title}</div>
                        <div className="cx-row-sub mono">{row.tool}</div>
                      </td>
                      <td>
                        {row.write ? (
                          <span className="cx-badge is-warn">{t('connectors.policy.write')}</span>
                        ) : (
                          <span className="cx-badge">{t('connectors.policy.read')}</span>
                        )}
                      </td>
                      <td className="cx-num">{row.calls}</td>
                      <td className="cx-num">
                        {row.failures > 0 ? (
                          <span className="cx-badge is-warn">{row.failures}</span>
                        ) : (
                          <span className="cx-muted">0</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="cx-panel-empty">{t('connectors.policy.empty')}</div>
          )}
          <div className="cx-panel-foot">
            <Icon name="shield" size={15} />
            <span>{t('connectors.policy.footnote')}</span>
          </div>
        </section>

        <div className="cx-stack">
          <section className="cx-panel" data-testid="connector-activity">
            <div className="cx-panel-head">
              <h2>{t('connectors.activity.title')}</h2>
              <span>{t('connectors.activity.subLive')}</span>
            </div>
            {(view?.activity.length ?? 0) > 0 ? (
              <ol className="cx-timeline">
                {view?.activity.map((entry) => (
                  <li key={`${entry.at}-${entry.tool}-${entry.connectorId}`} className={`cx-tl-item is-${entry.ok ? 'done' : 'now'}`}>
                    <span className="cx-tl-time mono">{shortTime(entry.at, language)}</span>
                    <span className="cx-tl-title">{entry.name} · {entry.tool}</span>
                    <span className="cx-tl-sub">
                      {entry.ok ? t('connectors.activity.ok') : t('connectors.activity.failed')}
                    </span>
                  </li>
                ))}
              </ol>
            ) : (
              <div className="cx-panel-empty">{t('connectors.activity.empty')}</div>
            )}
          </section>

          <section className="cx-panel" data-testid="connector-health">
            <div className="cx-panel-head">
              <h2>{t('connectors.health.title')}</h2>
            </div>
            {connectors.length > 0 ? (
              <div className="cx-health">
                {connectors.map((connector) => {
                  const state = statusOf(connector)
                  const width = state === 'connected' ? 100 : state === 'failed' ? 12 : state === 'disabled' ? 0 : 40
                  return (
                    <div key={connector.id} className="cx-health-row">
                      <div className="cx-health-copy">
                        <span>{connector.name}</span>
                        <span className="cx-muted mono">
                          {connector.probedAt
                            ? t('connectors.health.probed', { time: shortTime(connector.probedAt, language) })
                            : t('connectors.foot.unprobed')}
                        </span>
                      </div>
                      <div className={`cx-meter${state === 'failed' ? ' is-warn' : ''}`}>
                        <span style={{ width: `${width}%` }} />
                      </div>
                    </div>
                  )
                })}
              </div>
            ) : (
              <div className="cx-panel-empty">{t('connectors.health.empty')}</div>
            )}
          </section>
        </div>
      </div>

      <div className="cx-footnote">
        <Icon name="alert" size={14} />
        <span>{t('connectors.footnote')}</span>
      </div>

      {form ? (
        <LiveConnectorDialog
          mode="form"
          types={view?.types ?? []}
          kind={form.kind}
          connector={form.connector}
          onClose={close}
          onSave={onSave}
        />
      ) : null}
      {detail ? (
        <LiveConnectorDialog
          mode="detail"
          types={view?.types ?? []}
          kind={detail.kind}
          connector={detail}
          probe={probe}
          busy={busy === detail.id}
          onClose={close}
          onProbe={() => void runProbe(detail)}
          onEdit={(element) => openForm(element, detail.kind, detail)}
          onToggle={() => void onToggle(detail)}
          onDelete={() => void onDelete(detail)}
        />
      ) : null}
    </section>
  )
}

/** Kept for the sample page's type list: the three types this build supports. */
export function connectorTypeNames(types: ConnectorType[]): string[] {
  return types.map((kind) => kind.name)
}
