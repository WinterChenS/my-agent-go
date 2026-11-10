import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'

export type SkillDialogKind = 'builder' | 'import' | null

export default function SkillDialogs({
  kind,
  onClose,
  onNotice,
}: {
  kind: SkillDialogKind
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)
  const [step, setStep] = useState(0)
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [version, setVersion] = useState('')
  const [description, setDescription] = useState('')
  const [trigger, setTrigger] = useState('')
  const [error, setError] = useState('')
  const [importTab, setImportTab] = useState<'url' | 'file'>('url')
  const [url, setUrl] = useState('')
  const [file, setFile] = useState<File | null>(null)

  useEffect(() => {
    if (!kind) return
    setStep(0)
    setName('')
    setSlug('')
    setVersion('')
    setDescription('')
    setTrigger('')
    setError('')
    setImportTab('url')
    setUrl('')
    setFile(null)
  }, [kind])

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

  const next = () => {
    if (step === 0) {
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
    } else if (step === 1 && !trigger.trim()) {
      setError(t('skills.validation.triggerRequired'))
      return
    }
    setError('')
    setStep((current) => Math.min(2, current + 1))
  }
  const publish = () => {
    onNotice(t('skills.notice.publish'))
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
                  <button key={entry.label} type="button" className={index === step ? 'is-active' : index < step ? 'is-done' : ''} onClick={() => setStep(index)}>
                    <span>{index + 1}</span><b>{t(entry.label)}</b><small>{t(entry.desc)}</small>
                  </button>
                ))}
              </nav>
              <div className="skills-builder-main">
                {step === 0 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.step.basics')}</h3>
                    <label className="skills-field"><span>{t('skills.builder.name')}</span><input value={name} onChange={(event) => { setName(event.target.value); setError('') }} /></label>
                    <label className="skills-field"><span>{t('skills.builder.slug')}</span><input className="mono" value={slug} onChange={(event) => { setSlug(event.target.value); setError('') }} /></label>
                    <label className="skills-field"><span>{t('skills.builder.version')}</span><input className="mono" value={version} placeholder="v1.0.0" onChange={(event) => { setVersion(event.target.value); setError('') }} /></label>
                    <label className="skills-field"><span>{t('skills.builder.descriptionLabel')}</span><textarea rows={2} value={description} onChange={(event) => setDescription(event.target.value)} /></label>
                  </div>
                ) : null}
                {step === 1 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.step.triggers')}</h3>
                    <label className="skills-field"><span>{t('skills.builder.semantic')}</span><textarea rows={3} value={trigger} placeholder={t('skills.builder.triggerPlaceholder')} onChange={(event) => { setTrigger(event.target.value); setError('') }} aria-label={t('skills.builder.triggerLabel')} /></label>
                    <label className="skills-field"><span>{t('skills.builder.schedule')}</span><input className="mono" defaultValue="0 18 * * 1-5" /></label>
                  </div>
                ) : null}
                {step === 2 ? (
                  <div className="skills-form-stack">
                    <h3>{t('skills.builder.step.test')}</h3>
                    <div className="skills-checks">
                      <span><i className="is-ok" />{t('skills.test.structure')}</span>
                      <span><i className="is-ok" />{t('skills.test.triggerable')}</span>
                      <span><i />{t('skills.test.awaiting')}</span>
                    </div>
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
              {step < 2 ? (
                <button type="button" className="skills-btn skills-btn-primary" onClick={next}>{t('skills.builder.next')}</button>
              ) : (
                <button type="button" className="skills-btn skills-btn-primary" onClick={publish}>{t('skills.builder.publish')}</button>
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
