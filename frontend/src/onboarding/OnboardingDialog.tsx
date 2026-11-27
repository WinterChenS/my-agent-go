// First-run setup, in two steps:
//
//   1. 主题 — pick a theme and see it immediately (the window behind the wizard
//      follows, so nothing is guessed from a colour chip).
//   2. 运行时 — turn runtimes on and install the missing ones.
//
// No model picker: each runtime is enabled with the model it leads with
// (`pickModel`), and the chat composer can switch later. Asking someone to
// choose from twenty model ids before they have ever sent a message is noise,
// and a model that does not work is discovered on first use, not in a wizard.
//
// Why a checklist instead of "everything that was detected": discovery reports
// every runtime and every model it can reach. Handing all of that to the chat
// picker is noise, and a first launch should not silently switch on three
// runtimes the user never asked for. pi is checked by default because it is the
// reference adapter and the one this build ships extensions for.
//
// Data arrives late on purpose: discovery probes runtimes and takes seconds on a
// cold profile. `main.tsx` starts that probe before the first paint, step 1
// needs no data at all, and step 2 shows placeholders instead of blocking, so
// nothing here waits on a spinner before showing something useful.
//
// The wizard is only shown when the catalog says nothing is configured yet. A
// completed setup leaves a default/enabled model in the catalog, which lives in
// the data directory — so it survives a WebView storage reset, not just the
// localStorage flag.
//
// Styling: every colour is a token from design/tokens.css. This screen used to
// invent its own names (`--text`, `--button-bg`, `--input-bg`), none of which
// exist, so `var(--text, #1f2430)` always fell back to a near-black and the card
// was unreadable in the dark theme. The styles live in onboarding.css.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useCatalog } from '../catalog/useCatalog'
import {
  installPiSubagent,
  installRuntime,
  markCatalogSetupDone,
  piRuntimeStatus,
  piSubagentStatus,
  setCatalogModelEnabled,
  setDefaultCatalogModel,
  type BundledPiStatus,
  type CatalogModel,
  type CatalogRuntime,
  type CatalogSnapshot,
  type PiSubagentStatus,
} from '../catalog/client'
import { readCachedSnapshot, writeCachedSnapshot } from '../catalog/cache'
import { onRuntimeInstallLog } from '../catalog/installLog'
import { notifyCatalogChanged } from '../catalog/notify'
import { useTheme } from '../preferences/ThemeProvider'
import type { ThemeMode } from '../preferences/theme'
import { PREF_ONBOARDING_DONE, readBoolPreference, writeBoolPreference } from '../preferences/flags'
import { isTauriHost } from '../host/tauri'
import './onboarding.css'

/** Runtimes shown first, in this order; anything else is appended. */
// Only pi is wired in this build (see anvil-catalog adapters::PARKED), so this
// is a one-entry list. Keeping the mechanism — instead of hardcoding pi — means
// switching another adapter back on needs no edit here.
const PREFERRED_ORDER = ['pi']
/** The one that is on by default, when it is actually installed. */
const DEFAULT_RUNTIME = 'pi'

const THEME_OPTIONS: Array<{ value: ThemeMode; name: string; hint: string }> = [
  { value: 'system', name: '跟随系统', hint: '系统切到深色时自动跟着变' },
  { value: 'light', name: '浅色', hint: '明亮清爽，白天更通透' },
  { value: 'dark', name: '深色', hint: '低亮度，长时间看不刺眼' },
]

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

/** What a runtime that is not usable actually is.
 *
 * The badge used to say "未安装" for every non-available status, so a runtime
 * that was found but failed its probe (or has no credential) read as missing —
 * the one thing the reader could not act on.
 */
