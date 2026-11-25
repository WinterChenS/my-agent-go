// The settings card for "is there a newer Anvil, and can it install itself".
//
// The card is deliberately explicit about the two things a user cannot guess:
// which version they are on, and whether this build is able to update in place.
// A `.deb` install cannot, and a button that downloads a file and then fails to
// install it is worse than a link to the release page.

import { useEffect, useSyncExternalStore } from 'react'
import { invokeHost } from '../host/tauri'
import { useI18n } from '../i18n/LanguageProvider'
import {
  checkForUpdate,
  dismissUpdateError,
  installUpdate,
  subscribeUpdates,
  updateSnapshot,
} from './store'

function bytes(value: number): string {
  if (value >= 1024 * 1024 * 1024) return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`
  if (value >= 1024) return `${Math.round(value / 1024)} KB`
  return `${value} B`
}

export default function UpdateCard() {
  const { t } = useI18n()
  const state = useSyncExternalStore(subscribeUpdates, updateSnapshot)

  // Opening the card is an intent to know, so it does not wait for the
  // background check that was scheduled at launch.
  useEffect(() => {
    if (updateSnapshot().status === 'idle') void checkForUpdate()
  }, [])

  const check = state.check
  const available = check?.available ?? null
  const canInstall = Boolean(check?.installable)
  const progress = state.progress
  const percent =
    progress?.total && progress.total > 0
      ? Math.min(100, Math.round((progress.received / progress.total) * 100))
      : null

  const statusLine = (() => {
    switch (state.status) {
      case 'checking':
        return t('settings.update.checking')
      case 'current':
        return t('settings.update.current', { version: check?.current ?? '' })
      case 'available':
        return t('settings.update.available', { version: available?.version ?? '' })
      case 'downloading':
        return percent === null
          ? t('settings.update.downloading', { size: bytes(progress?.received ?? 0) })
          : t('settings.update.downloadingPercent', {
              percent: String(percent),
              size: bytes(progress?.received ?? 0),
              total: bytes(progress?.total ?? 0),
            })
      case 'installing':
        return t('settings.update.installing')
      case 'installed':
        return t('settings.update.installed')
      case 'error':
        return state.error ?? t('settings.update.error')
      default:
        return t('settings.update.idle')
    }
  })()

  const reason = (() => {
    if (!available || canInstall) return null
    if (check?.reason === 'package') return t('settings.update.reasonPackage')
    if (check?.reason === 'development') return t('settings.update.reasonDevelopment')
    return t('settings.update.reasonPlatform')
  })()

  const openRelease = () => {
    const url = check?.releaseUrl
    if (!url) return
    void invokeHost('open_external', { url }).catch(() => undefined)
  }

  const action = (() => {
    if (state.status === 'downloading' || state.status === 'installing' || state.status === 'installed') {
      return (
        <button type="button" className="st-btn" disabled data-testid="update-busy">
          {t('settings.update.working')}
        </button>
      )
    }
    if (state.status === 'checking') {
      return (
        <button type="button" className="st-btn" disabled>
          {t('settings.update.checkingShort')}
        </button>
      )
    }
    if (available && !canInstall) {
      return (
        <button type="button" className="st-btn" data-testid="update-download" onClick={openRelease}>
          {t('settings.update.download')}
        </button>
      )
    }
    if (available) {
      return (
        <button
          type="button"
          className="st-btn st-btn-primary"
          data-testid="update-install"
          onClick={() => void installUpdate()}
        >
          {t('settings.update.install')}
        </button>
      )
    }
    if (state.status === 'error') {
      return (
        <button
          type="button"
          className="st-btn"
          data-testid="update-retry"
          onClick={() => {
            dismissUpdateError()
            void checkForUpdate()
          }}
        >
          {t('settings.update.retry')}
        </button>
      )
    }
    return (
      <button
        type="button"
        className="st-btn"
        data-testid="update-check"
        onClick={() => void checkForUpdate()}
      >
        {t('settings.update.check')}
      </button>
    )
  })()

  return (
    <div className="card" data-testid="settings-update">
      <div className="card-head">
        <h3>{t('settings.update.title')}</h3>
        <span className="card-head-sub">{check?.current ? `v${check.current}` : ''}</span>
        <div className="st-card-actions">{action}</div>
      </div>
      <div className="st-rows">
        <div className="st-row">
          <div className="st-row-main">
            <span className="st-row-title" data-testid="update-status">
              {statusLine}
            </span>
            {reason ? (
              <span className="st-row-sub" data-testid="update-reason">
                {reason}
              </span>
            ) : null}
            {progress && percent !== null && state.status === 'downloading' ? (
              <span
                className="st-progress"
                role="progressbar"
                aria-valuenow={percent}
                aria-valuemin={0}
                aria-valuemax={100}
              >
                <span className="st-progress-fill" style={{ width: `${percent}%` }} />
              </span>
            ) : null}
            {available?.notes ? (
              <details className="st-update-notes">
                <summary>{t('settings.update.notes', { version: available.version })}</summary>
                <pre>{available.notes}</pre>
              </details>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  )
}
