import { useRef, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { AssetDetailDialog, ExportAssetsDialog, ImportAssetDialog } from './LibraryDialogs'
import {
  sampleAssets,
  sampleLibraryStats,
  sampleSessions,
  sampleTabs,
  useLibrarySample,
  type AssetRecord,
  type AssetType,
  type AssetView,
} from './sampleData'
import './library.css'

const TYPE_KEYS: Record<AssetType, MessageKey> = {
  doc: 'library.type.doc',
  sheet: 'library.type.sheet',
  page: 'library.type.page',
  chart: 'library.type.chart',
  code: 'library.type.code',
}

const STAT_LABEL_KEYS: Record<(typeof sampleLibraryStats)[number]['key'], MessageKey> = {
  assets: 'library.stats.assets',
  storage: 'library.stats.storage',
  references: 'library.stats.references',
  retention: 'library.stats.retention',
}

export default function LibraryPage() {
  const { t } = useI18n()
  const sample = useLibrarySample()
  const [search, setSearch] = useState('')
  const [type, setType] = useState<'all' | AssetType>('all')
  const [session, setSession] = useState('all')
  const [view, setView] = useState<AssetView>('grid')
  const [tab, setTab] = useState<'all' | AssetType>('all')
  const [selected, setSelected] = useState<string[]>(['migration-plan', 'compat-matrix'])
  const [notice, setNotice] = useState('')
  const [detail, setDetail] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const trigger = useRef<HTMLElement | null>(null)

  const query = search.trim().toLowerCase()
  const effectiveType: 'all' | AssetType = tab !== 'all' ? tab : type
  const assets = sample
    ? sampleAssets.filter((asset) => {
        const matchesSearch = !query || `${asset.name} ${asset.title} ${asset.session}`.toLowerCase().includes(query)
        const matchesType = effectiveType === 'all' || asset.type === effectiveType
        const matchesSession = session === 'all' || asset.session === session
        return matchesSearch && matchesType && matchesSession
      })
    : []
  const selectedAssets = sample ? sampleAssets.filter((asset) => selected.includes(asset.id)) : []

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
            disabled={!sample}
            title={sample ? t('library.upload') : t('library.action.unavailable')}
            onClick={(event) => open(setImportOpen, event.currentTarget)}
          >
            <Icon name="upload" size={14} />
            {t('library.upload')}
          </button>
          <button
            type="button"
            className="lib-btn lib-btn-primary"
            data-testid="library-export"
            disabled={!sample}
            title={sample ? t('library.export') : t('library.action.unavailable')}
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
      {notice ? <div className="lib-notice-bar" data-testid="library-notice" role="status">{notice}</div> : null}

      <section className="lib-stats" data-testid="library-stats" aria-label={t('library.title')}>
        {sampleLibraryStats.map((stat) => (
          <div key={stat.key} className="lib-stat" data-testid={`library-stat-${stat.key}`}>
            <span>{t(STAT_LABEL_KEYS[stat.key])}</span>
            <b>{sample ? stat.value : stat.key === 'retention' ? '—' : '0'}</b>
            <small>{sample ? t(stat.subKey as MessageKey) : t('library.stats.empty')}</small>
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
              disabled={!sample}
              placeholder={t('library.searchPlaceholder')}
              aria-label={t('library.searchPlaceholder')}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <select aria-label={t('library.filter.type')} value={type} disabled={!sample} onChange={(event) => { setType(event.target.value as typeof type); setTab('all') }}>
            <option value="all">{t('library.filter.allTypes')}</option>
            {(Object.keys(TYPE_KEYS) as AssetType[]).map((value) => (
              <option key={value} value={value}>{t(TYPE_KEYS[value])}</option>
            ))}
          </select>
          <select aria-label={t('library.filter.session')} value={session} disabled={!sample} onChange={(event) => setSession(event.target.value)}>
            <option value="all">{t('library.filter.allSessions')}</option>
            {sampleSessions.map((value) => <option key={value} value={value}>{value}</option>)}
          </select>
          <span className="lib-tag">{t('library.filter.recent')}</span>
          <span className="lib-toolbar-spacer" />
          <div className="lib-segmented" role="group" aria-label={t('library.view')}>
            <button type="button" aria-pressed={view === 'grid'} className={view === 'grid' ? 'is-active' : ''} onClick={() => setView('grid')} disabled={!sample}>{t('library.view.grid')}</button>
            <button type="button" aria-pressed={view === 'list'} className={view === 'list' ? 'is-active' : ''} onClick={() => setView('list')} disabled={!sample}>{t('library.view.list')}</button>
          </div>
        </div>

        <div className="lib-tabs" role="tablist" aria-label={t('library.title')}>
          {sampleTabs.map((entry) => (
            <button
              key={entry.id}
              type="button"
              role="tab"
              aria-selected={tab === entry.id}
              className={`lib-tab${tab === entry.id ? ' is-active' : ''}`}
              onClick={() => { setTab(entry.id); setType('all') }}
            >
              {entry.id === 'all' ? t('library.tab.all') : t(TYPE_KEYS[entry.id])}
              <span className="lib-tab-count">{sample ? entry.count : 0}</span>
            </button>
          ))}
        </div>

        {sample ? (
          assets.length > 0 ? (
            <div className={`lib-assets is-${view}`} data-testid="library-assets">
              {assets.map((asset) => (
                <article key={asset.id} className="lib-card" data-testid="library-card" data-asset={asset.id}>
                  <button type="button" className="lib-card-open" aria-label={t('library.card.open', { name: asset.name })} onClick={(event) => open(setDetail, event.currentTarget)}>
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
        <span>{t('library.footnote')}</span>
      </div>

      <AssetDetailDialog open={detail} onClose={() => close(setDetail)} onNotice={announce} />
      <ImportAssetDialog open={importOpen} onClose={() => close(setImportOpen)} onNotice={announce} />
      <ExportAssetsDialog open={exportOpen} assets={selectedAssets} onClose={() => close(setExportOpen)} onNotice={announce} />
    </section>
  )
}

function AssetThumb({ asset }: { asset: AssetRecord }) {
  const { t } = useI18n()
  return (
    <span className="lib-thumb" aria-hidden="true" data-testid="library-thumb">
      <span className="lib-thumb-page">
        <span className="lib-thumb-title">{asset.previewTitle}</span>
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
      </span>
    </span>
  )
}
