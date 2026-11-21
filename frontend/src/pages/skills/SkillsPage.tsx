import { useRef, useState, type KeyboardEvent } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import SkillDialogs, { type SkillDialogKind } from './SkillDialogs'
import {
  draftSkills,
  installedSkills,
  marketSkills,
  useSkillsSample,
  type SkillRecord,
  type SkillSource,
  type SkillTab,
} from './sampleData'
import './skills.css'
import { useCatalog } from '../../catalog/useCatalog'
import type { CatalogSkill } from '../../catalog/client'
import { CatalogSkillsPanel, CatalogSummary } from '../../catalog/CatalogPanels'
import { pickWorkspace } from '../../workspace/client'
import { createSkill, importSkill } from './client'

const SOURCE_KEYS: Record<SkillSource, MessageKey> = {
  builtin: 'skills.source.builtin',
  local: 'skills.source.local',
  market: 'skills.source.market',
  official: 'skills.source.official',
  community: 'skills.source.community',
}

export default function SkillsPage() {
  const { t } = useI18n()
  const sample = useSkillsSample()
  const catalog = useCatalog(!sample)
  const [tab, setTab] = useState<SkillTab>('installed')
  const [query, setQuery] = useState('')
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState<SkillDialogKind>(null)
  const [editing, setEditing] = useState<SkillRecord | null>(null)
  const [building, setBuilding] = useState(false)
  const [busy, setBusy] = useState(false)
  const dialogTrigger = useRef<HTMLElement | null>(null)

  const records = sample ? (tab === 'installed' ? installedSkills : tab === 'market' ? marketSkills : draftSkills) : []
  const normalized = query.trim().toLowerCase()
  const filtered = records.filter((skill) =>
    !normalized ||
    `${skill.name} ${skill.description} ${skill.tags.join(' ')}`.toLowerCase().includes(normalized),
  )
  // Live mode reads the very rows the catalog just synced from disk. Without
  // this the page showed a "not connected" empty grid even though the database
  // held dozens of real skills.
  const liveSkills = catalog.snapshot?.skills ?? []
  const liveReady = !sample && (catalog.status === 'ready' || catalog.status === 'loading') && catalog.snapshot !== null
  const liveFiltered = liveSkills.filter((skill) =>
    !normalized ||
    `${skill.name} ${skill.description} ${skill.runtimeId}`.toLowerCase().includes(normalized),
  )
  const tabs: Array<{ id: SkillTab; label: string }> = [
    { id: 'installed', label: sample ? t('skills.tab.installedCount', { count: installedSkills.length }) : liveReady ? t('skills.tab.installedCount', { count: liveSkills.length }) : t('skills.tab.installed') },
    { id: 'market', label: t('skills.tab.market') },
    { id: 'drafts', label: sample ? t('skills.tab.draftsCount', { count: draftSkills.length }) : t('skills.tab.drafts') },
  ]

  const announce = (message: string) => setNotice(message)
  const openDialog = (kind: SkillDialogKind, trigger: HTMLElement, skill: SkillRecord | null = null) => {
    dialogTrigger.current = trigger
    setEditing(skill)
    setDialog(kind)
  }
  const closeDialog = () => {
    setDialog(null)
    const trigger = dialogTrigger.current
    window.setTimeout(() => trigger?.focus(), 0)
  }
  const changeTab = (next: SkillTab) => {
    setTab(next)
    setQuery('')
  }
  // Live mode can really install: the catalog is scanned from disk, so both
  // buttons only need the host, not a populated list.
  const canWrite = sample || liveReady
  const draftLive = liveSkills.filter((skill) => skill.state !== 'valid')
  const onImport = async () => {
    if (sample) {
      openDialog('import', document.body as HTMLElement)
      return
    }
    setBusy(true)
    try {
      const folder = await pickWorkspace()
      if (!folder) return
      const installed = await importSkill(folder)
      await catalog.syncInventory()
      announce(t('skills.notice.imported', { path: installed }))
    } catch (error) {
      announce(t('skills.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy(false)
    }
  }
  const onCreate = async (values: { name: string; description: string; body: string }) => {
    setBusy(true)
    try {
      const installed = await createSkill(values.name, values.description, values.body)
      await catalog.syncInventory()
      setBuilding(false)
      announce(t('skills.notice.created', { name: values.name, path: installed }))
    } catch (error) {
      announce(t('skills.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy(false)
    }
  }

  const onTabKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
    event.preventDefault()
    const index = tabs.findIndex((entry) => entry.id === tab)
    changeTab(tabs[(index + (event.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length].id)
  }

  return (
    <section className="skills-page" data-testid="skills-page" data-sample={sample ? 'true' : 'false'}>
      <span className="sr-only" id="route-marker" data-testid="page-marker">
        R0-07 skills · /skills
      </span>
      <div className="skills-page-head">
        <div>
          <h1>{t('skills.title')}</h1>
          <p>{t('skills.description')}</p>
        </div>
        <div className="skills-head-actions">
          <button type="button" className="skills-btn" disabled={!canWrite || busy} title={canWrite ? t('skills.import') : t('skills.action.unavailable')} onClick={(event) => (sample ? openDialog('import', event.currentTarget) : void onImport())}>
            <Icon name="download" size={14} />
            {t('skills.import')}
          </button>
          <button type="button" className="skills-btn skills-btn-primary" disabled={!canWrite || busy} title={canWrite ? t('skills.new') : t('skills.action.unavailable')} onClick={(event) => (sample ? openDialog('builder', event.currentTarget) : setBuilding(true))}>
            <Icon name="plus" size={14} />
            {t('skills.new')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="skills-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('skills.sampleBanner')}
        </div>
      ) : null}
      {notice ? <div className="skills-notice-bar" data-testid="skill-notice" role="status">{notice}</div> : null}

      {!sample && (catalog.status === 'ready' || catalog.status === 'loading' || catalog.status === 'error') && (
        <div className="skills-catalog" data-testid="skills-catalog">
          <CatalogSummary view={catalog} />
          <CatalogSkillsPanel view={catalog} />
        </div>
      )}

      <div className="skills-tabs" role="tablist" aria-label={t('skills.title')} onKeyDown={onTabKeyDown}>
        {tabs.map((entry) => (
          <button key={entry.id} type="button" role="tab" aria-selected={tab === entry.id} className={`skills-tab${tab === entry.id ? ' is-active' : ''}`} onClick={() => changeTab(entry.id)}>
            {entry.label}
          </button>
        ))}
      </div>

      <div className="skills-toolbar">
        <label className="skills-search">
          <Icon name="search" size={12} />
          <input data-testid="skill-search" type="search" value={query} disabled={!sample && !liveReady} placeholder={t('skills.searchPlaceholder')} aria-label={t('skills.searchPlaceholder')} onChange={(event) => setQuery(event.target.value)} />
        </label>
        <select disabled={!sample} aria-label={t('skills.filter.source')}><option>{t('skills.filter.source')}</option></select>
        <select disabled={!sample} aria-label={t('skills.filter.agent')}><option>{t('skills.filter.agent')}</option></select>
        <div className="skills-toolbar-summary">{sample ? t('skills.toolbar.summary', { count: installedSkills.length }) : liveReady ? t('catalog.skills.sub', { count: liveFiltered.length, runtimes: new Set(liveSkills.map((skill) => skill.runtimeId)).size }) : t('skills.action.unavailable')}</div>
      </div>

      {sample ? (
        filtered.length > 0 ? (
          <div className="skills-grid" data-testid="skills-grid">
            {filtered.map((skill) => (
              <SkillCard
                key={skill.id}
                skill={skill}
                onEdit={(trigger) => openDialog('builder', trigger, skill)}
                onNotice={announce}
              />
            ))}
          </div>
        ) : (
          <div className="skills-empty-grid">{t('skills.listNoMatch')}</div>
        )
      ) : liveReady && tab === 'installed' && liveFiltered.length > 0 ? (
        <div className="skills-grid" data-testid="skills-grid">
          {liveFiltered.map((skill) => (
            <LiveSkillCard key={`${skill.runtimeId}:${skill.path}`} skill={skill} />
          ))}
        </div>
      ) : liveReady && tab === 'installed' ? (
        <div className="skills-empty-grid" data-testid="skill-empty-list">
          <Icon name="puzzle" size={22} />
          <b>{t('catalog.skills.empty')}</b>
          <span>{t('catalog.inventory.sync')}</span>
        </div>
      ) : liveReady && tab === 'drafts' && draftLive.length > 0 ? (
        <div className="skills-grid" data-testid="skills-grid">
          {draftLive.map((skill) => (
            <LiveSkillCard key={`${skill.runtimeId}:${skill.path}`} skill={skill} />
          ))}
        </div>
      ) : liveReady ? (
        <div className="skills-empty-grid" data-testid="skill-empty-list">
          <Icon name="puzzle" size={22} />
          <b>{tab === 'market' ? t('skills.market.unavailable') : t('skills.drafts.empty')}</b>
          <span>{tab === 'market' ? t('skills.market.unavailableBody') : t('skills.drafts.emptyBody')}</span>
        </div>
      ) : (
        <div className="skills-empty-grid" data-testid="skill-empty-list">
          <Icon name="puzzle" size={22} />
          <b>{t('skills.emptyList')}</b>
          <span>{t('skills.emptyListBody')}</span>
          <div className="skills-empty-actions">
            <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-save" disabled>{t('skills.detail.save')}</button>
            <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-enable" role="switch" aria-checked="false" disabled>{t('skills.detail.enable')}</button>
            <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-test-run" disabled>{t('skills.test.run')}</button>
          </div>
        </div>
      )}

      <div className="skills-footnote">
        <Icon name="shield" size={14} />
        <span>{t('skills.footnote')}</span>
      </div>

      <SkillDialogs kind={dialog} initial={editing} onClose={closeDialog} onNotice={announce} />
      {building ? (
        <LiveSkillBuilder busy={busy} onClose={() => setBuilding(false)} onCreate={(values) => void onCreate(values)} />
      ) : null}
    </section>
  )
}

function LiveSkillBuilder({
  busy,
  onClose,
  onCreate,
}: {
  busy: boolean
  onClose: () => void
  onCreate: (values: { name: string; description: string; body: string }) => void
}) {
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [body, setBody] = useState('')
  const [error, setError] = useState('')
  return (
    <div
      className="skills-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className="skills-dialog" role="dialog" aria-modal="true" aria-labelledby="skill-live-title">
        <div className="skills-dialog-head">
          <div>
            <h2 id="skill-live-title">{t('skills.live.title')}</h2>
            <p>{t('skills.live.description')}</p>
          </div>
          <button type="button" className="skills-iconbtn" aria-label={t('skills.builder.cancel')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="skills-dialog-body">
          <label className="skills-field">
            <span>{t('skills.builder.name')}</span>
            <input value={name} onChange={(event) => { setName(event.target.value); setError('') }} />
          </label>
          <label className="skills-field">
            <span>{t('skills.builder.descriptionLabel')}</span>
            <input value={description} onChange={(event) => setDescription(event.target.value)} />
          </label>
          <label className="skills-field">
            <span>{t('skills.live.body')}</span>
            <textarea rows={6} value={body} placeholder={t('skills.live.bodyPlaceholder')} onChange={(event) => setBody(event.target.value)} />
          </label>
          <small className="skills-hint">{t('skills.live.hint')}</small>
          {error ? <p className="skills-error" role="alert">{error}</p> : null}
        </div>
        <div className="skills-dialog-foot">
          <button type="button" className="skills-btn" onClick={onClose}>{t('skills.builder.cancel')}</button>
          <button
            type="button"
            className="skills-btn skills-btn-primary"
            disabled={busy}
            onClick={() => {
              if (!name.trim()) {
                setError(t('skills.validation.nameRequired'))
                return
              }
              onCreate({ name: name.trim(), description: description.trim(), body: body.trim() })
            }}
          >
            {t('skills.live.create')}
          </button>
        </div>
      </div>
    </div>
  )
}

/** A real catalog row: name, runtime, validation state and the path on disk. */
function LiveSkillCard({ skill }: { skill: CatalogSkill }) {
  return (
    <article className="skills-card" data-testid="skill-item" data-skill={skill.name}>
      <div className="skills-card-open">
        <span className="skills-card-head">
          <span className="skills-ico"><Icon name="puzzle" size={16} /></span>
          <span>
            <span className="skills-card-name">{skill.name}</span>
            <span className="skills-card-meta mono">{skill.runtimeId} · {skill.state}</span>
          </span>
        </span>
        <span className="skills-card-body">
          <span className="skills-card-desc">{skill.description || skill.path}</span>
          <span className="skills-pills"><span className="skills-chip mono">{skill.state}</span></span>
        </span>
      </div>
      <div className="skills-card-foot">
        <span className="skills-card-foot-copy mono" title={skill.path}>{skill.path}</span>
      </div>
    </article>
  )
}

function SkillCard({ skill, onEdit, onNotice }: { skill: SkillRecord; onEdit: (trigger: HTMLElement) => void; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <article className="skills-card" data-testid="skill-item">
      <button type="button" className="skills-card-open" aria-label={t('skills.card.open', { name: skill.name })} onClick={(event) => onEdit(event.currentTarget)}>
        <span className="skills-card-head">
          <span className="skills-ico"><Icon name={skill.icon} size={16} /></span>
          <span>
            <span className="skills-card-name">{skill.name}</span>
            <span className="skills-card-meta">{skill.version ? t('skills.meta', { version: skill.version, source: t(SOURCE_KEYS[skill.source]) }) : t('skills.status.draft')}</span>
          </span>
        </span>
        <span className="skills-card-body">
          <span className="skills-card-desc">{skill.description}</span>
          {skill.tags.length > 0 ? <span className="skills-pills">{skill.tags.map((tag) => <span key={tag} className="skills-chip">{tag}</span>)}</span> : null}
        </span>
      </button>
      <div className="skills-card-foot">
        <span className="skills-card-foot-copy">
          {skill.calls !== undefined ? <span>{t('skills.calls', { count: skill.calls })}</span> : null}
          {skill.updated ? <><span>·</span><span>{skill.updated}</span></> : null}
          {skill.installs !== undefined ? <span>{t('skills.installs', { count: skill.installs })}</span> : null}
          {skill.draftNote ? <span>{skill.draftNote}</span> : null}
        </span>
        {skill.enabled !== undefined ? (
          <button type="button" className="skills-switch" role="switch" aria-checked={skill.enabled} aria-label={`${skill.name} ${t('skills.detail.enable')}`} onClick={() => onNotice(skill.enabled ? t('skills.notice.disable') : t('skills.notice.enable'))}><span /></button>
        ) : null}
        {skill.installs !== undefined ? (
          <button type="button" className="skills-btn skills-btn-sm" onClick={() => onNotice(t('skills.notice.install'))}>{t('skills.card.install')}</button>
        ) : null}
        {skill.draftNote ? (
          <button type="button" className="skills-btn skills-btn-sm" onClick={(event) => onEdit(event.currentTarget)}>{t('skills.card.continue')}</button>
        ) : null}
        <button type="button" className="skills-card-menu" aria-label={t('skills.card.menu', { name: skill.name })} onClick={(event) => onEdit(event.currentTarget)}>
          <Icon name="more" size={14} />
        </button>
      </div>
    </article>
  )
}