function runtimeStatusLabel(status: string): string {
  switch (status) {
    case 'probe_failed':
      return '探测失败'
    case 'unauthenticated':
      return '未登录'
    case 'version_unsupported':
      return '版本过低'
    case 'detected':
      return '已检测到'
    case 'adapter_failed':
      return '适配器异常'
    case 'orphaned':
      return '已失效'
    default:
      return '未安装'
  }
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
 * A miniature window drawn in one theme's own palette.
 *
 * It cannot use the active tokens: it is showing a theme that is *not* the one
 * in force. The values live in onboarding.css as `--preview-*`.
 */
function ThemeShot({ tone }: { tone: 'light' | 'dark' }) {
  return (
    <span className="ob-shot" data-tone={tone} aria-hidden="true">
      <span className="ob-shot-top" />
      <span className="ob-shot-side" />
      <span className="ob-shot-body">
        <span className="ob-shot-line" style={{ width: '54%' }} />
        <span className="ob-shot-line" style={{ width: '78%' }} />
        <span className="ob-shot-block" />
      </span>
    </span>
  )
}

function StepMark({ step }: { step: 1 | 2 }) {
  return (
    <div className="ob-steps" aria-label={`第 ${step} 步，共 2 步`}>
      <span className="ob-step" data-active={step === 1} data-done={step > 1}>
        <span className="ob-step-dot">{step > 1 ? '✓' : '1'}</span>
        主题
      </span>
      <i className="ob-step-sep" aria-hidden="true" />
      <span className="ob-step" data-active={step === 2}>
        <span className="ob-step-dot">2</span>
        运行时
      </span>
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
  const { mode, setMode } = useTheme()
  const [step, setStep] = useState<1 | 2>(1)
  /** Runtimes the user turned on. Seeded once, to pi only. */
  const [selected, setSelected] = useState<string[]>([])
  const [subagent, setSubagent] = useState<PiSubagentStatus | null>(null)
  // What the installer carries (ADR 0009). A damaged tree is reported here, not
  // as "pi is not installed": the wizard must not send anyone to npm.
  const [piStatus, setPiStatus] = useState<BundledPiStatus | null>(null)
  const [busy, setBusy] = useState(false)
  /** runtimeId npm is installing right now, if any. */
  const [installing, setInstalling] = useState<string | null>(null)
  /** Incremental npm output, newest line last. */
  const [installLog, setInstallLog] = useState<string[]>([])
  const logRef = useRef<HTMLPreElement | null>(null)
  const [notice, setNotice] = useState('')
  const seeded = useRef(false)
  /**
   * Whether a snapshot was already on disk when this window opened. A returning
   * user must not see the wizard flash while that cache is being read.
   */
  const hadCache = useRef<boolean | null>(null)
  if (hadCache.current === null) hadCache.current = readCachedSnapshot() !== null
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
  /**
   * Runtimes worth offering.
   *
   * `unauthenticated` means discovery could not find a credential in the files
   * this build reads — not that pi cannot run. A key in `models.json`, an
   * `apiKey` on the provider, or an environment variable all work, and a
   * checkbox that cannot be ticked is a worse answer than the runtime's own
   * message on the first turn.
   */
  const usable = useMemo(
    () =>
      runtimes.filter(
        (runtime) => runtime.status === 'available' || runtime.status === 'unauthenticated',
      ),
    [runtimes],
  )
  const loading = catalog.status === 'idle' || catalog.status === 'loading'
  const failed = catalog.status === 'error' || catalog.status === 'unavailable'

  /**
   * "Already set up" is read from the data directory first — the wizard used to
   * be skipped only when a model had been promoted to default, and that is a
   * side effect discovery can no longer be relied on to produce (the seed is
   * chosen from disk now, so nothing is verified during startup). The promoted
   * default stays as the fallback for profiles set up before that flag existed.
   */
  const configured = useMemo(
    () => catalog.snapshot?.setupDone === true || models.some((model) => model.isDefault),
    [catalog.snapshot, models],
  )

  useEffect(() => {
    if (finished || !configured) return
    writeBoolPreference(PREF_ONBOARDING_DONE, true)
    setFinished(true)
  }, [configured, finished])

  // Seed the checklist once the catalog is known.
  useEffect(() => {
    if (finished || seeded.current || catalog.status !== 'ready') return
    seeded.current = true
    const hasPi = usable.some((runtime) => runtime.runtimeId === DEFAULT_RUNTIME)
    setSelected(hasPi ? [DEFAULT_RUNTIME] : [])
    initialEnabled.current = models
      .filter((model) => model.enabled)
      .map((model) => ({ runtimeId: model.runtimeId, modelId: model.modelId }))
  }, [finished, catalog.status, usable, models])

  useEffect(() => {
    if (finished || !isTauriHost()) return
    void piSubagentStatus()
      .then(setSubagent)
      .catch(() => undefined)
    void piRuntimeStatus()
      .then(setPiStatus)
      .catch(() => undefined)
  }, [finished])

  // npm can run for minutes. Show each line as it arrives and keep the box
  // pinned to the bottom, so a cold download never looks like a frozen wizard.
  useEffect(() => {
    if (!installing) return
    return onRuntimeInstallLog(installing, (line) => {
      setInstallLog((lines) => [...lines.slice(-399), line])
    })
  }, [installing])

  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [installLog])

  const toggleRuntime = useCallback((runtimeId: string) => {
    setSelected((current) =>
      current.includes(runtimeId)
        ? current.filter((value) => value !== runtimeId)
        : [...current, runtimeId],
    )
  }, [])

  const finish = useCallback((apply: boolean) => {
    writeBoolPreference(PREF_ONBOARDING_DONE, true)
    // Also in the data directory: localStorage lives in the WebView, and a
    // forced quit or a storage reset would otherwise reopen the wizard on a
    // profile that is already set up.
    void markCatalogSetupDone().catch(() => undefined)
    setFinished(true)
    if (apply) notifyCatalogChanged()
  }, [])

  const complete = useCallback(async () => {
    const chosen = selected.filter((runtimeId) =>
      usable.some((runtime) => runtime.runtimeId === runtimeId),
    )
    // One model per checked runtime, the one it leads with. No model picker: a
    // runtime is only useful with something to run, and the composer can
    // switch later. `pickModel` prefers the promoted entry, then an enabled
    // one, then a verified one, then simply the first.
    const picks: Array<{ runtimeId: string; modelId: string }> = []
    for (const runtimeId of chosen) {
      const choice = pickModel(models.filter((model) => model.runtimeId === runtimeId))
      if (choice) picks.push({ runtimeId, modelId: choice.modelId })
    }
    // An install can succeed while the catalog has not caught up. The wizard
    // must still close: leaving it open with a disabled button strands the user
    // even though settings can repair or re-detect the runtime afterwards.
    if (picks.length === 0) {
      finish(false)
      return
    }
    const wanted = new Set(picks.map((pick) => `${pick.runtimeId}|${pick.modelId}`))
    setBusy(true)
    setNotice('')
    try {
      let snapshot: CatalogSnapshot | null = null
      for (const pick of picks) {
        snapshot = await setCatalogModelEnabled(pick.runtimeId, pick.modelId, true)
      }
      // Undo what discovery switched on for the runtimes the user left
      // unchecked, so the checklist is not cosmetic.
      for (const entry of initialEnabled.current) {
        if (wanted.has(`${entry.runtimeId}|${entry.modelId}`)) continue
        snapshot = await setCatalogModelEnabled(entry.runtimeId, entry.modelId, false)
      }
      // Promote one default so the next launch opens on a known choice. The
      // store refuses to promote a model that was never verified, and this step
      // is not worth failing setup over — the enabled model above is what makes
      // the composer work, so a refusal just leaves the order as it was.
      const ordered = [...picks].sort(
        (left, right) =>
          Number(right.runtimeId === DEFAULT_RUNTIME) - Number(left.runtimeId === DEFAULT_RUNTIME),
      )
      for (const pick of ordered) {
        const model = models.find(
          (item) => item.runtimeId === pick.runtimeId && item.modelId === pick.modelId,
        )
        if (!model?.verified) continue
        try {
          snapshot = await setDefaultCatalogModel(pick.runtimeId, pick.modelId)
        } catch {
          // Unverified/absent: keep going, nothing else depends on it.
        }
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
  }, [catalog, finish, models, selected, usable])

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
      // Pi arrives with the installer. The old wizard offered a one-click npm
      // install for it; that path is gone (ADR 0009) and a stale row must not
      // bring it back.
      if (runtimeId === DEFAULT_RUNTIME) {
        setNotice('Pi 已内置在 Anvil 中，无需安装；若包内运行时损坏，请重新安装 Anvil。')
        return
      }
      setInstalling(runtimeId)
      setInstallLog([])
      setNotice('')
      try {
        const { report, snapshot } = await installRuntime(runtimeId)
        catalog.apply(snapshot)
        writeCachedSnapshot(snapshot)
        // Events can be missed while the tab is backgrounded; the final report
        // carries the same transcript, so the log is complete either way.
        if (report.output) {
          setInstallLog((lines) => [...lines, ...report.output.split(/\r?\n/)])
        }
        if (!report.ok) {
          setNotice(
            `${runtimeId} 安装未完成：${
              report.output.split(/\r?\n/).filter(Boolean).slice(-2).join(' ') ||
              'npm 没有返回输出，请检查网络或 Node.js 版本。'
            }`,
          )
          return
        }
        setSelected((current) =>
          current.includes(runtimeId) ? current : [...current, runtimeId],
        )
        setNotice(
          `已安装 ${runtimeId}${report.version ? ` v${report.version}` : ''}（${Math.max(
            1,
            Math.round(report.elapsedMs / 1000),
          )}s）${report.installedPath ? ` → ${report.installedPath}` : ''}`,
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

  // A returning window has rows in the cache and is about to hide the wizard
  // again; showing it for the duration of that read would look like a hitch.
  if (loading && hadCache.current) return null

  // Checked before the wizard renders so a configured user never sees it for a
  // frame; the effect above flips `finished` for the rest of the session.
  if (configured) return null

  const piSelected = selected.includes(DEFAULT_RUNTIME)
  const canInstallSubagent = piSelected && Boolean(subagent?.canInstall) && !subagent?.installed
  // A successful install adds the id to `selected` even if the catalog's probe
  // has not caught up yet. Counting `selected` keeps 完成设置 enabled in that
  // window instead of stranding the user behind a disabled button.
  const chosenCount = selected.length
  /**
   * Model lists land with the discovery pass. Finishing before they do would be
   * a silent no-op — there is nothing to switch on yet — and a button that does
   * nothing reads as broken.
   */
  const modelsPending = catalog.snapshot?.scanning === true && models.length === 0

  return (
    <div className="ob-backdrop" data-testid="onboarding">
      <div className="ob-card">
        <div className="ob-head">
          <div className="ob-head-text">
            <div className="ob-eyebrow">
              {step === 1 ? '首次设置 · 第 1 步，共 2 步' : '首次设置 · 第 2 步，共 2 步'}
            </div>
            <h2 className="ob-title">{step === 1 ? '欢迎使用 Anvil' : '选择运行时'}</h2>
          </div>
          <StepMark step={step} />
        </div>

        <div className="ob-body" data-testid={`onboarding-step-${step}`}>
          {step === 1 ? (
            <>
              <p className="ob-sub">
                先挑一个主题，选中会立刻生效；之后随时可以在「设置 → 外观」里改。
              </p>
              <div
                className="ob-themes"
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
                      data-selected={active ? 'true' : 'false'}
                      data-testid={`onboarding-theme-${option.value}`}
                      className="ob-theme"
                      onClick={() => setMode(option.value)}
                    >
                      {option.value === 'system' ? (
                        <span className="ob-shot-pair" aria-hidden="true">
                          <ThemeShot tone="light" />
                          <ThemeShot tone="dark" />
                        </span>
                      ) : (
                        <ThemeShot tone={option.value} />
                      )}
                      <span className="ob-theme-name">
                        {option.name}
                        <span className="ob-theme-check" aria-hidden="true">
                          ✓
                        </span>
                      </span>
                      <span className="ob-theme-hint">{option.hint}</span>
                    </button>
                  )
                })}
              </div>
            </>
          ) : (
            <>
              <p className="ob-sub">
                勾选要启用的运行时。Pi 随 Anvil 一起安装，无需额外下载；其他运行时可以在这里按 npm 全局方式装到当前用户目录（不需要管理员权限，也不改系统 PATH）。每个运行时会自动使用它的默认模型，进软件后还能在设置里换。
              </p>

              {piStatus?.state === 'damaged' ? (
                <p className="ob-notice is-error" data-testid="onboarding-pi-damaged">
                  {piStatus.error ?? '包内 Pi 运行时损坏。'} 请重新安装 Anvil 恢复包内 Pi；本版本不会通过 npm 安装 Pi。
                </p>
              ) : null}

              {failed ? (
                <p className="ob-notice is-error">
                  {catalog.error ?? '桌面宿主没有响应，读不到运行时目录。'}
                </p>
              ) : null}

              {loading && runtimes.length === 0 ? (
                <div className="ob-runtimes" data-testid="onboarding-runtimes-loading">
                  <div className="ob-skeleton" />
                  <div className="ob-skeleton" />
                  <div className="ob-skeleton" />
                  <p className="ob-hint">正在检测本机已安装的运行时…</p>
                </div>
              ) : (
                <div className="ob-runtimes">
                  {runtimes.map((runtime) => {
                    // Found and runnable, even if discovery could not see the
                    // credential: the checkbox stays usable and the pill says
                    // which of the two it is. A damaged bundled runtime is the
                    // opposite — found, but nothing this build can run.
                    const isPiRuntime = runtime.runtimeId === DEFAULT_RUNTIME
                    const piDamaged = isPiRuntime && piStatus?.state === 'damaged'
                    const ready =
                      (runtime.status === 'available' || runtime.status === 'unauthenticated') &&
                      !piDamaged
                    const signedIn = runtime.status === 'available'
                    const checked = selected.includes(runtime.runtimeId)
                    const pick =
                      ready
                        ? pickModel(models.filter((model) => model.runtimeId === runtime.runtimeId))
                        : undefined
                    const started = installing === runtime.runtimeId
                    const inputId = `ob-runtime-${runtime.runtimeId}`
                    return (
                      <div
                        key={runtime.runtimeId}
                        className="ob-runtime"
                        data-testid={`onboarding-runtime-${runtime.runtimeId}`}
                        data-checked={checked ? 'true' : 'false'}
                        data-installed={ready ? 'true' : 'false'}
                      >
                        <div className="ob-runtime-row">
                          {ready ? (
                            <input
                              id={inputId}
                              type="checkbox"
                              className="ob-checkbox"
                              checked={checked}
                              data-testid={`onboarding-runtime-toggle-${runtime.runtimeId}`}
                              onChange={() => toggleRuntime(runtime.runtimeId)}
                            />
                          ) : (
                            <span className="ob-checkbox" aria-hidden="true" />
                          )}
                          <label className="ob-runtime-name" htmlFor={ready ? inputId : undefined}>
                            {runtime.displayName || runtime.runtimeId}
                            {runtime.version ? (
                              <span className="ob-runtime-ver">v{runtime.version}</span>
                            ) : null}
                          </label>
                          {signedIn ? (
                            <span className="ob-pill">已就绪</span>
                          ) : ready ? (
                            <span className="ob-pill is-warn">{runtimeStatusLabel(runtime.status)}</span>
                          ) : (
                            <>
                              <span className="ob-pill is-muted">
                                {piDamaged ? '包内运行时损坏' : runtimeStatusLabel(runtime.status)}
                              </span>
                              {/* Pi is installed by Anvil's installer; there is no
                                  npm step to offer, damaged or not. */}
                              {isPiRuntime ? null : (
                                <button
                                  type="button"
                                  className="ob-btn is-small"
                                  disabled={installing !== null || busy}
                                  data-testid={`onboarding-install-${runtime.runtimeId}`}
                                  onClick={() => void installOne(runtime.runtimeId)}
                                >
                                  {started ? '安装中…' : '一键安装'}
                                </button>
                              )}
                            </>
                          )}
                        </div>

                        {/* A runtime that was found but cannot run must not read
                            as "not installed": the path and the probe's own
                            reason are the only way to tell a PATH problem from
                            a missing login. */}
                        {piDamaged || (runtime.status !== 'available' && runtime.status !== 'not_installed') ? (
                          <p className="ob-hint ob-runtime-detail" data-testid={`onboarding-detail-${runtime.runtimeId}`}>
                            {piDamaged
                              ? piStatus?.error || '包内 Pi 运行时损坏，请重新安装 Anvil。'
                              : runtime.health?.detail || '已找到可执行文件，但没有通过启动检查。'}
                            {!piDamaged && runtime.installPath ? ` · ${runtime.installPath}` : ''}
                          </p>
                        ) : null}

                        {ready && checked ? (
                          <p
                            className="ob-model-line"
                            data-testid={`onboarding-pick-${runtime.runtimeId}`}
                          >
                            {pick
                              ? `模型：${pick.displayName || pick.modelId}`
                              : '该运行时暂未返回模型，进软件后可在设置里同步'}
                          </p>
                        ) : null}
                      </div>
                    )
                  })}
                </div>
              )}

              {installing ? (
                <>
                  <p className="ob-hint" data-testid="onboarding-installing">
                    正在通过 npm 全局安装 {installing} 到当前用户目录（不修改系统 PATH）。若本机没有 Node，会先下载一份 Node 运行时；首次可能要几分钟，进度见下方日志。
                  </p>
                  <pre
                    ref={logRef}
                    className="ob-install-log"
                    data-testid="onboarding-install-log"
                    aria-live="polite"
                  >
                    {installLog.length ? installLog.join('\n') : '正在等待 npm 输出…'}
                  </pre>
                </>
              ) : installLog.length > 0 ? (
                <pre
                  ref={logRef}
                  className="ob-install-log is-done"
                  data-testid="onboarding-install-log"
                >
                  {installLog.join('\n')}
                </pre>
              ) : null}

              {!loading && !failed && usable.length === 0 ? (
                <p className="ob-hint">
                  {piStatus?.state === 'damaged'
                    ? '包内 Pi 运行时损坏，需要重新安装 Anvil 恢复。'
                    : '本机没有可用的运行时。其他运行时可以按 npm 全局方式装到当前用户目录（不动系统 PATH）；Pi 随 Anvil 安装包提供。'}
                </p>
              ) : null}

              <div className="ob-tools">
                <button
                  type="button"
                  className="ob-link"
                  disabled={busy || installing !== null}
                  data-testid="onboarding-redetect"
                  onClick={() => void catalog.reload()}
                >
                  重新检测
                </button>
                {subagent?.canInstall && !subagent?.installed ? (
                  <button
                    type="button"
                    className="ob-link"
                    disabled={busy || installing !== null}
                    data-testid="onboarding-install-subagent"
                    onClick={() => void installSubagent()}
                  >
                    {canInstallSubagent
                      ? '安装 pi 子代理扩展'
                      : '安装 pi 子代理扩展（需先勾选 pi）'}
                  </button>
                ) : null}
              </div>

              {notice ? (
                <p className="ob-notice" data-testid="onboarding-notice">
                  {notice}
                </p>
              ) : null}
            </>
          )}
        </div>

        <div className="ob-foot">
          <button
            type="button"
            className="ob-link"
            disabled={busy}
            data-testid="onboarding-skip"
            onClick={() => finish(false)}
          >
            稍后设置
          </button>
          <span className="ob-spacer" />
          {step === 2 ? (
            <button
              type="button"
              className="ob-btn is-ghost"
              disabled={busy || installing !== null}
              data-testid="onboarding-back"
              onClick={() => setStep(1)}
            >
              上一步
            </button>
          ) : null}
          {step === 1 ? (
            <button
              type="button"
              className="ob-btn"
              data-testid="onboarding-next"
              onClick={() => setStep(2)}
            >
              下一步
            </button>
          ) : (
            <button
              type="button"
              className="ob-btn"
              disabled={busy || chosenCount === 0 || modelsPending}
              data-testid="onboarding-finish"
              onClick={() => void complete()}
            >
              {busy ? '处理中…' : modelsPending ? '正在读取模型…' : '完成设置'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
