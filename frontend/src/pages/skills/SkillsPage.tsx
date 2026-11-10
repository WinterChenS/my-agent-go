import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import SkillDialogs, { type SkillDialogKind } from './SkillDialogs'
import {
  draftSkills,
  installedSkills,
  marketSkills,
  testFailure,
  testInput,
  testSuccess,
  triggerConditions,
  triggerResources,
  useSkillsSample,
  workflowSteps,
  type SkillRecord,
  type SkillSource,
  type SkillTab,
} from './sampleData'
import './skills.css'

const SOURCE_KEYS: Record<SkillSource, MessageKey> = {
  builtin: 'skills.source.builtin',
  local: 'skills.source.local',
  market: 'skills.source.market',
  official: 'skills.source.official',
  community: 'skills.source.community',
}

const RESOURCE_KEYS: Record<string, { label: MessageKey; desc: MessageKey }> = {
  sentry: { label: 'skills.resource.sentry', desc: 'skills.resource.sentryDesc' },
  dingtalk: { label: 'skills.resource.dingtalk', desc: 'skills.resource.dingtalkDesc' },
  library: { label: 'skills.resource.library', desc: 'skills.resource.libraryDesc' },
  terminal: { label: 'skills.resource.terminal', desc: 'skills.resource.terminalDesc' },
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

export default function SkillsPage() {
  const { t } = useI18n()
  const sample = useSkillsSample()
  const narrow = useNarrow()
  const [tab, setTab] = useState<SkillTab>('installed')
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(sample ? installedSkills[0].id : null)
  const [mobilePane, setMobilePane] = useState<'list' | 'detail'>('detail')
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState<SkillDialogKind>(null)
  const [workflowOpen, setWorkflowOpen] = useState<Record<string, boolean>>({})
  const [testStatus, setTestStatus] = useState<'idle' | 'running' | 'success' | 'failure'>('idle')
  const dialogTrigger = useRef<HTMLElement | null>(null)
  const testTimer = useRef<number | null>(null)

  const records = sample ? (tab === 'installed' ? installedSkills : tab === 'market' ? marketSkills : draftSkills) : []
  const normalized = query.trim().toLowerCase()
  const filtered = records.filter((skill) =>
    !normalized ||
    `${skill.name} ${skill.description} ${skill.tags.join(' ')}`.toLowerCase().includes(normalized),
  )
  const selected = sample ? [...installedSkills, ...marketSkills, ...draftSkills].find((skill) => skill.id === selectedId) ?? null : null
  const tabs: Array<{ id: SkillTab; label: string }> = [
    { id: 'installed', label: sample ? t('skills.tab.installedCount', { count: installedSkills.length }) : t('skills.tab.installed') },
    { id: 'market', label: t('skills.tab.market') },
    { id: 'drafts', label: sample ? t('skills.tab.draftsCount', { count: draftSkills.length }) : t('skills.tab.drafts') },
  ]

  useEffect(() => {
    setTestStatus('idle')
    setWorkflowOpen({})
  }, [selectedId])

  useEffect(() => () => {
    if (testTimer.current !== null) window.clearTimeout(testTimer.current)
  }, [])

  const announce = (message: string) => setNotice(message)
  const openDialog = (kind: SkillDialogKind, trigger: HTMLElement) => {
    dialogTrigger.current = trigger
    setDialog(kind)
  }
  const closeDialog = () => {
    setDialog(null)
    const trigger = dialogTrigger.current
    window.setTimeout(() => trigger?.focus(), 0)
  }
  const changeTab = (next: SkillTab) => {
    setTab(next)
    const list = next === 'installed' ? installedSkills : next === 'market' ? marketSkills : draftSkills
    setSelectedId(sample && list.length > 0 ? list[0].id : null)
    setQuery('')
  }
  const selectSkill = (skill: SkillRecord) => {
    setSelectedId(skill.id)
    setMobilePane('detail')
  }
  const runTest = () => {
    if (testTimer.current !== null) window.clearTimeout(testTimer.current)
    setTestStatus('running')
    testTimer.current = window.setTimeout(() => setTestStatus('success'), 120)
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

      <div className="skills-layout" data-mobile-pane={narrow ? mobilePane : 'both'} data-testid="skills-layout">
        <section className="skills-list-card" data-testid="skills-list">
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
            <div className="skills-filters">
              <select disabled={!sample} aria-label={t('skills.filter.source')}><option>{t('skills.filter.source')}</option></select>
              <select disabled={!sample} aria-label={t('skills.filter.agent')}><option>{t('skills.filter.agent')}</option></select>
            </div>
            <div className="skills-toolbar-summary">{sample ? t('skills.toolbar.summary', { count: installedSkills.length }) : t('skills.action.unavailable')}</div>
          </div>

          {sample ? (
            filtered.length > 0 ? (
              <div className="skills-rows">
                {filtered.map((skill) => (
                  <SkillRow
                    key={skill.id}
                    skill={skill}
                    active={selected?.id === skill.id}
                    sample={sample}
                    onSelect={() => selectSkill(skill)}
                    onNotice={announce}
                  />
                ))}
              </div>
            ) : (
              <div className="skills-list-empty">{t('skills.listNoMatch')}</div>
            )
          ) : (
            <div className="skills-list-empty" data-testid="skill-empty-list">
              <Icon name="puzzle" size={20} />
              <b>{t('skills.emptyList')}</b>
              <span>{t('skills.emptyListBody')}</span>
            </div>
          )}
        </section>

        <section className="skills-detail-card" data-testid="skill-detail">
          {narrow ? (
            <button type="button" className="skills-back" data-testid="skills-back" onClick={() => setMobilePane('list')}>
              <Icon name="arrowLeft" size={13} />
              {t('skills.detail.backToList')}
            </button>
          ) : null}
          {selected ? (
            <>
              <div className="skills-hero">
                <span className="skills-ico skills-hero-ico">
                  <Icon name={selected.icon} size={20} />
                </span>
                <div className="skills-hero-main">
                  <h2>{selected.name}</h2>
                  <p>{selected.description}</p>
                  <div className="skills-chips">
                    <span className="skills-chip">{t('skills.detail.version', { version: selected.version || t('skills.status.draft') })}</span>
                    <span className="skills-chip">{t(SOURCE_KEYS[selected.source])}</span>
                    {selected.calls !== undefined ? <span className="skills-chip">{t('skills.calls', { count: selected.calls })}</span> : null}
                    {selected.installs !== undefined ? <span className="skills-chip">{t('skills.installs', { count: selected.installs })}</span> : null}
                    <span className="skills-chip">{t('skills.detail.successRate')} · 97%</span>
                  </div>
                </div>
                <div className="skills-hero-actions">
                  <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-save" onClick={() => announce(t('skills.notice.save'))} disabled={!sample}>{t('skills.detail.save')}</button>
                  <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-enable" disabled={!sample} role="switch" aria-checked={selected.enabled ?? false} onClick={() => announce(selected.enabled ? t('skills.notice.disable') : t('skills.notice.enable'))}>{t('skills.detail.enable')}</button>
                </div>
              </div>

              <div className="skills-panel">
                <div className="skills-overview">
                  <div><span>{t('skills.detail.recentRun')}</span><b>{selected.updated ?? '今天 16:02'}</b></div>
                  <div><span>{t('skills.detail.successRate')}</span><b>97%</b></div>
                  {selected.draftNote ? <div><span>{t('skills.status.draft')}</span><b>{selected.draftNote}</b></div> : null}
                </div>

                <section className="skills-section" aria-label={t('skills.workflow.title')}>
                  <div className="skills-section-head"><h3>{t('skills.workflow.title')}</h3><span>{t('skills.workflow.stepCount', { count: workflowSteps.length })}</span></div>
                  <div className="skills-timeline">
                    {workflowSteps.map((step, index) => {
                      const open = Boolean(workflowOpen[step.id])
                      return (
                        <section key={step.id} className={`skills-step${open ? ' is-open' : ''}`} data-testid="skill-workflow-step" aria-expanded={open}>
                          <button type="button" className="skills-step-head" aria-expanded={open} aria-label={t('skills.workflow.toggle')} onClick={() => setWorkflowOpen((current) => ({ ...current, [step.id]: !current[step.id] }))}>
                            <span className="skills-step-mark">{index + 1}</span>
                            <span className="skills-step-main"><b>{step.title}</b><small>{step.description}</small></span>
                            <span className={`skills-run-status is-${step.status}`}>{t(step.status === 'done' ? 'skills.workflow.done' : step.status === 'ready' ? 'skills.workflow.ready' : 'skills.workflow.waiting')}</span>
                            <span className="skills-caret" aria-hidden="true"><Icon name="chevDown" size={12} /></span>
                          </button>
                          {open ? (
                            <div className="skills-step-body">
                              <div>{step.description}</div>
                              <div className="skills-step-tools"><span>{t('skills.workflow.tools')}</span>{step.tools.map((tool) => <span key={tool} className="skills-chip">{tool}</span>)}</div>
                            </div>
                          ) : null}
                        </section>
                      )
                    })}
                  </div>
                </section>

                <section className="skills-section" aria-label={t('skills.trigger.title')}>
                  <div className="skills-section-head"><h3>{t('skills.trigger.title')}</h3></div>
                  <div className="skills-trigger-grid">
                    <div>
                      <div className="skills-label">{t('skills.trigger.conditions')}</div>
                      <ul className="skills-conditions">{triggerConditions.map((condition) => <li key={condition}>{condition}</li>)}</ul>
                      <div className="skills-label">{t('skills.trigger.schedule')}</div>
                      <code className="skills-code">0 18 * * 1-5</code>
                    </div>
                    <div>
                      <div className="skills-label">{t('skills.trigger.resources')}</div>
                      <div className="skills-resource-list">
                        {triggerResources.map((resource) => (
                          <label key={resource.key} className="skills-resource">
                            <input type="checkbox" defaultChecked={resource.enabled} disabled={!sample} onChange={() => announce(t('skills.notice.toggle'))} />
                            <span><b>{t(RESOURCE_KEYS[resource.key].label)}</b><small>{t(RESOURCE_KEYS[resource.key].desc)}</small></span>
                          </label>
                        ))}
                      </div>
                    </div>
                  </div>
                </section>

                <section className="skills-section" aria-label={t('skills.test.title')}>
                  <div className="skills-section-head"><h3>{t('skills.test.title')}</h3><span>{t('skills.test.description')}</span></div>
                  <div className="skills-test-grid">
                    <div>
                      <div className="skills-label">{t('skills.test.input')}</div>
                      <textarea className="skills-test-input" rows={5} defaultValue={testInput} disabled={!sample} aria-label={t('skills.test.input')} />
                      <div className="skills-test-actions">
                        <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-test-run" disabled={!sample} onClick={runTest}>{t('skills.test.run')}</button>
                        <button type="button" className="skills-btn skills-btn-sm" disabled={!sample} onClick={() => setTestStatus('failure')}>{t('skills.test.fail')}</button>
                        <button type="button" className="skills-btn skills-btn-sm skills-btn-primary" data-testid="skill-enable-after-test" disabled={testStatus !== 'success'} onClick={() => announce(t('skills.notice.enable'))}>{t('skills.test.enableAfter')}</button>
                      </div>
                      {testStatus === 'running' ? <div className="skills-test-result is-running" role="status">{t('skills.test.running')}</div> : null}
                      {testStatus === 'success' ? <div className="skills-test-result is-success" data-testid="skill-test-success" role="status"><b>{t('skills.test.success')}</b><span>{testSuccess}</span></div> : null}
                      {testStatus === 'failure' ? <div className="skills-test-result is-failure" data-testid="skill-test-failure" role="alert"><b>{t('skills.test.failure')}</b><span>{testFailure}</span></div> : null}
                    </div>
                    <div>
                      <div className="skills-label">{t('skills.test.checks')}</div>
                      <div className="skills-checks" data-testid="skill-checks">
                        {[
                          ['skills.test.structure', true],
                          ['skills.test.triggerable', true],
                          ['skills.test.dependencies', true],
                          ['skills.test.awaiting', testStatus === 'success'],
                        ].map(([key, ok]) => (
                          <span key={key as string}><i className={ok ? 'is-ok' : ''} aria-hidden="true" />{t(key as MessageKey)}</span>
                        ))}
                      </div>
                    </div>
                  </div>
                  <p className="skills-default-agent">{t('skills.test.defaultAgent')}</p>
                </section>
              </div>
            </>
          ) : (
            <div className="skills-detail-empty" data-testid="skill-empty-detail">
              <Icon name="puzzle" size={22} />
              <h2>{t('skills.emptyDetail')}</h2>
              <p>{t('skills.emptyDetailBody')}</p>
              <div className="skills-empty-actions">
                <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-save" disabled>{t('skills.detail.save')}</button>
                <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-enable" role="switch" aria-checked="false" disabled>{t('skills.detail.enable')}</button>
                <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-test-run" disabled>{t('skills.test.run')}</button>
              </div>
            </div>
          )}
        </section>
      </div>

      <SkillDialogs kind={dialog} onClose={closeDialog} onNotice={announce} />
    </section>
  )
}

function SkillRow({ skill, active, sample, onSelect, onNotice }: { skill: SkillRecord; active: boolean; sample: boolean; onSelect: () => void; onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <div className={`skills-row${active ? ' is-active' : ''}`} data-testid="skill-item">
      <button type="button" className="skills-row-main" aria-pressed={active} onClick={onSelect}>
        <span className="skills-ico"><Icon name={skill.icon} size={15} /></span>
        <span className="skills-row-copy">
          <span className="skills-row-title">{skill.name}{skill.version ? <small>{t('skills.meta', { version: skill.version, source: t(SOURCE_KEYS[skill.source]) })}</small> : <small>{t('skills.status.draft')}</small>}</span>
          <span className="skills-row-desc">{skill.description}</span>
          <span className="skills-row-foot">
            {skill.tags.map((tag) => <span key={tag} className="skills-chip">{tag}</span>)}
            {skill.calls !== undefined ? <span>{t('skills.calls', { count: skill.calls })}</span> : null}
            {skill.updated ? <span>{t('skills.updated', { time: skill.updated })}</span> : null}
            {skill.installs !== undefined ? <span>{t('skills.installs', { count: skill.installs })}</span> : null}
            {skill.draftNote ? <span>{skill.draftNote}</span> : null}
          </span>
        </span>
      </button>
      <div className="skills-row-actions">
        {skill.enabled !== undefined ? (
          <button type="button" className="skills-switch" role="switch" aria-checked={skill.enabled} aria-label={`${skill.name} ${t('skills.detail.enable')}`} disabled={!sample} onClick={() => onNotice(skill.enabled ? t('skills.notice.disable') : t('skills.notice.enable'))}><span /></button>
        ) : null}
        <button type="button" className="skills-more" disabled={!sample} aria-label={t('skills.more')} onClick={() => onNotice(t('skills.notice.edit'))}><Icon name="more" size={14} /></button>
      </div>
    </div>
  )
}
