import { useState, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import LanguageSettings from '../../i18n/LanguageSettings'
import Icon from '../../shell/Icon'
import ThemeSettings from './ThemeSettings'
import {
  fastRouteModels,
  modelRouteModels,
  replyLanguageKeys,
  sampleAgentBars,
  sampleEnvVars,
  sampleExecutions,
  samplePermissions,
  sampleProviders,
  sampleRuntimes,
  sampleTraces,
  sampleUsage,
  sampleUsageBars,
  sampleUsageChart,
  timezones,
  useSettingsSample,
} from './settingsData'
import './settings.css'
import { useCatalog } from '../../catalog/useCatalog'
import {
  CatalogModelsPanel,
  CatalogRuntimeTable,
  CatalogSummary,
} from '../../catalog/CatalogPanels'

type SectionId = 'general' | 'models' | 'runtime' | 'observability' | 'security'
type ObsTab = 'usage' | 'traces' | 'executions'

const SECTIONS: Array<{ id: SectionId; icon: string; labelKey: MessageKey; keywords: string }> = [
  { id: 'general', icon: 'settings', labelKey: 'settings.nav.general', keywords: '通用 工作区 外观 通知' },
  { id: 'models', icon: 'brain', labelKey: 'settings.nav.models', keywords: '模型 供应商 API 密钥 目录' },
  { id: 'runtime', icon: 'terminal', labelKey: 'settings.nav.runtime', keywords: '运行时 并发 环境 权限 沙箱' },
  { id: 'observability', icon: 'zap', labelKey: 'settings.nav.observability', keywords: '观测 Token 用量 审计 链路 执行' },
  { id: 'security', icon: 'shield', labelKey: 'settings.nav.security', keywords: '安全 权限 确认 审计 数据' },
]

function sectionFromHash(hash: string): SectionId {
  const raw = hash.replace('#', '')
  const key = raw === 'obs' ? 'observability' : raw
  return (SECTIONS.find((section) => section.id === key)?.id ?? 'general') as SectionId
}

export default function SettingsPage() {
  const { t, language, setLanguage } = useI18n()
  const sample = useSettingsSample()
  const location = useLocation()
  const navigate = useNavigate()
  const section = sectionFromHash(location.hash)
  const [navQuery, setNavQuery] = useState('')
  const [notice, setNotice] = useState('')

  const sections = SECTIONS.filter((entry) => {
    const query = navQuery.trim().toLowerCase()
    if (!query) return true
    return `${t(entry.labelKey)} ${entry.keywords}`.toLowerCase().includes(query)
  })

  // The isolated sample entry must survive a section switch; otherwise the
  // fixtures disappear as soon as the user opens another section.
  const base = sample ? '/__sample/settings' : '/settings'
  const open = (id: SectionId) => navigate({ pathname: base, search: location.search, hash: id === 'general' ? '' : `#${id}` })
  const announce = (message: string) => setNotice(message)

  return (
    <section className="st-page" data-testid="settings-page" data-sample={sample ? 'true' : 'false'}>
      <span className="st-sr-only" id="route-marker" data-testid="page-marker">
        R0-12 settings · /settings
      </span>
      <div className="st-page-head">
        <div>
          <h1>{t('settings.title')}</h1>
          <p>{t('settings.description')}</p>
        </div>
        <div className="st-head-actions">
          <button type="button" className="st-btn" onClick={() => announce(t('settings.notice.check'))}>
            <Icon name="check" size={14} />
            {t('settings.check')}
          </button>
          <button type="button" className="st-btn st-btn-primary" onClick={() => announce(t('settings.notice.save'))}>
            <Icon name="check" size={14} />
            {t('settings.save')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="st-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('settings.sampleBanner')}
        </div>
      ) : null}
      {notice ? <div className="st-notice-bar" data-testid="settings-notice" role="status">{notice}</div> : null}

      <div className="st-layout">
        <nav className="st-nav" aria-label={t('settings.nav.label')}>
          <label className="st-nav-search">
            <Icon name="search" size={12} />
            <input
              data-testid="settings-nav-search"
              type="search"
              value={navQuery}
              placeholder={t('settings.nav.search')}
              aria-label={t('settings.nav.search')}
              onChange={(event) => setNavQuery(event.target.value)}
            />
          </label>
          {sections.map((entry) => (
            <button
              key={entry.id}
              type="button"
              className={`st-nav-item${section === entry.id ? ' is-active' : ''}`}
              aria-current={section === entry.id ? 'true' : undefined}
              data-testid={`settings-nav-${entry.id}`}
              onClick={() => open(entry.id)}
            >
              <Icon name={entry.icon} size={14} />
              {t(entry.labelKey)}
            </button>
          ))}
          {sections.length === 0 ? <div className="st-nav-empty" data-testid="settings-nav-empty">{t('settings.nav.empty')}</div> : null}
        </nav>

        <div className="st-content">
          {section === 'general' ? (
            <>
              <GeneralPanel
                sample={sample}
                language={language}
                onLanguage={setLanguage}
                onNotice={announce}
              />
              <ThemeSettings />
              <LanguageSettings />
              <NotificationsCard sample={sample} onNotice={announce} />
            </>
          ) : null}
          {section === 'models' ? <ModelsPanel sample={sample} onNotice={announce} /> : null}
          {section === 'runtime' ? <RuntimePanel sample={sample} onNotice={announce} /> : null}
          {section === 'observability' ? <ObservabilityPanel sample={sample} onNotice={announce} /> : null}
          {section === 'security' ? <SecurityPanel sample={sample} onNotice={announce} /> : null}
        </div>
      </div>
    </section>
  )
}

