import { useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import AddMcpDialog from './AddMcpDialog'
import {
  sampleLogs,
  sampleServices,
  sampleStats,
  useMcpSample,
  type McpService,
  type McpStatus,
  type McpTag,
  type McpTransport,
} from './sampleData'
import './mcp.css'
import { useCatalog } from '../../catalog/useCatalog'
import {
  probeCatalogMcp,
  syncCatalogInventory,
  type CatalogMcp,
  type McpProbeResult,
} from '../../catalog/client'
import { CatalogMcpPanel, CatalogSummary } from '../../catalog/CatalogPanels'

const STATUS_KEYS: Record<McpStatus, MessageKey> = {
  connected: 'mcp.status.connected',
  needsAuth: 'mcp.status.needsAuth',
  disconnected: 'mcp.status.disconnected',
}

const TAG_KEYS: Record<McpTag, MessageKey> = {
  stdio: 'mcp.tag.stdio',
  readonly: 'mcp.tag.readonly',
  http: 'mcp.tag.http',
  rowLimit: 'mcp.tag.rowLimit',
  writesWeb: 'mcp.tag.writesWeb',
  highRisk: 'mcp.tag.highRisk',
  oauthPending: 'mcp.tag.oauthPending',
}

const STAT_LABEL_KEYS: Record<(typeof sampleStats)[number]['key'], MessageKey> = {
  connected: 'mcp.stats.connected',
  tools: 'mcp.stats.tools',
  calls: 'mcp.stats.calls',
  latency: 'mcp.stats.latency',
}

export default function McpPage() {
  const { t } = useI18n()
  const sample = useMcpSample()
  const catalog = useCatalog(!sample)
  const [search, setSearch] = useState('')
  const [transport, setTransport] = useState<'all' | McpTransport>('all')
  const [status, setStatus] = useState<'all' | McpStatus>('all')
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState(false)
  const dialogTrigger = useRef<HTMLElement | null>(null)
  const [probeResults, setProbeResults] = useState<Record<string, McpProbeResult>>({})
  const [logs, setLogs] = useState<Array<{ time: string; service: string; ok: boolean; detail: string }>>([])
  const [probing, setProbing] = useState<string | null>(null)

  const query = search.trim().toLowerCase()
  // The catalog already holds the imported declarations; show them instead of
  // the "not connected" placeholder that made the sync look like it failed.
  const liveServers = catalog.snapshot?.mcp ?? []
  const liveReady = !sample && (catalog.status === 'ready' || catalog.status === 'loading') && catalog.snapshot !== null
  const liveServices = liveServers.filter((server) =>
    (!query || `${server.name} ${server.runtimeId} ${server.command ?? ''} ${server.url ?? ''}`.toLowerCase().includes(query)) &&
    (transport === 'all' || server.transport === transport) &&
    (status === 'all' || server.state === status),
  )
  const runtimeChoices = (catalog.snapshot?.runtimes ?? [])
    .filter((runtime) => runtime.capabilities.mcp !== 'unsupported')
    .map((runtime) => ({ id: runtime.runtimeId, label: runtime.displayName }))
  const keyOf = (server: CatalogMcp) => `${server.runtimeId}:${server.name}`
  const liveConnected = liveServers.filter((server) => server.state === 'connected').length
  const liveTools = Object.values(probeResults).reduce((total, report) => total + (report.ok ? report.tools.length : 0), 0)
  const services = sample ? sampleServices.filter((service) => {
    const matchesSearch = !query || service.name.toLowerCase().includes(query) ||
      service.tools.some((tool) => tool.name.toLowerCase().includes(query))
    const matchesTransport = transport === 'all' || service.transport === transport
    const matchesStatus = status === 'all' || service.status === status
    return matchesSearch && matchesTransport && matchesStatus
  }) : []

  const announce = (message: string) => setNotice(message)
  const openDialog = (trigger: HTMLElement) => {
    dialogTrigger.current = trigger
    setDialog(true)
  }
  const probeOne = async (server: CatalogMcp) => {
    const key = keyOf(server)
    setProbing(key)
    announce(t('mcp.notice.probing'))
    try {
      const report = await probeCatalogMcp(server.runtimeId, server.name)
      setProbeResults((current) => ({ ...current, [key]: report }))
      setLogs((current) => [
        {
          time: new Date().toLocaleTimeString('zh-CN', { hour12: false }),
          service: server.name,
          ok: report.ok,
          detail: report.detail || (report.ok ? `${report.tools.length} tools` : 'failed'),
        },
        ...current,
      ].slice(0, 20))
      announce(report.ok
        ? t('mcp.form.testOk', { count: report.tools.length })
        : t('mcp.form.testFailed', { message: report.detail || 'unknown error' }))
      return report
    } catch (cause) {
      const detail = cause instanceof Error ? cause.message : String(cause)
      setLogs((current) => [
        { time: new Date().toLocaleTimeString('zh-CN', { hour12: false }), service: server.name, ok: false, detail },
        ...current,
      ].slice(0, 20))
      announce(t('mcp.form.testFailed', { message: detail }))
      return null
    } finally {
      setProbing(null)
    }
  }
  const reconnect = async (sampleKey: MessageKey = 'mcp.notice.reconnect', quiet = false) => {
    if (sample) {
      announce(t(sampleKey))
      return
    }
    try {
      const fresh = await syncCatalogInventory()
      catalog.apply(fresh)
      const servers = fresh.mcp ?? []
      const reports = await Promise.all(servers.map((server) => probeCatalogMcp(server.runtimeId, server.name)))
      const results: Record<string, McpProbeResult> = {}
      const entries = servers.map((server, index) => ({
        time: new Date().toLocaleTimeString('zh-CN', { hour12: false }),
        service: server.name,
        ok: reports[index].ok,
        detail: reports[index].detail || (reports[index].ok ? `${reports[index].tools.length} tools` : 'failed'),
      }))
      servers.forEach((server, index) => { results[keyOf(server)] = reports[index] })
      setProbeResults(results)
      setLogs(entries.slice(0, 20))
      if (!quiet) announce(t('mcp.notice.reconnected'))
    } catch (cause) {
      announce(cause instanceof Error ? cause.message : String(cause))
    }
  }

  const closeDialog = () => {
    setDialog(false)
    const trigger = dialogTrigger.current
    window.setTimeout(() => trigger?.focus(), 0)
  }

  return (
    <section className="mcp-page" data-testid="mcp-page" data-sample={sample ? 'true' : 'false'}>
      <span className="sr-only" id="route-marker" data-testid="page-marker">R0-08 mcp · /mcp</span>
      <div className="mcp-page-head">
        <div>
          <h1>{t('mcp.title')}</h1>
          <p>{t('mcp.description')}</p>
        </div>
        <div className="mcp-head-actions">
          <button type="button" className="mcp-btn" data-testid="mcp-reconnect" onClick={() => void reconnect()}>
            <Icon name="refresh" size={14} />
            {t('mcp.reconnect')}
          </button>
          <button type="button" className="mcp-btn mcp-btn-primary" disabled={!sample && runtimeChoices.length === 0} title={!sample && runtimeChoices.length === 0 ? t('mcp.action.unavailable') : t('mcp.add')} onClick={(event) => openDialog(event.currentTarget)}>
            <Icon name="plus" size={14} />
            {t('mcp.add')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="mcp-sample-banner" data-testid="sample-banner" role="status"><Icon name="sparkle" size={12} />{t('mcp.sampleBanner')}</div>
      ) : null}
      {notice ? <div className="mcp-notice-bar" data-testid="mcp-notice" role="status">{notice}</div> : null}

      {!sample && (catalog.status === 'ready' || catalog.status === 'loading' || catalog.status === 'error') && (
        <div className="mcp-catalog" data-testid="mcp-catalog">
          <CatalogSummary view={catalog} />
          <CatalogMcpPanel view={catalog} />
        </div>
      )}

      <section className="mcp-stats" data-testid="mcp-stats" aria-label={t('mcp.title')}>
        {sampleStats.map((stat) => (
          <div key={stat.key} className="mcp-stat" data-testid={`mcp-stat-${stat.key}`}>
            <span>{t(STAT_LABEL_KEYS[stat.key])}</span>
            <b>{sample ? stat.value : liveReady ? (stat.key === 'connected' ? liveConnected : stat.key === 'tools' ? liveTools : '—') : stat.key === 'latency' ? '—' : '0'}</b>
            <small>{sample ? t(stat.subKey as MessageKey) : t('mcp.stats.empty')}</small>
          </div>
        ))}
      </section>

      <div className="mcp-toolbar">
        <label className="mcp-search">
          <Icon name="search" size={12} />
          <input data-testid="mcp-search" type="search" value={search} disabled={!sample && !liveReady} placeholder={t('mcp.searchPlaceholder')} aria-label={t('mcp.searchPlaceholder')} onChange={(event) => setSearch(event.target.value)} />
        </label>
        <select aria-label={t('mcp.filter.transportLabel')} value={transport} disabled={!sample && !liveReady} onChange={(event) => setTransport(event.target.value as typeof transport)}>
          <option value="all">{t('mcp.filter.transport')}</option>
          <option value="stdio">{t('mcp.transport.stdio')}</option>
          <option value="http">{t('mcp.transport.http')}</option>
          <option value="websocket">{t('mcp.transport.websocket')}</option>
        </select>
        <select aria-label={t('mcp.filter.statusLabel')} value={status} disabled={!sample && !liveReady} onChange={(event) => setStatus(event.target.value as typeof status)}>
          <option value="all">{t('mcp.filter.status')}</option>
          <option value="connected">{t('mcp.status.connected')}</option>
          <option value="needsAuth">{t('mcp.status.needsAuth')}</option>
          <option value="disconnected">{t('mcp.status.disconnected')}</option>
        </select>
        <span className="mcp-toolbar-spacer" />
        <span className="mcp-config-path">{t('mcp.configPath')} <code className="mono">~/.anvil/mcp.json</code></span>
      </div>

      {sample ? (
        services.length > 0 ? (
          <div className="mcp-grid" data-testid="mcp-grid">
            {services.map((service) => (
              <McpCard key={service.id} service={service} onNotice={announce} />
            ))}
          </div>
        ) : (
          <div className="mcp-empty">{t('mcp.noResults')}</div>
        )
      ) : liveReady && liveServices.length > 0 ? (
        <div className="mcp-grid" data-testid="mcp-grid">
          {liveServices.map((server) => (
            <LiveMcpCard
              key={keyOf(server)}
              server={server}
              report={probeResults[keyOf(server)]}
              probing={probing === keyOf(server)}
              onProbe={() => void probeOne(server)}
            />
          ))}
        </div>
      ) : (
        <div className="mcp-empty" data-testid="mcp-empty-services">
          <Icon name="plug" size={22} />
          <b>{liveReady ? t('catalog.mcp.empty') : t('mcp.empty')}</b>
          <span>{liveReady ? t('catalog.inventory.sync') : t('mcp.emptyBody')}</span>
        </div>
      )}

      <section className="mcp-logs" data-testid="mcp-logs" aria-label={t('mcp.logs.title')}>
        <div className="mcp-logs-head">
          <h2>{t('mcp.logs.title')}</h2>
          <span>{sample ? t('mcp.logs.recent', { count: sampleLogs.length }) : ''}</span>
          <button type="button" className="mcp-btn mcp-btn-sm" data-testid="mcp-logs-refresh" onClick={() => void reconnect('mcp.logs.notice')}>
            <Icon name="refresh" size={13} />
            {t('mcp.logs.refresh')}
          </button>
        </div>
        {sample ? (
          <div className="mcp-log-list">
            {sampleLogs.map((log) => (
              <div key={`${log.time}-${log.service}`} className="mcp-log" data-testid="mcp-log">
                <span className="mcp-log-time">{log.time}</span>
                <span className="mcp-log-service">{log.service}</span>
                <span className={`mcp-log-result${log.ok ? ' is-ok' : ' is-error'}`}>{log.ok ? '✓' : '✗'} {log.result}</span>
                <span className="mcp-log-detail">{log.detail}</span>
              </div>
            ))}
          </div>
        ) : logs.length > 0 ? (
          <div className="mcp-log-list">
            {logs.map((log, index) => (
              <div key={`${log.time}-${log.service}-${index}`} className="mcp-log" data-testid="mcp-log">
                <span className="mcp-log-time">{log.time}</span>
                <span className="mcp-log-service">{log.service}</span>
                <span className={`mcp-log-result${log.ok ? ' is-ok' : ' is-error'}`}>{log.ok ? '✓' : '✗'}</span>
                <span className="mcp-log-detail">{log.detail}</span>
              </div>
            ))}
          </div>
        ) : (
          <div className="mcp-logs-empty" data-testid="mcp-empty-logs">{t('mcp.logs.empty')}</div>
        )}
      </section>

      <div className="mcp-footnote"><Icon name="shield" size={14} /><span>{t('mcp.footnote')}</span></div>
      <AddMcpDialog
        open={dialog}
        sample={sample}
        runtimes={runtimeChoices}
        onClose={closeDialog}
        onNotice={announce}
        onSaved={() => void reconnect('mcp.notice.reconnect', true)}
      />
    </section>
  )
}

