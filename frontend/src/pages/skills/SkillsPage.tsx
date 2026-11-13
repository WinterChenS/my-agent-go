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
import { CatalogSkillsPanel, CatalogSummary } from '../../catalog/CatalogPanels'

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
  const dialogTrigger = useRef<HTMLElement | null>(null)

  const records = sample ? (tab === 'installed' ? installedSkills : tab === 'market' ? marketSkills : draftSkills) : []
  const normalized = query.trim().toLowerCase()
  const filtered = records.filter((skill) =>
    !normalized ||
    `${skill.name} ${skill.description} ${skill.tags.join(' ')}`.toLowerCase().includes(normalized),
  )
  const tabs: Array<{ id: SkillTab; label: string }> = [
    { id: 'installed', label: sample ? t('skills.tab.installedCount', { count: installedSkills.length }) : t('skills.tab.installed') },
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
          <button type="button" className="skills-btn" disabled={!sample} title={sample ? t('skills.import') : t('skills.action.unavailable')} onClick={(event) => openDialog('import', event.currentTarget)}>
            <Icon name="download" size={14} />
            {t('skills.import')}
          </button>
          <button type="button" className="skills-btn skills-btn-primary" disabled={!sample} title={sample ? t('skills.new') : t('skills.action.unavailable')} onClick={(event) => openDialog('builder', event.currentTarget)}>
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
          <input data-testid="skill-search" type="search" value={query} disabled={!sample} placeholder={t('skills.searchPlaceholder')} aria-label={t('skills.searchPlaceholder')} onChange={(event) => setQuery(event.target.value)} />
        </label>
        <select disabled={!sample} aria-label={t('skills.filter.source')}><option>{t('skills.filter.source')}</option></select>
        <select disabled={!sample} aria-label={t('skills.filter.agent')}><option>{t('skills.filter.agent')}</option></select>
        <div className="skills-toolbar-summary">{sample ? t('skills.toolbar.summary', { count: installedSkills.length }) : t('skills.action.unavailable')}</div>
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
    </section>
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
