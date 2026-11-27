import { useEffect, useState, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import ThemeSettings from './ThemeSettings'
import UpdateCard from '../../updates/UpdateCard'
import MemoryPanel from './MemoryPanel'
import {
  describeSessionError,
  exportSessionAudit,
  sessionAudit,
  type AuditPeriod,
  type AuditReport,
  type AuditShare,
} from '../../chat/client'
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
import { PREF_AUDIT_MASK, PREF_AUTO_SYNC, PREF_SHOW_TIMELINE, useBoolPreference, useSendShortcut, type SendShortcut } from '../../preferences/flags'
import {
  CatalogModelsPanel,
  CatalogRuntimeTable,
  CatalogSummary,
  PiLegacyImport,
  SubagentExtensionPanel,
} from '../../catalog/CatalogPanels'

type SectionId = 'general' | 'models' | 'runtime' | 'observability' | 'memory' | 'security'
type ObsTab = 'usage' | 'traces' | 'executions'

const SECTIONS: Array<{ id: SectionId; icon: string; labelKey: MessageKey; keywords: string }> = [
  { id: 'general', icon: 'settings', labelKey: 'settings.nav.general', keywords: '通用 工作区 外观 通知' },
  { id: 'models', icon: 'brain', labelKey: 'settings.nav.models', keywords: '模型 供应商 API 密钥 目录' },
  { id: 'runtime', icon: 'terminal', labelKey: 'settings.nav.runtime', keywords: '运行时 并发 环境 权限 沙箱' },
  { id: 'observability', icon: 'zap', labelKey: 'settings.nav.observability', keywords: '观测 Token 用量 审计 链路 执行' },
  { id: 'memory', icon: 'database', labelKey: 'settings.nav.memory', keywords: '记忆 长期记忆 偏好 会话 memory' },
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
              <BehaviourCard />
              <ThemeSettings />
              <NotificationsCard sample={sample} onNotice={announce} />
            </>
          ) : null}
          {section === 'models' ? <ModelsPanel sample={sample} onNotice={announce} /> : null}
          {section === 'runtime' ? <RuntimePanel sample={sample} onNotice={announce} /> : null}
          {section === 'observability' ? <ObservabilityPanel sample={sample} onNotice={announce} /> : null}
          {section === 'memory' ? <MemoryPanel /> : null}
          {section === 'security' ? <SecurityPanel sample={sample} onNotice={announce} /> : null}
        </div>
      </div>
    </section>
  )
}

/** Real UI preferences. These are not sample-only switches: both change the
 *  running app, so they stay enabled outside the prototype fixture mode. */
function BehaviourCard() {
  const { t } = useI18n()
  const [showTimeline, setShowTimeline] = useBoolPreference(PREF_SHOW_TIMELINE, false)
  const [autoSync, setAutoSync] = useBoolPreference(PREF_AUTO_SYNC, true)
  const [sendShortcut, setSendShortcut] = useSendShortcut()
  return (
    <Card title={t('settings.behaviour.title')} sub={t('settings.behaviour.sub')} testId="settings-behaviour">
      <div className="st-rows">
        <SettingRow
          title={t('settings.behaviour.sendShortcut')}
          sub={t('settings.behaviour.sendShortcutSub')}
        >
          <select
            className="st-select"
            aria-label={t('settings.behaviour.sendShortcut')}
            data-testid="settings-send-shortcut"
            value={sendShortcut}
            onChange={(event) => setSendShortcut(event.target.value as SendShortcut)}
          >
            <option value="enter">{t('settings.behaviour.sendShortcut.enter')}</option>
            <option value="ctrl-enter">{t('settings.behaviour.sendShortcut.ctrlEnter')}</option>
          </select>
        </SettingRow>
        <SettingRow title={t('settings.behaviour.timeline')} sub={t('settings.behaviour.timelineSub')}>
          <PrefSwitch
            checked={showTimeline}
            label={t('settings.behaviour.timeline')}
            testId="settings-show-timeline"
            onChange={() => setShowTimeline(!showTimeline)}
          />
        </SettingRow>
        <SettingRow title={t('settings.behaviour.autoSync')} sub={t('settings.behaviour.autoSyncSub')}>
          <PrefSwitch
            checked={autoSync}
            label={t('settings.behaviour.autoSync')}
            testId="settings-auto-sync"
            onChange={() => setAutoSync(!autoSync)}
          />
        </SettingRow>
      </div>
    </Card>
  )
}

