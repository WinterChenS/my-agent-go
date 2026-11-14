// Settings → Agent, live mode.
//
// An agent is the name for a runtime+model pair. The prototype page showed
// five tabs of sample data and no way to bind anything, so "the machine has an
// agent" was a claim the app could not back up. This page is the one place the
// binding is created and edited, and it is deliberately small: list, edit,
// remove. The prompt, tools and permission tabs stay prototype-only until their
// backends exist.

import { useMemo, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import {
  deleteCatalogAgent,
  saveCatalogAgent,
  type CatalogAgent,
  type CatalogModel,
  type CatalogRuntime,
} from '../../catalog/client'
import { useCatalog } from '../../catalog/useCatalog'
import Icon from '../../shell/Icon'
import { ROUTES_BY_ID } from '../../shell/routes'
import './agents.css'

interface Draft {
  id?: string
  name: string
  runtimeId: string
  modelId: string
}


export default function AgentsLive() {
  const { t } = useI18n()
  const catalog = useCatalog(true)
  const snapshot = catalog.snapshot
  const [draft, setDraft] = useState<Draft | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const agents = snapshot?.agents ?? []
  // A runtime is offered when discovery registered it; its `status` can still
  // be `orphaned` while an adapter is missing, so the status alone would hide
  // runtimes the user does have.
  const runtimes = useMemo<CatalogRuntime[]>(
    () => (snapshot?.runtimes ?? []).filter((runtime) => runtime.status !== 'not_installed'),
    [snapshot],
  )
  const modelsFor = (runtimeId: string): CatalogModel[] =>
    (snapshot?.models ?? []).filter((model) => model.runtimeId === runtimeId)

  const begin = (agent?: CatalogAgent) => {
    setError(null)
    setNotice('')
    setConfirming(null)
    if (agent) {
      setDraft({
        id: agent.id,
        name: agent.name,
        runtimeId: agent.runtimeId,
        modelId: agent.modelId,
      })
      return
    }
    const runtime = runtimes[0]
    setDraft({
      name: '',
      runtimeId: runtime?.runtimeId ?? '',
      modelId: runtime ? (modelsFor(runtime.runtimeId)[0]?.modelId ?? '') : '',
    })
  }

  const save = async () => {
    if (!draft) return
    if (!draft.name.trim()) {
      setError(t('agents.live.nameRequired'))
      return
    }
    if (!draft.runtimeId || !draft.modelId) {
      setError(t('agents.live.pickRuntime'))
      return
    }
    setSaving(true)
    setError(null)
    try {
      const next = await saveCatalogAgent({
        id: draft.id,
        name: draft.name.trim(),
        runtimeId: draft.runtimeId,
        modelId: draft.modelId,
      })
      catalog.apply(next)
      setDraft(null)
      setNotice(t('agents.live.saved'))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (agent: CatalogAgent) => {
    if (confirming !== agent.id) {
      setConfirming(agent.id)
      return
    }
    setConfirming(null)
    setSaving(true)
    setError(null)
    try {
      const next = await deleteCatalogAgent(agent.id)
      catalog.apply(next)
      setNotice(t('agents.live.deleted'))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  const draftModels = draft ? modelsFor(draft.runtimeId) : []
  const modelKnown = draft ? draftModels.some((model) => model.modelId === draft.modelId) : true

  return (
    <section className="agents-live" data-testid="agents-page" data-mode="live">
      <span className="sr-only" id="route-marker" data-testid="page-marker">
        {ROUTES_BY_ID.get('agents')?.marker}
      </span>
      <header className="agents-live-head">
        <div>
          <h1>{t('agents.live.title')}</h1>
          <p>{t('agents.live.subtitle')}</p>
        </div>
        <button type="button" className="agents-live-primary" onClick={() => begin()} disabled={saving || runtimes.length === 0}>
          <Icon name="plus" size={13} />
          <span>{t('agents.live.new')}</span>
        </button>
      </header>

      {notice ? (
        <p className="agents-live-notice" role="status" data-testid="agents-live-notice">
          {notice}
        </p>
      ) : null}
      {error ? (
        <p className="agents-live-notice is-error" role="alert" data-testid="agents-live-error">
          {error}
        </p>
      ) : null}

      {catalog.status === 'unavailable' ? (
        <p className="agents-live-empty">{t('agents.live.unavailable')}</p>
      ) : catalog.status === 'loading' && !snapshot ? (
        <p className="agents-live-empty">{t('agents.live.loading')}</p>
      ) : runtimes.length === 0 ? (
        <p className="agents-live-empty">{t('agents.live.noRuntime')}</p>
      ) : (
        <ul className="agents-live-list" data-testid="agents-live-list">
          {agents.length === 0 ? (
            <li className="agents-live-empty">{t('agents.live.empty')}</li>
          ) : (
            agents.map((agent) => {
              const model = (snapshot?.models ?? []).find(
                (row) => row.runtimeId === agent.runtimeId && row.modelId === agent.modelId,
              )
              return (
                <li key={agent.id} className="agents-live-card" data-testid={`agents-live-row-${agent.id}`}>
                  <div className="agents-live-card-main">
                    <div className="agents-live-card-title">
                      <b>{agent.isDefault ? t('agents.live.default') : agent.name}</b>
                      {agent.isDefault ? (
                        <span className="agents-live-badge">{t('agents.live.builtIn')}</span>
                      ) : null}
                      {model?.enabled ? (
                        <span className="agents-live-badge is-on">{t('agents.live.enabled')}</span>
                      ) : (
                        <span className="agents-live-badge">{t('agents.live.notEnabled')}</span>
                      )}
                    </div>
                    <div className="agents-live-binding">
                      <span className="agents-live-chip">
                        <Icon name="terminal" size={11} />
                        {agent.runtimeId}
                      </span>
                      <span className="agents-live-chip">
                        <Icon name="sparkle" size={11} />
                        {agent.modelId}
                      </span>
                      {!model ? <span className="agents-live-warn">{t('agents.live.modelMissing')}</span> : null}
                    </div>
                  </div>
                  <div className="agents-live-card-actions">
                    <button type="button" className="agents-live-ghost" onClick={() => begin(agent)} disabled={saving}>
                      {t('agents.live.edit')}
                    </button>
                    {agent.isDefault ? null : (
                      <button
                        type="button"
                        className={`agents-live-ghost${confirming === agent.id ? ' is-danger' : ''}`}
                        onClick={() => void remove(agent)}
                        disabled={saving}
                      >
                        {confirming === agent.id ? t('agents.live.deleteConfirm') : t('agents.live.delete')}
                      </button>
                    )}
                  </div>
                </li>
              )
            })
          )}
        </ul>
      )}

      {draft ? (
        <div className="agents-live-editor" role="dialog" aria-modal="false" aria-label={t('agents.live.new')}>
          <div className="agents-live-field">
            <label htmlFor="agents-live-name">{t('agents.live.name')}</label>
            <input
              id="agents-live-name"
              autoFocus
              type="text"
              value={draft.name}
              placeholder={t('agents.live.namePlaceholder')}
              onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            />
          </div>
          <div className="agents-live-field">
            <label htmlFor="agents-live-runtime">{t('agents.live.runtime')}</label>
            <select
              id="agents-live-runtime"
              value={draft.runtimeId}
              onChange={(event) => {
                const runtimeId = event.target.value
                const first = modelsFor(runtimeId)[0]?.modelId ?? ''
                setDraft({ ...draft, runtimeId, modelId: first })
              }}
            >
              <option value="">{t('agents.live.pickRuntime')}</option>
              {runtimes.map((runtime) => (
                <option key={runtime.runtimeId} value={runtime.runtimeId}>
                  {runtime.displayName || runtime.runtimeId} · {runtime.runtimeId}
                </option>
              ))}
            </select>
          </div>
          <div className="agents-live-field">
            <label htmlFor="agents-live-model">{t('agents.live.model')}</label>
            <select
              id="agents-live-model"
              value={draft.modelId}
              onChange={(event) => setDraft({ ...draft, modelId: event.target.value })}
              disabled={!draft.runtimeId}
            >
              <option value="">{t('agents.live.pickModel')}</option>
              {draftModels.map((model) => (
                <option key={model.modelId} value={model.modelId}>
                  {model.modelId}
                  {model.enabled ? '' : ` · ${t('agents.live.modelDisabled')}`}
                </option>
              ))}
            </select>
            {draft.modelId && !modelKnown ? (
              <small className="agents-live-warn">{t('agents.live.modelMissing')}</small>
            ) : null}
            {draft.id === 'default' ? (
              <small className="agents-live-hint">{t('agents.live.defaultHint')}</small>
            ) : null}
          </div>
          <div className="agents-live-editor-actions">
            <button type="button" className="agents-live-ghost" onClick={() => setDraft(null)} disabled={saving}>
              {t('agents.live.cancel')}
            </button>
            <button type="button" className="agents-live-primary" onClick={() => void save()} disabled={saving}>
              {t('agents.live.save')}
            </button>
          </div>
        </div>
      ) : null}
    </section>
  )
}
