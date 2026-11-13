import { useEffect, useRef, useState, type Dispatch, type KeyboardEvent, type SetStateAction } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { ROUTES_BY_ID } from '../../shell/routes'
import AgentDialogs, { type AgentDialogKind } from './AgentDialogs'
import {
  sampleAgents,
  sampleRuns,
  sampleSkills,
  sampleSubAgents,
  sampleTools,
  systemPrompt,
  useAgentsSample,
  type AgentRecord,
  type AgentTool,
} from './sampleData'
import './agents.css'
import { useCatalog } from '../../catalog/useCatalog'
import { CatalogRuntimeTable, CatalogSummary } from '../../catalog/CatalogPanels'

type AgentTab = 'basic' | 'prompt' | 'sub' | 'perm' | 'runs'

type BasicDraft = {
  name: string
  slug: string
  description: string
  runtime: AgentRecord['runtime']
  model: string
  thinking: AgentRecord['thinking']
  context: string
  memory: boolean
  autoCompact: boolean
}

const RUNTIME_KEYS: Record<AgentRecord['runtime'], MessageKey> = {
  'pi-agent': 'agents.runtime.pi',
  'codex-cli': 'agents.runtime.codex',
  'claude-code': 'agents.runtime.claude',
}

const RUNTIME_OPTION_KEYS: Record<AgentRecord['runtime'], MessageKey> = {
  'pi-agent': 'agents.runtime.piOption',
  'codex-cli': 'agents.runtime.codexOption',
  'claude-code': 'agents.runtime.claudeOption',
}

const THINKING_KEYS: Record<AgentRecord['thinking'], MessageKey> = {
  off: 'agents.thinking.off',
  low: 'agents.thinking.low',
  medium: 'agents.thinking.medium',
  high: 'agents.thinking.high',
}

const CHAT_THINKING_KEYS: Record<AgentRecord['thinking'], MessageKey> = {
  off: 'chat.composer.thinking.off',
  low: 'chat.composer.thinking.low',
  medium: 'chat.composer.thinking.medium',
  high: 'chat.composer.thinking.high',
}

const PERMISSION_KEYS: Record<AgentRecord['permission'], MessageKey> = {
  readonly: 'chat.composer.permission.readonly',
  medium: 'chat.composer.permission.medium',
  high: 'chat.composer.permission.high',
  full: 'chat.composer.permission.full',
}

function useNarrow() {
  const [narrow, setNarrow] = useState(() =>
    typeof window.matchMedia === 'function' ? window.matchMedia('(max-width: 900px)').matches : false,
  )
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const query = window.matchMedia('(max-width: 900px)')
    const update = () => setNarrow(query.matches)
    update()
    query.addEventListener?.('change', update)
    return () => query.removeEventListener?.('change', update)
  }, [])
  return narrow
}

function runtimeLabel(t: (key: MessageKey) => string, runtime: AgentRecord['runtime']) {
  return t(RUNTIME_KEYS[runtime])
}

function AgentSwitch({
  checked,
  onChange,
  label,
  testId,
  disabled = false,
}: {
  checked: boolean
  onChange: () => void
  label: string
  testId?: string
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      className="agents-switch"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      data-testid={testId}
      disabled={disabled}
      onClick={onChange}
    >
      <span />
    </button>
  )
}

