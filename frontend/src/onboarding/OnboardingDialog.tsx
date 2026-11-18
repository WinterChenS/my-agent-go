// First-run setup: pick a theme, choose which runtimes to turn on, and one
// model per runtime.
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
// Styling: every colour comes from the frozen tokens in design/tokens.css
// (`--surface`, `--fg`, `--fg-2`, `--muted`, `--border`, `--accent`, ...).
// This screen used to invent its own names (`--text`, `--button-bg`,
// `--input-bg`), none of which exist, so `var(--text, #1f2430)` always fell back
// to a near-black and the whole card was unreadable in the dark theme. Text is
// toned down with `--fg-2` / `--muted`, never with `opacity`, which used to make
// the body copy fade into the scrim.

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
import { useTheme } from '../preferences/ThemeProvider'
import type { ThemeMode } from '../preferences/theme'
import { PREF_ONBOARDING_DONE, readBoolPreference, writeBoolPreference } from '../preferences/flags'
import { isTauriHost } from '../host/tauri'

/** Runtimes shown first, in this order; anything else is appended. */
const PREFERRED_ORDER = ['pi', 'codex', 'opencode']
/** The one that is on by default, when it is actually installed. */
const DEFAULT_RUNTIME = 'pi'

const THEME_OPTIONS: Array<{ value: ThemeMode; label: string }> = [
  { value: 'system', label: '跟随系统' },
  { value: 'light', label: '浅色' },
  { value: 'dark', label: '深色' },
]

