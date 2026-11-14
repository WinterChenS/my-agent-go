import { useMemo, useState } from 'react'
import { useI18n } from '../i18n/LanguageProvider'
import SyncButton from '../shell/SyncButton'
import type { MessageKey } from '../i18n/catalogue'
import {
  addCustomModel,
  loadCatalog,
  probeCatalogMcp,
  setCatalogModelEnabled,
  setDefaultCatalogModel,
  verifyCatalogModel,
  type CatalogModel,
  type CatalogMcp,
} from './client'
import { byRuntime, modelsFor, syncProblems, type CatalogView } from './useCatalog'

/** Human status label for a runtime row. */
export function runtimeStatusKey(status: string): MessageKey {
  switch (status) {
    case 'available':
      return 'catalog.status.available'
    case 'detected':
      return 'catalog.status.detected'
    case 'version_unsupported':
      return 'catalog.status.versionUnsupported'
    case 'unauthenticated':
      return 'catalog.status.unauthenticated'
    case 'probe_failed':
      return 'catalog.status.probeFailed'
    case 'adapter_failed':
      return 'catalog.status.adapterFailed'
    case 'orphaned':
      return 'catalog.status.orphaned'
    default:
      return 'catalog.status.notInstalled'
  }
}

function statusTone(status: string): string {
  switch (status) {
    case 'available':
      return 'is-success'
    case 'unauthenticated':
    case 'version_unsupported':
      return 'is-warn'
    case 'probe_failed':
    case 'adapter_failed':
      return 'is-danger'
    default:
      return ''
  }
}

/** Scan state line with the refresh control. */
export function CatalogSummary({ view }: { view: CatalogView }) {
  const { t } = useI18n()
  const problems = syncProblems(view.snapshot)
  const scannedAt = view.snapshot?.scannedAt ?? ''
  return (
    <div className="cat-summary" data-testid="catalog-summary">
      <span className={`cat-dot ${view.status === 'ready' ? 'is-live' : ''}`} aria-hidden="true" />
      <span className="cat-summary-text">
        {view.status === 'loading' && t('catalog.loading')}
        {view.status === 'unavailable' && t('catalog.unavailable')}
        {view.status === 'error' && t('catalog.error', { message: view.error ?? '' })}
        {view.status === 'ready' && t('catalog.scannedAt', { at: scannedAt })}
        {view.status === 'idle' && t('catalog.loading')}
      </span>
      {problems.length > 0 && (
        <span className="cat-warn" title={problems.join('\n')} data-testid="catalog-problems">
          {t('catalog.problems', { count: problems.length })}
        </span>
      )}
      <SyncButton
        label={t('catalog.refresh')}
        text={t('catalog.refresh')}
        busy={view.refreshing}
        disabled={view.status === 'unavailable'}
        onClick={() => void view.refresh()}
        testId="catalog-sync"
      />
    </div>
  )
}

