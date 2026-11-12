import { useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
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

const STAT_LABEL_KEYS: Record<(typeof sampleConnectorStats)[number]['key'], MessageKey> = {
  connected: 'connectors.stats.connected',
  actions: 'connectors.stats.actions',
  calls: 'connectors.stats.calls',
  sync: 'connectors.stats.sync',
}

const ICON_BY_CATEGORY: Record<ConnectorCategory, string> = {
  mail: 'mail',
  im: 'message',
  docs: 'layers',
  code: 'branch',
}

export default function ConnectorsPage() {
  const { t } = useI18n()
  const sample = useConnectorsSample()
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState<'all' | ConnectorCategory>('all')
  const [status, setStatus] = useState<'all' | ConnectorStatus>('all')
  const [notice, setNotice] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [settings, setSettings] = useState<ConnectorSettings | null>(null)
  const trigger = useRef<HTMLElement | null>(null)

  const query = search.trim().toLowerCase()
  const connectors = sample
    ? sampleConnectors.filter((connector) => {
        const matchesSearch =
          !query ||
          t(connector.nameKey as MessageKey).toLowerCase().includes(query) ||
          (connector.accountKey ? t(connector.accountKey as MessageKey) : (connector.account ?? '')).toLowerCase().includes(query) ||
          connector.scopes.some((scope) => t(SCOPE_KEYS[scope]).toLowerCase().includes(query))
        const matchesCategory = category === 'all' || connector.category === category
        const matchesStatus = status === 'all' || connector.status === status
        return matchesSearch && matchesCategory && matchesStatus
      })
    : []

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
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }

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
            disabled={!sample}
            title={sample ? t('connectors.verify') : t('connectors.action.unavailable')}
            onClick={() => announce(t('connectors.notice.verify'))}
          >
            <Icon name="shield" size={14} />
            {t('connectors.verify')}
          </button>
          <button
            type="button"
            className="cx-btn cx-btn-primary"
            data-testid="connector-add"
            disabled={!sample}
            title={sample ? t('connectors.add') : t('connectors.action.unavailable')}
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
      {notice ? <div className="cx-notice-bar" data-testid="connector-notice" role="status">{notice}</div> : null}

      <section className="cx-stats" data-testid="connector-stats" aria-label={t('connectors.title')}>
        {sampleConnectorStats.map((stat) => (
          <div key={stat.key} className="cx-stat" data-testid={`connector-stat-${stat.key}`}>
            <span>{t(STAT_LABEL_KEYS[stat.key])}</span>
            <b>{sample ? stat.value : stat.key === 'sync' ? '—' : '0'}</b>
            <small>{sample ? t(stat.subKey as MessageKey) : t('connectors.stats.empty')}</small>
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
            disabled={!sample}
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
        <select aria-label={t('connectors.filter.statusLabel')} value={status} disabled={!sample} onChange={(event) => setStatus(event.target.value as typeof status)}>
          <option value="all">{t('connectors.filter.status')}</option>
          {(Object.keys(STATUS_KEYS) as ConnectorStatus[]).map((value) => (
            <option key={value} value={value}>{t(STATUS_KEYS[value])}</option>
          ))}
        </select>
        <span className="cx-toolbar-spacer" />
        <span className="cx-hint">{t('connectors.credentialsHint')}</span>
      </div>

      {sample ? (
        connectors.length > 0 ? (
          <div className="cx-grid" data-testid="connector-grid">
            {connectors.map((connector) => (
              <article key={connector.id} className="cx-card" data-testid="connector-card" data-connector={connector.id}>
                <div className="cx-card-head">
                  <span className="cx-logo"><Icon name={ICON_BY_CATEGORY[connector.category]} size={16} /></span>
                  <div className="cx-card-title">
                    <div className="cx-card-name-row">
                      <h3>{t(connector.nameKey as MessageKey)}</h3>
                      <span className={`cx-status is-${connector.status}`}><i aria-hidden="true" />{t(STATUS_KEYS[connector.status])}</span>
                    </div>
                    <div className="cx-card-desc">{t(connector.descriptionKey as MessageKey)}</div>
                  </div>
                </div>
                <div className="cx-card-body">
                  <div className="cx-account mono">{connector.accountKey ? t(connector.accountKey as MessageKey) : connector.account}</div>
                  <div className="cx-scopes">
                    {connector.scopes.map((scope) => (
                      <span key={scope} className="cx-tag">{t(SCOPE_KEYS[scope])}</span>
                    ))}
                  </div>
                </div>
                <div className="cx-card-foot">
                  <span className={`cx-foot-copy mono${connector.footWarn ? ' is-warn' : ''}`}>
                    {connector.footKey === 'connectors.syncAt'
                      ? t('connectors.syncAt', { time: connector.footTime ?? '' })
                      : connector.footKey === 'connectors.expiredDays'
                        ? t('connectors.expiredDays', { count: connector.footCount ?? 0 })
                        : t(connector.footKey as MessageKey, { count: connector.footCount ?? 0 })}
                  </span>
                  <button
                    type="button"
                    className="cx-btn cx-btn-sm"
                    data-testid={`connector-action-${connector.id}`}
                    onClick={(event) =>
                      connector.action === 'configure'
                        ? openSettings(event.currentTarget, connector.id)
                        : announce(
                            connector.action === 'reauthorize'
                              ? t('connectors.notice.reauthorize')
                              : t('connectors.notice.connect'),
                          )
                    }
                  >
                    {t(ACTION_KEYS[connector.action])}
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
              <span>{t('connectors.activity.sub')}</span>
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

      <AddConnectorDialog open={addOpen} onClose={closeDialog} onNotice={announce} />
      <ConnectorSettingsDialog settings={settings} onClose={closeDialog} onNotice={announce} />
    </section>
  )
}