/** A declaration the catalog actually read from a runtime's config file. */
function LiveMcpCard({
  server,
  report,
  probing,
  onProbe,
}: {
  server: CatalogMcp
  report?: McpProbeResult
  probing: boolean
  onProbe: () => void
}) {
  const { t } = useI18n()
  return (
    <section className={`mcp-card is-${server.state}`} data-testid="mcp-card" data-service={`${server.runtimeId}:${server.name}`}>
      <div className="mcp-card-head">
        <div>
          <h2 className="mono">{server.name}</h2>
          <span className="mcp-card-desc mono">{server.source}</span>
        </div>
        <div className="mcp-card-actions">
          <span className={`mcp-status is-${report ? (report.ok ? 'connected' : 'disconnected') : 'disconnected'}`} data-state={report ? (report.ok ? 'connected' : 'failed') : server.state}>{report ? (report.ok ? 'connected' : 'failed') : server.state}</span>
          <button type="button" className="mcp-btn mcp-btn-sm" data-testid="mcp-probe" disabled={probing} onClick={onProbe}>{probing ? '…' : t('mcp.action.probe')}</button>
        </div>
      </div>
      <div className="mcp-card-config">
        <code className="mono">{server.command ?? server.url ?? '—'}</code>
        <div className="mcp-tags">
          <span className="mcp-tag">{server.transport}</span>
          <span className="mcp-tag mono">{server.runtimeId}</span>
          {server.envKeys.length + server.headerKeys.length > 0 ? (
            <span className="mcp-tag">{server.envKeys.length + server.headerKeys.length} keys</span>
          ) : null}
          {report?.ok ? <span className="mcp-tag">{t('mcp.toolsCount', { count: report.tools.length })}</span> : null}
        </div>
      </div>
      {report?.tools.length ? (
        <div className="mcp-tools" data-testid="mcp-tools" role="list" aria-label={`${server.name} ${t('mcp.toolsLabel')}`}>
          {report.tools.map((tool) => <div key={tool} className="mcp-tool" data-testid="mcp-tool" role="listitem"><code>{tool}</code></div>)}
        </div>
      ) : null}
      {report && !report.ok && report.detail ? <p className="mcp-error" data-testid="mcp-probe-error">{report.detail}</p> : null}
    </section>
  )
}

