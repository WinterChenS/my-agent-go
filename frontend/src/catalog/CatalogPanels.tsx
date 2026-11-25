import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useI18n } from '../i18n/LanguageProvider'
import SyncButton from '../shell/SyncButton'
import Icon from '../shell/Icon'
import type { MessageKey } from '../i18n/catalogue'
import {
  addCustomModel,
  checkRuntimeUpdate,
  installPiSubagent,
  installRuntime,
  loadCatalog,
  piExtensions,
  piSubagentStatus,
  probeCatalogMcp,
  updatePiSubagent,
  setCatalogModelEnabled,
  setDefaultCatalogModel,
  verifyCatalogModel,
  type CatalogModel,
  type CatalogMcp,
  type CatalogSnapshot,
  type PiExtension,
  type PiSubagentStatus,
  type RuntimeUpdateCheck,
} from './client'
import { byRuntime, modelsFor, syncProblems, type CatalogView } from './useCatalog'
import { onRuntimeInstallLog } from './installLog'
import { CatalogProvidersPanel } from './ProvidersPanel'

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

/**
 * The next step for a status the reader can actually fix.
 *
 * The adapter's own detail is an English sentence about files
 * ("no credential found: ... run `pi login`"), which is accurate and useless to
 * someone looking at a table. These two states have a concrete action, so they
 * get one line in the reader's language; the technical detail stays as the
 * cell's title.
 */
