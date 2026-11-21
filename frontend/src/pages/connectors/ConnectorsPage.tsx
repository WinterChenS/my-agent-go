import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { useCatalog } from '../../catalog/useCatalog'
import AddMcpDialog from '../mcp/AddMcpDialog'
import { AddConnectorDialog, ConnectorSettingsDialog, settingsFor } from './ConnectorDialogs'
import {
  sampleActivity,
  sampleConnectorStats,
  sampleConnectors,
  sampleHealth,
  samplePolicies,
  useConnectorsSample,
  type ConnectorAction,
  type ConnectorCategory,
  type ConnectorScope,
  type ConnectorSettings,
  type ConnectorStatus,
} from './sampleData'
import {
  hasConnectorHost,
  loadConnectors,
  verifyConnector,
  type Connector,
  type ConnectorView,
} from './client'
import './connectors.css'

const STATUS_KEYS: Record<ConnectorStatus, MessageKey> = {
  connected: 'connectors.status.connected',
  needsAuth: 'connectors.status.needsAuth',
  disconnected: 'connectors.status.disconnected',
}

const CATEGORY_KEYS: Record<ConnectorCategory, MessageKey> = {
  mail: 'connectors.category.mail',
  im: 'connectors.category.im',
  docs: 'connectors.category.docs',
  code: 'connectors.category.code',
}

const SCOPE_KEYS: Record<ConnectorScope, MessageKey> = {
  searchMail: 'connectors.scope.searchMail',
  readAttachment: 'connectors.scope.readAttachment',
  sendConfirm: 'connectors.scope.sendConfirm',
  sendGroup: 'connectors.scope.sendGroup',
  readConversation: 'connectors.scope.readConversation',
  mention: 'connectors.scope.mention',
  readDoc: 'connectors.scope.readDoc',
  writeDocConfirm: 'connectors.scope.writeDocConfirm',
  sendMessage: 'connectors.scope.sendMessage',
  readChannel: 'connectors.scope.readChannel',
  replyThread: 'connectors.scope.replyThread',
  queryDb: 'connectors.scope.queryDb',
  createPageConfirm: 'connectors.scope.createPageConfirm',
  readRepo: 'connectors.scope.readRepo',
  commentPr: 'connectors.scope.commentPr',
}

const ACTION_KEYS: Record<ConnectorAction, MessageKey> = {
  configure: 'connectors.action.configure',
  reauthorize: 'connectors.action.reauthorize',
  connect: 'connectors.action.connect',
}

const STAT_LABEL_KEYS: Record<'connected' | 'actions' | 'calls' | 'sync', MessageKey> = {
  connected: 'connectors.stats.connected',
  actions: 'connectors.stats.actions',
  calls: 'connectors.stats.calls',
  sync: 'connectors.stats.sync',
}

const STAT_LABEL_KEYS_LIVE: Record<'connected' | 'actions' | 'calls' | 'sync', MessageKey> = {
  connected: 'connectors.stats.connected',
  actions: 'connectors.stats.actionsLive',
  calls: 'connectors.stats.callsLive',
  sync: 'connectors.stats.syncLive',
}

const ICON_BY_CATEGORY: Record<ConnectorCategory, string> = {
  mail: 'mail',
  im: 'message',
  docs: 'layers',
  code: 'branch',
}

interface CardView {
  id: string
  name: string
  desc: string
  status: ConnectorStatus
  account: string
  tags: string[]
  foot: string
  warn: boolean
  action: ConnectorAction
  icon: string
  live?: Connector
}

function statusOf(state: string): ConnectorStatus {
  if (state === 'connected') return 'connected'
  if (state === 'declared') return 'disconnected'
  return 'needsAuth'
}