export default function AgentsPage() {
  const { t } = useI18n()
  const sample = useAgentsSample()
  const catalog = useCatalog(!sample)
  const narrow = useNarrow()
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(sample ? sampleAgents[0].id : null)
  const [tab, setTab] = useState<AgentTab>('basic')
  const [mobilePane, setMobilePane] = useState<'list' | 'detail'>('detail')
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState<AgentDialogKind>(null)
  const dialogTrigger = useRef<HTMLElement | null>(null)

  const selected = sample ? sampleAgents.find((agent) => agent.id === selectedId) ?? sampleAgents[0] : null
  const [draft, setDraft] = useState<BasicDraft>({
    name: selected?.name ?? '',
    slug: selected?.slug ?? '',
    description: selected?.description ?? '',
    runtime: selected?.runtime ?? ('pi-agent' as AgentRecord['runtime']),
    model: selected?.model ?? '',
    thinking: selected?.thinking ?? ('medium' as AgentRecord['thinking']),
    context: selected?.context ?? '200K',
    memory: selected?.memory ?? false,
    autoCompact: selected?.autoCompact ?? false,
  })
  const [errors, setErrors] = useState<{ name?: string; slug?: string }>({})

  useEffect(() => {
    if (!selected) return
    setDraft({
      name: selected.name,
      slug: selected.slug,
      description: selected.description,
      runtime: selected.runtime,
      model: selected.model,
      thinking: selected.thinking,
      context: selected.context,
      memory: selected.memory,
      autoCompact: selected.autoCompact,
    })
    setErrors({})
    setTab('basic')
  }, [selected?.id])

  const normalize = query.trim().toLowerCase()
  const agents = sample
    ? sampleAgents.filter((agent) =>
        !normalize ||
        `${agent.name} ${agent.description} ${agent.model} ${runtimeLabel(t, agent.runtime)}`.toLowerCase().includes(normalize),
      )
    : []
  const mainAgents = agents.filter((agent) => agent.group === 'main')
  const subAgents = agents.filter((agent) => agent.group === 'sub')
  const mainTotal = sample ? sampleAgents.filter((agent) => agent.group === 'main').length : 0
  const subTotal = sample ? sampleAgents.filter((agent) => agent.group === 'sub').length : 0

  const announce = (message: string) => setNotice(message)
  const openDialog = (kind: AgentDialogKind, trigger: HTMLElement) => {
    dialogTrigger.current = trigger
    setDialog(kind)
  }
  const closeDialog = () => {
    setDialog(null)
    const trigger = dialogTrigger.current
    window.setTimeout(() => trigger?.focus(), 0)
  }
  const selectAgent = (agent: AgentRecord) => {
    setSelectedId(agent.id)
    setMobilePane('detail')
  }
  const save = () => {
    const next: { name?: string; slug?: string } = {}
    if (!draft.name.trim()) next.name = t('agents.validation.nameRequired')
    if (!draft.slug.trim()) next.slug = t('agents.validation.slugRequired')
    else if (!/^[a-z0-9-]+$/.test(draft.slug)) next.slug = t('agents.validation.slugInvalid')
    setErrors(next)
    if (next.name || next.slug) return
    announce(t('agents.notice.save'))
  }

  const tabs: Array<{ id: AgentTab; label: string }> = [
    { id: 'basic', label: t('agents.tab.basic') },
    { id: 'prompt', label: t('agents.tab.prompt') },
    { id: 'sub', label: t('agents.tab.sub', { count: subTotal }) },
    { id: 'perm', label: t('agents.tab.perm') },
    { id: 'runs', label: t('agents.tab.runs') },
  ]

  const onTabKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
    event.preventDefault()
    const index = tabs.findIndex((entry) => entry.id === tab)
    const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : (index - 1 + tabs.length) % tabs.length
    setTab(tabs[next].id)
  }

  return (
    <section className="agents-page" data-testid="agents-page" data-sample={sample ? 'true' : 'false'}>
      <span className="sr-only" id="route-marker" data-testid="page-marker">
        {ROUTES_BY_ID.get('agents')?.marker}
      </span>
      <div className="agents-page-head">
        <div>
          <h1>{t('agents.title')}</h1>
          <p>{t('agents.description')}</p>
        </div>
        <div className="agents-head-actions">
          <button
            type="button"
            className="agents-btn"
            disabled={!sample}
            title={sample ? t('agents.import') : t('agents.action.unavailable')}
            onClick={(event) => openDialog('import', event.currentTarget)}
          >
            <Icon name="upload" size={14} />
            {t('agents.import')}
          </button>
          <button
            type="button"
            className="agents-btn agents-btn-primary"
            disabled={!sample}
            title={sample ? t('agents.new') : t('agents.action.unavailable')}
            onClick={(event) => openDialog('new', event.currentTarget)}
          >
            <Icon name="plus" size={14} />
            {t('agents.new')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="agents-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('agents.sampleBanner')}
        </div>
      ) : null}
      {notice ? (
        <div className="agents-notice-bar" data-testid="agent-notice" role="status">
          {notice}
        </div>
      ) : null}

      {!sample && (catalog.status === 'ready' || catalog.status === 'loading' || catalog.status === 'error') && (
        <div className="agents-catalog" data-testid="agents-catalog">
          <CatalogSummary view={catalog} />
          <CatalogRuntimeTable view={catalog} />
        </div>
      )}

      <div
        className="agents-layout"
        data-mobile-pane={narrow ? mobilePane : 'both'}
        data-testid="agents-layout"
      >
        <section className="agents-list-card" data-testid="agents-list" aria-label={t('agents.title')}>
          <label className="agents-filter">
            <Icon name="search" size={12} />
            <input
              data-testid="agent-filter"
              type="search"
              value={query}
              disabled={!sample}
              placeholder={t('agents.searchPlaceholder')}
              aria-label={t('agents.searchLabel')}
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>
          {sample ? (
            <>
              <div className="agents-group-label">{t('agents.group.main', { count: mainTotal })}</div>
              {mainAgents.map((agent) => (
                <AgentListItem
                  key={agent.id}
                  agent={agent}
                  active={selected?.id === agent.id}
                  onSelect={() => selectAgent(agent)}
                />
              ))}
              <div className="agents-group-label agents-group-sub">
                <span>{t('agents.group.sub', { count: subTotal })}</span>
                <button
                  type="button"
                  className="agents-iconbtn"
                  aria-label={t('agents.addSub')}
                  onClick={(event) => openDialog('new', event.currentTarget)}
                >
                  <Icon name="plus" size={13} />
                </button>
              </div>
              {subAgents.map((agent) => (
                <AgentListItem
                  key={agent.id}
                  agent={agent}
                  active={selected?.id === agent.id}
                  onSelect={() => selectAgent(agent)}
                />
              ))}
              {agents.length === 0 ? (
                <div className="agents-list-empty">{t('agents.listNoMatch')}</div>
              ) : null}
            </>
          ) : (
            <div className="agents-list-empty" data-testid="agent-empty-list">
              <Icon name="bot" size={20} />
              <b>{t('agents.emptyList')}</b>
              <span>{t('agents.emptyListBody')}</span>
            </div>
          )}
        </section>

        <section className="agents-detail-card" data-testid="agents-detail">
          {narrow ? (
            <button type="button" className="agents-back" data-testid="agents-back" onClick={() => setMobilePane('list')}>
              <Icon name="arrowLeft" size={13} />
              {t('agents.backToList')}
            </button>
          ) : null}
          {selected ? (
            <>
              <div className="agents-hero">
                <span className="agents-badge agents-hero-badge">{selected.badge}</span>
                <div className="agents-hero-main">
                  <h2>{selected.name}</h2>
                  <p>{selected.description}</p>
                  <div className="agents-chips">
                    <span className="agents-chip">{runtimeLabel(t, selected.runtime)}</span>
                    <span className="agents-chip">{selected.model}</span>
                    <span className="agents-chip">{t(CHAT_THINKING_KEYS[selected.thinking])}</span>
                    <span className="agents-chip">{t('agents.permissionChip', { level: t(PERMISSION_KEYS[selected.permission]) })}</span>
                    <span className="agents-chip">{t('agents.skills', { count: selected.skills })}</span>
                  </div>
                </div>
                <div className="agents-hero-actions">
                  <button type="button" className="agents-btn agents-btn-sm" data-testid="agent-start-chat" onClick={() => announce(t('agents.notice.chat'))}>
                    <Icon name="message" size={13} />
                    {t('agents.startChat')}
                  </button>
                  <button type="button" className="agents-btn agents-btn-sm agents-btn-primary" data-testid="agent-save" onClick={save}>
                    {t('agents.save')}
                  </button>
                </div>
              </div>
            </>
          ) : (
            <div className="agents-detail-empty" data-testid="agent-empty-detail">
              <Icon name="bot" size={22} />
              <h2>{t('agents.emptyDetail')}</h2>
              <p>{t('agents.emptyDetailBody')}</p>
              <div className="agents-empty-actions">
                <button type="button" className="agents-btn agents-btn-sm" data-testid="agent-start-chat" disabled title={t('agents.action.unavailable')}>
                  <Icon name="message" size={13} />
                  {t('agents.startChat')}
                </button>
                <button type="button" className="agents-btn agents-btn-sm agents-btn-primary" data-testid="agent-save" disabled title={t('agents.action.unavailable')}>
                  {t('agents.save')}
                </button>
              </div>
            </div>
          )}

          <div className="agents-tabs" role="tablist" aria-label={t('agents.title')} onKeyDown={onTabKeyDown}>
            {tabs.map((entry) => (
              <button
                key={entry.id}
                type="button"
                role="tab"
                id={`agent-tab-${entry.id}`}
                aria-selected={tab === entry.id}
                aria-controls={`agent-pane-${entry.id}`}
                className={`agents-tab${tab === entry.id ? ' is-active' : ''}`}
                onClick={() => setTab(entry.id)}
              >
                {entry.label}
              </button>
            ))}
          </div>

          <div role="tabpanel" id={`agent-pane-${tab}`} aria-labelledby={`agent-tab-${tab}`}>
            {tab === 'basic' ? (
              <BasicPanel
                sample={sample}
                draft={draft}
                setDraft={setDraft}
                errors={errors}
                setErrors={setErrors}
                onToggle={(key) => {
                  setDraft((current) => ({ ...current, [key]: !current[key] }))
                  announce(t('agents.notice.toggle'))
                }}
              />
            ) : null}
            {tab === 'prompt' ? <PromptPanel sample={sample} onNotice={announce} /> : null}
            {tab === 'sub' ? (
              <SubPanel
                sample={sample}
                onNotice={announce}
                onNew={(trigger) => openDialog('new', trigger)}
              />
            ) : null}
            {tab === 'perm' ? <PermPanel sample={sample} onNotice={announce} /> : null}
            {tab === 'runs' ? <RunsPanel sample={sample} /> : null}
          </div>
        </section>
      </div>

      <AgentDialogs kind={dialog} onClose={closeDialog} onNotice={announce} />
    </section>
  )
}