/** Runtime rows: status, version, install path, config and snapshot state. */
export function CatalogRuntimeTable({ view }: { view: CatalogView }) {
  const { t } = useI18n()
  const runtimes = view.snapshot?.runtimes ?? []
  if (view.status !== 'ready') return null
  if (runtimes.length === 0) {
    return <p className="cat-empty" data-testid="catalog-runtimes-empty">{t('catalog.runtimes.empty')}</p>
  }
  return (
    <div className="cat-table-wrap">
      <table className="cat-table" aria-label={t('catalog.runtimes.title')} data-testid="catalog-runtimes">
        <thead>
          <tr>
            <th>{t('catalog.runtimes.runtime')}</th>
            <th>{t('catalog.runtimes.status')}</th>
            <th>{t('catalog.runtimes.version')}</th>
            <th>{t('catalog.runtimes.path')}</th>
            <th>{t('catalog.runtimes.snapshot')}</th>
          </tr>
        </thead>
        <tbody>
          {runtimes.map((runtime) => (
            <tr key={runtime.id} data-testid={`catalog-runtime-${runtime.runtimeId}`}>
              <td>
                <span className="cat-name">{runtime.displayName}</span>
                <span className="cat-sub mono">{runtime.runtimeId}</span>
              </td>
              <td>
                <span className={`cat-badge ${statusTone(runtime.status)}`} data-status={runtime.status}>
                  <i />{t(runtimeStatusKey(runtime.status))}
                </span>
                {runtime.health.detail && (
                  <span className="cat-sub" title={runtime.health.detail}>{runtime.health.detail}</span>
                )}
              </td>
              <td className="mono">{runtime.version ?? '—'}</td>
              <td className="cat-path mono" title={runtime.installPath}>
                {runtime.installPath || '—'}
              </td>
              <td>
                <span className="cat-sub">
                  {runtime.snapshotState === 'fresh' && t('catalog.snapshot.fresh')}
                  {runtime.snapshotState === 'stale' && t('catalog.snapshot.stale')}
                  {runtime.snapshotState === 'never' && t('catalog.snapshot.never')}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Models across runtimes plus the custom-model form. */
export function CatalogModelsPanel({ view }: { view: CatalogView }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [runtimeId, setRuntimeId] = useState('')
  const [modelId, setModelId] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [protocol, setProtocol] = useState('openai')
  const [endpoint, setEndpoint] = useState('')
  const [credential, setCredential] = useState('')
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')

  const runtimes = view.snapshot?.runtimes ?? []
  const customisable = runtimes.filter(
    (runtime) => runtime.capabilities.customModels === 'supported' && runtime.status !== 'not_installed',
  )
  const models = modelsFor(view.snapshot)
  const sorted = useMemo(
    () => [...models].sort((left, right) => Number(right.isDefault) - Number(left.isDefault)),
    [models],
  )

  if (view.status !== 'ready') return null

  const submit = async () => {
    setBusy('add')
    setNotice('')
    try {
      const snapshot = await addCustomModel({
        runtimeId: runtimeId || customisable[0]?.runtimeId || '',
        modelId: modelId.trim(),
        displayName: displayName.trim() || modelId.trim(),
        protocol: protocol.trim() || undefined,
        endpoint: endpoint.trim() || undefined,
        credential: credential.trim() || undefined,
      })
      view.apply(snapshot)
      setNotice(t('catalog.models.added'))
      setOpen(false)
      setModelId('')
      setDisplayName('')
      setEndpoint('')
      setCredential('')
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  const verify = async (model: CatalogModel) => {
    setBusy(`verify:${model.runtimeId}:${model.modelId}`)
    setNotice('')
    try {
      const result = await verifyCatalogModel(model.runtimeId, model.modelId)
      const refreshed = await probeRefresh(view)
      if (refreshed) view.apply(refreshed)
      setNotice(
        result.ok
          ? t('catalog.models.verifiedOk', { model: model.modelId })
          : t('catalog.models.verifyFailedDetail', { detail: result.detail ?? '' }),
      )
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  const makeDefault = async (model: CatalogModel) => {
    setBusy(`default:${model.runtimeId}:${model.modelId}`)
    setNotice('')
    try {
      const snapshot = await setDefaultCatalogModel(model.runtimeId, model.modelId)
      view.apply(snapshot)
      setNotice(t('catalog.models.defaultSet', { model: model.modelId }))
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  const toggleEnabled = async (model: CatalogModel) => {
    setBusy(`enable:${model.runtimeId}:${model.modelId}`)
    setNotice('')
    try {
      const snapshot = await setCatalogModelEnabled(
        model.runtimeId,
        model.modelId,
        !model.enabled,
      )
      view.apply(snapshot)
      setNotice(
        model.enabled
          ? t('catalog.models.disabledNotice', { model: model.modelId })
          : t('catalog.models.enabledNotice', { model: model.modelId }),
      )
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="cat-panel" data-testid="catalog-models">
      <div className="cat-panel-head">
        <div>
          <h3>{t('catalog.models.title')}</h3>
          <p className="cat-sub">{t('catalog.models.sub', { count: models.length })}</p>
        </div>
        <SyncButton
          label={t('catalog.models.sync')}
          busy={view.refreshing}
          onClick={() => void view.refresh()}
          testId="catalog-models-sync"
        />
        <button
          type="button"
          className="cat-btn cat-btn-primary"
          disabled={customisable.length === 0}
          title={customisable.length === 0 ? t('catalog.models.noCustomRuntime') : undefined}
          onClick={() => setOpen((value) => !value)}
        >
          {t('catalog.models.add')}
        </button>
      </div>

      {open && (
        <div className="cat-form" data-testid="catalog-model-form">
          <label>
            <span>{t('catalog.models.runtime')}</span>
            <select
              value={runtimeId || customisable[0]?.runtimeId || ''}
              aria-label={t('catalog.models.runtime')}
              onChange={(event) => setRuntimeId(event.target.value)}
            >
              {customisable.map((runtime) => (
                <option key={runtime.runtimeId} value={runtime.runtimeId}>{runtime.displayName}</option>
              ))}
            </select>
          </label>
          <label>
            <span>{t('catalog.models.modelId')}</span>
            <input
              value={modelId}
              aria-label={t('catalog.models.modelId')}
              onChange={(event) => setModelId(event.target.value)}
              placeholder="gpt-6.1-sol"
            />
          </label>
          <label>
            <span>{t('catalog.models.displayName')}</span>
            <input
              value={displayName}
              aria-label={t('catalog.models.displayName')}
              onChange={(event) => setDisplayName(event.target.value)}
              placeholder={t('catalog.models.displayPlaceholder')}
            />
          </label>
          <label>
            <span>{t('catalog.models.protocol')}</span>
            <input
              value={protocol}
              aria-label={t('catalog.models.protocol')}
              onChange={(event) => setProtocol(event.target.value)}
              placeholder="openai"
            />
          </label>
          <label>
            <span>{t('catalog.models.endpoint')}</span>
            <input
              value={endpoint}
              aria-label={t('catalog.models.endpoint')}
              onChange={(event) => setEndpoint(event.target.value)}
              placeholder="https://api.example.com/v1"
            />
          </label>
          <label>
            <span>{t('catalog.models.credential')}</span>
            <input
              type="password"
              value={credential}
              onChange={(event) => setCredential(event.target.value)}
              placeholder="sk-…"
              aria-label={t('catalog.models.credential')}
              autoComplete="off"
            />
            <small className="cat-sub">{t('catalog.models.credentialHint')}</small>
          </label>
          <div className="cat-form-actions">
            <button type="button" className="cat-btn" onClick={() => setOpen(false)}>{t('catalog.models.cancel')}</button>
            <button
              type="button"
              className="cat-btn cat-btn-primary"
              disabled={busy === 'add' || modelId.trim().length === 0}
              onClick={() => void submit()}
            >
              {busy === 'add' ? t('catalog.models.saving') : t('catalog.models.submit')}
            </button>
          </div>
        </div>
      )}

      {notice && <p className="cat-notice" role="status">{notice}</p>}

      {sorted.length === 0 ? (
        <p className="cat-empty">{t('catalog.models.empty')}</p>
      ) : (
        <div className="cat-table-wrap">
          <table className="cat-table" aria-label={t('catalog.models.title')}>
            <thead>
              <tr>
                <th>{t('catalog.models.model')}</th>
                <th>{t('catalog.models.runtime')}</th>
                <th>{t('catalog.models.origin')}</th>
                <th>{t('catalog.models.state')}</th>
                <th className="cat-num">{t('catalog.models.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((model) => {
                const key = `${model.runtimeId}:${model.modelId}`
                return (
                  <tr key={key} data-testid={`catalog-model-${key}`}>
                    <td>
                      <span className="cat-name mono">{model.modelId}</span>
                      {model.displayName !== model.modelId && <span className="cat-sub">{model.displayName}</span>}
                    </td>
                    <td className="mono">{model.runtimeId}</td>
                    <td>
                      <span className="cat-sub">
                        {model.origin === 'custom' ? t('catalog.models.custom') : t('catalog.models.discovered')}
                      </span>
                      {model.endpoint && <span className="cat-sub mono" title={model.endpoint}>{model.endpoint}</span>}
                    </td>
                    <td>
                      {model.isDefault ? (
                        <span className="cat-badge is-accent"><i />{t('catalog.models.default')}</span>
                      ) : model.verified ? (
                        <span className="cat-badge is-success"><i />{t('catalog.models.verified')}</span>
                      ) : (
                        <span className="cat-badge"><i />{t('catalog.models.unverified')}</span>
                      )}
                      {model.enabled && (
                        <span className="cat-badge is-success"><i />{t('catalog.models.inPicker')}</span>
                      )}
                      {model.hasCredential && <span className="cat-sub">{t('catalog.models.hasCredential')}</span>}
                    </td>
                    <td className="cat-num">
                      <button
                        type="button"
                        className="cat-btn"
                        data-testid={`catalog-enable-${key}`}
                        aria-pressed={model.enabled}
                        disabled={busy.startsWith('enable:')}
                        onClick={() => void toggleEnabled(model)}
                      >
                        {model.enabled ? t('catalog.models.disable') : t('catalog.models.enable')}
                      </button>
                      <button
                        type="button"
                        className="cat-btn"
                        disabled={busy.startsWith('verify:')}
                        onClick={() => void verify(model)}
                      >
                        {busy === `verify:${key}` ? t('catalog.models.verifying') : t('catalog.models.verify')}
                      </button>
                      <button
                        type="button"
                        className="cat-btn"
                        disabled={!model.verified || model.isDefault || busy.startsWith('default:')}
                        onClick={() => void makeDefault(model)}
                      >
                        {t('catalog.models.makeDefault')}
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

async function probeRefresh(_view: CatalogView) {
  // Verification flips a column the snapshot carries, so re-read it instead of
  // patching the client copy.
  try {
    return await loadCatalog()
  } catch {
    return null
  }
}

/** Skill inventory summary with problem rows. */
export function CatalogSkillsPanel({ view }: { view: CatalogView }) {
  const { t } = useI18n()
  const skills = view.snapshot?.skills ?? []
  if (view.status !== 'ready') return null
  const grouped = byRuntime(skills)
  const broken = skills.filter((skill) => skill.state !== 'valid')
  return (
    <div className="cat-panel" data-testid="catalog-skills">
      <div className="cat-panel-head">
        <div>
          <h3>{t('catalog.skills.title')}</h3>
          <p className="cat-sub">{t('catalog.skills.sub', { count: skills.length, runtimes: grouped.size })}</p>
        </div>
      </div>
      {skills.length === 0 ? (
        <p className="cat-empty">{t('catalog.skills.empty')}</p>
      ) : (
        <>
          <ul className="cat-chips">
            {[...grouped.entries()].map(([runtimeId, items]) => (
              <li key={runtimeId}>
                <span className="cat-chip">
                  <b className="mono">{runtimeId}</b>
                  <span>{t('catalog.skills.count', { count: items.length })}</span>
                </span>
              </li>
            ))}
          </ul>
          {broken.length > 0 && (
            <ul className="cat-problems" data-testid="catalog-skill-problems">
              {broken.slice(0, 6).map((skill) => (
                <li key={`${skill.runtimeId}:${skill.path}`}>
                  <span className={`cat-badge ${skill.state === 'linked' ? 'is-warn' : 'is-danger'}`}><i />{skill.state}</span>
                  <span className="mono">{skill.name}</span>
                  <span className="cat-sub">{skill.reason ?? skill.path}</span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}

/** Imported MCP declarations with the explicit probe. */
export function CatalogMcpPanel({ view }: { view: CatalogView }) {
  const { t } = useI18n()
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const servers = view.snapshot?.mcp ?? []
  if (view.status !== 'ready') return null

  const probe = async (server: CatalogMcp) => {
    const key = `${server.runtimeId}:${server.name}`
    setBusy(key)
    setNotice('')
    try {
      const result = await probeCatalogMcp(server.runtimeId, server.name)
      const refreshed = await probeRefresh(view)
      if (refreshed) view.apply(refreshed)
      setNotice(
        result.ok
          ? t('catalog.mcp.probeOk', { name: server.name, count: result.tools.length })
          : t('catalog.mcp.probeFailed', { name: server.name, detail: result.detail ?? '' }),
      )
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="cat-panel" data-testid="catalog-mcp">
      <div className="cat-panel-head">
        <div>
          <h3>{t('catalog.mcp.title')}</h3>
          <p className="cat-sub">{t('catalog.mcp.sub', { count: servers.length })}</p>
        </div>
      </div>
      {notice && <p className="cat-notice" role="status">{notice}</p>}
      {servers.length === 0 ? (
        <p className="cat-empty">{t('catalog.mcp.empty')}</p>
      ) : (
        <div className="cat-table-wrap">
          <table className="cat-table" aria-label={t('catalog.mcp.title')}>
            <thead>
              <tr>
                <th>{t('catalog.mcp.name')}</th>
                <th>{t('catalog.mcp.transport')}</th>
                <th>{t('catalog.mcp.source')}</th>
                <th>{t('catalog.mcp.secrets')}</th>
                <th className="cat-num">{t('catalog.mcp.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {servers.map((server) => {
                const key = `${server.runtimeId}:${server.name}`
                return (
                  <tr key={key} data-testid={`catalog-mcp-${key}`}>
                    <td>
                      <span className="cat-name mono">{server.name}</span>
                      <span className="cat-sub mono" title={server.command ?? server.url ?? ''}>
                        {server.command ?? server.url ?? '—'}
                      </span>
                    </td>
                    <td>{server.transport}</td>
                    <td>
                      <span className="cat-sub mono">{server.runtimeId}</span>
                      <span className="cat-badge" data-state={server.state}><i />{server.state}</span>
                    </td>
                    <td>
                      <span className="cat-sub">
                        {server.envKeys.length + server.headerKeys.length === 0
                          ? t('catalog.mcp.noSecrets')
                          : t('catalog.mcp.secretKeys', { count: server.envKeys.length + server.headerKeys.length })}
                      </span>
                    </td>
                    <td className="cat-num">
                      <button
                        type="button"
                        className="cat-btn"
                        disabled={busy === key}
                        onClick={() => void probe(server)}
                      >
                        {busy === key ? t('catalog.mcp.probing') : t('catalog.mcp.probe')}
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