function Card({ title, sub, actions, children, testId }: { title: string; sub?: string; actions?: ReactNode; children: ReactNode; testId?: string }) {
  return (
    <div className="card" data-testid={testId}>
      <div className="card-head">
        <h3>{title}</h3>
        {sub ? <span className="card-head-sub">{sub}</span> : null}
        {actions ? <div className="st-card-actions">{actions}</div> : null}
      </div>
      {children}
    </div>
  )
}

function Empty({ text, testId }: { text: string; testId?: string }) {
  return <div className="st-empty" data-testid={testId}>{text}</div>
}

function GeneralPanel({
  sample,
  language,
  onLanguage,
  onNotice,
}: {
  sample: boolean
  language: 'zh-CN' | 'en-US'
  onLanguage: (language: 'zh-CN' | 'en-US') => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  return (
    <div className="st-stack">
      <Card title={t('settings.general.workspace')} sub={t('settings.general.workspaceSub')} testId="settings-workspace">
        <div className="st-rows">
          <SettingRow title={t('settings.general.workspaceName')} sub={t('settings.general.workspaceNameSub')}>
            <input
              className="st-input"
              aria-label={t('settings.general.workspaceName')}
              defaultValue={sample ? 'Anvil 个人工作区' : ''}
              placeholder={sample ? undefined : t('settings.action.unavailable')}
              disabled={!sample}
              onChange={() => undefined}
            />
          </SettingRow>
          <SettingRow title={t('settings.language.title')} sub={t('settings.language.subtitle')}>
            <select
              className="st-select"
              aria-label={t('settings.language.subtitle')}
              value={language}
              onChange={(event) => onLanguage(event.target.value as 'zh-CN' | 'en-US')}
            >
              <option value="zh-CN">{t('settings.language.zh')}</option>
              <option value="en-US">{t('settings.language.en')}</option>
            </select>
          </SettingRow>
          <SettingRow title={t('settings.general.replyLanguage')} sub={t('settings.general.replyLanguageSub')}>
            <select className="st-select" aria-label={t('settings.general.replyLanguage')} disabled={!sample}>
              {replyLanguageKeys.map((key) => <option key={key}>{t(key as MessageKey)}</option>)}
            </select>
          </SettingRow>
          <SettingRow title={t('settings.general.timezone')} sub={t('settings.general.timezoneSub')}>
            <select className="st-select" aria-label={t('settings.general.timezone')} disabled={!sample}>
              {timezones.map((value) => <option key={value}>{value}</option>)}
            </select>
          </SettingRow>
          <SettingRow title={t('settings.general.autoArchive')} sub={t('settings.general.autoArchiveSub')}>
            <Switch checked={sample} label={t('settings.general.autoArchive')} disabled={!sample} onChange={() => onNotice(t('settings.notice.toggle'))} />
          </SettingRow>
        </div>
      </Card>
      {!sample ? <p className="st-hint">{t('settings.general.onlyReal')}</p> : null}
    </div>
  )
}

function NotificationsCard({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <Card title={t('settings.notify.title')} testId="settings-notifications">
      {sample ? (
        <div className="st-rows">
          <SettingRow title={t('settings.notify.pending')} sub={t('settings.notify.pendingSub')}>
            <Switch checked label={t('settings.notify.pending')} onChange={() => onNotice(t('settings.notice.toggle'))} />
          </SettingRow>
          <SettingRow title={t('settings.notify.failed')} sub={t('settings.notify.failedSub')}>
            <Switch checked label={t('settings.notify.failed')} onChange={() => onNotice(t('settings.notice.toggle'))} />
          </SettingRow>
          <SettingRow title={t('settings.notify.weekly')} sub={t('settings.notify.weeklySub')}>
            <Switch checked={false} label={t('settings.notify.weekly')} onChange={() => onNotice(t('settings.notice.toggle'))} />
          </SettingRow>
        </div>
      ) : (
        <Empty text={t('settings.notify.empty')} testId="settings-notifications-empty" />
      )}
    </Card>
  )
}

function ModelsPanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const catalog = useCatalog(!sample)
  const [query, setQuery] = useState('')
  const providers = sample
    ? sampleProviders.filter((provider) => {
        const value = query.trim().toLowerCase()
        if (!value) return true
        return `${provider.name} ${provider.host} ${provider.models.join(' ')}`.toLowerCase().includes(value)
      })
    : []

  return (
    <div className="st-stack">
      <Card
        title={t('settings.models.title')}
        sub={sample ? t('settings.models.sub') : undefined}
        testId="settings-models"
        actions={
          <>
            <input
              className="st-input st-input-sm"
              type="search"
              value={query}
              disabled={!sample}
              placeholder={t('settings.models.search')}
              aria-label={t('settings.models.search')}
              onChange={(event) => setQuery(event.target.value)}
            />
            <button type="button" className="st-btn st-btn-sm" disabled={!sample} onClick={() => onNotice(t('settings.notice.addProvider'))}>
              <Icon name="plus" size={12} />
              {t('settings.models.add')}
            </button>
          </>
        }
      >
        {sample ? (
          providers.length > 0 ? (
            <div className="st-table-wrap">
              <table className="st-table" aria-label={t('settings.models.title')}>
                <thead>
                  <tr>
                    <th>{t('settings.table.provider')}</th>
                    <th>{t('settings.table.models')}</th>
                    <th>{t('settings.table.credential')}</th>
                    <th>{t('settings.table.status')}</th>
                    <th className="st-num">{t('settings.table.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {providers.map((provider) => (
                    <tr key={provider.id} data-testid="settings-provider-row">
                      <td>
                        <div className="st-provider">
                          <span className="st-mark">{provider.mark}</span>
                          <span>
                            <span className="st-row-title">{provider.name}</span>
                            <span className="st-row-sub">{provider.host}</span>
                          </span>
                        </div>
                      </td>
                      <td>
                        <span className="st-chips">
                          {provider.models.map((model) => <span key={model} className="st-chip mono">{model}</span>)}
                        </span>
                      </td>
                      <td><span className="st-row-sub mono">{provider.key}</span></td>
                      <td><span className="st-badge is-success"><i />{t('settings.models.available')}</span></td>
                      <td className="st-num">
                        <button type="button" className="st-btn st-btn-sm st-btn-ghost" onClick={() => onNotice(t('settings.notice.testProvider', { name: provider.name }))}>
                          {t('settings.models.test')}
                        </button>
                        <button type="button" className="st-iconbtn" aria-label={t('settings.models.more', { name: provider.name })} onClick={() => onNotice(t('settings.notice.providerMenu'))}>
                          <Icon name="more" size={14} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty text={t('settings.models.noMatch')} />
          )
        ) : catalog.status === 'ready' ? (
          <div className="st-catalog" data-testid="settings-models-catalog">
            <CatalogSummary view={catalog} />
            <CatalogModelsPanel view={catalog} />
          </div>
        ) : (
          <Empty
            text={
              catalog.status === 'loading' ? t('catalog.loading')
                : catalog.status === 'error' ? t('catalog.error', { message: catalog.error ?? '' })
                  : t('settings.models.empty')
            }
            testId="settings-models-empty"
          />
        )}
      </Card>

      <Card title={t('settings.route.title')} sub={t('settings.route.sub')} testId="settings-route">
        <div className="st-rows">
          <SettingRow title={t('settings.route.complex')} sub={t('settings.route.complexSub')}>
            <select className="st-select" aria-label={t('settings.route.complex')} disabled={!sample}>
              {modelRouteModels.map((value) => <option key={value}>{value}</option>)}
            </select>
          </SettingRow>
          <SettingRow title={t('settings.route.fast')} sub={t('settings.route.fastSub')}>
            <select className="st-select" aria-label={t('settings.route.fast')} disabled={!sample}>
              {fastRouteModels.map((value) => <option key={value}>{value}</option>)}
            </select>
          </SettingRow>
          <SettingRow title={t('settings.route.failover')} sub={t('settings.route.failoverSub')}>
            <Switch checked={sample} label={t('settings.route.failover')} disabled={!sample} onChange={() => onNotice(t('settings.notice.toggle'))} />
          </SettingRow>
        </div>
      </Card>
    </div>
  )
}

function RuntimePanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const catalog = useCatalog(!sample)
  const [permission, setPermission] = useState('balanced')
  const current = samplePermissions.find((entry) => entry.id === permission) ?? samplePermissions[1]

  return (
    <div className="st-stack">
      <Card
        title={t('settings.runtime.title')}
        sub={t('settings.runtime.sub')}
        testId="settings-runtime"
        actions={
          <button type="button" className="st-btn st-btn-sm" disabled={!sample} onClick={() => onNotice(t('settings.notice.addRuntime'))}>
            <Icon name="plus" size={12} />
            {t('settings.runtime.add')}
          </button>
        }
      >
        {sample ? (
          <div className="st-runtimes">
            {sampleRuntimes.map((runtime) => (
              <article key={runtime.id} className={`st-runtime${runtime.isDefault ? ' is-default' : ''}`} data-testid="settings-runtime-card">
                <div className="st-runtime-head">
                  <span className="st-runtime-mark">{runtime.mark}</span>
                  <span className="st-runtime-title">
                    <span className="st-row-title">{runtime.name}</span>
                    <span className="st-row-sub mono">{runtime.cli}</span>
                  </span>
                  <span className={`st-badge ${runtime.state === 'running' ? 'is-success' : ''}`}>
                    <i />{runtime.state === 'running' ? t('settings.runtime.running') : t('settings.runtime.installed')}
                  </span>
                </div>
                <p className="st-runtime-desc">{runtime.description}</p>
                <span className="st-chips">
                  {runtime.models.map((model) => <span key={model} className="st-chip mono">{model}</span>)}
                </span>
                <div className="st-runtime-foot">
                  <span className="st-row-sub">{runtime.usage}</span>
                  <button type="button" className="st-btn st-btn-sm st-btn-ghost" onClick={() => onNotice(t('settings.notice.checkRuntime'))}>{t('settings.runtime.check')}</button>
                  <button type="button" className="st-btn st-btn-sm" onClick={() => onNotice(t('settings.notice.configureRuntime'))}>{t('settings.runtime.configure')}</button>
                </div>
              </article>
            ))}
          </div>
        ) : catalog.status === 'ready' ? (
          <div className="st-catalog" data-testid="settings-runtime-catalog">
            <CatalogSummary view={catalog} />
            <CatalogRuntimeTable view={catalog} />
          </div>
        ) : (
          <Empty
            text={catalog.status === 'loading' ? t('catalog.loading') : t('settings.runtime.empty')}
            testId="settings-runtime-empty"
          />
        )}
      </Card>

      <Card title={t('settings.runtime.defaults')} sub={t('settings.runtime.defaultsSub')} testId="settings-runtime-defaults">
        <div className="st-rows">
          <SettingRow title={t('settings.runtime.defaultRuntime')} sub={t('settings.runtime.defaultRuntimeSub')}>
            <select className="st-select" aria-label={t('settings.runtime.defaultRuntime')} disabled={!sample}>
              {sampleRuntimes.map((runtime) => <option key={runtime.id}>{runtime.name}</option>)}
            </select>
          </SettingRow>
          <SettingRow title={t('settings.runtime.cwd')} sub={t('settings.runtime.cwdSub')}>
            <input className="st-input mono" aria-label={t('settings.runtime.cwd')} defaultValue={sample ? '~/work' : ''} disabled={!sample} readOnly />
          </SettingRow>
          <SettingRow title={t('settings.runtime.concurrency')} sub={t('settings.runtime.concurrencySub')}>
            <select className="st-select" aria-label={t('settings.runtime.concurrency')} disabled={!sample}>
              <option>{t('settings.runtime.processes', { count: 4 })}</option>
              <option>{t('settings.runtime.processes', { count: 2 })}</option>
              <option>{t('settings.runtime.processes', { count: 8 })}</option>
            </select>
          </SettingRow>
          <SettingRow title={t('settings.runtime.recover')} sub={t('settings.runtime.recoverSub')}>
            <Switch checked={sample} label={t('settings.runtime.recover')} disabled={!sample} onChange={() => onNotice(t('settings.notice.toggle'))} />
          </SettingRow>
        </div>
      </Card>

      <Card title={t('settings.perm.title')} sub={t('settings.perm.sub')} testId="settings-permissions">
        {sample ? (
          <>
            <div className="st-perms" role="radiogroup" aria-label={t('settings.perm.title')}>
              {samplePermissions.map((entry) => (
                <button
                  key={entry.id}
                  type="button"
                  role="radio"
                  aria-checked={permission === entry.id}
                  className={`st-perm${permission === entry.id ? ' is-selected' : ''}`}
                  onClick={() => {
                    setPermission(entry.id)
                    onNotice(t('settings.notice.permission', { name: t(entry.titleKey as MessageKey) }))
                  }}
                >
                  <span className="st-perm-mark" aria-hidden="true">{permission === entry.id ? '✓' : ''}</span>
                  <span className="st-perm-copy">
                    <span className="st-perm-title">
                      <span>{t(entry.titleKey as MessageKey)}</span>
                      {entry.badgeKey ? <span className="st-badge is-accent">{t(entry.badgeKey as MessageKey)}</span> : null}
                    </span>
                    <span className="st-row-sub">{t(entry.subKey as MessageKey)}</span>
                  </span>
                </button>
              ))}
            </div>
            <div className="st-perm-foot">
              <Icon name="shield" size={13} />
              <span data-testid="settings-permission-summary">{t('settings.perm.current', { name: t(current.titleKey as MessageKey) })}</span>
            </div>
          </>
        ) : (
          <Empty text={t('settings.perm.empty')} testId="settings-perm-empty" />
        )}
      </Card>

      <Card title={t('settings.env.title')} sub={sample ? t('settings.env.sub') : undefined} testId="settings-env">
        {sample ? (
          <div className="st-table-wrap">
            <table className="st-table">
              <thead>
                <tr>
                  <th>{t('settings.env.key')}</th>
                  <th>{t('settings.env.scope')}</th>
                  <th>{t('settings.env.updated')}</th>
                  <th className="st-num">{t('settings.table.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {sampleEnvVars.map((entry) => (
                  <tr key={entry.key} data-testid="settings-env-row">
                    <td className="mono">{entry.key}</td>
                    <td>{entry.scope}</td>
                    <td className="st-row-sub">{entry.updated}</td>
                    <td className="st-num">
                      <button type="button" className="st-btn st-btn-sm st-btn-ghost" onClick={() => onNotice(t('settings.notice.editEnv'))}>{t('settings.env.edit')}</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty text={t('settings.env.empty')} testId="settings-env-empty" />
        )}
      </Card>
    </div>
  )
}

function ObservabilityPanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const [tab, setTab] = useState<ObsTab>('usage')

  return (
    <div className="st-stack">
      <div className="st-tabs" role="tablist" aria-label={t('settings.obs.label')}>
        {(['usage', 'traces', 'executions'] as ObsTab[]).map((id) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            className={`st-tab${tab === id ? ' is-active' : ''}`}
            onClick={() => setTab(id)}
          >
            {t(`settings.obs.tab.${id}` as MessageKey)}
          </button>
        ))}
      </div>

      {tab === 'usage' ? (
        <>
          <Card
            title={t('settings.obs.usageTitle')}
            sub={sample ? t('settings.obs.usageSub') : undefined}
            testId="settings-usage"
            actions={
              <>
                <select className="st-select st-select-sm" aria-label={t('settings.obs.period')} disabled={!sample}>
                  <option>{t('settings.obs.period7')}</option>
                  <option>{t('settings.obs.period30')}</option>
                  <option>{t('settings.obs.periodMonth')}</option>
                </select>
                <button type="button" className="st-btn st-btn-sm st-btn-ghost" disabled={!sample} onClick={() => onNotice(t('settings.notice.exportCsv'))}>
                  <Icon name="download" size={12} />
                  {t('settings.obs.export')}
                </button>
              </>
            }
          >
            {sample ? (
              <>
                <div className="st-stats">
                  {sampleUsage.map((entry) => (
                    <div key={entry.key} className="st-stat">
                      <span>{t(`settings.obs.${entry.key}` as MessageKey)}</span>
                      <b>{entry.value}</b>
                      <small>{t(entry.subKey as MessageKey)}</small>
                    </div>
                  ))}
                </div>
                <div className="st-chart-wrap">
                  <div className="st-chart" role="img" aria-label={t('settings.obs.chartAria')}>
                    {sampleUsageChart.map((column) => (
                      <div key={column.label} className="st-chart-col">
                        <b className="st-seg-in" style={{ height: `${column.input}%` }} />
                        <b className="st-seg-out" style={{ height: `${column.output}%` }} />
                      </div>
                    ))}
                  </div>
                  <div className="st-axis">
                    {sampleUsageChart.map((column) => (
                      <span key={column.label}>{column.label.startsWith('settings.') ? t(column.label as MessageKey) : column.label}</span>
                    ))}
                  </div>
                  <div className="st-legend">
                    <span><i className="st-legend-in" />{t('settings.obs.input')}</span>
                    <span><i className="st-legend-out" />{t('settings.obs.output')}</span>
                  </div>
                </div>
              </>
            ) : (
              <Empty text={t('settings.obs.empty')} testId="settings-usage-empty" />
            )}
          </Card>

          <div className="st-split">
            <Card title={t('settings.obs.byModel')} testId="settings-usage-model">
              {sample ? (
                <div className="st-bars">
                  {sampleUsageBars.map((entry) => (
                    <div key={entry.label} className="st-bar-row">
                      <span className="st-row-sub mono">{entry.label.startsWith('settings.') ? t(entry.label as MessageKey) : entry.label}</span>
                      <span className="st-meter"><span style={{ width: `${entry.width}%` }} /></span>
                      <span className="st-row-sub mono st-bar-note">{entry.note}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <Empty text={t('settings.obs.empty')} />
              )}
            </Card>
            <Card title={t('settings.obs.byAgent')} testId="settings-usage-agent">
              {sample ? (
                <div className="st-bars">
                  {sampleAgentBars.map((entry) => (
                    <div key={entry.label} className="st-bar-row">
                      <span className="st-row-sub">{entry.label.startsWith('settings.') ? t(entry.label as MessageKey) : entry.label}</span>
                      <span className="st-meter"><span style={{ width: `${entry.width}%` }} /></span>
                      <span className="st-row-sub mono st-bar-note">{entry.note}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <Empty text={t('settings.obs.empty')} />
              )}
            </Card>
          </div>
        </>
      ) : null}

      {tab === 'traces' ? (
        <Card title={t('settings.obs.tracesTitle')} sub={t('settings.obs.tracesSub')} testId="settings-traces">
          {sample ? (
            <div className="st-table-wrap">
              <table className="st-table">
                <thead>
                  <tr>
                    <th>{t('settings.obs.traceId')}</th>
                    <th>{t('settings.obs.traceSession')}</th>
                    <th>{t('settings.obs.traceSteps')}</th>
                    <th>{t('settings.obs.traceDuration')}</th>
                    <th>{t('settings.obs.traceState')}</th>
                  </tr>
                </thead>
                <tbody>
                  {sampleTraces.map((trace) => (
                    <tr key={trace.id} data-testid="settings-trace-row">
                      <td className="mono">{trace.id}</td>
                      <td>
                        <div className="st-row-title">{trace.session}</div>
                        <div className="st-row-sub">{trace.agent}</div>
                      </td>
                      <td>
                        <span className="st-trace-bars">
                          {trace.steps.map((width, index) => (
                            <i key={`${trace.id}-${index}`} style={{ width: `${width}%` }} className={index % 2 === 0 ? 'is-in' : 'is-out'} />
                          ))}
                        </span>
                      </td>
                      <td className="mono">{trace.duration}</td>
                      <td>
                        <span className={`st-badge is-${trace.state}`}>
                          <i />{t(trace.state === 'warn' ? 'settings.obs.state.waiting' : trace.state === 'success' ? 'settings.obs.state.done' : 'settings.obs.state.failed')}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty text={t('settings.obs.empty')} testId="settings-traces-empty" />
          )}
        </Card>
      ) : null}

      {tab === 'executions' ? (
        <Card title={t('settings.obs.execTitle')} sub={t('settings.obs.execSub')} testId="settings-executions">
          {sample ? (
            <div className="st-table-wrap">
              <table className="st-table">
                <thead>
                  <tr>
                    <th>{t('settings.obs.execTime')}</th>
                    <th>{t('settings.obs.execAction')}</th>
                    <th>{t('settings.obs.execActor')}</th>
                    <th>{t('settings.obs.execPolicy')}</th>
                    <th>{t('settings.obs.execResult')}</th>
                    <th className="st-num">{t('settings.obs.traceDuration')}</th>
                  </tr>
                </thead>
                <tbody>
                  {sampleExecutions.map((entry) => (
                    <tr key={entry.time} data-testid="settings-execution-row">
                      <td className="mono st-row-sub">{entry.time}</td>
                      <td>
                        <div className="mono">{entry.action}</div>
                        <div className="st-row-sub">{entry.subject}</div>
                      </td>
                      <td>{entry.actor}</td>
                      <td>
                        <span className={`st-badge${entry.policy === 'user' ? ' is-warn' : ''}`}>
                          {t(entry.policy === 'user' ? 'settings.obs.policy.user' : entry.policy === 'low' ? 'settings.obs.policy.low' : 'settings.obs.policy.allowed')}
                        </span>
                      </td>
                      <td>
                        <span className={`st-badge ${entry.result === 'success' ? 'is-success' : 'is-danger'}`}>
                          {t(entry.result === 'success' ? 'settings.obs.result.success' : 'settings.obs.result.exit1')}
                        </span>
                      </td>
                      <td className="st-num mono">{entry.duration}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty text={t('settings.obs.empty')} testId="settings-executions-empty" />
          )}
        </Card>
      ) : null}
    </div>
  )
}

function SecurityPanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const rules = [
    { titleKey: 'settings.security.external', subKey: 'settings.security.externalSub' },
    { titleKey: 'settings.security.files', subKey: 'settings.security.filesSub' },
    { titleKey: 'settings.security.credentials', subKey: 'settings.security.credentialsSub' },
    { titleKey: 'settings.security.paid', subKey: 'settings.security.paidSub' },
  ] as const

  return (
    <div className="st-stack">
      <Card title={t('settings.security.rules')} sub={t('settings.security.rulesSub')} testId="settings-security-rules">
        {sample ? (
          <div className="st-rows">
            {rules.map((rule) => (
              <SettingRow key={rule.titleKey} title={t(rule.titleKey)} sub={t(rule.subKey)}>
                <select className="st-select" aria-label={t(rule.titleKey)}>
                  <option>{t('settings.security.mediumFirst')}</option>
                  <option>{t('settings.security.highEach')}</option>
                  <option>{t('settings.security.deny')}</option>
                </select>
              </SettingRow>
            ))}
          </div>
        ) : (
          <Empty text={t('settings.security.empty')} testId="settings-security-empty" />
        )}
      </Card>

      <Card title={t('settings.security.audit')} testId="settings-audit">
        {sample ? (
          <div className="st-rows">
            <SettingRow title={t('settings.security.retention')} sub={t('settings.security.retentionSub')}>
              <select className="st-select" aria-label={t('settings.security.retention')}>
                <option>{t('settings.security.days', { count: 180 })}</option>
                <option>{t('settings.security.days', { count: 90 })}</option>
                <option>{t('settings.security.days', { count: 365 })}</option>
              </select>
            </SettingRow>
            <SettingRow title={t('settings.security.mask')} sub={t('settings.security.maskSub')}>
              <Switch checked label={t('settings.security.mask')} onChange={() => onNotice(t('settings.notice.toggle'))} />
            </SettingRow>
            <SettingRow title={t('settings.security.export')} sub={t('settings.security.exportSub')}>
              <button type="button" className="st-btn" onClick={() => onNotice(t('settings.notice.auditExport'))}>
                <Icon name="download" size={13} />
                {t('settings.security.generate')}
              </button>
            </SettingRow>
          </div>
        ) : (
          <Empty text={t('settings.security.empty')} />
        )}
      </Card>

      <Card title={t('settings.security.data')} testId="settings-data">
        {sample ? (
          <div className="st-rows">
            <SettingRow title={t('settings.security.clear')} sub={t('settings.security.clearSub')} danger>
              <button type="button" className="st-btn st-btn-danger" onClick={() => onNotice(t('settings.notice.clear'))}>
                {t('settings.security.choose')}
              </button>
            </SettingRow>
          </div>
        ) : (
          <Empty text={t('settings.security.empty')} />
        )}
      </Card>
    </div>
  )
}

function SettingRow({ title, sub, children, danger }: { title: string; sub: string; children: ReactNode; danger?: boolean }) {
  return (
    <div className={`st-row${danger ? ' is-danger' : ''}`}>
      <div className="st-row-main">
        <div className="st-row-title">{title}</div>
        <div className="st-row-sub">{sub}</div>
      </div>
      <div className="st-row-control">{children}</div>
    </div>
  )
}

function Switch({ checked, label, disabled, onChange }: { checked: boolean; label: string; disabled?: boolean; onChange: () => void }) {
  const [on, setOn] = useState(checked)
  return (
    <button
      type="button"
      className="st-switch"
      role="switch"
      aria-checked={on}
      aria-label={label}
      disabled={disabled}
      onClick={() => {
        setOn((value) => !value)
        onChange()
      }}
    >
      <span />
    </button>
  )
}

/** Exported for the settings test so the section list stays in sync. */
export const SETTINGS_SECTIONS = SECTIONS.map((entry) => entry.id)
