import { useMemo, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { AddConnectorDialog, ConnectorSettingsDialog, settingsFor } from './ConnectorDialogs'
import LiveConnectorsPage from './LiveConnectorsPage'
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

const ICON_BY_CATEGORY: Record<ConnectorCategory, string> = {
  mail: 'mail',
  im: 'message',
  docs: 'layers',
  code: 'branch',
}

/**
 * The connector page.
 *
 * The live entry reads the real connectors (mail / Notion / DingTalk docs) from
 * the host; the prototype fixture stays behind `/?sample=connectors` so the
 * design reference never gets mixed into real data.
 */
export default function ConnectorsPage() {
  const sample = useConnectorsSample()
  return sample ? <SampleConnectorsPage /> : <LiveConnectorsPage />
}

/** The prototype page: six connectors, their scopes and credential meters. */
function SampleConnectorsPage() {
  const { t } = useI18n()
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState<'all' | ConnectorCategory>('all')
  const [status, setStatus] = useState<'all' | ConnectorStatus>('all')
  const [notice, setNotice] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [settings, setSettings] = useState<ConnectorSettings | null>(null)
  const trigger = useRef<HTMLElement | null>(null)

  const cards = useMemo(
    () =>
      sampleConnectors.map((connector) => ({
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
        category: connector.category,
      })),
    [t],
  )

  const query = search.trim().toLowerCase()
  const filtered = cards.filter((connector) => {
    const matchesSearch =
      !query ||
      connector.name.toLowerCase().includes(query) ||
      connector.account.toLowerCase().includes(query) ||
      connector.tags.some((tag) => tag.toLowerCase().includes(query))
    const matchesStatus = status === 'all' || connector.status === status
    const matchesCategory = category === 'all' || connector.category === category
    return matchesSearch && matchesStatus && matchesCategory
  })

  const announce = (message: string) => setNotice(message)
  const closeDialog = () => {
    setAddOpen(false)
    setSettings(null)
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }

  return (
    <section className="cx-page" data-testid="connectors-page" data-sample="true">
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
            title={t('connectors.verify')}
            onClick={() => announce(t('connectors.notice.verify'))}
          >
            <Icon name="shield" size={14} />
            {t('connectors.verify')}
          </button>
          <button
            type="button"
            className="cx-btn cx-btn-primary"
            data-testid="connector-add"
            title={t('connectors.add')}
            onClick={(event) => {
              trigger.current = event.currentTarget
              setAddOpen(true)
            }}
          >
            <Icon name="plus" size={14} />
            {t('connectors.add')}
          </button>
        </div>
      </div>

      <div className="cx-sample-banner" data-testid="sample-banner" role="status">
        <Icon name="sparkle" size={12} />
        {t('connectors.sampleBanner')}
      </div>
      {notice ? <div className="cx-notice-bar" data-testid="connector-notice" role="status">{notice}</div> : null}

      <section className="cx-stats" data-testid="connector-stats" aria-label={t('connectors.title')}>
        {sampleConnectorStats.map((stat) => (
          <div key={stat.key} className="cx-stat" data-testid={`connector-stat-${stat.key}`}>
            <span>{t(`connectors.stats.${stat.key}` as MessageKey)}</span>
            <b>{stat.value}</b>
            <small>{t(stat.subKey as MessageKey)}</small>
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
        <select aria-label={t('connectors.filter.categoryLabel')} value={category} onChange={(event) => setCategory(event.target.value as typeof category)}>
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
        <span className="cx-hint">{t('connectors.credentialsHint')}</span>
      </div>

      {filtered.length > 0 ? (
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
                  {connector.tags.map((tag) => <span key={tag} className="cx-tag">{tag}</span>)}
                </div>
              </div>
              <div className="cx-card-foot">
                <span className={`cx-foot-copy mono${connector.warn ? ' is-warn' : ''}`}>{connector.foot}</span>
                <button
                  type="button"
                  className="cx-btn cx-btn-sm"
                  data-testid={`connector-action-${connector.id}`}
                  onClick={(event) => {
                    if (connector.action === 'configure') {
                      trigger.current = event.currentTarget
                      setSettings(settingsFor(connector.id))
                    } else {
                      announce(
                        connector.action === 'reauthorize'
                          ? t('connectors.notice.reauthorize')
                          : t('connectors.notice.connect'),
                      )
                    }
                  }}
                >
                  {t(ACTION_KEYS[connector.action])}
                </button>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <div className="cx-empty">{t('connectors.noResults')}</div>
      )}

      <div className="cx-split">
        <section className="cx-panel" data-testid="connector-policy">
          <div className="cx-panel-head">
            <h2>{t('connectors.policy.title')}</h2>
            <span>{t('connectors.policy.sub')}</span>
          </div>
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
            <ol className="cx-timeline">
              {sampleActivity.map((entry) => (
                <li key={`${entry.time}-${entry.titleKey}`} className={`cx-tl-item is-${entry.state}`}>
                  <span className="cx-tl-time mono">{entry.time}</span>
                  <span className="cx-tl-title">{t(entry.titleKey as MessageKey)}</span>
                  <span className="cx-tl-sub">{t(entry.subKey as MessageKey)}</span>
                </li>
              ))}
            </ol>
          </section>

          <section className="cx-panel" data-testid="connector-health">
            <div className="cx-panel-head">
              <h2>{t('connectors.health.title')}</h2>
            </div>
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
