// First-run setup: choose which runtimes to turn on, and one model per runtime.
//
// Why a checklist instead of "everything that was detected": discovery reports
// every runtime and every model it can reach. Handing all of that to the chat
// picker is noise, and a first launch should not silently switch on three
// runtimes the user never asked for. pi is the default choice because it is the
// reference adapter and the one this build ships extensions for; the rest are
// listed unchecked and can be turned on here or later in settings.
//
// The wizard is only shown when the catalog says nothing is configured yet. A
// completed setup leaves a default/enabled model in the catalog, which lives in
// the data directory — so it survives a WebView storage reset, not just the
// localStorage flag.
//
// The host can auto-install pi's `subagent` extension, but not pi/codex itself;
// when nothing is available we say so honestly and offer a re-detect instead of
// pretending a button can install a whole CLI.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useCatalog } from '../catalog/useCatalog'
import {
  installPiSubagent,
  installRuntime,
  piSubagentStatus,
  setCatalogModelEnabled,
  setDefaultCatalogModel,
  type CatalogModel,
  type CatalogRuntime,
  type CatalogSnapshot,
  type PiSubagentStatus,
} from '../catalog/client'
import { writeCachedSnapshot } from '../catalog/cache'
import { notifyCatalogChanged } from '../catalog/notify'
import { PREF_ONBOARDING_DONE, readBoolPreference, writeBoolPreference } from '../preferences/flags'
import { isTauriHost } from '../host/tauri'

/** Runtimes shown first, in this order; anything else is appended. */
const PREFERRED_ORDER = ['pi', 'codex', 'opencode']
/** The one that is on by default, when it is actually installed. */
const DEFAULT_RUNTIME = 'pi'

const overlayStyle: React.CSSProperties = {
  position: 'fixed',
  inset: 0,
  background: 'rgba(8, 12, 22, 0.55)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  zIndex: 9000,
}
const cardStyle: React.CSSProperties = {
  width: 560,
  maxWidth: '92vw',
  maxHeight: '86vh',
  overflow: 'auto',
  background: 'var(--surface, #ffffff)',
  color: 'var(--text, #1f2430)',
  borderRadius: 16,
  padding: 26,
  boxShadow: '0 24px 80px rgba(0, 0, 0, 0.35)',
  border: '1px solid var(--border, rgba(127,127,127,.2))',
}
const buttonBase: React.CSSProperties = {
  border: '1px solid var(--border, rgba(127,127,127,.3))',
  background: 'var(--button-bg, #2563eb)',
  color: '#fff',
  borderRadius: 8,
  padding: '9px 18px',
  cursor: 'pointer',
  fontSize: 13,
  fontWeight: 600,
}
const ghostButton: React.CSSProperties = {
  ...buttonBase,
  background: 'transparent',
  color: 'var(--text, #1f2430)',
}
const sectionTitle: React.CSSProperties = { fontSize: 12, fontWeight: 700, marginBottom: 8 }
const selectStyle: React.CSSProperties = {
  width: '100%',
  padding: '8px 10px',
  borderRadius: 8,
  border: '1px solid var(--border, rgba(127,127,127,.3))',
  background: 'var(--input-bg, transparent)',
  color: 'var(--text, inherit)',
  fontSize: 13,
}

const modelKey = (model: CatalogModel) => `${model.runtimeId}|${model.modelId}`
const modelLabel = (model: CatalogModel) =>
  `${model.displayName || model.modelId}${model.verified ? '' : '（未验证）'}`

/** Available first, then the ones this build knows by name, then the rest. */
function orderRuntimes(runtimes: CatalogRuntime[]): CatalogRuntime[] {
  const rank = (runtime: CatalogRuntime) => {
    const index = PREFERRED_ORDER.indexOf(runtime.runtimeId)
    return index === -1 ? PREFERRED_ORDER.length : index
  }
  return [...runtimes].sort((left, right) => {
    const ready = Number(right.status === 'available') - Number(left.status === 'available')
    if (ready !== 0) return ready
    const byName = rank(left) - rank(right)
    if (byName !== 0) return byName
    return left.runtimeId.localeCompare(right.runtimeId)
  })
}