function statusHintKey(status: string): MessageKey | null {
  switch (status) {
    case 'unauthenticated':
      return 'catalog.status.unauthenticatedHint'
    case 'probe_failed':
      return 'catalog.status.probeFailedHint'
    default:
      return null
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
      {view.status === 'ready' ? (
        <span className="cat-sub cat-auto-sync" data-testid="catalog-auto-sync">
          {view.autoSync
            ? t('catalog.inventory.auto', {
                at: view.lastInventorySyncAt
                  ? view.lastInventorySyncAt.slice(11, 19)
                  : t('catalog.inventory.startup'),
              })
            : t('catalog.inventory.autoOff')}
        </span>
      ) : null}
      {view.status === 'ready' && view.snapshot?.scanning ? (
        <span className="cat-sub" data-testid="catalog-scanning">
          {t('catalog.scanning')}
        </span>
      ) : null}
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
export function CatalogRuntimeTable({
  view,
  onNotice,
}: {
  view: CatalogView
  onNotice?: (message: string) => void
}) {
  const { t } = useI18n()
  // Installing returns a fresh snapshot; show it immediately instead of
  // waiting for the shared hook's background re-read.
  const [override, setOverride] = useState<CatalogSnapshot | null>(null)
  const [checks, setChecks] = useState<Record<string, RuntimeUpdateCheck>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [installLog, setInstallLog] = useState<string[]>([])
  const logRef = useRef<HTMLPreElement | null>(null)
  // The npm transcript streams while `installRuntime` is still in flight.
  useEffect(() => {
    if (!busy) return
    return onRuntimeInstallLog(busy, (line) => {
      setInstallLog((lines) => [...lines.slice(-399), line])
    })
  }, [busy])

  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [installLog])

  const snapshot = override ?? view.snapshot
  const runtimes = snapshot?.runtimes ?? []
  if (view.status !== 'ready' && !override) return null
  if (runtimes.length === 0) {
    return <p className="cat-empty" data-testid="catalog-runtimes-empty">{t('catalog.runtimes.empty')}</p>
  }

  const check = async (runtimeId: string) => {
    setBusy(runtimeId)
    try {
      const result = await checkRuntimeUpdate(runtimeId)
      setChecks((current) => ({ ...current, [runtimeId]: result }))
      onNotice?.(
        result.error
          ? t('catalog.runtimes.checkFailed', { message: result.error })
          : result.updateAvailable && result.latest
            ? t('catalog.runtimes.hasUpdate', { version: result.latest })
            : t('catalog.runtimes.upToDate', { version: result.installed ?? result.latest ?? '—' }),
      )
    } catch (error) {
      onNotice?.(t('catalog.runtimes.checkFailed', { message: failureText(error) }))
    } finally {
      setBusy(null)
    }
  }

  const update = async (runtimeId: string, version: string) => {
    setBusy(runtimeId)
    setInstallLog([])
    try {
      const result = await installRuntime(runtimeId)
      if (result.report.output) {
        setInstallLog((lines) => [...lines, ...result.report.output.split(/\r?\n/)])
      }
      if (result.snapshot) setOverride(result.snapshot)
      setChecks((current) => {
        const next = { ...current }
        delete next[runtimeId]
        return next
      })
      onNotice?.(
        result.report.ok
          ? t('catalog.runtimes.updated', { version })
          : t('catalog.runtimes.updateFailed', { message: result.report.output || 'npm failed' }),
      )
    } catch (error) {
      onNotice?.(t('catalog.runtimes.updateFailed', { message: failureText(error) }))
    } finally {
      setBusy(null)
    }
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
            <th>{t('catalog.runtimes.actions')}</th>
          </tr>
        </thead>
        <tbody>
          {runtimes.map((runtime) => {
            const result = checks[runtime.runtimeId]
            const working = busy === runtime.runtimeId
            return (
              <tr key={runtime.id} data-testid={`catalog-runtime-${runtime.runtimeId}`}>
                <td>
                  <span className="cat-name">{runtime.displayName}</span>
                  <span className="cat-sub mono">{runtime.runtimeId}</span>
                </td>
                <td>
                  <span className={`cat-badge ${statusTone(runtime.status)}`} data-status={runtime.status}>
                    <i />{t(runtimeStatusKey(runtime.status))}
                  </span>
                  {statusHintKey(runtime.status) ? (
                    <span
                      className="cat-sub cat-hint"
                      title={runtime.health.detail ?? undefined}
                      data-testid={`catalog-runtime-hint-${runtime.runtimeId}`}
                    >
                      {t(statusHintKey(runtime.status) as MessageKey)}
                    </span>
                  ) : runtime.health.detail ? (
                    <span className="cat-sub" title={runtime.health.detail}>{runtime.health.detail}</span>
                  ) : null}
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
                <td>
                  <div className="cat-row-actions" data-testid={`catalog-runtime-actions-${runtime.runtimeId}`}>
                    {result?.updateAvailable && result.latest ? (
                      <>
                        <span className="cat-sub mono">{t('catalog.runtimes.latest', { version: result.latest })}</span>
                        <button
                          type="button"
                          className="cat-btn cat-btn-primary"
                          data-testid={`catalog-runtime-update-${runtime.runtimeId}`}
                          disabled={working}
                          onClick={() => void update(runtime.runtimeId, result.latest ?? '')}
                        >
                          {working ? t('catalog.runtimes.updating') : t('catalog.runtimes.update')}
                        </button>
                      </>
                    ) : result?.error ? (
                      <span className="cat-sub" title={result.error}>{t('catalog.runtimes.checkFailedShort')}</span>
                    ) : result ? (
                      <span className="cat-sub">{t('catalog.runtimes.upToDateShort')}</span>
                    ) : null}
                    <button
                      type="button"
                      className="cat-btn"
                      data-testid={`catalog-runtime-check-${runtime.runtimeId}`}
                      disabled={working}
                      onClick={() => void check(runtime.runtimeId)}
                    >
                      {working ? t('catalog.runtimes.checking') : t('catalog.runtimes.check')}
                    </button>
                  </div>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {installLog.length > 0 ? (
        <pre
          ref={logRef}
          className={`cat-install-log${busy ? ' is-running' : ''}`}
          data-testid="catalog-runtime-install-log"
          aria-live="polite"
        >
          {installLog.join('\n')}
        </pre>
      ) : null}
    </div>
  )
}
/** Host failures arrive as `{code,message}` JSON; anything else passes through. */
function failureText(error: unknown): string {
  if (typeof error === 'string') {
    const start = error.indexOf('{')
    if (start >= 0) {
      try {
        const parsed = JSON.parse(error.slice(start)) as { code?: string; message?: string }
        if (parsed.message) return parsed.code ? `${parsed.code}: ${parsed.message}` : parsed.message
      } catch {
        // Not the host shape; keep the raw text.
      }
    }
    return error
  }
  if (error instanceof Error) return error.message
  return String(error)
}

/**
 * The runtime-side home for pi's `subagent` extension.
 *
 * The chat page offers the same install when the extension is missing, but the
 * capability belongs to the runtime: someone reading "pi" in 设置 → 运行时
 * should be able to see that delegation is unavailable and fix it there, without
 * having to start a conversation first. Both surfaces call the same host
 * commands, so neither is a copy of the other's state.
 */
export function SubagentExtensionPanel({
  view,
  onNotice,
}: {
  view: CatalogView
  onNotice?: (message: string) => void
}) {
  const { t } = useI18n()
  const runtime = view.snapshot?.runtimes.find((row) => row.runtimeId === 'pi')
  const [status, setStatus] = useState<PiSubagentStatus | null>(null)
  const [checking, setChecking] = useState(false)
  const [busy, setBusy] = useState(false)
  /** Every extension pi loads, not just the subagent one. */
  const [extensions, setExtensions] = useState<PiExtension[] | null>(null)

  const reloadExtensions = useCallback(() => {
    setExtensions(null)
    piExtensions()
      .then((rows) => setExtensions(rows ?? []))
      .catch(() => setExtensions([]))
  }, [])

  const check = useCallback(() => {
    setChecking(true)
    piSubagentStatus()
      .then((next) => setStatus(next && next.extensionDir ? next : null))
      .catch(() => setStatus(null))
      .finally(() => setChecking(false))
    reloadExtensions()
  }, [reloadExtensions])

  // Keyed on the pi build, not the object: a rescan replaces the snapshot object
  // every time, and re-checking on every scan would fight the button's own state.
  const runtimeKey = runtime ? `${runtime.id}:${runtime.version ?? ''}` : ''
  useEffect(() => {
    if (!runtimeKey) return
    check()
  }, [check, runtimeKey])

  if (!runtime) return null

  const installed = status?.installed ?? false
  const install = () => {
    if (busy || !status?.canInstall) return
    setBusy(true)
    installPiSubagent()
      .then((report) => {
        setStatus(report.status)
        onNotice?.(t('catalog.subagent.installedNotice', { count: report.status.agents }))
      })
      .catch((cause) => onNotice?.(t('catalog.subagent.failed', { message: failureText(cause) })))
      .finally(() => setBusy(false))
  }

  // The update path is the install path with a different promise: the sources
  // are replaced, the user's own agent and prompt files are not.
  const update = () => {
    if (busy) return
    setBusy(true)
    updatePiSubagent()
      .then((report) => {
        setStatus(report.status)
        onNotice?.(t('catalog.subagent.updatedNotice', { written: report.written }))
      })
      .catch((cause) => onNotice?.(t('catalog.subagent.failed', { message: failureText(cause) })))
      .finally(() => setBusy(false))
  }

  const stale = installed && (status?.updateAvailable ?? false)
  const badge = checking && !status
    ? t('catalog.subagent.checking')
    : stale
      ? t('catalog.subagent.updateBadge')
      : installed
        ? t('catalog.subagent.installedBadge')
        : t('catalog.subagent.missingBadge')
  const line = stale
    ? t('catalog.subagent.updateLine', {
        agents: status?.agents ?? 0,
        prompts: status?.prompts ?? 0,
      })
    : installed
      ? t('catalog.subagent.installedLine', {
          agents: status?.agents ?? 0,
          prompts: status?.prompts ?? 0,
        })
      : status?.canInstall
        ? t('catalog.subagent.missingLine')
        : t('catalog.subagent.noSource')

  return (
    <div className="cat-subagent-wrap">
      <div className="cat-subagent" data-testid="catalog-subagent">
      <span className="cat-subagent-mark" aria-hidden="true">
        <Icon name="puzzle" size={14} />
      </span>
      <div className="cat-subagent-copy">
        <span className="cat-subagent-title">
          <b>{t('catalog.subagent.title')}</b>
          <span
            className={`cat-badge ${stale ? 'is-warn' : installed ? 'is-success' : checking ? '' : 'is-warn'}`}
            data-status={stale ? 'stale' : installed ? 'installed' : 'missing'}
            data-testid="catalog-subagent-state"
          >
            <i />{badge}
          </span>
        </span>
        <span className="cat-sub">{line}</span>
        {status ? (
          <span className="cat-sub mono cat-subagent-path" title={status.extensionDir}>
            {status.extensionDir}
          </span>
        ) : null}
      </div>
      <div className="cat-subagent-actions">
        <button
          type="button"
          className="cat-btn"
          data-testid="catalog-subagent-recheck"
          disabled={checking || busy}
          title={status?.installedDigest ? `installed ${status.installedDigest}` : undefined}
          onClick={check}
        >
          {checking ? t('catalog.subagent.checkBusy') : t('catalog.subagent.check')}
        </button>
        {stale ? (
          <button
            type="button"
            className="cat-btn cat-btn-primary"
            data-testid="catalog-subagent-update"
            disabled={busy || checking}
            title={t('catalog.subagent.updateHint')}
            onClick={update}
          >
            <Icon name="download" size={12} />
            {busy ? t('catalog.subagent.updating') : t('catalog.subagent.update')}
          </button>
        ) : null}
        {installed ? null : (
          <button
            type="button"
            className="cat-btn cat-btn-primary"
            data-testid="catalog-subagent-install"
            disabled={!status?.canInstall || busy || checking}
            title={status?.source ?? status?.detail}
            onClick={install}
          >
            <Icon name="download" size={12} />
            {busy ? t('catalog.subagent.installing') : t('catalog.subagent.install')}
          </button>
        )}
      </div>
      </div>

      {/* Everything else pi will load. The subagent row answers one question;
          this answers "what did I actually install", which is what someone
          looking at their plugins is asking. */}
      <div className="cat-ext-list" data-testid="catalog-extensions">
        <div className="cat-ext-head">
          <b>{t('catalog.extensions.title')}</b>
          <span className="cat-sub">
            {extensions === null
              ? t('catalog.extensions.loading')
              : t('catalog.extensions.count', {
                  count: extensions.filter((row) => row.active).length,
                })}
          </span>
          <button
            type="button"
            className="cat-btn"
            data-testid="catalog-extensions-refresh"
            disabled={extensions === null}
            onClick={reloadExtensions}
          >
            {t('catalog.subagent.check')}
          </button>
        </div>
        {extensions && extensions.length === 0 ? (
          <span className="cat-sub" data-testid="catalog-extensions-empty">
            {t('catalog.extensions.empty', { dir: status?.agentDir ?? '' })}
          </span>
        ) : null}
        {extensions?.map((row) => (
          <div
            className="cat-ext-row"
            key={`${row.kind}:${row.path}`}
            data-testid="catalog-extension-row"
            data-active={row.active ? 'true' : 'false'}
          >
            <span className={`cat-ext-dot${row.active ? ' is-on' : ''}`} aria-hidden="true" />
            <span className="cat-ext-name">{row.name}</span>
            <span className="cat-badge is-muted">{t(`catalog.extensions.kind.${row.kind}` as MessageKey)}</span>
            <span className="cat-ext-path mono" title={row.entry ?? row.path}>
              {row.path}
            </span>
            {row.detail ? <span className="cat-ext-detail">{row.detail}</span> : null}
          </div>
        ))}
      </div>
    </div>
  )
}

/** Models across runtimes plus the custom-model form. */
export function CatalogModelsPanel({ view }: { view: CatalogView }) {  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [runtimeId, setRuntimeId] = useState('')
  const [modelId, setModelId] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [protocol, setProtocol] = useState('openai')
  const [endpoint, setEndpoint] = useState('')
  const [credential, setCredential] = useState('')
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  /** Runtime whose tab is open; empty means "the default one". */
  const [tab, setTab] = useState('')
  /** Tabs already synced in this mount, so switching back is instant. */
  const synced = useRef<Set<string>>(new Set())

  const runtimes = view.snapshot?.runtimes ?? []
  const customisable = runtimes.filter(
    (runtime) => runtime.capabilities.customModels === 'supported' && runtime.status !== 'not_installed',
  )
  // One tab per installed runtime, `pi` first: it is the runtime the picker
  // defaults to, so it is also the one worth showing when the page opens.
  const tabs = useMemo(() => {
    const installed = runtimes.filter((runtime) => runtime.status !== 'not_installed')
    return [...installed].sort(
      (left, right) => Number(right.runtimeId === 'pi') - Number(left.runtimeId === 'pi'),
    )
  }, [runtimes])
  const active = tabs.find((runtime) => runtime.runtimeId === tab)?.runtimeId ?? tabs[0]?.runtimeId ?? ''
  const models = modelsFor(view.snapshot, active)
  const sorted = useMemo(
    () => [...models].sort((left, right) => Number(right.isDefault) - Number(left.isDefault)),
    [models],
  )

  // Sync the open tab, and only the open tab. A tab that was already read in
  // this mount is not read again until the user asks; a failed read is left
  // unsynced so re-opening (or the button) retries it.
  useEffect(() => {
    if (view.status !== 'ready' || !active || synced.current.has(active)) return
    synced.current.add(active)
    void view.syncRuntime(active)
  }, [active, view])

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
    <>
      {/* Accounts sit above the models: a model list without a credential behind
          it is a list of models nobody can call. */}
      <CatalogProvidersPanel onChanged={() => void view.syncRuntime(active)} />
      <div className="cat-panel" data-testid="catalog-models">
      <div className="cat-panel-head">
        <div>
          <h3>{t('catalog.models.title')}</h3>
          <p className="cat-sub">{t('catalog.models.sub', { count: models.length })}</p>
        </div>
        <SyncButton
          label={t('catalog.models.sync')}
          busy={Boolean(active) && view.syncingRuntime === active}
          disabled={!active || Boolean(view.syncingRuntime)}
          onClick={() => void view.syncRuntime(active)}
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

      {tabs.length > 1 ? (
        <div
          className="cat-tabs"
          role="tablist"
          aria-label={t('catalog.models.runtime')}
          data-testid="catalog-model-tabs"
        >
          {tabs.map((runtime) => (
            <button
              key={runtime.runtimeId}
              type="button"
              role="tab"
              className={`cat-tab${runtime.runtimeId === active ? ' is-active' : ''}`}
              aria-selected={runtime.runtimeId === active}
              data-testid={`catalog-model-tab-${runtime.runtimeId}`}
              onClick={() => setTab(runtime.runtimeId)}
            >
              <span>{runtime.displayName}</span>
              <span className="cat-tab-count">
                {modelsFor(view.snapshot, runtime.runtimeId).length}
              </span>
            </button>
          ))}
        </div>
      ) : null}

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
    </>
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
        <SyncButton
          label={t('catalog.inventory.sync')}
          text={t('catalog.inventory.sync')}
          busy={view.inventorySyncing}
          disabled={view.inventorySyncing}
          onClick={() => void view.syncInventory()}
          testId="catalog-skills-sync"
        />
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

/** How one imported MCP row's last probe reads. */
export function mcpStateKey(state: string): MessageKey {
  switch (state) {
    case 'connected':
      return 'catalog.mcp.state.connected'
    case 'needs_auth':
      return 'catalog.mcp.state.needsAuth'
    case 'failed':
      return 'catalog.mcp.state.failed'
    case 'missing_url':
      return 'catalog.mcp.state.missingUrl'
    case 'missing_command':
      return 'catalog.mcp.state.missingCommand'
    default:
      return 'catalog.mcp.state.unknown'
  }
}

function mcpStateTone(state: string): string {
  switch (state) {
    case 'connected':
      return 'is-success'
    // Not a failure: the server refused a visitor who has no session, and the
    // session it wants lives in the runtime.
    case 'needs_auth':
      return 'is-warn'
    case 'failed':
      return 'is-danger'
    default:
      return ''
  }
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
          : result.state === 'needs_auth'
            ? t('catalog.mcp.probeNeedsAuth', { name: server.name })
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
        <SyncButton
          label={t('catalog.inventory.sync')}
          text={t('catalog.inventory.sync')}
          busy={view.inventorySyncing}
          disabled={view.inventorySyncing}
          onClick={() => void view.syncInventory()}
          testId="catalog-mcp-sync"
        />
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
                      <span
                        className={`cat-badge ${mcpStateTone(server.state)}`}
                        data-state={server.state}
                      >
                        <i />{t(mcpStateKey(server.state))}
                      </span>
                    </td>
                    <td>
                      <span className="cat-sub">
                        {server.envKeys.length + server.headerKeys.length > 0
                          ? t('catalog.mcp.secretKeys', { count: server.envKeys.length + server.headerKeys.length })
                          : server.transport === 'stdio'
                            ? t('catalog.mcp.noSecrets')
                            : t('catalog.mcp.oauthCredential')}
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
