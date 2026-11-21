import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { AssetDetailDialog, ExportAssetsDialog, ImportAssetDialog } from './LibraryDialogs'
import {
  sampleAssets,
  sampleLibraryStats,
  sampleTabs,
  useLibrarySample,
  type AssetType,
  type AssetView as AssetLayout,
} from './sampleData'
import {
  exportLibrary,
  formatBytes,
  hasLibraryHost,
  importLibrary,
  loadLibrary,
  type LibraryAsset,
  type LibraryView,
} from './client'
import './library.css'

const TYPE_KEYS: Record<AssetType, MessageKey> = {
  doc: 'library.type.doc',
  sheet: 'library.type.sheet',
  page: 'library.type.page',
  chart: 'library.type.chart',
  code: 'library.type.code',
}

type StatKey = 'assets' | 'storage' | 'references' | 'retention'

interface AssetView {
  id: string
  name: string
  title: string
  kind: AssetType | 'other'
  session: string
  meta: string
  tags: string[]
  references?: number
  preview: 'page' | 'table' | 'bars' | 'code'
  previewTitle: string
  previewLines?: string[]
  path?: string
  source: string
  bytes: number
  at: string
  ext: string
}

/** Extensions shown as a typed glyph rather than a page mock. */
function previewFor(kind: AssetType | 'other'): 'page' | 'table' | 'bars' | 'code' {
  switch (kind) {
    case 'sheet':
      return 'table'
    case 'chart':
      return 'bars'
    case 'code':
      return 'code'
    default:
      return 'page'
  }
}

function kindOfAsset(row: LibraryAsset): AssetType | 'other' {
  return row.kind === 'doc' || row.kind === 'sheet' || row.kind === 'page' || row.kind === 'chart' || row.kind === 'code'
    ? row.kind
    : 'other'
}

function relativeTime(stamp: string, language: string): string {
  const time = Date.parse(stamp)
  if (!Number.isFinite(time)) return ''
  const seconds = Math.round((time - Date.now()) / 1000)
  const absolute = Math.abs(seconds)
  const relative = new Intl.RelativeTimeFormat(language, { numeric: 'auto' })
  if (absolute < 60) return relative.format(seconds, 'second')
  if (absolute < 3600) return relative.format(Math.round(seconds / 60), 'minute')
  if (absolute < 86_400) return relative.format(Math.round(seconds / 3600), 'hour')
  return relative.format(Math.round(seconds / 86_400), 'day')
}