/** Controlled switch: unlike the sample-only Switch above it has no local state. */
function PrefSwitch({ checked, label, testId, onChange }: { checked: boolean; label: string; testId: string; onChange: () => void }) {
  return (
    <button
      type="button"
      className="st-switch"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      data-testid={testId}
      onClick={onChange}
    >
      <span />
    </button>
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
      {/* The update card talks to the host, so the isolated sample page (which
          must render without one) does not include it. */}
      {!sample ? <UpdateCard /> : null}
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
            <CatalogRuntimeTable view={catalog} onNotice={onNotice} />
            {/* The legacy import is offered only when `~/.pi` still has
                something to carry over; the component hides itself otherwise. */}
            <PiLegacyImport onNotice={onNotice} />
          </div>
        ) : (
          <Empty
            text={catalog.status === 'loading' ? t('catalog.loading') : t('settings.runtime.empty')}
            testId="settings-runtime-empty"
          />
        )}
      </Card>

      {!sample && catalog.status === 'ready' && catalog.snapshot?.runtimes.some((row) => row.runtimeId === 'pi') ? (
        <Card
          title={t('catalog.subagent.cardTitle')}
          sub={t('catalog.subagent.cardSub')}
          testId="settings-extensions"
        >
          <SubagentExtensionPanel view={catalog} onNotice={onNotice} />
        </Card>
      ) : null}

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
  const [period, setPeriod] = useState<AuditPeriod>('7')
  const [report, setReport] = useState<AuditReport | null>(null)
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState('')
  const [exporting, setExporting] = useState(false)

  // The sample route keeps its fixtures for design comparison. A real window
  // reads the ledger instead: every number below comes from a usage event, a
  // run row or a tool call, never from a seeded chart.
  useEffect(() => {
    if (sample) return
    let cancelled = false
    setLoading(true)
    setFailed('')
    sessionAudit(period)
      .then((next) => {
        if (!cancelled) setReport(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setFailed(describeSessionError(cause))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [sample, period])

  const exportRows = async () => {
    if (sample) {
      onNotice(t('settings.notice.exportCsv'))
      return
    }
    setExporting(true)
    try {
      const result = await exportSessionAudit(period)
      onNotice(t('settings.obs.exported', { count: result.rows, path: result.path }))
    } catch (cause) {
      onNotice(t('settings.obs.exportFailed', { message: describeSessionError(cause) }))
    } finally {
      setExporting(false)
    }
  }

  const totals = report?.usage.totals
  const hasAny =
    !!totals && (totals.calls > 0 || totals.tools > 0 || report.traces.length > 0 || report.executions.length > 0)
  // Cache hit rate is a ratio over input tokens: the cache can only save what
  // would otherwise have been sent. A provider that reports no counters must
  // read as "—", never as 0%.
  const cacheRate =
    totals && totals.reported && totals.cached + totals.input > 0
      ? (totals.cached / (totals.cached + totals.input)) * 100
      : null
  const liveStats = totals
    ? [
        {
          key: 'tokens',
          value: totals.reported ? formatCount(totals.tokens) : '—',
          sub: totals.reported
            ? `${t('settings.obs.input')} ${formatCount(totals.input)} · ${t('settings.obs.output')} ${formatCount(totals.output)} · ${t('settings.obs.cached')} ${formatCount(totals.cached)}`
            : t('settings.obs.noTokens'),
        },
        {
          key: 'cache',
          value: cacheRate === null ? '—' : `${cacheRate.toFixed(1)}%`,
          sub:
            cacheRate === null
              ? t('settings.obs.cacheNone')
              : t('settings.obs.cacheSub', {
                  cached: formatCount(totals.cached),
                  input: formatCount(totals.input),
                }),
        },
        { key: 'calls', value: formatCount(totals.calls), sub: `${report?.usage.days.length ?? 0} ${t('settings.obs.usageSub')}` },
        { key: 'tools', value: formatCount(totals.tools), sub: `${formatCount(totals.tools)} ${t('settings.obs.tab.executions')}` },
      ]
    : []
  const maxDay = Math.max(
    1,
    ...(report?.usage.days.map((day) => day.input + day.output) ?? [1]),
  )
  const shares = (rows: AuditShare[] | undefined, fallback: string) => {
    const list = rows ?? []
    const max = Math.max(1, ...list.map((row) => row.tokens))
    return list.map((row) => ({
      label: row.label || fallback,
      width: Math.round((row.tokens / max) * 100),
      // Cached tokens ride along with the row so a "cheap" model that is only
      // cheap because of cache reads is visible as such.
      note: `${formatCount(row.tokens)} · ${formatCount(row.calls)} · ${t('settings.obs.cached')} ${formatCount(row.cached)}`,
    }))
  }

  const statusLine = loading
    ? t('settings.obs.loading')
    : failed
      ? t('settings.obs.failed', { message: failed })
      : !hasAny
        ? t('settings.obs.realEmpty')
        : ''

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
            sub={sample ? t('settings.obs.usageSub') : t('settings.obs.liveSub')}
            testId="settings-usage"
            actions={
              <>
                <select
                  className="st-select st-select-sm"
                  aria-label={t('settings.obs.period')}
                  data-testid="settings-audit-period"
                  value={period}
                  onChange={(event) => setPeriod(event.target.value as AuditPeriod)}
                >
                  <option value="7">{t('settings.obs.period7')}</option>
                  <option value="30">{t('settings.obs.period30')}</option>
                  <option value="month">{t('settings.obs.periodMonth')}</option>
                </select>
                <button
                  type="button"
                  className="st-btn st-btn-sm st-btn-ghost"
                  data-testid="settings-audit-export"
                  disabled={exporting}
                  onClick={() => void exportRows()}
                >
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
            ) : statusLine ? (
              <Empty text={statusLine} testId="settings-usage-empty" />
            ) : (
              <>
                <div className="st-stats" data-testid="settings-audit-totals">
                  {liveStats.map((entry) => (
                    <div key={entry.key} className="st-stat">
                      <span>{t(`settings.obs.${entry.key}` as MessageKey)}</span>
                      <b>{entry.value}</b>
                      <small>{entry.sub}</small>
                    </div>
                  ))}
                </div>
                <div className="st-chart-wrap">
                  <div className="st-chart" role="img" aria-label={t('settings.obs.chartAria')} data-testid="settings-audit-chart">
                    {(report?.usage.days ?? []).map((day) => (
                      <div key={day.date} className="st-chart-col" title={`${day.date} · ${day.input + day.output} · ${t('settings.obs.cached')} ${day.cached}`}>
                        <b className="st-seg-in" style={{ height: `${Math.round((day.input / maxDay) * 100)}%` }} />
                        <b className="st-seg-out" style={{ height: `${Math.round((day.output / maxDay) * 100)}%` }} />
                      </div>
                    ))}
                  </div>
                  <div className="st-axis">
                    {(report?.usage.days ?? []).map((day) => <span key={day.date}>{day.date.slice(5)}</span>)}
                  </div>
                  <div className="st-legend">
                    <span><i className="st-legend-in" />{t('settings.obs.input')}</span>
                    <span><i className="st-legend-out" />{t('settings.obs.output')}</span>
                  </div>
                </div>
              </>
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
              ) : shares(report?.usage.by_model, t('settings.obs.unknownModel')).length ? (
                <div className="st-bars" data-testid="settings-audit-by-model">
                  {shares(report?.usage.by_model, t('settings.obs.unknownModel')).map((entry) => (
                    <div key={entry.label} className="st-bar-row">
                      <span className="st-row-sub mono st-bar-label" title={entry.label}>{entry.label}</span>
                      <span className="st-meter"><span style={{ width: `${entry.width}%` }} /></span>
                      <span className="st-row-sub mono st-bar-note">{entry.note}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <Empty text={t('settings.obs.byModelEmpty')} />
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
              ) : shares(report?.usage.by_agent, t('settings.obs.unknownAgent')).length ? (
                <div className="st-bars" data-testid="settings-audit-by-agent">
                  {shares(report?.usage.by_agent, t('settings.obs.unknownAgent')).map((entry) => (
                    <div key={entry.label} className="st-bar-row">
                      <span className="st-row-sub st-bar-label" title={entry.label}>{entry.label}</span>
                      <span className="st-meter"><span style={{ width: `${entry.width}%` }} /></span>
                      <span className="st-row-sub mono st-bar-note">{entry.note}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <Empty text={t('settings.obs.byModelEmpty')} />
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
          ) : statusLine ? (
            <Empty text={statusLine} testId="settings-traces-empty" />
          ) : (
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
                  {(report?.traces ?? []).map((trace) => (
                    <tr key={trace.id} data-testid="settings-trace-row">
                      <td className="mono">{trace.id.slice(0, 8)}</td>
                      <td>
                        <div className="st-row-title">{trace.session || '—'}</div>
                        <div className="st-row-sub">{trace.model || t('settings.obs.unknownModel')}</div>
                      </td>
                      <td className="mono">{trace.steps}</td>
                      <td className="mono">{formatDuration(trace.duration_ms)}</td>
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
          ) : statusLine ? (
            <Empty text={statusLine} testId="settings-executions-empty" />
          ) : (
            <div className="st-table-wrap">
              <table className="st-table">
                <thead>
                  <tr>
                    <th>{t('settings.obs.execTime')}</th>
                    <th>{t('settings.obs.execAction')}</th>
                    <th>{t('settings.obs.execActor')}</th>
                    <th>{t('settings.obs.execResult')}</th>
                    <th className="st-num">{t('settings.obs.traceDuration')}</th>
                  </tr>
                </thead>
                <tbody>
                  {(report?.executions ?? []).map((row, index) => (
                    <tr key={`${row.at}-${index}`} data-testid="settings-execution-row">
                      <td className="mono st-row-sub">{row.at.slice(0, 19).replace('T', ' ')}</td>
                      <td>
                        <div className="mono">{row.action}</div>
                        <div className="st-row-sub">{row.subject || row.session || '—'}</div>
                      </td>
                      <td>{row.model || t('settings.obs.unknownModel')}</td>
                      <td>
                        <span className={`st-badge ${row.ok === true ? 'is-success' : row.ok === false ? 'is-danger' : 'is-warn'}`}>
                          {t(row.ok === true ? 'settings.obs.result.success' : row.ok === false ? 'settings.obs.result.failed' : 'settings.obs.result.running')}
                        </span>
                      </td>
                      <td className="st-num mono">{row.duration_ms === null ? '—' : formatDuration(row.duration_ms)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      ) : null}
    </div>
  )
}

/** Compact, locale-stable number for a stat card. */
function formatCount(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return String(value)
}

function formatDuration(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.round(ms / 60_000)}m`
}

function SecurityPanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const [auditMask, setAuditMask] = useBoolPreference(PREF_AUDIT_MASK, true)
  const [auditPeriod, setAuditPeriod] = useState<AuditPeriod>('30')
  const [exporting, setExporting] = useState(false)
  const generateAudit = async () => {
    if (sample) {
      onNotice(t('settings.notice.auditExport'))
      return
    }
    setExporting(true)
    try {
      const result = await exportSessionAudit(auditPeriod, auditMask)
      onNotice(t('settings.security.exportDone', { count: result.rows, path: result.path }))
    } catch (cause) {
      onNotice(t('settings.security.exportFailed', { message: describeSessionError(cause) }))
    } finally {
      setExporting(false)
    }
  }
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
        <div className="st-rows">
          <SettingRow title={t('settings.security.exportScope')} sub={t('settings.security.exportScopeSub')}>
            <select
              className="st-select"
              aria-label={t('settings.security.exportScope')}
              data-testid="settings-audit-scope"
              value={auditPeriod}
              onChange={(event) => setAuditPeriod(event.target.value as AuditPeriod)}
            >
              <option value="7">{t('settings.obs.period7')}</option>
              <option value="30">{t('settings.obs.period30')}</option>
              <option value="month">{t('settings.obs.periodMonth')}</option>
            </select>
          </SettingRow>
          <SettingRow title={t('settings.security.mask')} sub={t('settings.security.maskSub')}>
            <Switch
              checked={auditMask}
              value={auditMask}
              label={t('settings.security.mask')}
              onChange={() => {
                setAuditMask(!auditMask)
                onNotice(t(!auditMask ? 'settings.security.maskOn' : 'settings.security.maskOff'))
              }}
            />
          </SettingRow>
          <SettingRow title={t('settings.security.export')} sub={t('settings.security.exportSub')}>
            <button
              type="button"
              className="st-btn"
              data-testid="settings-audit-generate"
              disabled={exporting}
              onClick={() => void generateAudit()}
            >
              <Icon name="download" size={13} />
              {exporting ? t('settings.security.exporting') : t('settings.security.generate')}
            </button>
          </SettingRow>
        </div>
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

function Switch({
  checked,
  label,
  disabled,
  value,
  onChange,
}: {
  checked: boolean
  label: string
  disabled?: boolean
  /** Controlled state, for a preference that must survive other pages. */
  value?: boolean
  onChange: () => void
}) {
  const [on, setOn] = useState(checked)
  const shown = value ?? on
  return (
    <button
      type="button"
      className="st-switch"
      role="switch"
      aria-checked={shown}
      aria-label={label}
      disabled={disabled}
      onClick={() => {
        if (value === undefined) setOn((previous) => !previous)
        onChange()
      }}
    >
      <span />
    </button>
  )
}

/** Exported for the settings test so the section list stays in sync. */
export const SETTINGS_SECTIONS = SECTIONS.map((entry) => entry.id)