function AgentListItem({
  agent,
  active,
  onSelect,
}: {
  agent: AgentRecord
  active: boolean
  onSelect: () => void
}) {
  const { t } = useI18n()
  return (
    <button
      type="button"
      className={`agents-item${active ? ' is-active' : ''}`}
      data-testid="agent-item"
      aria-pressed={active}
      onClick={onSelect}
    >
      <span className={`agents-badge${agent.ready ? ' is-ready' : ''}`}>{agent.badge}</span>
      <span className="agents-item-main">
        <span className="agents-item-name">
          {agent.name}
          {agent.group === 'sub' ? <span className="agents-sub-badge">{t('agents.subBadge')}</span> : null}
          {agent.ready ? (
            <span className="agents-ready">
              <i />
              {t('agents.status.ready')}
            </span>
          ) : null}
        </span>
        {agent.group === 'sub' ? (
          <span className="agents-item-sub">
            <span>{t('agents.subReadonly')}</span>
            <span>·</span>
            <span>{t('agents.subDelegated', { name: agent.delegatedFrom ?? '' })}</span>
          </span>
        ) : (
          <span className="agents-item-sub">{t('agents.listMeta', { model: agent.model, count: agent.skills })}</span>
        )}
      </span>
    </button>
  )
}

function BasicPanel({
  sample,
  draft,
  setDraft,
  errors,
  setErrors,
  onToggle,
}: {
  sample: boolean
  draft: BasicDraft
  setDraft: Dispatch<SetStateAction<BasicDraft>>
  errors: { name?: string; slug?: string }
  setErrors: React.Dispatch<React.SetStateAction<{ name?: string; slug?: string }>>
  onToggle: (key: 'memory' | 'autoCompact') => void
}) {
  const { t } = useI18n()
  const placeholder = sample ? undefined : t('agents.form.unavailable')
  return (
    <div className="agents-panel">
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.form.name')}</span>
          <input
            data-testid="agent-field-name"
            value={draft.name}
            disabled={!sample}
            placeholder={placeholder}
            aria-invalid={Boolean(errors.name)}
            onChange={(event) => {
              setDraft((current) => ({ ...current, name: event.target.value }))
              setErrors((current) => ({ ...current, name: undefined }))
            }}
          />
          {errors.name ? <small className="agents-error" data-testid="agent-error-name">{errors.name}</small> : null}
        </label>
        <label className="agents-field">
          <span>{t('agents.form.slug')}</span>
          <input
            data-testid="agent-field-slug"
            className="mono"
            value={draft.slug}
            disabled={!sample}
            placeholder={placeholder}
            aria-invalid={Boolean(errors.slug)}
            onChange={(event) => {
              setDraft((current) => ({ ...current, slug: event.target.value }))
              setErrors((current) => ({ ...current, slug: undefined }))
            }}
          />
          {errors.slug ? <small className="agents-error" data-testid="agent-error-slug">{errors.slug}</small> : null}
        </label>
      </div>
      <label className="agents-field">
        <span>{t('agents.form.description')}</span>
        <textarea
          rows={2}
          value={draft.description}
          disabled={!sample}
          placeholder={placeholder}
          onChange={(event) => setDraft((current) => ({ ...current, description: event.target.value }))}
        />
      </label>
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.form.runtime')}</span>
          <select
            value={draft.runtime}
            disabled={!sample}
            aria-label={t('agents.form.runtime')}
            onChange={(event) =>
              setDraft((current) => ({ ...current, runtime: event.target.value as AgentRecord['runtime'] }))
            }
          >
            {sample ? (
              (Object.keys(RUNTIME_OPTION_KEYS) as AgentRecord['runtime'][]).map((runtime) => (
                <option key={runtime} value={runtime}>
                  {t(RUNTIME_OPTION_KEYS[runtime])}
                </option>
              ))
            ) : (
              <option>{t('agents.form.unavailable')}</option>
            )}
          </select>
          <small>{t('agents.form.runtimeHint')}</small>
        </label>
        <label className="agents-field">
          <span>{t('agents.form.model')}</span>
          <select
            value={draft.model}
            disabled={!sample}
            aria-label={t('agents.form.model')}
            onChange={(event) => setDraft((current) => ({ ...current, model: event.target.value }))}
          >
            {sample ? (
              <>
                <option>claude-sonnet-4.5</option>
                <option>gpt-5.1</option>
                <option>gemini-3-pro</option>
              </>
            ) : (
              <option>{t('agents.form.unavailable')}</option>
            )}
          </select>
          <small>{sample ? t('agents.form.modelHint', { runtime: runtimeLabel(t, draft.runtime) }) : t('agents.form.modelUnavailableHint')}</small>
        </label>
      </div>
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.form.thinking')}</span>
          <select
            value={draft.thinking}
            disabled={!sample}
            aria-label={t('agents.form.thinking')}
            onChange={(event) =>
              setDraft((current) => ({ ...current, thinking: event.target.value as AgentRecord['thinking'] }))
            }
          >
            {sample ? (
              (Object.keys(THINKING_KEYS) as AgentRecord['thinking'][]).map((thinking) => (
                <option key={thinking} value={thinking}>
                  {t(THINKING_KEYS[thinking])}
                </option>
              ))
            ) : (
              <option>{t('agents.form.unavailable')}</option>
            )}
          </select>
          <small>{t('agents.form.thinkingHint')}</small>
        </label>
        <label className="agents-field">
          <span>{t('agents.form.context')}</span>
          <select
            value={draft.context}
            disabled={!sample}
            aria-label={t('agents.form.context')}
            onChange={(event) => setDraft((current) => ({ ...current, context: event.target.value }))}
          >
            {sample ? (
              <>
                <option>200K</option>
                <option>400K</option>
                <option>1M</option>
              </>
            ) : (
              <option>{t('agents.form.unavailable')}</option>
            )}
          </select>
          <small>{t('agents.form.contextHint')}</small>
        </label>
      </div>
      <div className="agents-setting-row">
        <div>
          <b>{t('agents.form.memory')}</b>
          <span>{t('agents.form.memoryDesc')}</span>
        </div>
        <AgentSwitch
          checked={sample ? draft.memory : false}
          disabled={!sample}
          label={t('agents.form.memory')}
          testId="agent-switch-memory"
          onChange={() => onToggle('memory')}
        />
      </div>
      <div className="agents-setting-row">
        <div>
          <b>{t('agents.form.autoCompact')}</b>
          <span>{t('agents.form.autoCompactDesc')}</span>
        </div>
        <AgentSwitch
          checked={sample ? draft.autoCompact : false}
          disabled={!sample}
          label={t('agents.form.autoCompact')}
          testId="agent-switch-compact"
          onChange={() => onToggle('autoCompact')}
        />
      </div>
    </div>
  )
}

function PromptPanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <div className="agents-panel agents-panel-stack">
      <label className="agents-field">
        <span>{t('agents.prompt.system')}</span>
        <textarea className="mono" rows={9} value={sample ? systemPrompt : ''} disabled={!sample} placeholder={!sample ? t('agents.form.unavailable') : undefined} onChange={() => {}} />
      </label>
      <div className="agents-pills">
        {['{{repo_path}}', '{{target_version}}', '{{owner}}'].map((variable) => (
          <span key={variable} className="agents-chip mono">{variable}</span>
        ))}
        <button type="button" className="agents-btn agents-btn-sm agents-btn-ghost" disabled={!sample} onClick={() => onNotice(t('agents.notice.variable'))}>
          <Icon name="plus" size={12} />
          {t('agents.prompt.variables')}
        </button>
      </div>
      <div className="agents-section-label">{t('agents.prompt.tools')}</div>
      <div className="agents-table-wrap">
        <table className="agents-table">
          <thead>
            <tr>
              <th>{t('agents.prompt.tool')}</th>
              <th>{t('agents.prompt.desc')}</th>
              <th>{t('agents.prompt.risk')}</th>
              <th className="is-right">{t('agents.prompt.enabled')}</th>
            </tr>
          </thead>
          <tbody>
            {sampleTools.map((tool) => (
              <tr key={tool.name}>
                <td className="mono">{tool.name}</td>
                <td>{t(tool.descKey)}</td>
                <td><RiskBadge tool={tool} /></td>
                <td className="is-right"><AgentSwitch checked={tool.enabled} disabled={!sample} label={`${tool.name} ${t('agents.prompt.enabled')}`} onChange={() => onNotice(t('agents.notice.toggle'))} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="agents-section-label">{t('agents.prompt.skillCount', { count: sampleSkills.length })}</div>
      <div className="agents-skill-grid">
        {sampleSkills.map((skill) => (
          <label key={skill.nameKey} className="agents-check-row">
            <input type="checkbox" defaultChecked={skill.enabled} disabled={!sample} aria-label={t(skill.nameKey)} />
            <span><b>{t(skill.nameKey)}</b><small>{t(skill.descKey)}</small></span>
          </label>
        ))}
      </div>
    </div>
  )
}

function RiskBadge({ tool }: { tool: AgentTool }) {
  const { t } = useI18n()
  const key = tool.risk === 'low' ? 'agents.risk.low' : tool.risk === 'medium' ? 'agents.risk.medium' : 'agents.risk.high'
  return <span className={`agents-badge-risk is-${tool.risk}`}>{t(key)}</span>
}

function SubPanel({ sample, onNotice, onNew }: { sample: boolean; onNotice: (message: string) => void; onNew: (trigger: HTMLElement) => void }) {
  const { t } = useI18n()
  return (
    <div className="agents-panel agents-panel-stack">
      <div className="agents-notice">
        <Icon name="branch" size={14} />
        <span>{t('agents.sub.notice')}</span>
      </div>
      <div className="agents-table-wrap">
        <table className="agents-table">
          <thead>
            <tr>
              <th>{t('agents.sub.table.agent')}</th>
              <th>{t('agents.sub.table.runtime')}</th>
              <th>{t('agents.sub.table.model')}</th>
              <th>{t('agents.sub.table.condition')}</th>
              <th>{t('agents.sub.table.concurrency')}</th>
              <th className="is-right">{t('agents.sub.table.enabled')}</th>
            </tr>
          </thead>
          <tbody>
            {(sample ? sampleSubAgents : []).map((agent) => (
              <tr key={agent.name}>
                <td><span className="agents-table-agent"><span className="agents-badge agents-badge-sm">{agent.name.slice(0, 1)}</span>{agent.name}</span></td>
                <td>{runtimeLabel(t, agent.runtime)}</td>
                <td className="mono">{agent.model}</td>
                <td>{t(agent.conditionKey)}</td>
                <td className="mono">{agent.concurrency}</td>
                <td className="is-right"><AgentSwitch checked={agent.enabled} disabled={!sample} label={`${agent.name} ${t('agents.sub.table.enabled')}`} onChange={() => onNotice(t('agents.notice.toggle'))} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.sub.policy')}</span>
          <select disabled={!sample} aria-label={t('agents.sub.policy')}>
            {sample ? (
              <>
                <option>{t('agents.sub.policy.auto')}</option>
                <option>{t('agents.sub.policy.explicit')}</option>
                <option>{t('agents.sub.policy.never')}</option>
              </>
            ) : <option>{t('agents.form.unavailable')}</option>}
          </select>
          <small>{t('agents.sub.policyHint')}</small>
        </label>
        <label className="agents-field">
          <span>{t('agents.sub.concurrency')}</span>
          <select disabled={!sample} aria-label={t('agents.sub.concurrency')}>
            {sample ? <><option>2</option><option>4</option><option>8</option></> : <option>{t('agents.form.unavailable')}</option>}
          </select>
          <small>{t('agents.sub.concurrencyHint')}</small>
        </label>
      </div>
      <button type="button" className="agents-btn" disabled={!sample} onClick={(event) => onNew(event.currentTarget)}>
        <Icon name="plus" size={13} />
        {t('agents.sub.new')}
      </button>
    </div>
  )
}

function PermPanel({ sample, onNotice }: { sample: boolean; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  const modes: Array<{ id: AgentRecord['permission']; label: MessageKey; desc: MessageKey }> = [
    { id: 'readonly', label: 'agents.perm.readonly', desc: 'agents.perm.readonlyDesc' },
    { id: 'medium', label: 'agents.perm.medium', desc: 'agents.perm.mediumDesc' },
    { id: 'high', label: 'agents.perm.high', desc: 'agents.perm.highDesc' },
    { id: 'full', label: 'agents.perm.full', desc: 'agents.perm.fullDesc' },
  ]
  const [mode, setMode] = useState<AgentRecord['permission']>('medium')
  return (
    <div className="agents-panel agents-panel-stack">
      <div className="agents-section-label">{t('agents.perm.mode')}</div>
      <div className="agents-perm-grid" role="radiogroup" aria-label={t('agents.perm.mode')}>
        {modes.map((entry) => (
          <button
            key={entry.id}
            type="button"
            role="radio"
            aria-checked={mode === entry.id}
            className={`agents-perm-option${mode === entry.id ? ' is-selected' : ''}`}
            disabled={!sample}
            onClick={() => {
              setMode(entry.id)
              onNotice(t('agents.notice.toggle'))
            }}
          >
            <span className="agents-radio" aria-hidden="true" />
            <span><b>{t(entry.label)}</b><small>{t(entry.desc)}</small></span>
          </button>
        ))}
      </div>
      <label className="agents-field">
        <span>{t('agents.perm.writable')}</span>
        <input className="mono" defaultValue={sample ? 'docs/**  src/api/**' : ''} disabled={!sample} placeholder={!sample ? t('agents.form.unavailable') : undefined} />
        <small>{t('agents.perm.writableHint')}</small>
      </label>
      <label className="agents-field">
        <span>{t('agents.perm.allowlist')}</span>
        <input className="mono" defaultValue={sample ? 'pnpm test, pnpm lint, git diff, git status' : ''} disabled={!sample} placeholder={!sample ? t('agents.form.unavailable') : undefined} />
      </label>
      <div className="agents-setting-row">
        <div><b>{t('agents.perm.network')}</b><span>{t('agents.perm.networkDesc')}</span></div>
        <AgentSwitch checked={sample} disabled={!sample} label={t('agents.perm.network')} onChange={() => onNotice(t('agents.notice.toggle'))} />
      </div>
      <div className="agents-setting-row">
        <div><b>{t('agents.perm.confirm')}</b><span>{t('agents.perm.confirmDesc')}</span></div>
        <AgentSwitch checked={sample} disabled={!sample} label={t('agents.perm.confirm')} onChange={() => onNotice(t('agents.notice.toggle'))} />
      </div>
    </div>
  )
}

function RunsPanel({ sample }: { sample: boolean }) {
  const { t } = useI18n()
  return (
    <div className="agents-panel">
      <div className="agents-table-wrap">
        <table className="agents-table">
          <thead>
            <tr>
              <th>{t('agents.runs.time')}</th><th>{t('agents.runs.chat')}</th><th>{t('agents.runs.turns')}</th><th>{t('agents.runs.tools')}</th><th>{t('agents.runs.tokens')}</th><th>{t('agents.runs.status')}</th>
            </tr>
          </thead>
          <tbody>
            {(sample ? sampleRuns : []).map((run) => (
              <tr key={run.chat}>
                <td className="mono">{run.time}</td>
                <td>{run.chat}</td>
                <td className="mono is-right">{run.turns}</td>
                <td className="mono is-right">{run.tools}</td>
                <td className="mono is-right">{run.tokens}</td>
                <td><span className={`agents-run-status is-${run.status}`}>{t(run.status === 'pending' ? 'agents.runs.pending' : run.status === 'done' ? 'agents.runs.done' : 'agents.runs.archived')}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