function relativeTime(stamp: string, language: string): string {
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

export default function ConnectorsPage() {
  const { t, language } = useI18n()
  const sample = useConnectorsSample()
  const host = hasConnectorHost()
  const catalog = useCatalog(!sample && host)
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState<'all' | ConnectorCategory>('all')
  const [status, setStatus] = useState<'all' | ConnectorStatus>('all')
  const [notice, setNotice] = useState('')
  const [failure, setFailure] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [settings, setSettings] = useState<ConnectorSettings | null>(null)
  const [info, setInfo] = useState<Connector | null>(null)
  const [view, setView] = useState<ConnectorView | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const trigger = useRef<HTMLElement | null>(null)

  const reload = useCallback(async () => {
    if (sample || !host) return
    setLoading(true)
    try {
      const next = await loadConnectors()
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
  }, [host, reload, sample])

  const cards: CardView[] = useMemo(() => {
    if (sample) {
      return sampleConnectors.map((connector) => ({
        id: connector.id,
        name: t(connector.nameKey as MessageKey),
        desc: t(connector.descriptionKey as MessageKey),
        status: connector.status,
        account: connector.accountKey ? t(connector.accountKey as MessageKey) : (connector.account ?? ''),
        tags: connector.scopes.map((scope) => t(SCOPE_KEYS[scope])),
        foot:
          connector.footKey === 'connectors.syncAt'
            ? t('connectors.syncAt', { time: connector.footTime ?? '' })
            : connector.footKey === 'connectors.expiredDays'
              ? t('connectors.expiredDays', { count: connector.footCount ?? 0 })
              : t(connector.footKey as MessageKey, { count: connector.footCount ?? 0 }),
        warn: Boolean(connector.footWarn),
        action: connector.action,
        icon: ICON_BY_CATEGORY[connector.category],
      }))
    }
    return (view?.connectors ?? []).map((connector) => {
      const state = statusOf(connector.state)
      return {
        id: `${connector.runtimeId}::${connector.name}`,
        name: connector.name,
        desc: t(connector.transport === 'stdio' ? 'connectors.transport.stdio' : 'connectors.transport.remote'),
        status: state,
        account: connector.endpoint || t('connectors.noEndpoint'),
        tags: connector.tools.slice(0, 4),
        foot: connector.lastUsed
          ? t('connectors.foot.lastUsed', { time: relativeTime(connector.lastUsed, language) })
          : connector.probedAt
            ? t('connectors.foot.probed', { time: relativeTime(connector.probedAt, language) })
            : t('connectors.foot.unprobed'),
        warn: state === 'needsAuth',
        action: state === 'connected' ? 'configure' : state === 'needsAuth' ? 'reauthorize' : 'connect',
        icon: connector.transport === 'stdio' ? 'terminal' : 'plug',
        live: connector,
      }
    })
  }, [language, sample, t, view])

  const stats = useMemo(() => {
    if (sample) {
      return sampleConnectorStats.map((stat) => ({
        key: stat.key,
        labelKey: STAT_LABEL_KEYS[stat.key],
        value: stat.value,
        sub: t(stat.subKey as MessageKey),
      }))
    }
    const live = view?.stats
    const failed = live ? live.total - live.connected : 0
    return [
      {
        key: 'connected' as const,
        labelKey: STAT_LABEL_KEYS_LIVE.connected,
        value: String(live?.connected ?? 0),
        sub: live && live.total > 0 ? t('connectors.stats.connectedSubLive', { total: live.total, failed }) : t('connectors.stats.empty'),
      },
      {
        key: 'actions' as const,
        labelKey: STAT_LABEL_KEYS_LIVE.actions,
        value: String(live?.actions ?? 0),
        sub: live?.remoteUnprobeable ? t('connectors.stats.remoteNote') : t('connectors.stats.actionsSubLive'),
      },
      {
        key: 'calls' as const,
        labelKey: STAT_LABEL_KEYS_LIVE.calls,
        value: String(live?.weekCalls ?? 0),
        sub: live && live.successRate != null
          ? t('connectors.stats.callsSubLive', { failed: live.weekFailures, rate: live.successRate.toFixed(1) })
          : t('connectors.stats.noCalls'),
      },
      {
        key: 'sync' as const,
        labelKey: STAT_LABEL_KEYS_LIVE.sync,
        value: live?.lastProbe ? relativeTime(live.lastProbe, language) : '—',
        sub: live?.lastProbe ? t('connectors.stats.syncSubLive') : t('connectors.stats.empty'),
      },
    ]
  }, [language, sample, t, view])

  const query = search.trim().toLowerCase()
  const filtered = cards.filter((connector) => {
    const matchesSearch =
      !query ||
      connector.name.toLowerCase().includes(query) ||
      connector.account.toLowerCase().includes(query) ||
      connector.tags.some((tag) => tag.toLowerCase().includes(query))
    const matchesStatus = status === 'all' || connector.status === status
    const matchesCategory =
      category === 'all' ||
      (sample && (sampleConnectors.find((row) => row.id === connector.id)?.category === category))
    return matchesSearch && matchesStatus && matchesCategory
  })

  const announce = (message: string) => setNotice(message)
  const openAdd = (element: HTMLElement) => {
    trigger.current = element
    setAddOpen(true)
  }
  const openSettings = (element: HTMLElement, connectorId: string) => {
    trigger.current = element
    setSettings(settingsFor(connectorId))
  }
  const closeDialog = () => {
    setAddOpen(false)
    setSettings(null)
    setInfo(null)
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }

  const runVerify = async (connector: Connector) => {
    setBusy(`${connector.runtimeId}::${connector.name}`)
    try {
      const result = await verifyConnector(connector.runtimeId, connector.name)
      announce(
        result.ok
          ? t('connectors.notice.verified', { name: connector.name, count: result.tools.length })
          : t('connectors.notice.verifyFailed', { name: connector.name, detail: result.detail ?? '' }),
      )
      await reload()
    } catch (error) {
      announce(t('connectors.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy('')
    }
  }

  const verifyAll = async () => {
    if (sample || !view) {
      announce(t('connectors.notice.verify'))
      return
    }
    let ok = 0
    let failed = 0
    for (const connector of view.connectors) {
      try {
        const result = await verifyConnector(connector.runtimeId, connector.name)
        if (result.ok) ok += 1
        else failed += 1
      } catch {
        failed += 1
      }
    }
    announce(t('connectors.notice.verifiedAll', { ok, failed }))
    await reload()
  }

  const runtimes = useMemo(
    () =>
      (catalog.snapshot?.runtimes ?? []).map((runtime) => ({
        id: runtime.runtimeId,
        label: runtime.displayName || runtime.runtimeId,
      })),
    [catalog.snapshot],
  )

  // Live policy table: one row per connector tool, with real call counts.
  const policyRows = useMemo(() => {
    if (sample) return []
    const rows: Array<{ id: string; title: string; tool: string; calls: number; failures: number }> = []
    for (const connector of view?.connectors ?? []) {
      for (const action of connector.actions) {
        rows.push({
          id: `${connector.name}-${action.tool}`,
          title: connector.name,
          tool: action.tool,
          calls: action.calls,
          failures: action.failures,
        })
      }
    }
    return rows.slice(0, 12)
  }, [sample, view])

  return (
    <section className="cx-page" data-testid="connectors-page" data-sample={sample ? 'true' : 'false'}>
      <span className="cx-sr-only" id="route-marker" data-testid="page-marker">
        R0-09 connectors · /connectors
      </span>
      <div className="cx-page-head">
        <div>
          <h1>{t('connectors.title')}</h1>
          <p>{t('connectors.description')}</p>
        </div>
        <div className="cx-head-actions">
          <button
            type="button"
            className="cx-btn"
            disabled={!sample && !host}
            title={sample || host ? t('connectors.verify') : t('connectors.action.unavailable')}
            onClick={() => void verifyAll()}
          >
            <Icon name="shield" size={14} />
            {t('connectors.verify')}
          </button>
          <button
            type="button"
            className="cx-btn cx-btn-primary"
            data-testid="connector-add"
            disabled={!sample && !host}
            title={sample || host ? t('connectors.add') : t('connectors.action.unavailable')}
            onClick={(event) => openAdd(event.currentTarget)}
          >
            <Icon name="plus" size={14} />
            {t('connectors.add')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="cx-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('connectors.sampleBanner')}
        </div>
      ) : null}
      {failure ? <div className="cx-notice-bar" data-testid="connector-error" role="alert">{failure}</div> : null}
      {notice ? <div className="cx-notice-bar" data-testid="connector-notice" role="status">{notice}</div> : null}

      <section className="cx-stats" data-testid="connector-stats" aria-label={t('connectors.title')}>
        {stats.map((stat) => (
          <div key={stat.key} className="cx-stat" data-testid={`connector-stat-${stat.key}`}>
            <span>{t(stat.labelKey)}</span>
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
        <select aria-label={t('connectors.filter.categoryLabel')} value={category} disabled={!sample} onChange={(event) => setCategory(event.target.value as typeof category)}>
          <option value="all">{t('connectors.filter.category')}</option>
          {(Object.keys(CATEGORY_KEYS) as ConnectorCategory[]).map((value) => (
            <option key={value} value={value}>{t(CATEGORY_KEYS[value])}</option>
          ))}
        </select>
        <select aria-label={t('connectors.filter.statusLabel')} value={status} onChange={(event) => setStatus(event.target.value as typeof status)}>
          <option value="all">{t('connectors.filter.status')}</option>
          {(Object.keys(STATUS_KEYS) as ConnectorStatus[]).map((value) => (
            <option key={value} value={value}>{t(STATUS_KEYS[value])}</option>
          ))}
        </select>
        <span className="cx-toolbar-spacer" />
        <span className="cx-hint">{loading ? t('connectors.loading') : t('connectors.credentialsHint')}</span>
      </div>

      {cards.length > 0 ? (
        filtered.length > 0 ? (
          <div className="cx-grid" data-testid="connector-grid">
            {filtered.map((connector) => (
              <article key={connector.id} className="cx-card" data-testid="connector-card" data-connector={connector.id}>
                <div className="cx-card-head">
                  <span className="cx-logo"><Icon name={connector.icon} size={16} /></span>
                  <div className="cx-card-title">
                    <div className="cx-card-name-row">
                      <h3>{connector.name}</h3>
                      <span className={`cx-status is-${connector.status}`}><i aria-hidden="true" />{t(STATUS_KEYS[connector.status])}</span>
                    </div>
                    <div className="cx-card-desc">{connector.desc}</div>
                  </div>
                </div>
                <div className="cx-card-body">
                  <div className="cx-account mono">{connector.account}</div>
                  <div className="cx-scopes">
                    {connector.tags.length > 0 ? (
                      connector.tags.map((tag) => <span key={tag} className="cx-tag">{tag}</span>)
                    ) : (
                      <span className="cx-tag">{t('connectors.noTools')}</span>
                    )}
                  </div>
                </div>
                <div className="cx-card-foot">
                  <span className={`cx-foot-copy mono${connector.warn ? ' is-warn' : ''}`}>{connector.foot}</span>
                  <button
                    type="button"
                    className="cx-btn cx-btn-sm"
                    data-testid={`connector-action-${connector.id}`}
                    disabled={busy === connector.id}
                    onClick={(event) => {
                      if (connector.live) {
                        // Every state opens the same detail dialog: a declared
                        // server is verified here, not re-created.
                        trigger.current = event.currentTarget
                        setInfo(connector.live)
                        return
                      }
                      if (connector.action === 'configure') {
                        openSettings(event.currentTarget, connector.id)
                      } else {
                        announce(
                          connector.action === 'reauthorize'
                            ? t('connectors.notice.reauthorize')
                            : t('connectors.notice.connect'),
                        )
                      }
                    }}
                  >
                    {connector.live
                      ? connector.status === 'connected'
                        ? t('connectors.action.configure')
                        : t('connectors.action.verify')
                      : t(ACTION_KEYS[connector.action])}
                  </button>
                </div>
              </article>
            ))}
          </div>
        ) : (
          <div className="cx-empty">{t('connectors.noResults')}</div>
        )
      ) : (
        <div className="cx-empty" data-testid="connector-empty">
          <Icon name="plug" size={22} />
          <b>{t('connectors.empty')}</b>
          <span>{t('connectors.emptyBody')}</span>
        </div>
      )}

      <div className="cx-split">
        <section className="cx-panel" data-testid="connector-policy">
          <div className="cx-panel-head">
            <h2>{t('connectors.policy.title')}</h2>
            <span>{t('connectors.policy.sub')}</span>
          </div>
          {sample ? (
            <div className="cx-table-wrap">
              <table className="cx-table">
                <thead>
                  <tr>
                    <th>{t('connectors.policy.connectorAction')}</th>
                    <th>{t('connectors.policy.permission')}</th>
                    <th>{t('connectors.policy.confirm')}</th>
                    <th className="cx-num">{t('connectors.policy.calls')}</th>
                  </tr>
                </thead>
                <tbody>
                  {samplePolicies.map((row) => (
                    <tr key={row.action} data-testid="connector-policy-row">
                      <td>
                        <div className="cx-row-title">{t(row.titleKey as MessageKey)}</div>
                        <div className="cx-row-sub mono">{row.action}</div>
                      </td>
                      <td><span className="cx-badge">{t(row.permissionKey as MessageKey)}</span></td>
                      <td>
                        {row.confirmKey === 'connectors.confirm.none' ? (
                          <span className="cx-muted">{t(row.confirmKey as MessageKey)}</span>
                        ) : (
                          <span className="cx-badge is-warn">{t(row.confirmKey as MessageKey)}</span>
                        )}
                      </td>
                      <td className="cx-num">{row.calls}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : policyRows.length > 0 ? (
            <div className="cx-table-wrap">
              <table className="cx-table">
                <thead>
                  <tr>
                    <th>{t('connectors.policy.tool')}</th>
                    <th>{t('connectors.policy.calls')}</th>
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
                      <td>{row.calls}</td>
                      <td className="cx-num">
                        {row.failures > 0 ? <span className="cx-badge is-warn">{row.failures}</span> : <span className="cx-muted">0</span>}
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
              <span>{sample ? t('connectors.activity.sub') : t('connectors.activity.subLive')}</span>
            </div>
            {sample ? (
              <ol className="cx-timeline">
                {sampleActivity.map((entry) => (
                  <li key={`${entry.time}-${entry.titleKey}`} className={`cx-tl-item is-${entry.state}`}>
                    <span className="cx-tl-time mono">{entry.time}</span>
                    <span className="cx-tl-title">{t(entry.titleKey as MessageKey)}</span>
                    <span className="cx-tl-sub">{t(entry.subKey as MessageKey)}</span>
                  </li>
                ))}
              </ol>
            ) : (view?.activity.length ?? 0) > 0 ? (
              <ol className="cx-timeline">
                {view?.activity.map((entry) => (
                  <li key={`${entry.at}-${entry.tool}`} className={`cx-tl-item is-${entry.ok ? 'done' : 'now'}`}>
                    <span className="cx-tl-time mono">
                      {new Date(entry.at).toLocaleTimeString(language, { hour: '2-digit', minute: '2-digit' })}
                    </span>
                    <span className="cx-tl-title">{entry.connector} · {entry.tool}</span>
                    <span className="cx-tl-sub">{entry.ok ? t('connectors.activity.ok') : t('connectors.activity.failed')}</span>
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
            {sample ? (
              <div className="cx-health">
                {sampleHealth.map((row) => (
                  <div key={row.labelKey} className="cx-health-row">
                    <div className="cx-health-copy">
                      <span>{t(row.labelKey as MessageKey)}</span>
                      <span className="cx-muted mono">{t(row.noteKey as MessageKey)}</span>
                    </div>
                    <div className={`cx-meter${row.warn ? ' is-warn' : ''}`}><span style={{ width: `${row.width}%` }} /></div>
                  </div>
                ))}
                <div className="cx-hairline" />
                <div className="cx-health-row">
                  <span className="cx-muted">{t('connectors.health.autoRenew')}</span>
                  <button
                    type="button"
                    className="cx-switch"
                    role="switch"
                    aria-checked="true"
                    aria-label={t('connectors.health.autoRenew')}
                    onClick={() => announce(t('connectors.notice.autoRenew'))}
                  >
                    <span />
                  </button>
                </div>
              </div>
            ) : (view?.connectors.length ?? 0) > 0 ? (
              <div className="cx-health">
                {view?.connectors.map((connector) => {
                  const state = statusOf(connector.state)
                  const width = state === 'connected' ? 100 : state === 'needsAuth' ? 12 : 0
                  return (
                    <div key={`${connector.runtimeId}-${connector.name}`} className="cx-health-row">
                      <div className="cx-health-copy">
                        <span>{connector.name}</span>
                        <span className="cx-muted mono">
                          {connector.probedAt
                            ? t('connectors.health.probed', { time: relativeTime(connector.probedAt, language) })
                            : t('connectors.foot.unprobed')}
                        </span>
                      </div>
                      <div className={`cx-meter${state === 'needsAuth' ? ' is-warn' : ''}`}><span style={{ width: `${width}%` }} /></div>
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

      {sample ? (
        <>
          <AddConnectorDialog open={addOpen} onClose={closeDialog} onNotice={announce} />
          <ConnectorSettingsDialog settings={settings} onClose={closeDialog} onNotice={announce} />
        </>
      ) : (
        <>
          <AddMcpDialog
            open={addOpen}
            runtimes={runtimes}
            onClose={closeDialog}
            onNotice={announce}
            onSaved={() => void reload()}
          />
          {info ? (
            <ConnectorInfoDialog
              connector={info}
              busy={busy === `${info.runtimeId}::${info.name}`}
              onClose={closeDialog}
              onVerify={() => void runVerify(info)}
            />
          ) : null}
        </>
      )}
    </section>
  )
}

function ConnectorInfoDialog({
  connector,
  busy,
  onClose,
  onVerify,
}: {
  connector: Connector
  busy: boolean
  onClose: () => void
  onVerify: () => void
}) {
  const { t } = useI18n()
  return (
    <div
      className="cx-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className="cx-dialog" role="dialog" aria-modal="true" aria-labelledby="cx-info-title">
        <div className="cx-dialog-head">
          <div>
            <h2 id="cx-info-title">{connector.name}</h2>
            <p className="mono">{connector.endpoint || '—'}</p>
          </div>
          <button type="button" className="cx-iconbtn" aria-label={t('connectors.dialog.close')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="cx-dialog-body">
          <dl className="cx-dl">
            <dt>{t('connectors.dialog.transport')}</dt><dd className="mono">{connector.transport}</dd>
            <dt>{t('connectors.dialog.state')}</dt><dd>{connector.state}</dd>
            <dt>{t('connectors.dialog.probed')}</dt><dd className="mono">{connector.probedAt ?? '—'}</dd>
            <dt>{t('connectors.dialog.calls')}</dt><dd>{connector.calls}</dd>
          </dl>
          <div className="cx-label">{t('connectors.dialog.tools')}</div>
          {connector.tools.length > 0 ? (
            <div className="cx-scopes">
              {connector.tools.map((tool) => <span key={tool} className="cx-tag">{tool}</span>)}
            </div>
          ) : (
            <p className="cx-muted">{t('connectors.dialog.noTools')}</p>
          )}
        </div>
        <div className="cx-dialog-foot">
          <button type="button" className="cx-btn cx-btn-ghost" onClick={onClose}>{t('connectors.dialog.close')}</button>
          <button type="button" className="cx-btn cx-btn-primary" disabled={busy} onClick={onVerify}>
            <Icon name="shield" size={13} />
            {t('connectors.dialog.verify')}
          </button>
        </div>
      </div>
    </div>
  )
}
