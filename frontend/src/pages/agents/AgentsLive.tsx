// Settings → Agent, live mode.
//
// This is the prototype's five-tab agent page, wired to the real catalog and
// the real session ledger. Everything the user can change is saved; the persona
// fields (system prompt, thinking, tools, skills, model, runtime) are also what
// `session_open` resolves and the pi adapter puts on the command line. Fields
// the runtime has no switch for are saved too, but the page says so rather than
// showing a switch that changes nothing.

import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import {
  agentToDraft,
  blankAgentDraft,
  deleteCatalogAgent,
  loadAgentRuns,
  normalizeAgent,
  saveCatalogAgent,
  type AgentRun,
  type CatalogAgent,
  type CatalogAgentDraft,
  type CatalogModel,
  type CatalogRuntime,
  type CatalogSkill,
} from '../../catalog/client'
import { useCatalog } from '../../catalog/useCatalog'
import Icon from '../../shell/Icon'
import { ROUTES_BY_ID } from '../../shell/routes'
import './agents.css'

type AgentTab = 'basic' | 'prompt' | 'sub' | 'perm' | 'runs'
type Thinking = CatalogAgent['thinking']
type Permission = CatalogAgent['permission']

interface Editor {
  /** `null` while the user is creating a new agent. */
  id: string | null
  draft: CatalogAgentDraft
}

const THINKING: Thinking[] = ['off', 'low', 'medium', 'high']
const THINKING_KEYS: Record<Thinking, MessageKey> = {
  off: 'agents.thinking.off',
  low: 'agents.thinking.low',
  medium: 'agents.thinking.medium',
  high: 'agents.thinking.high',
}
const CONTEXT_WINDOWS = ['200K', '400K', '1M']
const PERMISSIONS: Array<{ id: Permission; label: MessageKey; desc: MessageKey }> = [
  { id: 'readonly', label: 'agents.perm.readonly', desc: 'agents.perm.readonlyDesc' },
  { id: 'medium', label: 'agents.perm.medium', desc: 'agents.perm.mediumDesc' },
  { id: 'high', label: 'agents.perm.high', desc: 'agents.perm.highDesc' },
  { id: 'full', label: 'agents.perm.full', desc: 'agents.perm.fullDesc' },
]
const PERMISSION_KEYS: Record<Permission, MessageKey> = {
  readonly: 'chat.composer.permission.readonly',
  medium: 'chat.composer.permission.medium',
  high: 'chat.composer.permission.high',
  full: 'chat.composer.permission.full',
}
const POLICIES: Array<{ id: CatalogAgent['delegationPolicy']; key: MessageKey }> = [
  { id: 'auto', key: 'agents.sub.policy.auto' },
  { id: 'explicit', key: 'agents.sub.policy.explicit' },
  { id: 'never', key: 'agents.sub.policy.never' },
]
const VARIABLES = ['{{repo_path}}', '{{model}}', '{{agent}}', '{{date}}']

/** pi's built-in tools, labelled with the prototype's copy and risk levels. */
const TOOLS: Array<{ name: string; desc: MessageKey; risk: 'low' | 'medium' | 'high' }> = [
  { name: 'read', desc: 'agents.tool.read.desc', risk: 'low' },
  { name: 'grep', desc: 'agents.tool.search.desc', risk: 'low' },
  { name: 'find', desc: 'agents.tool.search.desc', risk: 'low' },
  { name: 'ls', desc: 'agents.tool.search.desc', risk: 'low' },
  { name: 'write', desc: 'agents.tool.write.desc', risk: 'medium' },
  { name: 'edit', desc: 'agents.tool.write.desc', risk: 'medium' },
  { name: 'bash', desc: 'agents.tool.shell.desc', risk: 'high' },
  // Registered by the runtime's own extension, not built in. Named here so a
  // narrowed tool list does not silently hide delegation from the model.
  { name: 'subagent', desc: 'agents.tool.subagent.desc', risk: 'medium' },
]
const RISK_KEYS = {
  low: 'agents.risk.low',
  medium: 'agents.risk.medium',
  high: 'agents.risk.high',
} as const

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

