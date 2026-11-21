import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { pickWorkspaceFiles } from '../../workspace/client'
import { assetDetail, sampleSessions } from './sampleData'
import { previewAsset, revealAsset } from './client'

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

/** The page's unified row; the dialog only reads what it shows. */
export interface DialogAsset {
  id: string
  name: string
  title: string
  kind: string
  session: string
  meta: string
  tags: string[]
  path?: string
  source: string
  bytes: number
  at: string
  ext: string
}

export function AssetDetailDialog({
  asset,
  sample = false,
  onClose,
  onNotice,
  onExport,
}: {
  asset: DialogAsset | null
  sample?: boolean
  onClose: () => void
  onNotice: (message: string) => void
  onExport?: (path: string) => void
}) {
  const { t } = useI18n()
  const open = asset !== null
  const ref = useDialogKeys(open, onClose)
  const [preview, setPreview] = useState('')
  const [previewState, setPreviewState] = useState<'idle' | 'loading' | 'text' | 'binary' | 'error'>('idle')

  useEffect(() => {
    if (sample || !asset?.path) {
      setPreview('')
      setPreviewState('idle')
      return
    }
    let cancelled = false
    setPreviewState('loading')
    void previewAsset(asset.path)
      .then((text) => {
        if (cancelled) return
        setPreview(text)
        setPreviewState(text ? 'text' : 'binary')
      })
      .catch(() => {
        if (!cancelled) setPreviewState('error')
      })
    return () => {
      cancelled = true
    }
  }, [asset?.path, sample])

  if (sample) {
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

  if (!asset) return null

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
            <h2 id="lib-detail-title">{asset.name}</h2>
            <p>{asset.session ? `${asset.session} · ${asset.meta}` : asset.meta}</p>
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
                <dt>{t('library.detail.type')}</dt><dd>{asset.ext.toUpperCase() || asset.kind}</dd>
                <dt>{t('library.detail.session')}</dt><dd>{asset.session || '—'}</dd>
                <dt>{t('library.detail.source')}</dt><dd>{asset.source === 'library' ? t('library.tag.imported') : t('library.tag.session')}</dd>
                <dt>{t('library.detail.path')}</dt><dd className="mono lib-path">{asset.path ?? ''}</dd>
              </dl>
            </div>
            <div>
              <div className="lib-label">{t('library.detail.actions')}</div>
              <div className="lib-detail-actions">
                <button
                  type="button"
                  className="lib-btn"
                  onClick={() => {
                    if (!asset.path) return
                    void revealAsset(asset.path)
                      .then(() => onNotice(t('library.notice.revealed')))
                      .catch((error: unknown) => onNotice(t('library.notice.failed', { message: String(error) })))
                  }}
                >
                  <Icon name="external" size={14} />{t('library.detail.reveal')}
                </button>
                <button
                  type="button"
                  className="lib-btn"
                  onClick={() => {
                    if (!asset.path) return
                    void navigator.clipboard
                      .writeText(asset.path)
                      .then(() => onNotice(t('library.notice.copyPath')))
                      .catch(() => onNotice(t('library.notice.copyPath')))
                  }}
                >
                  <Icon name="copy" size={14} />{t('library.detail.copyPath')}
                </button>
                <button type="button" className="lib-btn" onClick={() => asset.path && onExport?.(asset.path)}>
                  <Icon name="download" size={14} />{t('library.detail.exportOne')}
                </button>
              </div>
            </div>
          </div>
          <div className="lib-label">{t('library.detail.preview')}</div>
          {previewState === 'text' ? (
            <pre className="lib-codeblock">{preview}</pre>
          ) : (
            <div className="lib-codeblock lib-codeblock-empty">
              {previewState === 'loading'
                ? t('library.preview.loading')
                : previewState === 'error'
                  ? t('library.preview.failed')
                  : t('library.preview.binary')}
            </div>
          )}
        </div>
        <div className="lib-dialog-foot">
          <button type="button" className="lib-btn lib-spread" onClick={onClose}>{t('library.detail.close')}</button>
          <button type="button" className="lib-btn lib-btn-primary" onClick={() => asset.path && onExport?.(asset.path)}>
            <Icon name="download" size={14} />{t('library.detail.download')}
          </button>
        </div>
      </div>
    </div>
  )
}

