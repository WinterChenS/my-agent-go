import { useEffect, useRef } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { assetDetail, sampleSessions, type AssetRecord } from './sampleData'

function useDialogKeys(open: boolean, onClose: () => void) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const root = ref.current
    if (!root) return
    const focusables = () =>
      Array.from(
        root.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      )
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
  }, [open, onClose])
  return ref
}

export function AssetDetailDialog({
  open,
  onClose,
  onNotice,
}: {
  open: boolean
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useDialogKeys(open, onClose)
  if (!open) return null
  return (
    <div
      className="lib-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div ref={ref} className="lib-dialog lib-dialog-wide" role="dialog" aria-modal="true" aria-labelledby="lib-detail-title">
        <div className="lib-dialog-head">
          <div>
            <h2 id="lib-detail-title">{assetDetail.name}</h2>
            <p>{assetDetail.subtitle}</p>
          </div>
          <button type="button" className="lib-iconbtn" aria-label={t('library.form.dismiss')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="lib-dialog-body">
          <div className="lib-detail-grid">
            <div>
              <div className="lib-label">{t('library.detail.properties')}</div>
              <dl className="lib-dl">
                <dt>{t('library.detail.type')}</dt><dd>{assetDetail.typeLabel}</dd>
                <dt>{t('library.detail.session')}</dt><dd>{assetDetail.session}</dd>
                <dt>{t('library.detail.origin')}</dt><dd>{assetDetail.origin}</dd>
                <dt>{t('library.detail.referencedBy')}</dt><dd>{assetDetail.references}</dd>
              </dl>
            </div>
            <div>
              <div className="lib-label">{t('library.detail.actions')}</div>
              <div className="lib-detail-actions">
                <button type="button" className="lib-btn" onClick={() => onNotice(t('library.notice.open'))}>
                  <Icon name="external" size={14} />{t('library.detail.openTab')}
                </button>
                <button type="button" className="lib-btn" onClick={() => onNotice(t('library.notice.copyLink'))}>
                  <Icon name="copy" size={14} />{t('library.detail.copyLink')}
                </button>
                <button type="button" className="lib-btn" onClick={() => onNotice(t('library.notice.addContext'))}>
                  <Icon name="message" size={14} />{t('library.detail.addContext')}
                </button>
              </div>
            </div>
          </div>
          <div className="lib-label">{t('library.detail.preview')}</div>
          <pre className="lib-codeblock">{assetDetail.preview.join('\n')}</pre>
        </div>
        <div className="lib-dialog-foot">
          <button type="button" className="lib-btn lib-spread" onClick={onClose}>{t('library.detail.close')}</button>
          <button type="button" className="lib-btn" onClick={() => onNotice(t('library.notice.download'))}>
            <Icon name="download" size={14} />{t('library.detail.download')}
          </button>
          <button type="button" className="lib-btn lib-btn-primary" onClick={() => { onClose(); onNotice(t('library.notice.archive')) }}>
            {t('library.detail.archive')}
          </button>
        </div>
      </div>
    </div>
  )
}

export function ImportAssetDialog({
  open,
  onClose,
  onNotice,
}: {
  open: boolean
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useDialogKeys(open, onClose)
  if (!open) return null
  return (
    <div
      className="lib-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div ref={ref} className="lib-dialog" role="dialog" aria-modal="true" aria-labelledby="lib-import-title">
        <div className="lib-dialog-head">
          <div>
            <h2 id="lib-import-title">{t('library.import.title')}</h2>
            <p>{t('library.import.description')}</p>
          </div>
          <button type="button" className="lib-iconbtn" aria-label={t('library.form.dismiss')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="lib-dialog-body">
          <label className="lib-field">
            <span>{t('library.import.file')}</span>
            <input type="file" multiple aria-label={t('library.import.file')} />
          </label>
          <label className="lib-field">
            <span>{t('library.import.session')}</span>
            <select aria-label={t('library.import.session')}>
              <option>{t('library.import.noSession')}</option>
              {sampleSessions.map((session) => <option key={session}>{session}</option>)}
            </select>
          </label>
          <div className="lib-notice"><Icon name="shield" size={14} /><span>{t('library.import.footnote')}</span></div>
        </div>
        <div className="lib-dialog-foot">
          <button type="button" className="lib-btn" onClick={onClose}>{t('library.form.cancel')}</button>
          <button type="button" className="lib-btn lib-btn-primary" onClick={() => { onClose(); onNotice(t('library.notice.import')) }}>
            {t('library.import.submit')}
          </button>
        </div>
      </div>
    </div>
  )
}

export function ExportAssetsDialog({
  open,
  assets,
  onClose,
  onNotice,
}: {
  open: boolean
  assets: AssetRecord[]
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const ref = useDialogKeys(open, onClose)
  if (!open) return null
  const message = (key: MessageKey) => onNotice(t(key))
  return (
    <div
      className="lib-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div ref={ref} className="lib-dialog" role="dialog" aria-modal="true" aria-labelledby="lib-export-title">
        <div className="lib-dialog-head">
          <div>
            <h2 id="lib-export-title">{t('library.export.title')}</h2>
            <p>{assets.length > 0 ? t('library.export.subtitle', { count: assets.length }) : t('library.export.empty')}</p>
          </div>
          <button type="button" className="lib-iconbtn" aria-label={t('library.form.dismiss')} onClick={onClose}>
            <Icon name="x" size={15} />
          </button>
        </div>
        <div className="lib-dialog-body">
          {assets.length > 0 ? (
            <ul className="lib-export-list">
              {assets.map((asset) => (
                <li key={asset.id} className="lib-export-item">
                  <Icon name="check" size={13} />
                  <span>{asset.name}</span>
                  <small>{asset.meta}</small>
                </li>
              ))}
            </ul>
          ) : null}
          <label className="lib-field">
            <span>{t('library.export.format')}</span>
            <select aria-label={t('library.export.format')}>
              <option>{t('library.export.zip')}</option>
              <option>{t('library.export.folder')}</option>
            </select>
          </label>
        </div>
        <div className="lib-dialog-foot">
          <button type="button" className="lib-btn" onClick={onClose}>{t('library.form.cancel')}</button>
          <button type="button" className="lib-btn lib-btn-primary" disabled={assets.length === 0} onClick={() => { onClose(); message('library.notice.export') }}>
            {t('library.export.submit')}
          </button>
        </div>
      </div>
    </div>
  )
}
