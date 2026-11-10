import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import {
  testFailure,
  testInput,
  testSuccess,
  triggerConditions,
  triggerResources,
  workflowSteps,
  type SkillRecord,
} from './sampleData'

export type SkillDialogKind = 'builder' | 'import' | null

const RESOURCE_KEYS: Record<string, { label: MessageKey; desc: MessageKey }> = {
  sentry: { label: 'skills.resource.sentry', desc: 'skills.resource.sentryDesc' },
  dingtalk: { label: 'skills.resource.dingtalk', desc: 'skills.resource.dingtalkDesc' },
  library: { label: 'skills.resource.library', desc: 'skills.resource.libraryDesc' },
  terminal: { label: 'skills.resource.terminal', desc: 'skills.resource.terminalDesc' },
}

export default function SkillDialogs({
  kind,
  initial,
  onClose,
  onNotice,
}: {
  kind: SkillDialogKind
  initial: SkillRecord | null
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const testTimer = useRef<number | null>(null)
  const [step, setStep] = useState(0)
  const [intent, setIntent] = useState('')
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [version, setVersion] = useState('')
  const [description, setDescription] = useState('')
  const [trigger, setTrigger] = useState('')
  const [error, setError] = useState('')
  const [workflowOpen, setWorkflowOpen] = useState<Record<string, boolean>>({})
  const [testStatus, setTestStatus] = useState<'idle' | 'running' | 'success' | 'failure'>('idle')
  const [importTab, setImportTab] = useState<'url' | 'file'>('url')
  const [url, setUrl] = useState('')
  const [file, setFile] = useState<File | null>(null)

  useEffect(() => {
    if (!kind) return
    setStep(initial ? 1 : 0)
    setIntent(initial?.description ?? '')
    setName(initial?.name ?? '')
    setSlug(initial?.id ?? '')
    setVersion(initial?.version ?? '')
    setDescription(initial?.description ?? '')
    setTrigger(initial ? `用户要求「${initial.name}」时` : '')
    setError('')
    setWorkflowOpen({})
    setTestStatus('idle')
    setImportTab('url')
    setUrl('')
    setFile(null)
  }, [kind, initial])

  useEffect(() => () => {
    if (testTimer.current !== null) window.clearTimeout(testTimer.current)
  }, [])

  useEffect(() => {
    if (!kind) return
    const root = ref.current
    if (!root) return
    const focusables = () =>
      Array.from(root.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'))
    window.setTimeout(() => focusables()[0]?.focus(), 0)
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab') return
      const items = focusables()
      if (items.length === 0) {
        event.preventDefault()
        return
      }
      const first = items[0]
      const last = items[items.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [kind, onClose])

  if (!kind) return null

  const runTest = () => {
    if (testTimer.current !== null) window.clearTimeout(testTimer.current)
    setTestStatus('running')
    testTimer.current = window.setTimeout(() => setTestStatus('success'), 120)
  }

  const next = () => {
    if (step === 1) {
      if (!name.trim()) {
        setError(t('skills.validation.nameRequired'))
        return
      }
      if (!slug.trim()) {
        setError(t('skills.validation.slugRequired'))
        return
      }
      if (!/^[a-z0-9-]+$/.test(slug)) {
        setError(t('skills.validation.slugInvalid'))
        return
      }
      if (!version.trim()) {
        setError(t('skills.validation.versionRequired'))
        return
      }
      if (!/^v?\d+\.\d+\.\d+$/.test(version.trim())) {
        setError(t('skills.validation.versionInvalid'))
        return
      }
    } else if (step === 2 && !trigger.trim()) {
      setError(t('skills.validation.triggerRequired'))
      return
    }
    setError('')
    setStep((current) => Math.min(3, current + 1))
  }

  const analyzeImport = () => {
    if (!url.trim()) {
      setError(t('skills.validation.urlRequired'))
      return
    }
    if (!/^https?:\/\/\S+$/i.test(url.trim())) {
      setError(t('skills.validation.urlInvalid'))
      return
    }
    setError('')
    onNotice(t('skills.notice.import'))
  }
  const importFile = () => {
    if (!file) {
      setError(t('skills.validation.fileRequired'))
      return
    }
    setError('')
    onNotice(t('skills.notice.import'))
  }

  const builderSteps: Array<{ label: MessageKey; desc: MessageKey }> = [
    { label: 'skills.builder.step.intent', desc: 'skills.builder.step.intentDesc' },
    { label: 'skills.builder.step.basics', desc: 'skills.builder.step.basicsDesc' },
    { label: 'skills.builder.step.triggers', desc: 'skills.builder.step.triggersDesc' },
    { label: 'skills.builder.step.test', desc: 'skills.builder.step.testDesc' },
  ]

  return (
    <div
      className="skills-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.stopPropagation()
          onClose()
        }
      }}
    >
      <div
        ref={ref}
        className={`skills-dialog${kind === 'import' ? ' skills-dialog-wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`skills-dialog-${kind}-title`}
      >
        {kind === 'builder' ? (
          <>
            <div className="skills-dialog-head">
              <div>
                <div className="skills-kicker"><Icon name="puzzle" size={13} />{t('skills.builder.kicker')}</div>
                <h2 id="skills-dialog-builder-title">{t('skills.builder.title')}</h2>
                <p>{t('skills.builder.description')}</p>
              </div>
              <button type="button" className="skills-iconbtn" aria-label={t('skills.import.close')} onClick={onClose}><Icon name="x" size={15} /></button>
            </div>
            <div className="skills-builder-layout">
              <nav className="skills-builder-steps" aria-label={t('skills.builder.title')}>
                {builderSteps.map((entry, index) => (
                  <button key={entry.label} type="button" aria-label={t(entry.label)} className={index === step ? 'is-active' : index < step ? 'is-done' : ''} onClick={() => setStep(index)}>
                    <span>{index + 1}</span><b>{t(entry.label)}</b><small>{t(entry.desc)}</small>
                  </button>
                ))}
              </nav>
              <div className="skills-builder-main">
                {step === 0 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.intentTitle')}</h3>
                    <p className="skills-hint">{t('skills.builder.intentDesc')}</p>
                    <label className="skills-field"><span>{t('skills.builder.intentLabel')}</span><textarea rows={5} value={intent} placeholder={t('skills.builder.intentPlaceholder')} onChange={(event) => setIntent(event.target.value)} /></label>
                  </div>
                ) : null}
                {step === 1 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.step.basics')}</h3>
                    <label className="skills-field"><span>{t('skills.builder.name')}</span><input value={name} onChange={(event) => { setName(event.target.value); setError('') }} /></label>
                    <label className="skills-field"><span>{t('skills.builder.slug')}</span><input className="mono" value={slug} onChange={(event) => { setSlug(event.target.value); setError('') }} /></label>
                    <label className="skills-field"><span>{t('skills.builder.version')}</span><input className="mono" value={version} placeholder="v1.0.0" onChange={(event) => { setVersion(event.target.value); setError('') }} /></label>
                    <label className="skills-field"><span>{t('skills.builder.descriptionLabel')}</span><textarea rows={2} value={description} onChange={(event) => setDescription(event.target.value)} /></label>
                  </div>
                ) : null}
                {step === 2 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.step.triggers')}</h3>
                    <label className="skills-field"><span>{t('skills.builder.semantic')}</span><textarea rows={3} value={trigger} placeholder={t('skills.builder.triggerPlaceholder')} onChange={(event) => { setTrigger(event.target.value); setError('') }} aria-label={t('skills.builder.triggerLabel')} /></label>
                    <label className="skills-field"><span>{t('skills.builder.schedule')}</span><input className="mono" defaultValue="0 18 * * 1-5" /></label>
                    <div className="skills-section-label">{t('skills.builder.workflow')}</div>
                    <div className="skills-timeline">
                      {workflowSteps.map((workflowStep, index) => {
                        const open = Boolean(workflowOpen[workflowStep.id])
                        return (
                          <section key={workflowStep.id} className={`skills-step${open ? ' is-open' : ''}`} data-testid="skill-workflow-step" aria-expanded={open}>
                            <button type="button" className="skills-step-head" aria-expanded={open} aria-label={t('skills.workflow.toggle')} onClick={() => setWorkflowOpen((current) => ({ ...current, [workflowStep.id]: !current[workflowStep.id] }))}>
                              <span className="skills-step-mark">{index + 1}</span>
                              <span className="skills-step-main"><b>{workflowStep.title}</b><small>{workflowStep.description}</small></span>
                              <span className={`skills-run-status is-${workflowStep.status}`}>{t(workflowStep.status === 'done' ? 'skills.workflow.done' : workflowStep.status === 'ready' ? 'skills.workflow.ready' : 'skills.workflow.waiting')}</span>
                              <span className="skills-caret" aria-hidden="true"><Icon name="chevDown" size={12} /></span>
                            </button>
                            {open ? <div className="skills-step-body"><div>{workflowStep.description}</div><div className="skills-step-tools"><span>{t('skills.workflow.tools')}</span>{workflowStep.tools.map((tool) => <span key={tool} className="skills-chip">{tool}</span>)}</div></div> : null}
                          </section>
                        )
                      })}
                    </div>
                    <div className="skills-section-label">{t('skills.trigger.resources')}</div>
                    <div className="skills-resource-list">
                      {triggerResources.map((resource) => (
                        <label key={resource.key} className="skills-resource">
                          <input type="checkbox" defaultChecked={resource.enabled} onChange={() => onNotice(t('skills.notice.toggle'))} />
                          <span><b>{t(RESOURCE_KEYS[resource.key].label)}</b><small>{t(RESOURCE_KEYS[resource.key].desc)}</small></span>
                        </label>
                      ))}
                    </div>
                    <div className="skills-conditions">{triggerConditions.map((condition) => <span key={condition}>{condition}</span>)}</div>
                  </div>
                ) : null}
                {step === 3 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.step.test')}</h3>
                    <label className="skills-field"><span>{t('skills.test.input')}</span><textarea className="skills-test-input" rows={5} defaultValue={testInput} aria-label={t('skills.test.input')} /></label>
                    <div className="skills-test-actions">
                      <button type="button" className="skills-btn skills-btn-sm" data-testid="skill-test-run" onClick={runTest}>{t('skills.test.run')}</button>
                      <button type="button" className="skills-btn skills-btn-sm" onClick={() => setTestStatus('failure')}>{t('skills.test.fail')}</button>
                      <button type="button" className="skills-btn skills-btn-sm skills-btn-primary" data-testid="skill-enable-after-test" disabled={testStatus !== 'success'} onClick={() => onNotice(t('skills.notice.enable'))}>{t('skills.test.enableAfter')}</button>
                    </div>
                    {testStatus === 'running' ? <div className="skills-test-result is-running" role="status">{t('skills.test.running')}</div> : null}
                    {testStatus === 'success' ? <div className="skills-test-result is-success" data-testid="skill-test-success" role="status"><b>{t('skills.test.success')}</b><span>{testSuccess}</span></div> : null}
                    {testStatus === 'failure' ? <div className="skills-test-result is-failure" data-testid="skill-test-failure" role="alert"><b>{t('skills.test.failure')}</b><span>{testFailure}</span></div> : null}
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
                    <p className="skills-default-agent">{t('skills.test.defaultAgent')}</p>
                  </div>
                ) : null}
                {error ? <p className="skills-error" role="alert">{error}</p> : null}
              </div>
            </div>
            <div className="skills-builder-foot">
              <button type="button" className="skills-btn" disabled={step === 0} onClick={() => setStep((current) => Math.max(0, current - 1))}>{t('skills.builder.prev')}</button>
              <span className="skills-progress">{t('skills.builder.progress', { current: step + 1, total: builderSteps.length })}</span>
              <span className="skills-spacer" />
              <button type="button" className="skills-btn" onClick={onClose}>{t('skills.builder.saveDraft')}</button>
              {step < 3 ? (
                <button type="button" className="skills-btn skills-btn-primary" onClick={next}>{t('skills.builder.next')}</button>
              ) : (
                <button type="button" className="skills-btn skills-btn-primary" onClick={() => onNotice(t('skills.notice.publish'))}>{t('skills.builder.publish')}</button>
              )}
            </div>
          </>
        ) : (
          <>
            <div className="skills-dialog-head">
              <div>
                <h2 id="skills-dialog-import-title">{t('skills.import.title')}</h2>
                <p>{t('skills.import.description')}</p>
              </div>
              <button type="button" className="skills-iconbtn" aria-label={t('skills.import.close')} onClick={onClose}><Icon name="x" size={15} /></button>
            </div>
            <div className="skills-dialog-body">
              <div className="skills-tabs" role="tablist" aria-label={t('skills.import.title')}>
                <button type="button" role="tab" aria-selected={importTab === 'url'} className={importTab === 'url' ? 'skills-tab is-active' : 'skills-tab'} onClick={() => { setImportTab('url'); setError('') }}>{t('skills.import.urlTab')}</button>
                <button type="button" role="tab" aria-selected={importTab === 'file'} className={importTab === 'file' ? 'skills-tab is-active' : 'skills-tab'} onClick={() => { setImportTab('file'); setError('') }}>{t('skills.import.fileTab')}</button>
              </div>
              {importTab === 'url' ? (
                <div className="skills-form-stack">
                  <label className="skills-field"><span>{t('skills.import.url')}</span><input className="mono" value={url} placeholder={t('skills.import.urlPlaceholder')} aria-label={t('skills.import.url')} onChange={(event) => { setUrl(event.target.value); setError('') }} /></label>
                  <small className="skills-hint">{t('skills.import.urlHint')}</small>
                  <button type="button" className="skills-btn skills-btn-primary" onClick={analyzeImport}>{t('skills.import.analyze')}</button>
                </div>
              ) : (
                <div className="skills-form-stack">
                  <label className="skills-field"><span>{t('skills.import.file')}</span><input type="file" accept=".md,.zip,.yaml,.yml" aria-label={t('skills.import.file')} onChange={(event) => { setFile(event.target.files?.[0] ?? null); setError('') }} /></label>
                  <small className="skills-hint">{t('skills.import.fileHint')}</small>
                  {file ? <p className="skills-selected-file">{file.name}</p> : null}
                  <button type="button" className="skills-btn skills-btn-primary" onClick={importFile}>{t('skills.import.action')}</button>
                </div>
              )}
              <div className="skills-notice"><Icon name="shield" size={14} /><span>{t('skills.import.notice')}</span></div>
              {error ? <p className="skills-error" role="alert">{error}</p> : null}
            </div>
            <div className="skills-dialog-foot">
              <button type="button" className="skills-btn" onClick={onClose}>{t('skills.import.cancel')}</button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