/** The model a runtime should start with: promoted, then on, then verified. */
function pickModel(models: CatalogModel[]): CatalogModel | undefined {
  return (
    models.find((model) => model.isDefault) ??
    models.find((model) => model.enabled) ??
    models.find((model) => model.verified) ??
    models[0]
  )
}

export default function OnboardingDialog() {
  const catalog = useCatalog(true)
  const [finished, setFinished] = useState(
    () => !isTauriHost() || readBoolPreference(PREF_ONBOARDING_DONE, false),
  )
  /** Runtimes the user turned on. Seeded once, to pi only. */
  const [selected, setSelected] = useState<string[]>([])
  /** runtimeId → modelId, for the model picker under each checked runtime. */
  const [picked, setPicked] = useState<Record<string, string>>({})
  const [subagent, setSubagent] = useState<PiSubagentStatus | null>(null)
  const [busy, setBusy] = useState(false)
  /** runtimeId npm is installing right now, if any. */
  const [installing, setInstalling] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const seeded = useRef(false)
  /**
   * Models discovery had already switched on before the user chose.
   *
   * Discovery turns on a couple of entries by itself (pi's own default and
   * codex's auto-review). Leaving those alone when their runtime was not
   * checked would contradict the checklist: the chat picker would still offer
   * a runtime the user just declined.
   */
  const initialEnabled = useRef<Array<{ runtimeId: string; modelId: string }>>([])

  const runtimes = useMemo(() => orderRuntimes(catalog.snapshot?.runtimes ?? []), [catalog.snapshot])
  const models = useMemo(() => catalog.snapshot?.models ?? [], [catalog.snapshot])
  const available = useMemo(
    () => runtimes.filter((runtime) => runtime.status === 'available'),
    [runtimes],
  )

  /**
   * "Already set up" is read from a promoted default model, not from a flag:
   * discovery pre-enables a couple of entries on a brand-new profile (pi's own
   * default and codex's auto-review), so `enabled` alone cannot tell a fresh
   * install from a configured one. A default model only ever comes from a user
   * decision, and it lives in the data directory — so it survives a WebView
   * storage reset instead of depending on the localStorage flag alone.
   */
  const configured = useMemo(() => models.some((model) => model.isDefault), [models])

  useEffect(() => {
    if (finished || !configured) return
    writeBoolPreference(PREF_ONBOARDING_DONE, true)
    setFinished(true)
  }, [configured, finished])

  // Seed the checklist and one model per runtime, once the catalog is known.
  useEffect(() => {
    if (finished || seeded.current || catalog.status !== 'ready') return
    seeded.current = true
    const hasPi = available.some((runtime) => runtime.runtimeId === DEFAULT_RUNTIME)
    setSelected(hasPi ? [DEFAULT_RUNTIME] : [])
    initialEnabled.current = models
      .filter((model) => model.enabled)
      .map((model) => ({ runtimeId: model.runtimeId, modelId: model.modelId }))
    const next: Record<string, string> = {}
    for (const runtime of available) {
      const choice = pickModel(models.filter((model) => model.runtimeId === runtime.runtimeId))
      if (choice) next[runtime.runtimeId] = choice.modelId
    }
    setPicked(next)
  }, [finished, catalog.status, available, models])

  useEffect(() => {
    if (finished || !isTauriHost()) return
    void piSubagentStatus()
      .then(setSubagent)
      .catch(() => undefined)
  }, [finished])

  const modelsOf = useCallback(
    (runtimeId: string) =>
      models
        .filter((model) => model.runtimeId === runtimeId)
        .sort((left, right) => {
          const score = (model: CatalogModel) =>
            (model.isDefault ? 4 : 0) + (model.enabled ? 2 : 0) + (model.verified ? 1 : 0)
          return score(right) - score(left)
        }),
    [models],
  )

  const toggle = useCallback((runtimeId: string) => {
    setSelected((current) =>
      current.includes(runtimeId)
        ? current.filter((value) => value !== runtimeId)
        : [...current, runtimeId],
    )
  }, [])

  const finish = useCallback(
    (apply: boolean) => {
      writeBoolPreference(PREF_ONBOARDING_DONE, true)
      setFinished(true)
      if (apply) notifyCatalogChanged()
    },
    [],
  )

  const complete = useCallback(async () => {
    const chosen = selected.filter((runtimeId) => Boolean(picked[runtimeId]))
    if (chosen.length === 0) return
    setBusy(true)
    setNotice('')
    try {
      // Turn on exactly one model per selected runtime, so the chat picker has
      // that runtime's entry available. Everything else stays off until the
      // user turns it on in settings.
      let snapshot: CatalogSnapshot | null = null
      for (const runtimeId of chosen) {
        snapshot = await setCatalogModelEnabled(runtimeId, picked[runtimeId], true)
      }
      // Undo what discovery switched on for the runtimes the user left
      // unchecked, so the checklist is not cosmetic.
      for (const entry of initialEnabled.current) {
        if (chosen.includes(entry.runtimeId)) continue
        snapshot = await setCatalogModelEnabled(entry.runtimeId, entry.modelId, false)
      }
      // Promote one default model so the next launch opens on a known choice.
      // `catalog_set_default_model` refuses an unverified model, and a promoted
      // model is switched on by the host as well, so this is best-effort only:
      // the enabled models above already make the setup "done".
      const ordered = [...chosen].sort(
        (left, right) =>
          Number(right === DEFAULT_RUNTIME) - Number(left === DEFAULT_RUNTIME),
      )
      for (const runtimeId of ordered) {
        const model = models.find(
          (item) => item.runtimeId === runtimeId && item.modelId === picked[runtimeId],
        )
        if (!model?.verified) continue
        snapshot = await setDefaultCatalogModel(runtimeId, picked[runtimeId])
        break
      }
      if (snapshot) {
        catalog.apply(snapshot)
        writeCachedSnapshot(snapshot)
      }
      finish(true)
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }, [catalog, finish, models, picked, selected])

  const installSubagent = useCallback(async () => {
    setBusy(true)
    setNotice('')
    try {
      const result = await installPiSubagent()
      setSubagent(result.status)
      if (result.written > 0) setNotice(`已安装 ${result.written} 个子代理扩展文件。`)
      await catalog.refresh()
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }, [catalog])

  /**
   * One-click install for a runtime that is not on this machine.
   *
   * The host runs npm into the shared prefix and rescans, so the snapshot that
   * comes back already lists the runtime as available. It is switched on
   * straight away: pressing install is the user saying they want it.
   */
  const installOne = useCallback(
    async (runtimeId: string) => {
      setInstalling(runtimeId)
      setNotice('')
      try {
        const { report, snapshot } = await installRuntime(runtimeId)
        catalog.apply(snapshot)
        writeCachedSnapshot(snapshot)
        if (!report.ok) {
          setNotice(
            `${runtimeId} 安装未完成：${report.output || 'npm 没有返回输出，请检查网络或 Node.js 版本。'}`,
          )
          return
        }
        setSelected((current) =>
          current.includes(runtimeId) ? current : [...current, runtimeId],
        )
        const choice = pickModel(snapshot.models.filter((model) => model.runtimeId === runtimeId))
        if (choice) setPicked((current) => ({ ...current, [runtimeId]: choice.modelId }))
        setNotice(
          `已安装 ${runtimeId}（${Math.max(1, Math.round(report.elapsedMs / 1000))}s）${
            report.installedPath ? ` → ${report.installedPath}` : ''
          }`,
        )
      } catch (cause) {
        setNotice(cause instanceof Error ? cause.message : String(cause))
      } finally {
        setInstalling(null)
      }
    },
    [catalog],
  )

  if (finished) return null

  // Nothing useful to ask until discovery answers, and a returning user must
  // not see the wizard flash while the cached snapshot is being read.
  if (catalog.status === 'idle' || catalog.status === 'loading') return null

  // Checked before the wizard renders so a configured user never sees it for a
  // frame; the effect above flips `finished` for the rest of the session.
  if (configured) return null

  if (catalog.status === 'error' || catalog.status === 'unavailable') {
    return (
      <div style={overlayStyle} data-testid="onboarding">
        <div style={cardStyle}>
          <h2 style={{ margin: '0 0 10px', fontSize: 18 }}>无法读取运行时目录</h2>
          <p style={{ margin: '0 0 16px', fontSize: 13, opacity: 0.85, lineHeight: 1.6 }}>
            {catalog.error ?? '桌面宿主没有响应。'}
          </p>
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button type="button" style={ghostButton} onClick={() => finish(false)}>
              稍后设置
            </button>
            <button
              type="button"
              style={buttonBase}
              data-testid="onboarding-retry"
              onClick={() => void catalog.reload()}
            >
              重新检测
            </button>
          </div>
        </div>
      </div>
    )
  }

  const piSelected = selected.includes(DEFAULT_RUNTIME)
  const canInstallSubagent = piSelected && Boolean(subagent?.canInstall) && !subagent?.installed

  return (
    <div style={overlayStyle} data-testid="onboarding">
      <div style={cardStyle}>
        <h2 style={{ margin: '0 0 6px', fontSize: 18 }}>欢迎使用 Anvil</h2>
        <p style={{ margin: '0 0 18px', fontSize: 13, opacity: 0.8, lineHeight: 1.6 }}>
          只需设置一次。默认只启用已安装的 pi，其它运行时按需勾选；没装的可以一键安装（走 npm，装到当前用户目录，不需要系统权限），设置完成后启动会直接使用上次的配置。
        </p>

        <div style={{ marginBottom: 18 }}>
          <div style={sectionTitle}>
            1. 运行时
            <span style={{ fontWeight: 400, opacity: 0.6, marginLeft: 8 }}>
              勾选要启用的，其余保持关闭
            </span>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {runtimes.map((runtime) => {
              const ready = runtime.status === 'available'
              const checked = selected.includes(runtime.runtimeId)
              if (!ready) {
                const started = installing === runtime.runtimeId
                return (
                  <div
                    key={runtime.runtimeId}
                    data-testid={`onboarding-runtime-${runtime.runtimeId}`}
                    data-checked="false"
                    data-installed="false"
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 10,
                      padding: '10px 12px',
                      borderRadius: 10,
                      border: '1px dashed var(--border, rgba(127,127,127,.35))',
                      opacity: started ? 1 : 0.9,
                    }}
                  >
                    <span style={{ flex: 1, fontSize: 13, fontWeight: 600 }}>
                      {runtime.displayName || runtime.runtimeId}
                      <span style={{ fontWeight: 400, opacity: 0.6, marginLeft: 6 }}>未安装</span>
                    </span>
                    <button
                      type="button"
                      style={{
                        ...buttonBase,
                        padding: '5px 12px',
                        fontSize: 12,
                        opacity: installing && !started ? 0.5 : 1,
                      }}
                      disabled={installing !== null || busy}
                      data-testid={`onboarding-install-${runtime.runtimeId}`}
                      onClick={() => void installOne(runtime.runtimeId)}
                    >
                      {started ? '安装中…' : '一键安装'}
                    </button>
                  </div>
                )
              }
              return (
                <label
                  key={runtime.runtimeId}
                  data-testid={`onboarding-runtime-${runtime.runtimeId}`}
                  data-checked={checked ? 'true' : 'false'}
                  data-installed="true"
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 10,
                    padding: '10px 12px',
                    borderRadius: 10,
                    cursor: 'pointer',
                    border: `1px solid ${checked ? 'rgba(37,99,235,.55)' : 'var(--border, rgba(127,127,127,.25))'}`,
                    background: checked ? 'rgba(37,99,235,.08)' : 'transparent',
                  }}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    data-testid={`onboarding-runtime-toggle-${runtime.runtimeId}`}
                    onChange={() => toggle(runtime.runtimeId)}
                    style={{ width: 15, height: 15, cursor: 'pointer' }}
                  />
                  <span style={{ flex: 1, fontSize: 13, fontWeight: 600 }}>
                    {runtime.displayName || runtime.runtimeId}
                    {runtime.version ? (
                      <span style={{ fontWeight: 400, opacity: 0.6, marginLeft: 6 }}>
                        v{runtime.version}
                      </span>
                    ) : null}
                  </span>
                  <span
                    style={{
                      fontSize: 11,
                      padding: '2px 8px',
                      borderRadius: 999,
                      border: '1px solid rgba(34,197,94,.45)',
                      background: 'rgba(34,197,94,.12)',
                    }}
                  >
                    已就绪
                  </span>
                </label>
              )
            })}
          </div>

          {installing ? (
            <p
              style={{ margin: '8px 0 0', fontSize: 12, opacity: 0.75 }}
              data-testid="onboarding-installing"
            >
              正在通过 npm 安装 {installing}，首次下载可能需要一到三分钟…
            </p>
          ) : null}

          {available.length === 0 ? (
            <p style={{ margin: '8px 0 0', fontSize: 12, opacity: 0.75, lineHeight: 1.6 }}>
              本机还没有可用的运行时。点“一键安装”会用 npm 装到当前用户目录（不动系统 PATH），装完自动识别并勾选。
            </p>
          ) : null}

          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 8 }}>
            <button
              type="button"
              style={{ ...ghostButton, padding: '6px 12px', fontSize: 12 }}
              disabled={busy || installing !== null}
              data-testid="onboarding-redetect"
              onClick={() => void catalog.refresh()}
            >
              重新检测
            </button>
            {subagent?.canInstall && !subagent?.installed ? (
              <button
                type="button"
                style={{ ...ghostButton, padding: '6px 12px', fontSize: 12 }}
                disabled={busy || installing !== null}
                data-testid="onboarding-install-subagent"
                onClick={() => void installSubagent()}
              >
                {canInstallSubagent ? '安装 pi 子代理扩展' : '安装 pi 子代理扩展（需先勾选 pi）'}
              </button>
            ) : null}
          </div>
        </div>

        <div style={{ marginBottom: 8 }}>
          <div style={sectionTitle}>
            2. 模型
            <span style={{ fontWeight: 400, opacity: 0.6, marginLeft: 8 }}>
              每个启用的运行时选一个，其余可在设置里再打开
            </span>
          </div>
          {selected.length === 0 ? (
            <p style={{ margin: 0, fontSize: 13, opacity: 0.7 }}>勾选运行时后，在这里选择它的模型。</p>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {selected.map((runtimeId) => {
                const runtime = available.find((item) => item.runtimeId === runtimeId)
                const choices = modelsOf(runtimeId)
                return (
                  <div key={runtimeId}>
                    <div style={{ fontSize: 12, opacity: 0.7, marginBottom: 4 }}>
                      {runtime?.displayName || runtimeId}
                    </div>
                    {choices.length > 0 ? (
                      <select
                        value={picked[runtimeId] ?? choices[0].modelId}
                        onChange={(event) =>
                          setPicked((current) => ({ ...current, [runtimeId]: event.target.value }))
                        }
                        style={selectStyle}
                        data-testid={`onboarding-model-${runtimeId}`}
                      >
                        {choices.map((model) => (
                          <option key={modelKey(model)} value={model.modelId}>
                            {modelLabel(model)}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <p style={{ margin: 0, fontSize: 12, opacity: 0.7 }}>
                        该运行时暂未返回模型，可在设置里同步。
                      </p>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {notice ? (
          <p style={{ margin: '12px 0 0', fontSize: 12, color: '#f59e0b' }} data-testid="onboarding-notice">
            {notice}
          </p>
        ) : null}

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 18 }}>
          <button
            type="button"
            style={ghostButton}
            disabled={busy}
            data-testid="onboarding-skip"
            onClick={() => finish(false)}
          >
            稍后设置
          </button>
          <button
            type="button"
            style={buttonBase}
            disabled={busy || selected.length === 0}
            data-testid="onboarding-finish"
            onClick={() => void complete()}
          >
            {busy ? '处理中…' : '完成设置'}
          </button>
        </div>
      </div>
    </div>
  )
}