export default function LibraryPage() {
  const { t, language } = useI18n()
  const sample = useLibrarySample()
  const host = hasLibraryHost()
  const [search, setSearch] = useState('')
  const [type, setType] = useState<'all' | AssetType>('all')
  const [session, setSession] = useState('all')
  const [view, setView] = useState<AssetLayout>('grid')
  const [tab, setTab] = useState<'all' | AssetType>('all')
  // The sample keeps the prototype's two preselected rows; live starts empty.
  const [selected, setSelected] = useState<string[]>(() => (sample ? ['migration-plan', 'compat-matrix'] : []))
  const [notice, setNotice] = useState('')
  const [failure, setFailure] = useState('')
  const [detail, setDetail] = useState<AssetView | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const [view_data, setViewData] = useState<LibraryView | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const trigger = useRef<HTMLElement | null>(null)

  const reload = useCallback(async () => {
    if (sample || !host) return
    setLoading(true)
    try {
      const next = await loadLibrary()
      setViewData(next)
      setFailure('')
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }, [host, sample])

  useEffect(() => {
    if (sample || !host) return
    void reload()
  }, [host, reload, sample])

  const assets: AssetView[] = useMemo(() => {
    if (sample) {
      return sampleAssets.map((asset) => ({
        id: asset.id,
        name: asset.name,
        title: asset.title,
        kind: asset.type,
        session: asset.session,
        meta: asset.meta,
        tags: asset.tags,
        references: asset.references,
        preview: asset.preview,
        previewTitle: asset.previewTitle,
        previewLines: asset.previewLines,
        source: 'session',
        bytes: 0,
        at: '',
        ext: asset.name.split('.').pop()?.toUpperCase() ?? '',
      }))
    }
    return (view_data?.assets ?? []).map((row) => {
      const kind = kindOfAsset(row)
      return {
        id: row.id,
        name: row.name,
        title: row.name,
        kind,
        session: row.session || t('library.source.imported'),
        meta: `${formatBytes(row.bytes)} · ${relativeTime(row.at, language)}`,
        tags: [row.ext.toUpperCase() || t('library.type.other'), row.source === 'library' ? t('library.tag.imported') : t('library.tag.session')],
        preview: previewFor(kind),
        previewTitle: row.name,
        previewLines: kind === 'page' ? ['w70', 'w85', 'w50'] : undefined,
        path: row.path,
        source: row.source,
        bytes: row.bytes,
        at: row.at,
        ext: row.ext,
      }
    })
  }, [language, sample, t, view_data])

  const stats = useMemo<
    Array<{ key: StatKey; labelKey: MessageKey; value: string; sub: string }>
  >(() => {
    const rows: Array<{ key: StatKey; labelKey: MessageKey; value: string; sub: string }> = []
    if (sample) {
      const labels: Record<StatKey, MessageKey> = {
        assets: 'library.stats.assets',
        storage: 'library.stats.storage',
        references: 'library.stats.references',
        retention: 'library.stats.retention',
      }
      for (const stat of sampleLibraryStats) {
        rows.push({
          key: stat.key,
          labelKey: labels[stat.key],
          value: stat.value,
          sub: t(stat.subKey as MessageKey),
        })
      }
      return rows
    }
    const live = view_data?.stats
    const newest = view_data?.assets.reduce<string>((latest, row) => (row.at > latest ? row.at : latest), '')
    return [
      {
        key: 'assets',
        labelKey: 'library.stats.assets',
        value: String(live?.assets ?? 0),
        sub: live && live.weekAdded > 0
          ? t('library.stats.assetsSubLive', { count: live.weekAdded })
          : t('library.stats.empty'),
      },
      {
        key: 'storage',
        labelKey: 'library.stats.storage',
        value: formatBytes(live?.bytes ?? 0),
        sub: live && live.imported > 0
          ? t('library.stats.storageSubLive', { count: live.imported })
          : live && live.sessions > 0
            ? t('library.stats.storageSubSession', { count: live.sessions })
            : t('library.stats.empty'),
      },
      {
        key: 'references',
        labelKey: 'library.stats.sessionsLive',
        value: String(live?.sessions ?? 0),
        sub: live && live.sessions > 0 ? t('library.stats.sessionsSubLive') : t('library.stats.empty'),
      },
      {
        key: 'retention',
        labelKey: 'library.stats.updatedLive',
        value: newest ? relativeTime(newest, language) : '—',
        sub: t('library.stats.updatedSubLive'),
      },
    ]
  }, [language, sample, t, view_data])

  const tabs = useMemo(() => {
    if (sample) return sampleTabs
    const counts = view_data?.stats.byKind ?? {}
    return [
      { id: 'all' as const, count: view_data?.stats.assets ?? 0 },
      ...(Object.keys(TYPE_KEYS) as AssetType[]).map((kind) => ({ id: kind, count: counts[kind] ?? 0 })),
    ]
  }, [sample, view_data])

  const sessions = useMemo(() => {
    if (sample) return ['API 迁移评估', '周报数据核对', '竞品定价整理', '客户工单聚类']
    return Array.from(new Set(assets.map((asset) => asset.session).filter(Boolean))).sort()
  }, [assets, sample])

  const query = search.trim().toLowerCase()
  const effectiveType: 'all' | AssetType = tab !== 'all' ? tab : type
  const filtered = assets.filter((asset) => {
    const matchesSearch = !query || `${asset.name} ${asset.title} ${asset.session}`.toLowerCase().includes(query)
    const matchesType = effectiveType === 'all' || asset.kind === effectiveType
    const matchesSession = session === 'all' || asset.session === session
    return matchesSearch && matchesType && matchesSession
  })
  const selectedAssets = assets.filter((asset) => selected.includes(asset.id))

  const announce = (message: string) => setNotice(message)
  const open = (setter: (value: boolean) => void, element: HTMLElement) => {
    trigger.current = element
    setter(true)
  }
  const close = (setter: (value: boolean) => void) => {
    setter(false)
    const element = trigger.current
    window.setTimeout(() => element?.focus(), 0)
  }
  const toggleSelected = (id: string) =>
    setSelected((current) => (current.includes(id) ? current.filter((item) => item !== id) : [...current, id]))

  const onImport = async (paths: string[]) => {
    if (sample) {
      announce(t('library.notice.import'))
      return
    }
    setBusy(true)
    try {
      const report = await importLibrary(paths)
      announce(t('library.notice.imported', { count: report.imported.length }))
      await reload()
    } catch (error) {
      announce(t('library.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy(false)
    }
  }

  const onExport = async (paths: string[]) => {
    if (sample) {
      announce(t('library.notice.export'))
      return
    }
    setBusy(true)
    try {
      const directory = await exportLibrary(paths)
      announce(t('library.notice.exported', { path: directory }))
    } catch (error) {
      announce(t('library.notice.failed', { message: error instanceof Error ? error.message : String(error) }))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="lib-page" data-testid="library-page" data-sample={sample ? 'true' : 'false'}>
      <span className="lib-sr-only" id="route-marker" data-testid="page-marker">
        R0-11 library · /library
      </span>
      <div className="lib-page-head">
        <div>
          <h1>{t('library.title')}</h1>
          <p>{t('library.description')}</p>
        </div>
        <div className="lib-head-actions">
          <button
            type="button"
            className="lib-btn"
            data-testid="library-upload"
            disabled={!sample && !host}
            title={sample || host ? t('library.upload') : t('library.action.unavailable')}
            onClick={(event) => open(setImportOpen, event.currentTarget)}
          >
            <Icon name="upload" size={14} />
            {t('library.upload')}
          </button>
          <button
            type="button"
            className="lib-btn lib-btn-primary"
            data-testid="library-export"
            disabled={!sample && (!host || selected.length === 0)}
            title={sample || host ? t('library.export') : t('library.action.unavailable')}
            onClick={(event) => open(setExportOpen, event.currentTarget)}
          >
            <Icon name="download" size={14} />
            {t('library.export')}
          </button>
        </div>
      </div>

      {sample ? (
        <div className="lib-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('library.sampleBanner')}
        </div>
      ) : null}
      {failure ? <div className="lib-notice-bar" data-testid="library-error" role="alert">{failure}</div> : null}
      {notice ? <div className="lib-notice-bar" data-testid="library-notice" role="status">{notice}</div> : null}

      <section className="lib-stats" data-testid="library-stats" aria-label={t('library.title')}>
        {stats.map((stat) => (
          <div key={stat.key} className="lib-stat" data-testid={`library-stat-${stat.key}`}>
            <span>{t(stat.labelKey)}</span>
            <b>{stat.value}</b>
            <small>{stat.sub}</small>
          </div>
        ))}
      </section>

      <section className="lib-panel" data-testid="library-browser">
        <div className="lib-toolbar">
          <label className="lib-search">
            <Icon name="search" size={12} />
            <input
              data-testid="library-search"
              type="search"
              value={search}
              placeholder={t('library.searchPlaceholder')}
              aria-label={t('library.searchPlaceholder')}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <select aria-label={t('library.filter.type')} value={type} onChange={(event) => { setType(event.target.value as typeof type); setTab('all') }}>
            <option value="all">{t('library.filter.allTypes')}</option>
            {(Object.keys(TYPE_KEYS) as AssetType[]).map((value) => (
              <option key={value} value={value}>{t(TYPE_KEYS[value])}</option>
            ))}
          </select>
          <select aria-label={t('library.filter.session')} value={session} onChange={(event) => setSession(event.target.value)}>
            <option value="all">{t('library.filter.allSessions')}</option>
            {sessions.map((value) => <option key={value} value={value}>{value}</option>)}
          </select>
          <span className="lib-tag">{loading ? t('library.loading') : t('library.filter.recent')}</span>
          <span className="lib-toolbar-spacer" />
          <div className="lib-segmented" role="group" aria-label={t('library.view')}>
            <button type="button" aria-pressed={view === 'grid'} className={view === 'grid' ? 'is-active' : ''} onClick={() => setView('grid')}>{t('library.view.grid')}</button>
            <button type="button" aria-pressed={view === 'list'} className={view === 'list' ? 'is-active' : ''} onClick={() => setView('list')}>{t('library.view.list')}</button>
          </div>
        </div>

        <div className="lib-tabs" role="tablist" aria-label={t('library.title')}>
          {tabs.map((entry) => (
            <button
              key={entry.id}
              type="button"
              role="tab"
              aria-selected={tab === entry.id}
              className={`lib-tab${tab === entry.id ? ' is-active' : ''}`}
              onClick={() => { setTab(entry.id); setType('all') }}
            >
              {entry.id === 'all' ? t('library.tab.all') : t(TYPE_KEYS[entry.id])}
              <span className="lib-tab-count">{entry.count}</span>
            </button>
          ))}
        </div>

        {assets.length > 0 ? (
          filtered.length > 0 ? (
            <div className={`lib-assets is-${view}`} data-testid="library-assets">
              {filtered.map((asset) => (
                <article key={asset.id} className="lib-card" data-testid="library-card" data-asset={asset.id}>
                  <button type="button" className="lib-card-open" aria-label={t('library.card.open', { name: asset.name })} onClick={() => setDetail(asset)}>
                    <AssetThumb asset={asset} />
                    <span className="lib-meta">
                      <span className="lib-name">{asset.name}</span>
                      <span className="lib-sub">{asset.session} · {asset.meta}</span>
                      <span className="lib-tags">
                        {asset.tags.map((tag) => <span key={tag} className="lib-tag">{tag}</span>)}
                        {asset.references ? <span className="lib-ref">{t('library.references', { count: asset.references })}</span> : null}
                      </span>
                    </span>
                  </button>
                  <label className="lib-select">
                    <input
                      type="checkbox"
                      checked={selected.includes(asset.id)}
                      aria-label={t('library.card.select', { name: asset.name })}
                      onChange={() => toggleSelected(asset.id)}
                    />
                    <span>{t('library.card.selectShort')}</span>
                  </label>
                </article>
              ))}
            </div>
          ) : (
            <div className="lib-empty">{t('library.noResults')}</div>
          )
        ) : (
          <div className="lib-empty" data-testid="library-empty">
            <Icon name="folder" size={22} />
            <b>{t('library.empty')}</b>
            <span>{t('library.emptyBody')}</span>
          </div>
        )}
      </section>

      <div className="lib-footnote">
        <Icon name="alert" size={14} />
        <span>{sample ? t('library.footnote') : t('library.footnoteLive')}</span>
      </div>

      <AssetDetailDialog
        asset={detail}
        sample={sample}
        onClose={() => setDetail(null)}
        onNotice={announce}
        onExport={(path) => void onExport([path])}
      />
      <ImportAssetDialog
        open={importOpen}
        sample={sample}
        root={view_data?.root ?? ''}
        busy={busy}
        onClose={() => close(setImportOpen)}
        onNotice={announce}
        onImport={(paths) => void onImport(paths)}
      />
      <ExportAssetsDialog
        open={exportOpen}
        sample={sample}
        assets={selectedAssets}
        busy={busy}
        onClose={() => close(setExportOpen)}
        onNotice={announce}
        onExport={(paths) => void onExport(paths)}
      />
    </section>
  )
}

function AssetThumb({ asset }: { asset: AssetView }) {
  const { t } = useI18n()
  // A real file has no prototype mock: show its name and a neutral page glyph
  // rather than pretending to render its content.
  const live = Boolean(asset.path)
  return (
    <span className="lib-thumb" aria-hidden="true" data-testid="library-thumb">
      <span className="lib-thumb-page">
        <span className="lib-thumb-title">{asset.previewTitle}</span>
        {live ? (
          <span className="lib-lines">
            {['w85', 'w70', 'w50'].map((width, index) => (
              <span key={`${width}-${index}`} className={`lib-line ${width}`} />
            ))}
          </span>
        ) : (
          <>
            {asset.preview === 'page' ? (
              <span className="lib-lines">
                {(asset.previewLines ?? ['w70', 'w85', 'w50']).map((width, index) => (
                  <span key={`${width}-${index}`} className={`lib-line ${width}`} />
                ))}
              </span>
            ) : null}
            {asset.preview === 'table' ? (
              <span className="lib-mini-table">
                <span className="lib-mini-row is-head"><span>{t('library.thumb.field')}</span><span>v1</span><span>v2</span></span>
                {(asset.previewLines ?? []).map((line) => {
                  const cells = line.split(' · ')
                  return (
                    <span key={line} className="lib-mini-row">
                      {cells.map((cell) => <span key={cell}>{cell}</span>)}
                    </span>
                  )
                })}
              </span>
            ) : null}
            {asset.preview === 'bars' ? (
              <span className="lib-bars">
                {[38, 52, 46, 64, 58, 72, 66].map((height, index) => (
                  <i key={height} className={index > 4 ? 'is-light' : ''} style={{ height: `${height}%` }} />
                ))}
              </span>
            ) : null}
            {asset.preview === 'code' ? (
              <span className="lib-mini-code">
                {(asset.previewLines ?? []).map((line) => <span key={line}>{line}</span>)}
              </span>
            ) : null}
          </>
        )}
      </span>
    </span>
  )
}