export function ImportAssetDialog({
  open,
  sample = false,
  root = '',
  busy = false,
  onClose,
  onNotice,
  onImport,
}: {
  open: boolean
  sample?: boolean
  root?: string
  busy?: boolean
  onClose: () => void
  onNotice: (message: string) => void
  onImport?: (paths: string[]) => void
}) {
  const { t } = useI18n()
  const ref = useDialogKeys(open, onClose)
  const [paths, setPaths] = useState<string[]>([])
  const [picking, setPicking] = useState(false)

  useEffect(() => {
    if (open) setPaths([])
  }, [open])

  if (!open) return null

  const pick = async () => {
    setPicking(true)
    try {
      const chosen = await pickWorkspaceFiles()
      setPaths(chosen)
      if (chosen.length === 0) onNotice(t('library.import.none'))
    } catch (error) {
      onNotice(t('library.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setPicking(false)
    }
  }

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
          {sample ? (
            <>
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
            </>
          ) : (
            <>
              <div className="lib-field">
                <span>{t('library.import.file')}</span>
                <button type="button" className="lib-btn" disabled={picking} onClick={() => void pick()}>
                  <Icon name="folder" size={14} />
                  {picking ? t('library.import.picking') : t('library.import.choose')}
                </button>
              </div>
              {paths.length > 0 ? (
                <ul className="lib-export-list">
                  {paths.map((path) => (
                    <li key={path} className="lib-export-item">
                      <Icon name="check" size={13} />
                      <span>{path.split(/[\\/]/).pop()}</span>
                    </li>
                  ))}
                </ul>
              ) : null}
              <p className="lib-field-hint">{root ? t('library.import.root', { path: root }) : t('library.import.rootUnknown')}</p>
            </>
          )}
          <div className="lib-notice"><Icon name="shield" size={14} /><span>{t('library.import.footnote')}</span></div>
        </div>
        <div className="lib-dialog-foot">
          <button type="button" className="lib-btn" onClick={onClose}>{t('library.form.cancel')}</button>
          <button
            type="button"
            className="lib-btn lib-btn-primary"
            disabled={busy}
            onClick={() => {
              if (sample) {
                onClose()
                onNotice(t('library.notice.import'))
                return
              }
              if (paths.length === 0) {
                onNotice(t('library.import.none'))
                return
              }
              onImport?.(paths)
              onClose()
            }}
          >
            {t('library.import.submit')}
          </button>
        </div>
      </div>
    </div>
  )
}

export function ExportAssetsDialog({
  open,
  sample = false,
  assets,
  busy = false,
  onClose,
  onNotice,
  onExport,
}: {
  open: boolean
  sample?: boolean
  assets: DialogAsset[]
  busy?: boolean
  onClose: () => void
  onNotice: (message: string) => void
  onExport?: (paths: string[]) => void
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
          {sample ? (
            <label className="lib-field">
              <span>{t('library.export.format')}</span>
              <select aria-label={t('library.export.format')}>
                <option>{t('library.export.zip')}</option>
                <option>{t('library.export.folder')}</option>
              </select>
            </label>
          ) : (
            <p className="lib-field-hint">{t('library.export.hint')}</p>
          )}
        </div>
        <div className="lib-dialog-foot">
          <button type="button" className="lib-btn" onClick={onClose}>{t('library.form.cancel')}</button>
          <button
            type="button"
            className="lib-btn lib-btn-primary"
            disabled={assets.length === 0 || busy}
            onClick={() => {
              if (sample) {
                onClose()
                message('library.notice.export')
                return
              }
              const paths = assets.map((asset) => asset.path).filter((path): path is string => Boolean(path))
              if (paths.length === 0) {
                message('library.notice.exportEmpty')
                return
              }
              onExport?.(paths)
              onClose()
            }}
          >
            {t('library.export.submit')}
          </button>
        </div>
      </div>
    </div>
  )
}