function Switch({
  checked,
  onChange,
  label,
  testId,
}: {
  checked: boolean
  onChange: () => void
  label: string
  testId?: string
}) {
  return (
    <button
      type="button"
      className="agents-switch"
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

export default function AgentsLive() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const catalog = useCatalog(true)
  const snapshot = catalog.snapshot
  const narrow = useNarrow()
  const agents = (snapshot?.agents ?? []).map(normalizeAgent)
  const runtimes = useMemo<CatalogRuntime[]>(
    () => (snapshot?.runtimes ?? []).filter((runtime) => runtime.status !== 'not_installed'),
    [snapshot],
  )
  const modelsFor = (runtimeId: string): CatalogModel[] =>
    (snapshot?.models ?? []).filter((model) => model.runtimeId === runtimeId)
  const skillsFor = (runtimeId: string): CatalogSkill[] =>
    (snapshot?.skills ?? []).filter((skill) => skill.runtimeId === runtimeId && skill.state !== 'broken')

  const [editorState, setEditorState] = useState<Editor | null>(null)
  const [tab, setTab] = useState<AgentTab>('basic')
  const [query, setQuery] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [mobilePane, setMobilePane] = useState<'list' | 'detail'>('detail')
  const [runs, setRuns] = useState<AgentRun[]>([])
  const [runsState, setRunsState] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const mainAgents = agents.filter((agent) => agent.groupKind !== 'sub')
  const subAgents = agents.filter((agent) => agent.groupKind === 'sub')
  const needle = query.trim().toLowerCase()
  const matches = (agent: CatalogAgent) =>
    !needle ||
    `${agent.name} ${agent.slug} ${agent.description} ${agent.modelId}`.toLowerCase().includes(needle)

  // The prototype always shows a selected detail pane. Derive the initial
  // selection during render instead of in an effect: the first paint after the
  // catalog arrives must already have the tabs, not one frame of an empty pane.
  const editor: Editor | null =
    editorState ?? (agents.length > 0 ? { id: agents[0].id, draft: agentToDraft(agents[0]) } : null)

  useEffect(() => {
    if (tab !== 'runs' || !editor?.id) return
    let alive = true
    setRunsState('loading')
    loadAgentRuns(editor.id)
      .then((rows) => {
        if (!alive) return
        setRuns(rows)
        setRunsState('ready')
      })
      .catch(() => {
        if (alive) setRunsState('error')
      })
    return () => {
      alive = false
    }
  }, [tab, editor?.id, notice])

  const patch = (change: Partial<CatalogAgentDraft>) =>
    setEditorState((current) => {
      const base = current ?? editor
      return base ? { ...base, draft: { ...base.draft, ...change } } : current
    })

  const select = (agent: CatalogAgent) => {
    setEditorState({ id: agent.id, draft: agentToDraft(agent) })
    setTab('basic')
    setNotice('')
    setError('')
    setMobilePane('detail')
  }

  const begin = (groupKind: 'main' | 'sub' = 'main') => {
    const runtimeId = runtimes[0]?.runtimeId ?? ''
    const draft: CatalogAgentDraft = {
      ...blankAgentDraft(),
      runtimeId,
      modelId: modelsFor(runtimeId)[0]?.modelId ?? '',
      groupKind,
      delegatedFrom: groupKind === 'sub' ? (editor?.id ?? 'default') : '',
    }
    setEditorState({ id: null, draft })
    setTab('basic')
    setNotice('')
    setError('')
    setMobilePane('detail')
  }

  const save = async () => {
    if (!editor) return
    const { draft } = editor
    if (!draft.name.trim()) {
      setError(t('agents.live.nameRequired'))
      return
    }
    if (!draft.slug.trim()) {
      setError(t('agents.validation.slugRequired'))
      return
    }
    if (!/^[a-z0-9-]+$/.test(draft.slug)) {
      setError(t('agents.validation.slugInvalid'))
      return
    }
    if (!draft.runtimeId || !draft.modelId) {
      setError(t('agents.live.pickRuntime'))
      return
    }
    setSaving(true)
    setError('')
    try {
      const next = await saveCatalogAgent(draft)
      catalog.apply(next)
      const saved =
        next.agents.find((agent) => agent.id === draft.id) ??
        next.agents.find((agent) => agent.slug === draft.slug) ??
        next.agents[0]
        setEditorState(saved ? { id: saved.id, draft: agentToDraft(saved) } : editor)
      setNotice(t('agents.live.savedPersona'))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!editor?.id) return
    if (confirming !== editor.id) {
      setConfirming(editor.id)
      return
    }
    setConfirming(null)
    setSaving(true)
    setError('')
    try {
      const next = await deleteCatalogAgent(editor.id)
      catalog.apply(next)
      setEditorState(null)
      setNotice(t('agents.live.deleted'))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  /** A sub agent's enable switch writes that agent's row, not the open draft. */
  const toggleSubAgent = async (agent: CatalogAgent) => {
    setError('')
    try {
      const next = await saveCatalogAgent({ ...agentToDraft(agent), enabled: !agent.enabled })
      catalog.apply(next)
      setNotice(t('agents.live.savedPersona'))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

  const draft = editor?.draft ?? null
  const selected = editor?.id ? agents.find((agent) => agent.id === editor.id) ?? null : null
  const subRows = subAgents.filter((agent) => agent.delegatedFrom === editor?.id || !agent.delegatedFrom)
  const draftModels = draft ? modelsFor(draft.runtimeId) : []
  const draftSkills = draft ? skillsFor(draft.runtimeId) : []
  const tabs: Array<{ id: AgentTab; label: string }> = [
    { id: 'basic', label: t('agents.tab.basic') },
    { id: 'prompt', label: t('agents.tab.prompt') },
    { id: 'sub', label: t('agents.tab.sub', { count: subAgents.length }) },
    { id: 'perm', label: t('agents.tab.perm') },
    { id: 'runs', label: t('agents.tab.runs') },
  ]

  return (
    <section className="agents-page" data-testid="agents-page" data-mode="live">
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
            disabled
            title={t('agents.action.unavailable')}
            data-testid="agent-import"
          >
            <Icon name="upload" size={14} />
            {t('agents.import')}
          </button>
          <button
            type="button"
            className="agents-btn agents-btn-primary"
            data-testid="agents-live-new"
            disabled={saving || runtimes.length === 0}
            onClick={() => begin('main')}
          >
            <Icon name="plus" size={14} />
            {t('agents.new')}
          </button>
        </div>
      </div>

      {notice ? (
        <div className="agents-notice-bar" role="status" data-testid="agents-live-notice">
          {notice}
        </div>
      ) : null}
      {error ? (
        <div className="agents-notice-bar is-error" role="alert" data-testid="agents-live-error">
          {error}
        </div>
      ) : null}

      {catalog.status === 'unavailable' ? (
        <p className="agents-list-empty">{t('agents.live.unavailable')}</p>
      ) : catalog.status === 'loading' && !snapshot ? (
        <p className="agents-list-empty">{t('agents.live.loading')}</p>
      ) : (
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
                placeholder={t('agents.searchPlaceholder')}
                aria-label={t('agents.searchLabel')}
                onChange={(event) => setQuery(event.target.value)}
              />
            </label>
            <div data-testid="agents-live-list">
            <div className="agents-group-label">{t('agents.group.main', { count: mainAgents.length })}</div>
            {mainAgents.filter(matches).map((agent) => (
              <AgentRow
                key={agent.id}
                agent={agent}
                active={editor?.id === agent.id}
                onSelect={() => select(agent)}
              />
            ))}
            <div className="agents-group-label agents-group-sub">
              <span>{t('agents.group.sub', { count: subAgents.length })}</span>
              <button
                type="button"
                className="agents-iconbtn"
                aria-label={t('agents.addSub')}
                onClick={() => begin('sub')}
              >
                <Icon name="plus" size={13} />
              </button>
            </div>
            {subAgents.filter(matches).map((agent) => (
              <AgentRow
                key={agent.id}
                agent={agent}
                active={editor?.id === agent.id}
                onSelect={() => select(agent)}
              />
            ))}
            </div>
            {agents.length === 0 ? (
              <div className="agents-list-empty" data-testid="agent-empty-list">
                <Icon name="bot" size={20} />
                <b>{t('agents.live.noRuntime')}</b>
              </div>
            ) : null}
          </section>

          <section className="agents-detail-card" data-testid="agents-detail">
            {narrow ? (
              <button type="button" className="agents-back" data-testid="agents-back" onClick={() => setMobilePane('list')}>
                <Icon name="arrowLeft" size={13} />
                {t('agents.backToList')}
              </button>
            ) : null}
            {draft ? (
              <>
                <div className="agents-hero">
                  <span className="agents-badge agents-hero-badge">{draft.name.slice(0, 1) || 'A'}</span>
                  <div className="agents-hero-main">
                    <h2>{draft.name || t('agents.live.new')}</h2>
                    <p>{draft.description || t('agents.form.description')}</p>
                    <div className="agents-chips">
                      <span className="agents-chip">{draft.runtimeId || t('agents.form.runtime')}</span>
                      <span className="agents-chip">{draft.modelId || t('agents.form.model')}</span>
                      <span className="agents-chip">{t(THINKING_KEYS[draft.thinking])}</span>
                      <span className="agents-chip">{t('agents.permissionChip', { level: t(PERMISSION_KEYS[draft.permission]) })}</span>
                      <span className="agents-chip">{t('agents.skills', { count: draft.skills.length })}</span>
                    </div>
                  </div>
                  <div className="agents-hero-actions">
                    <button
                      type="button"
                      className="agents-btn agents-btn-sm"
                      data-testid="agent-start-chat"
                      disabled={!editor?.id}
                      title={!editor?.id ? t('agents.live.saveFirst') : undefined}
                      onClick={() => editor?.id && navigate(`/chat?agent=${encodeURIComponent(editor.id)}`)}
                    >
                      <Icon name="message" size={13} />
                      {t('agents.startChat')}
                    </button>
                    <button
                      type="button"
                      className="agents-btn agents-btn-sm agents-btn-primary"
                      data-testid="agent-save"
                      disabled={saving}
                      onClick={() => void save()}
                    >
                      {t('agents.save')}
                    </button>
                    {selected && !selected.isDefault ? (
                      <button
                        type="button"
                        className={`agents-btn agents-btn-sm${confirming === editor?.id ? ' is-danger' : ''}`}
                        data-testid="agent-delete"
                        disabled={saving}
                        onClick={() => void remove()}
                      >
                        {confirming === editor?.id ? t('agents.live.deleteConfirm') : t('agents.live.delete')}
                      </button>
                    ) : null}
                  </div>
                </div>

                <div className="agents-tabs" role="tablist" aria-label={t('agents.title')}>
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
                    <BasicPanel draft={draft} patch={patch} runtimes={runtimes} models={draftModels} t={t} />
                  ) : null}
                  {tab === 'prompt' ? (
                    <PromptPanel draft={draft} patch={patch} skills={draftSkills} t={t} />
                  ) : null}
                  {tab === 'sub' ? (
                    <SubPanel draft={draft} patch={patch} rows={subRows} subTotal={subAgents.length} onNew={() => begin('sub')} onToggleSub={(agent) => void toggleSubAgent(agent)} t={t} />
                  ) : null}
                  {tab === 'perm' ? <PermPanel draft={draft} patch={patch} t={t} /> : null}
                  {tab === 'runs' ? (
                    <RunsPanel rows={runs} state={runsState} t={t} />
                  ) : null}
                </div>
              </>
            ) : (
              <div className="agents-detail-empty" data-testid="agent-empty-detail">
                <Icon name="bot" size={22} />
                <h2>{t('agents.live.empty')}</h2>
                <p>{t('agents.live.noRuntime')}</p>
              </div>
            )}
          </section>
        </div>
      )}
    </section>
  )
}

type Translate = (key: MessageKey, vars?: Record<string, string | number>) => string

function AgentRow({
  agent,
  active,
  onSelect,
}: {
  agent: CatalogAgent
  active: boolean
  onSelect: () => void
}) {
  const { t } = useI18n()
  return (
    <button
      type="button"
      className={`agents-item${active ? ' is-active' : ''}`}
      data-testid={`agents-live-row-${agent.id}`}
      aria-pressed={active}
      onClick={onSelect}
    >
      <span className={`agents-badge${agent.enabled ? ' is-ready' : ''}`}>{agent.name.slice(0, 1) || 'A'}</span>
      <span className="agents-item-main">
        <span className="agents-item-name">
          {agent.name}
          {agent.groupKind === 'sub' ? <span className="agents-sub-badge">{t('agents.subBadge')}</span> : null}
          {agent.isDefault ? <span className="agents-ready"><i />{t('agents.live.builtIn')}</span> : null}
        </span>
        <span className="agents-item-sub">
          {t('agents.listMeta', { model: agent.modelId, count: agent.skills.length })}
        </span>
      </span>
    </button>
  )
}

function BasicPanel({
  draft,
  patch,
  runtimes,
  models,
  t,
}: {
  draft: CatalogAgentDraft
  patch: (change: Partial<CatalogAgentDraft>) => void
  runtimes: CatalogRuntime[]
  models: CatalogModel[]
  t: Translate
}) {
  return (
    <div className="agents-panel">
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.form.name')}</span>
          <input
            data-testid="agent-field-name"
            aria-label={t('agents.form.name')}
            value={draft.name}
            onChange={(event) => patch({ name: event.target.value })}
          />
        </label>
        <label className="agents-field">
          <span>{t('agents.form.slug')}</span>
          <input
            className="mono"
            data-testid="agent-field-slug"
            aria-label={t('agents.form.slug')}
            value={draft.slug}
            onChange={(event) => patch({ slug: event.target.value })}
          />
        </label>
      </div>
      <label className="agents-field">
        <span>{t('agents.form.description')}</span>
        <textarea
          rows={2}
          aria-label={t('agents.form.description')}
          value={draft.description}
          onChange={(event) => patch({ description: event.target.value })}
        />
      </label>
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.form.runtime')}</span>
          <select
            aria-label={t('agents.form.runtime')}
            data-testid="agents-live-runtime"
            value={draft.runtimeId}
            onChange={(event) => patch({ runtimeId: event.target.value, modelId: '' })}
          >
            <option value="">{t('agents.live.pickRuntime')}</option>
            {runtimes.map((runtime) => (
              <option key={runtime.runtimeId} value={runtime.runtimeId}>
                {runtime.displayName || runtime.runtimeId} · {runtime.runtimeId}
              </option>
            ))}
          </select>
          <small>{t('agents.form.runtimeHint')}</small>
        </label>
        <label className="agents-field">
          <span>{t('agents.form.model')}</span>
          <select
            aria-label={t('agents.form.model')}
            data-testid="agents-live-model"
            value={draft.modelId}
            disabled={!draft.runtimeId}
            onChange={(event) => patch({ modelId: event.target.value })}
          >
            <option value="">{t('agents.live.pickModel')}</option>
            {models.map((model) => (
              <option key={model.modelId} value={model.modelId}>
                {model.modelId}{model.enabled ? '' : ` · ${t('agents.live.modelDisabled')}`}
              </option>
            ))}
          </select>
          <small>{t('agents.live.bindingHint')}</small>
        </label>
      </div>
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.form.thinking')}</span>
          <select
            aria-label={t('agents.form.thinking')}
            data-testid="agent-field-thinking"
            value={draft.thinking}
            onChange={(event) => patch({ thinking: event.target.value as Thinking })}
          >
            {THINKING.map((level) => (
              <option key={level} value={level}>{t(THINKING_KEYS[level])}</option>
            ))}
          </select>
          <small>{t('agents.form.thinkingHint')}</small>
        </label>
        <label className="agents-field">
          <span>{t('agents.form.context')}</span>
          <select
            aria-label={t('agents.form.context')}
            data-testid="agent-field-context"
            value={draft.contextWindow}
            onChange={(event) => patch({ contextWindow: event.target.value })}
          >
            {CONTEXT_WINDOWS.map((size) => <option key={size} value={size}>{size}</option>)}
          </select>
          <small>{t('agents.live.contextHint')}</small>
        </label>
      </div>
      <div className="agents-setting-row">
        <div>
          <b>{t('agents.form.memory')}</b>
          <span>{t('agents.form.memoryDesc')}</span>
        </div>
        <Switch
          checked={draft.memory}
          label={t('agents.form.memory')}
          testId="agent-switch-memory"
          onChange={() => patch({ memory: !draft.memory })}
        />
      </div>
      <div className="agents-setting-row">
        <div>
          <b>{t('agents.form.autoCompact')}</b>
          <span>{t('agents.form.autoCompactDesc')}</span>
        </div>
        <Switch
          checked={draft.autoCompact}
          label={t('agents.form.autoCompact')}
          testId="agent-switch-compact"
          onChange={() => patch({ autoCompact: !draft.autoCompact })}
        />
      </div>
      <div className="agents-notice is-muted" data-testid="agent-compact-hint">
        <Icon name="alert" size={13} />
        <span>{t('agents.live.compactHint')}</span>
      </div>
      <div className="agents-notice" data-testid="agent-persona-hint">
        <Icon name="sparkle" size={13} />
        <span>{t('agents.live.personaHint')}</span>
      </div>
    </div>
  )
}

function PromptPanel({
  draft,
  patch,
  skills,
  t,
}: {
  draft: CatalogAgentDraft
  patch: (change: Partial<CatalogAgentDraft>) => void
  skills: CatalogSkill[]
  t: Translate
}) {
  const toggleTool = (name: string) =>
    patch({
      tools: draft.tools.includes(name)
        ? draft.tools.filter((item) => item !== name)
        : [...draft.tools, name],
    })
  const toggleSkill = (name: string) =>
    patch({
      skills: draft.skills.includes(name)
        ? draft.skills.filter((item) => item !== name)
        : [...draft.skills, name],
    })
  return (
    <div className="agents-panel agents-panel-stack">
      <label className="agents-field">
        <span>{t('agents.prompt.system')}</span>
        <textarea
          className="mono"
          rows={9}
          aria-label={t('agents.prompt.system')}
          data-testid="agent-field-system-prompt"
          value={draft.systemPrompt}
          placeholder={t('agents.live.promptPlaceholder')}
          onChange={(event) => patch({ systemPrompt: event.target.value })}
        />
        <small>{t('agents.live.promptHint')}</small>
      </label>
      <div className="agents-pills">
        {VARIABLES.map((variable) => (
          <button
            key={variable}
            type="button"
            className="agents-chip mono"
            title={t('agents.live.insertVariable')}
            onClick={() => patch({ systemPrompt: `${draft.systemPrompt}${draft.systemPrompt ? ' ' : ''}${variable}` })}
          >
            {variable}
          </button>
        ))}
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
            {TOOLS.map((tool) => (
              <tr key={tool.name}>
                <td className="mono">{tool.name}</td>
                <td>{t(tool.desc)}</td>
                <td><span className={`agents-badge-risk is-${tool.risk}`}>{t(RISK_KEYS[tool.risk])}</span></td>
                <td className="is-right">
                  <Switch
                    checked={draft.tools.includes(tool.name)}
                    label={`${tool.name} ${t('agents.prompt.enabled')}`}
                    testId={`agent-tool-${tool.name}`}
                    onChange={() => toggleTool(tool.name)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="agents-section-label agents-skill-head">
        <span>{t('agents.prompt.skillCount', { count: draft.skills.length })}</span>
        <span className="agents-skill-counter" data-testid="agent-skill-counter">
          {t('agents.prompt.skillSelected', {
            selected: skills.filter((skill) => draft.skills.includes(skill.name)).length,
            total: skills.length,
          })}
        </span>
        <span className="agents-skill-actions">
          <button
            type="button"
            className="agents-mini-btn"
            data-testid="agent-skill-select-all"
            disabled={skills.every((skill) => draft.skills.includes(skill.name))}
            onClick={() => patch({ skills: [...new Set(skills.map((skill) => skill.name))] })}
          >
            {t('agents.prompt.selectAll')}
          </button>
          <button
            type="button"
            className="agents-mini-btn"
            data-testid="agent-skill-clear-all"
            disabled={draft.skills.length === 0}
            onClick={() => patch({ skills: [] })}
          >
            {t('agents.prompt.clearAll')}
          </button>
        </span>
      </div>
      {skills.length === 0 ? (
        <div className="agents-list-empty" data-testid="agent-skill-empty">{t('agents.live.skillEmpty')}</div>
      ) : (
        <div className="agents-skill-grid">
          {skills.map((skill) => (
            <label key={skill.path} className="agents-check-row">
              <input
                type="checkbox"
                checked={draft.skills.includes(skill.name)}
                aria-label={skill.name}
                onChange={() => toggleSkill(skill.name)}
              />
              <span><b>{skill.name}</b><small>{skill.description || skill.path}</small></span>
            </label>
          ))}
        </div>
      )}
    </div>
  )
}

function SubPanel({
  draft,
  patch,
  rows,
  subTotal,
  onNew,
  onToggleSub,
  t,
}: {
  draft: CatalogAgentDraft
  patch: (change: Partial<CatalogAgentDraft>) => void
  rows: CatalogAgent[]
  subTotal: number
  onNew: () => void
  onToggleSub: (agent: CatalogAgent) => void
  t: Translate
}) {
  return (
    <div className="agents-panel agents-panel-stack">
      <div className="agents-notice">
        <Icon name="branch" size={14} />
        <span>{t('agents.sub.notice')}</span>
      </div>
      <div className="agents-notice is-muted" data-testid="agent-sub-limit">
        <Icon name="alert" size={13} />
        <span>{t('agents.live.delegationHint')}</span>
      </div>
      <div className="agents-table-wrap">
        <table className="agents-table">
          <thead>
            <tr>
              <th>{t('agents.sub.table.agent')}</th>
              <th>{t('agents.sub.table.runtime')}</th>
              <th>{t('agents.sub.table.model')}</th>
              <th>{t('agents.sub.table.concurrency')}</th>
              <th className="is-right">{t('agents.sub.table.enabled')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((agent) => (
              <tr key={agent.id}>
                <td><span className="agents-table-agent"><span className="agents-badge agents-badge-sm">{agent.name.slice(0, 1)}</span>{agent.name}</span></td>
                <td>{agent.runtimeId}</td>
                <td className="mono">{agent.modelId}</td>
                <td className="mono">{agent.concurrency}</td>
                <td className="is-right">
                  <Switch checked={agent.enabled} label={`${agent.name} ${t('agents.sub.table.enabled')}`} onChange={() => onToggleSub(agent)} />
                </td>
              </tr>
            ))}
            {rows.length === 0 ? (
              <tr><td colSpan={5} className="agents-list-empty">{t('agents.live.subEmpty', { count: subTotal })}</td></tr>
            ) : null}
          </tbody>
        </table>
      </div>
      <div className="agents-form-grid">
        <label className="agents-field">
          <span>{t('agents.sub.policy')}</span>
          <select
            aria-label={t('agents.sub.policy')}
            value={draft.delegationPolicy}
            onChange={(event) => patch({ delegationPolicy: event.target.value as CatalogAgent['delegationPolicy'] })}
          >
            {POLICIES.map((policy) => <option key={policy.id} value={policy.id}>{t(policy.key)}</option>)}
          </select>
          <small>{t('agents.sub.policyHint')}</small>
        </label>
        <label className="agents-field">
          <span>{t('agents.sub.concurrency')}</span>
          <select
            aria-label={t('agents.sub.concurrency')}
            value={draft.concurrency}
            onChange={(event) => patch({ concurrency: Number(event.target.value) })}
          >
            {[2, 4, 8].map((limit) => <option key={limit} value={limit}>{limit}</option>)}
          </select>
          <small>{t('agents.sub.concurrencyHint')}</small>
        </label>
      </div>
      <button type="button" className="agents-btn" onClick={onNew}>
        <Icon name="plus" size={13} />
        {t('agents.sub.new')}
      </button>
    </div>
  )
}

function PermPanel({
  draft,
  patch,
  t,
}: {
  draft: CatalogAgentDraft
  patch: (change: Partial<CatalogAgentDraft>) => void
  t: Translate
}) {
  return (
    <div className="agents-panel agents-panel-stack">
      <div className="agents-section-label">{t('agents.perm.mode')}</div>
      <div className="agents-perm-grid" role="radiogroup" aria-label={t('agents.perm.mode')}>
        {PERMISSIONS.map((entry) => (
          <button
            key={entry.id}
            type="button"
            role="radio"
            aria-checked={draft.permission === entry.id}
            className={`agents-perm-option${draft.permission === entry.id ? ' is-selected' : ''}`}
            onClick={() => patch({ permission: entry.id })}
          >
            <span className="agents-radio" aria-hidden="true" />
            <span><b>{t(entry.label)}</b><small>{t(entry.desc)}</small></span>
          </button>
        ))}
      </div>
      <div className="agents-notice is-muted">
        <Icon name="alert" size={13} />
        <span>{t('agents.live.permissionHint')}</span>
      </div>
      <label className="agents-field">
        <span>{t('agents.perm.writable')}</span>
        <input
          className="mono"
          aria-label={t('agents.perm.writable')}
          value={draft.writableDirs}
          onChange={(event) => patch({ writableDirs: event.target.value })}
        />
        <small>{t('agents.perm.writableHint')}</small>
      </label>
      <label className="agents-field">
        <span>{t('agents.perm.allowlist')}</span>
        <input
          className="mono"
          aria-label={t('agents.perm.allowlist')}
          value={draft.commandAllowlist}
          onChange={(event) => patch({ commandAllowlist: event.target.value })}
        />
      </label>
      <div className="agents-setting-row">
        <div><b>{t('agents.perm.network')}</b><span>{t('agents.perm.networkDesc')}</span></div>
        <Switch checked={draft.network} label={t('agents.perm.network')} onChange={() => patch({ network: !draft.network })} />
      </div>
      <div className="agents-setting-row">
        <div><b>{t('agents.perm.confirm')}</b><span>{t('agents.perm.confirmDesc')}</span></div>
        <Switch checked={draft.confirmDangerous} label={t('agents.perm.confirm')} onChange={() => patch({ confirmDangerous: !draft.confirmDangerous })} />
      </div>
      <div className="agents-notice is-muted" data-testid="agent-permission-limit">
        <Icon name="alert" size={13} />
        <span>{t('agents.live.limited')}</span>
      </div>
    </div>
  )
}

function RunsPanel({
  rows,
  state,
  t,
}: {
  rows: AgentRun[]
  state: 'idle' | 'loading' | 'ready' | 'error'
  t: Translate
}) {
  const statusKey = (status: string): MessageKey =>
    status === 'idle' ? 'agents.runs.done' : status === 'archived' ? 'agents.runs.archived' : 'agents.runs.pending'
  return (
    <div className="agents-panel">
      <div className="agents-table-wrap">
        <table className="agents-table" data-testid="agent-runs-table">
          <thead>
            <tr>
              <th>{t('agents.runs.time')}</th>
              <th>{t('agents.runs.chat')}</th>
              <th>{t('agents.runs.turns')}</th>
              <th>{t('agents.runs.tools')}</th>
              <th>{t('agents.runs.tokens')}</th>
              <th>{t('agents.runs.status')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((run) => (
              <tr key={run.sessionId}>
                <td className="mono">{run.at}</td>
                <td>{run.title}</td>
                <td className="mono is-right">{run.turns}</td>
                <td className="mono is-right">{run.toolCalls}</td>
                <td className="mono is-right">{run.tokens}</td>
                <td><span className={`agents-run-status is-${run.status}`}>{t(statusKey(run.status))}</span></td>
              </tr>
            ))}
            {rows.length === 0 && state !== 'loading' ? (
              <tr>
                <td colSpan={6} className="agents-list-empty" data-testid="agent-runs-empty">
                  {state === 'error' ? t('agents.live.runsError') : t('agents.live.runsEmpty')}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  )
}