function McpCard({ service, onNotice }: { service: McpService; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className={`mcp-card is-${service.status}`} data-testid="mcp-card" data-service={service.id}>
      <div className="mcp-card-head">
        <div>
          <h2>{service.name}</h2>
          <span className="mcp-card-desc">{t(service.descriptionKey as MessageKey)}</span>
        </div>
        <div className="mcp-card-actions">
          <span className={`mcp-status is-${service.status}`}><i aria-hidden="true" />{t(STATUS_KEYS[service.status])}</span>
          <button type="button" className="mcp-iconbtn" aria-label={t('mcp.more')} onClick={() => onNotice(t('mcp.notice.settings'))}><Icon name="more" size={15} /></button>
        </div>
      </div>
      <div className="mcp-card-config">
        <code className="mono">{service.command}</code>
        <div className="mcp-tags">
          {service.tags.map((tag) => <span key={tag} className="mcp-tag">{t(TAG_KEYS[tag])}</span>)}
          <span className="mcp-tag">{t('mcp.toolsCount', { count: service.tools.length })}</span>
        </div>
      </div>
      {service.tools.length > 0 ? (
        <div className="mcp-tools" data-testid="mcp-tools" role="list" aria-label={`${service.name} ${t('mcp.toolsLabel')}`}>
          {service.tools.map((tool) => (
            <div key={tool.name} className="mcp-tool" data-testid="mcp-tool" role="listitem">
              <code>{tool.name}</code>
              <span>{t(tool.descKey as MessageKey)}</span>
            </div>
          ))}
        </div>
      ) : (
        <div className="mcp-card-auth-note">{t('mcp.notionAuth')}</div>
      )}
      <div className="mcp-card-foot">
        {service.status === 'connected' ? (
          <>
            <span>{t('mcp.callsToday', { count: service.callsToday ?? 0 })}</span>
            <span>·</span>
            <span>{service.lastError ? t('mcp.lastError', { time: service.lastError }) : t('mcp.average', { latency: service.averageLatency ?? '' })}</span>
            <button type="button" className="mcp-switch" role="switch" aria-checked="true" aria-label={`${service.name} ${t('mcp.status.connected')}`} onClick={() => onNotice(t('mcp.notice.disable'))}><span /></button>
          </>
        ) : null}
        {service.status === 'needsAuth' ? (
          <>
            <span>{t('mcp.waitingAuth')}</span>
            <button type="button" className="mcp-btn mcp-btn-sm" onClick={() => onNotice(t('mcp.notice.auth'))}>{t('mcp.goConfirm')}</button>
          </>
        ) : null}
        {service.status === 'disconnected' ? (
          <>
            <span>{t('mcp.tryFailed', { time: service.tryAt ?? '' })}</span>
            <button type="button" className="mcp-btn mcp-btn-sm mcp-btn-primary" onClick={() => onNotice(t('mcp.notice.auth'))}>{t('mcp.authorize')}</button>
          </>
        ) : null}
      </div>
    </section>
  )
}