const overlayStyle: React.CSSProperties = {
  position: 'fixed',
  inset: 0,
  // `--scrim` is the token every other overlay in the app uses; it follows the
  // active theme instead of assuming a dark window.
  background: 'var(--scrim)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 24,
  zIndex: 9000,
}
const cardStyle: React.CSSProperties = {
  width: 566,
  maxWidth: '92vw',
  maxHeight: '86vh',
  overflow: 'auto',
  background: 'var(--surface)',
  color: 'var(--fg)',
  borderRadius: 'var(--r-lg)',
  padding: 26,
  boxShadow: 'var(--shadow-lg)',
  border: '1px solid var(--border-strong)',
  fontFamily: 'var(--font-body)',
}
const buttonBase: React.CSSProperties = {
  border: '1px solid transparent',
  background: 'var(--accent)',
  color: 'var(--on-accent)',
  borderRadius: 'var(--r-md)',
  padding: '9px 18px',
  cursor: 'pointer',
  fontSize: 'var(--text-md)',
  fontWeight: 600,
  fontFamily: 'inherit',
}
const ghostButton: React.CSSProperties = {
  ...buttonBase,
  background: 'transparent',
  border: '1px solid var(--border-strong)',
  color: 'var(--fg-2)',
}
const sectionTitle: React.CSSProperties = {
  fontSize: 'var(--text-sm)',
  fontWeight: 700,
  color: 'var(--fg-2)',
  marginBottom: 8,
}
const sectionNote: React.CSSProperties = {
  fontWeight: 400,
  color: 'var(--muted)',
  marginLeft: 8,
}
const selectStyle: React.CSSProperties = {
  width: '100%',
  padding: '8px 10px',
  borderRadius: 'var(--r-md)',
  border: '1px solid var(--border-strong)',
  background: 'var(--surface-2)',
  color: 'var(--fg)',
  fontSize: 'var(--text-md)',
  fontFamily: 'inherit',
}
const bodyText: React.CSSProperties = {
  margin: '0 0 18px',
  fontSize: 'var(--text-md)',
  color: 'var(--fg-2)',
  lineHeight: 1.7,
}
const hintText: React.CSSProperties = {
  margin: '8px 0 0',
  fontSize: 'var(--text-sm)',
  color: 'var(--muted)',
  lineHeight: 1.6,
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

/**
 * Theme switch, shown on the first screen so the look is settled before the app
 * is. Reads and writes through the normal ThemeProvider, so the choice made here
 * is the same one 设置 → 外观 shows, and it survives as the host preference.
 */
function ThemePicker() {
  const { mode, setMode } = useTheme()
  return (
    <div
      style={{
        display: 'flex',
        gap: 2,
        padding: 2,
        borderRadius: 'var(--r-md)',
        background: 'var(--surface-2)',
        border: '1px solid var(--border)',
        flexShrink: 0,
      }}
      role="radiogroup"
      aria-label="主题"
      data-testid="onboarding-theme"
    >
      {THEME_OPTIONS.map((option) => {
        const active = mode === option.value
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={active}
            data-testid={`onboarding-theme-${option.value}`}
            onClick={() => setMode(option.value)}
            style={{
              border: `1px solid ${active ? 'var(--border-strong)' : 'transparent'}`,
              background: active ? 'var(--surface)' : 'transparent',
              color: active ? 'var(--fg)' : 'var(--muted)',
              borderRadius: 'var(--r-xs)',
              padding: '5px 10px',
              cursor: 'pointer',
              fontSize: 'var(--text-sm)',
              fontWeight: active ? 600 : 500,
              fontFamily: 'inherit',
              lineHeight: 1.3,
            }}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

export default function OnboardingDialog() {
  const [finished, setFinished] = useState(
    () => !isTauriHost() || readBoolPreference(PREF_ONBOARDING_DONE, false),
  )
  // Only read the catalog while this screen can still be shown. A configured
  // user finishes on the first frame, and a finished wizard must not keep a
  // second background inventory poller alive next to the page's own.
  const catalog = useCatalog(!finished)
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
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 16, marginBottom: 10 }}>
            <h2 style={{ margin: 0, flex: 1, fontSize: 'var(--text-2xl)' }}>无法读取运行时目录</h2>
            <ThemePicker />
          </div>
          <p style={{ ...bodyText, marginBottom: 16 }}>
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
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 16, marginBottom: 8 }}>
          <h2 style={{ margin: 0, flex: 1, fontSize: 'var(--text-2xl)' }}>欢迎使用 Anvil</h2>
          <ThemePicker />
        </div>
        <p style={bodyText}>
          只需设置一次。默认只启用已安装的 pi，其它运行时按需勾选；没装的可以一键安装（走 npm，装到当前用户目录，不需要系统权限），设置完成后启动会直接使用上次的配置。
        </p>

        <div style={{ marginBottom: 18 }}>
          <div style={sectionTitle}>
            1. 运行时
            <span style={sectionNote}>勾选要启用的，其余保持关闭</span>
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
                      borderRadius: 'var(--r-md)',
                      background: 'var(--surface-2)',
                      border: '1px dashed var(--border-strong)',
                    }}
                  >
                    <span style={{ flex: 1, fontSize: 'var(--text-md)', fontWeight: 600 }}>
                      {runtime.displayName || runtime.runtimeId}
                      <span style={{ fontWeight: 400, color: 'var(--muted)', marginLeft: 6 }}>
                        未安装
                      </span>
                    </span>
                    <button
                      type="button"
                      style={{
                        ...buttonBase,
                        padding: '5px 12px',
                        fontSize: 'var(--text-sm)',
                        opacity: installing && !started ? 0.55 : 1,
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
                    borderRadius: 'var(--r-md)',
                    cursor: 'pointer',
                    border: `1px solid ${checked ? 'var(--accent)' : 'var(--border)'}`,
                    background: checked ? 'var(--accent-soft)' : 'transparent',
                  }}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    data-testid={`onboarding-runtime-toggle-${runtime.runtimeId}`}
                    onChange={() => toggle(runtime.runtimeId)}
                    style={{ width: 15, height: 15, cursor: 'pointer', accentColor: 'var(--accent)' }}
                  />
                  <span style={{ flex: 1, fontSize: 'var(--text-md)', fontWeight: 600 }}>
                    {runtime.displayName || runtime.runtimeId}
                    {runtime.version ? (
                      <span style={{ fontWeight: 400, color: 'var(--muted)', marginLeft: 6 }}>
                        v{runtime.version}
                      </span>
                    ) : null}
                  </span>
                  <span
                    style={{
                      fontSize: 'var(--text-2xs)',
                      padding: '2px 8px',
                      borderRadius: 999,
                      color: 'var(--success)',
                      border: '1px solid color-mix(in oklab, var(--success) 45%, transparent)',
                      background: 'var(--success-soft)',
                    }}
                  >
                    已就绪
                  </span>
                </label>
              )
            })}
          </div>

          {installing ? (
            <p style={hintText} data-testid="onboarding-installing">
              正在通过 npm 安装 {installing}，首次下载可能需要一到三分钟…
            </p>
          ) : null}

          {available.length === 0 ? (
            <p style={hintText}>
              本机还没有可用的运行时。点“一键安装”会用 npm 装到当前用户目录（不动系统 PATH），装完自动识别并勾选。
            </p>
          ) : null}

          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 8 }}>
            <button
              type="button"
              style={{ ...ghostButton, padding: '6px 12px', fontSize: 'var(--text-sm)' }}
              disabled={busy || installing !== null}
              data-testid="onboarding-redetect"
              onClick={() => void catalog.refresh()}
            >
              重新检测
            </button>
            {subagent?.canInstall && !subagent?.installed ? (
              <button
                type="button"
                style={{ ...ghostButton, padding: '6px 12px', fontSize: 'var(--text-sm)' }}
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
            <span style={sectionNote}>每个启用的运行时选一个，其余可在设置里再打开</span>
          </div>
          {selected.length === 0 ? (
            <p style={{ margin: 0, fontSize: 'var(--text-md)', color: 'var(--muted)' }}>
              勾选运行时后，在这里选择它的模型。
            </p>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {selected.map((runtimeId) => {
                const runtime = available.find((item) => item.runtimeId === runtimeId)
                const choices = modelsOf(runtimeId)
                return (
                  <div key={runtimeId}>
                    <div
                      style={{
                        fontSize: 'var(--text-sm)',
                        color: 'var(--muted)',
                        marginBottom: 4,
                      }}
                    >
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
                      <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--muted)' }}>
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
          <p
            style={{ margin: '12px 0 0', fontSize: 'var(--text-sm)', color: 'var(--warn)' }}
            data-testid="onboarding-notice"
          >
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
