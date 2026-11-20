import { useEffect, useRef, useState } from 'react'
import type { RailExec, RailView } from '../../chat/client'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { sampleChat } from './sampleData'

export type RailTab = 'browser' | 'artifacts' | 'sources' | 'exec'

const TABS: Array<{ id: RailTab; icon: string; key: 'chat.rail.browser' | 'chat.rail.artifacts' | 'chat.rail.sources' | 'chat.rail.exec' }> = [
  { id: 'browser', icon: 'globe', key: 'chat.rail.browser' },
  { id: 'artifacts', icon: 'file', key: 'chat.rail.artifacts' },
  { id: 'sources', icon: 'link', key: 'chat.rail.sources' },
  { id: 'exec', icon: 'terminal', key: 'chat.rail.exec' },
]

/** What the live rail needs from the page. */
export interface RailLive {
  view: RailView | null
  /** A turn is in flight: a call without a result is pending, not lost. */
  running: boolean
  /** Show one written file in place instead of leaving the pane. */
  onPreview?: (path: string) => void
  /** The embedded browser: a real webview the app places over the pane. */
  browser?: {
    open: (url: string) => void
    close: () => void
    action: (method: 'back' | 'forward' | 'reload') => void
    bounds: (rect: { x: number; y: number; w: number; h: number }) => void
    url: string
  }
  /**
   * Send the reader back to the turn a row came from.
   *
   * `turn` is the 1-based index the row already prints. It is the fallback
   * when the run id has no element yet — a turn that is still starting has a
   * stored run id but the bubble on screen is the optimistic one.
   */
  onTurn: (runId: string, turn?: number) => void
}

export default function ChatRail({
  sample,
  live,
  open,
  tab,
  onTab,
  onClose,
  onNotice,
}: {
  sample: boolean
  live?: RailLive
  open: boolean
  tab: RailTab
  onTab: (tab: RailTab) => void
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const view = live?.view ?? null
  const counts = sample
    ? { browser: 0, artifacts: 3, sources: 4, exec: 0 }
    : {
        browser: view?.browser.state === 'local' ? 1 : 0,
        artifacts: view?.artifacts.length ?? 0,
        // "引用" is the pane's whole subject: the attached files and the cited
        // URLs, so the number on the tab is what the pane lists.
        sources: (view?.references.length ?? 0) + (view?.sources.length ?? 0),
        exec: view?.exec.length ?? 0,
      }

  return (
    <aside
      id="chat-rail"
      data-testid="chat-rail"
      className={`chat-rail${open ? ' is-open' : ''}`}
      aria-label={t('chat.rail.label')}
    >
      <div className="chat-rail-handle" aria-hidden="true">
        <span />
      </div>
      <div className="chat-rail-head">
        <div className="chat-rail-tabs" role="tablist" aria-label={t('chat.rail.label')}>
          {TABS.map((entry) => (
            <button
              key={entry.id}
              type="button"
              role="tab"
              id={`chat-rail-tab-${entry.id}`}
              aria-selected={tab === entry.id}
              aria-controls={`chat-rail-pane-${entry.id}`}
              className={`chat-rail-tab${tab === entry.id ? ' is-active' : ''}`}
              onClick={() => onTab(entry.id)}
            >
              <Icon name={entry.icon} size={13} />
              <span>{t(entry.key, { count: counts[entry.id] })}</span>
            </button>
          ))}
        </div>
        <button type="button" className="chat-iconbtn" aria-label={t('chat.rail.close')} onClick={onClose}>
          <Icon name="x" size={14} />
        </button>
      </div>

      <div className="chat-rail-body">
        {!sample && !view && tab !== 'browser' ? (
          <div className="chat-rail-empty" data-testid="chat-rail-pending">
            <Icon name="panel" size={20} />
            {/* A turn in flight has data on the way; saying "no data" then is a
                claim about the conversation, not about the read. */}
            <p>{live?.running ? t('chat.rail.loading') : t('chat.rail.empty')}</p>
          </div>
        ) : null}

        {!sample && view && tab === 'exec' ? (
          <div role="tabpanel" id="chat-rail-pane-exec" aria-labelledby="chat-rail-tab-exec" className="chat-rail-pane">
            <LiveExec view={view} running={live?.running ?? false} onTurn={live?.onTurn} />
          </div>
        ) : null}

        {!sample && view && tab === 'artifacts' ? (
          <div role="tabpanel" id="chat-rail-pane-artifacts" aria-labelledby="chat-rail-tab-artifacts" className="chat-rail-pane">
            <LiveArtifacts view={view} onTurn={live?.onTurn} onPreview={live?.onPreview} />
          </div>
        ) : null}

        {!sample && view && tab === 'sources' ? (
          <div role="tabpanel" id="chat-rail-pane-sources" aria-labelledby="chat-rail-tab-sources" className="chat-rail-pane">
            <LiveSources view={view} onTurn={live?.onTurn} />
          </div>
        ) : null}

        {!sample && tab === 'browser' ? (
          <div
            role="tabpanel"
            id="chat-rail-pane-browser"
            aria-labelledby="chat-rail-tab-browser"
            className="chat-rail-pane is-browser"
          >
            <LiveBrowser view={view} browser={live?.browser} />
          </div>
        ) : null}

        {sample && tab === 'browser' ? (
          <div role="tabpanel" id="chat-rail-pane-browser" aria-labelledby="chat-rail-tab-browser" className="chat-rail-pane">
            <div className="chat-browser">
              <div className="chat-browser-chrome">
                <span className="chat-browser-dots" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </span>
                <span className="chat-browser-url">
                  <Icon name="file" size={10} />
                  {sampleChat.rail.browser.url}
                </span>
              </div>
              <div className="chat-browser-page">
                <h4>{sampleChat.rail.browser.title}</h4>
                <div className="chat-browser-meta">{sampleChat.rail.browser.meta}</div>
                <p>
                  <strong>1. 影响面</strong>
                </p>
                <p>{sampleChat.rail.browser.paragraphs[0]}</p>
                <hr />
                <p>
                  <strong>2. 分阶段上线</strong>
                </p>
                <p>{sampleChat.rail.browser.paragraphs[1]}</p>
                <hr />
                <p>
                  <strong>3. 回滚</strong>
                </p>
                <p>{sampleChat.rail.browser.paragraphs[2]}</p>
                <p className="chat-browser-preview">{t('chat.rail.browser.preview')}</p>
              </div>
              <div className="chat-browser-actions">
                <button type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.notice.rail'))}>
                  <Icon name="external" size={12} />
                  {t('chat.rail.browser.open')}
                </button>
                <button type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.notice.copy'))}>
                  <Icon name="copy" size={12} />
                  {t('chat.rail.browser.copy')}
                </button>
                <span className="mono chat-browser-rendered">{t('chat.rail.browser.rendered', { time: '16:09' })}</span>
              </div>
            </div>
          </div>
        ) : null}

        {sample && tab === 'artifacts' ? (
          <div role="tabpanel" id="chat-rail-pane-artifacts" aria-labelledby="chat-rail-tab-artifacts" className="chat-rail-pane">
            <div className="chat-rail-section-head">
              <span>{t('chat.rail.artifacts.heading', { count: 3 })}</span>
              <button type="button" className="chat-btn chat-btn-sm chat-btn-ghost" onClick={() => onNotice(t('chat.rail.artifacts.notice'))}>
                <Icon name="arrowLeft" size={11} />
                {t('chat.rail.artifacts.return')}
              </button>
            </div>
            {sampleChat.rail.artifacts.map((artifact) => (
              <button
                key={artifact.name}
                type="button"
                className="chat-file-row"
                data-turn={artifact.turn}
                onClick={() => onNotice(t('chat.rail.artifacts.notice'))}
              >
                <span className="chat-file-ext">{artifact.ext}</span>
                <span className="chat-file-main">
                  <span className="chat-file-name">{artifact.name}</span>
                  <span className="chat-file-sub">{artifact.meta}</span>
                </span>
                <span className={`chat-badge ${artifact.status === 'pending' ? 'chat-tone-warn' : 'chat-tone-success'}`}>
                  {artifact.status === 'pending' ? t('chat.rail.artifacts.pending') : t('chat.rail.artifacts.saved')}
                </span>
              </button>
            ))}
            <div className="chat-rail-foot">
              <span>{t('chat.rail.artifacts.foot')}</span>
              <button type="button" className="chat-btn chat-btn-sm chat-btn-ghost" onClick={() => onNotice(t('chat.rail.artifacts.notice'))}>
                {t('chat.rail.artifacts.library')}
              </button>
            </div>
          </div>
        ) : null}

        {sample && tab === 'sources' ? (
          <div role="tabpanel" id="chat-rail-pane-sources" aria-labelledby="chat-rail-tab-sources" className="chat-rail-pane">
            <div className="chat-rail-section-head">
              <span>{t('chat.rail.sources.heading', { count: 4 })}</span>
            </div>
            {sampleChat.rail.sources.map((source) => (
              <article key={source.num} className="chat-source">
                <div className="chat-source-head">
                  <span className="chat-source-num">{source.num}</span>
                  <span className="chat-source-title">{source.title}</span>
                </div>
                <div className="chat-source-url">{source.url}</div>
                <div className="chat-source-snippet">{source.snippet}</div>
              </article>
            ))}
            <div className="chat-rail-foot">
              <span>{t('chat.rail.sources.foot', { count: 3 })}</span>
              <button type="button" className="chat-btn chat-btn-sm chat-btn-ghost" onClick={() => onNotice(t('chat.notice.export'))}>
                {t('chat.rail.sources.export')}
              </button>
            </div>
          </div>
        ) : null}

        {sample && tab === 'exec' ? (
          <div role="tabpanel" id="chat-rail-pane-exec" aria-labelledby="chat-rail-tab-exec" className="chat-rail-pane">
            <div className="chat-rail-section-head">
              <span>{t('chat.rail.exec.heading', { count: 5 })}</span>
            </div>
            {sampleChat.rail.exec.map((row) => (
              <div key={row.name} className="chat-exec-row">
                <span className="chat-exec-time">{row.time}</span>
                <div>
                  <div className="chat-exec-name">{row.name}</div>
                  <div className="chat-exec-sub">{row.sub}</div>
                </div>
                {row.status === 'done' ? <span className="chat-badge chat-tone-success">{t('chat.rail.exec.done')}</span> : null}
                {row.status === 'pending' ? <span className="chat-badge chat-tone-warn">{t('chat.rail.exec.pending')}</span> : null}
                {row.status === 'paused' ? <span className="chat-badge">{t('chat.rail.exec.paused')}</span> : null}
              </div>
            ))}
            <div className="chat-rail-section-head">{t('chat.rail.exec.usage')}</div>
            <div className="chat-usage-card">
              <div>
                <span>{t('chat.rail.exec.input')}</span>
                <b>{sampleChat.rail.usage.input}</b>
              </div>
              <div>
                <span>{t('chat.rail.exec.output')}</span>
                <b>{sampleChat.rail.usage.output}</b>
              </div>
              <div>
                <span>{t('chat.rail.exec.cache')}</span>
                <b>{sampleChat.rail.usage.cache}</b>
              </div>
              <div>
                <span>{t('chat.rail.exec.cost')}</span>
                <b>{sampleChat.rail.usage.cost}</b>
              </div>
            </div>
          </div>
        ) : null}
      </div>
    </aside>
  )
}

/** Execution: one row per tool call, each one pointing at its turn. */
function LiveExec({
  view,
  running,
  onTurn,
}: {
  view: RailView
  running: boolean
  onTurn?: (runId: string, turn?: number) => void
}) {
  const { t } = useI18n()
  return (
    <>
      <div className="chat-rail-section-head">
        <span>{t('chat.rail.exec.heading', { count: view.exec.length })}</span>
      </div>
      {view.exec.length === 0 ? (
        <div className="chat-rail-hint" data-testid="chat-rail-exec-empty">
          {t('chat.rail.exec.empty')}
        </div>
      ) : (
        view.exec.map((row) => (
          <button
            key={`${row.runId}:${row.seq}`}
            type="button"
            className="chat-exec-row is-button"
            data-testid="chat-rail-exec"
            data-turn={row.turn}
            data-run={row.runId}
            data-status={row.status}
            onClick={() => onTurn?.(row.runId, row.turn)}
          >
            <span className="chat-exec-time">{timeOf(row.at)}</span>
            <div>
              <div className="chat-exec-name">
                {row.tool}
                <span className="chat-exec-turn">{t('chat.rail.exec.turn', { turn: row.turn })}</span>
              </div>
              <div className="chat-exec-sub">{row.label}</div>
            </div>
            <ExecBadge status={row.status} running={running} />
          </button>
        ))
      )}
      {view.turns.length > 0 ? <div className="chat-rail-section-head">{t('chat.rail.exec.usage')}</div> : null}
      {view.turns.length > 0 ? (
        <div className="chat-usage-card" data-testid="chat-rail-usage">
          <div>
            <span>{t('chat.rail.exec.turns')}</span>
            <b>{view.turns.length}</b>
          </div>
          <div>
            <span>{t('chat.rail.exec.input')}</span>
            <b>{view.usage ? compact(view.usage.input) : t('chat.rail.exec.noUsage')}</b>
          </div>
          <div>
            <span>{t('chat.rail.exec.output')}</span>
            <b>{view.usage ? compact(view.usage.output) : t('chat.rail.exec.noUsage')}</b>
          </div>
          <div>
            <span>{t('chat.rail.exec.cache')}</span>
            <b>{view.usage ? compact(view.usage.cacheRead) : t('chat.rail.exec.noUsage')}</b>
          </div>
          <div>
            <span>{t('chat.rail.exec.cost')}</span>
            <b>{view.usage && view.usage.cost > 0 ? `$${view.usage.cost.toFixed(4)}` : t('chat.rail.exec.noUsage')}</b>
          </div>
        </div>
      ) : null}
      {view.outsidePaths > 0 ? (
        <div className="chat-rail-hint" data-testid="chat-rail-outside">
          {t('chat.rail.exec.outside', { count: view.outsidePaths })}
        </div>
      ) : null}
    </>
  )
}

function ExecBadge({ status, running }: { status: RailExec['status']; running: boolean }) {
  const { t } = useI18n()
  if (status === 'running' && !running) {
    // A call that never got a result because the process went away.
    return <span className="chat-badge chat-tone-warn">{t('chat.rail.exec.noresult')}</span>
  }
  if (status === 'running') {
    return <span className="chat-badge">{t('chat.rail.exec.running')}</span>
  }
  if (status === 'failed') {
    return <span className="chat-badge chat-tone-danger">{t('chat.rail.exec.failed')}</span>
  }
  if (status === 'no_result') {
    return <span className="chat-badge chat-tone-warn">{t('chat.rail.exec.noresult')}</span>
  }
  return <span className="chat-badge chat-tone-success">{t('chat.rail.exec.done')}</span>
}

/** Artifacts: real files. A click opens the file; the small button returns
 *  to the turn that wrote it. */
function LiveArtifacts({
  view,
  onTurn,
  onPreview,
}: {
  view: RailView
  onTurn?: (runId: string, turn?: number) => void
  onPreview?: (path: string) => void
}) {
  const { t } = useI18n()
  return (
    <>
      <div className="chat-rail-section-head">
        <span>{t('chat.rail.artifacts.heading', { count: view.artifacts.length })}</span>
      </div>
      {view.artifacts.length === 0 ? (
        <div className="chat-rail-hint" data-testid="chat-rail-artifacts-empty">
          {t('chat.rail.artifacts.empty')}
        </div>
      ) : (
        view.artifacts.map((artifact) => (
          <div key={artifact.path} className="chat-file-row is-artifact" data-testid="chat-rail-artifact" data-turn={artifact.turn} data-run={artifact.runId}>
            <button
              type="button"
              className="chat-file-open"
              title={artifact.path}
              data-testid="chat-rail-artifact-open"
              onClick={() => onPreview?.(artifact.path)}
            >
              <span className="chat-file-ext">{(artifact.ext || 'file').slice(0, 4)}</span>
              <span className="chat-file-main">
                <span className="chat-file-name">{artifact.relative}</span>
                <span className="chat-file-sub">
                  {t('chat.rail.artifacts.turn', { turn: artifact.turn })} · {size(artifact.bytes)}
                </span>
              </span>
            </button>
            <button
              type="button"
              className="chat-file-return"
              data-testid="chat-rail-artifact-return"
              title={t('chat.rail.artifacts.return')}
              aria-label={t('chat.rail.artifacts.return')}
              onClick={() => onTurn?.(artifact.runId, artifact.turn)}
            >
              <Icon name="branch" size={11} />
            </button>
          </div>
        ))
      )}
      <div className="chat-rail-foot">
        <span>{t('chat.rail.artifacts.confined')}</span>
      </div>
    </>
  )
}

/** Sources: the files the reader attached, then the URLs an answer cited. */
function LiveSources({ view, onTurn }: { view: RailView; onTurn?: (runId: string, turn?: number) => void }) {
  const { t } = useI18n()
  const references = view.references ?? []
  const sources = view.sources ?? []
  return (
    <>
      <div className="chat-rail-section-head">
        <span>{t('chat.rail.references.heading', { count: references.length })}</span>
      </div>
      {references.length === 0 ? (
        <div className="chat-rail-hint" data-testid="chat-rail-references-empty">
          {t('chat.rail.references.empty')}
        </div>
      ) : (
        references.map((reference) => {
          // One line of facts under the name, not a row of badges: the rail can
          // be 200px wide, and badges there squeezed the file name to "a…".
          const facts = [
            reference.turn > 0 ? t('chat.rail.artifacts.turn', { turn: reference.turn }) : '',
            reference.kind === 'dir' ? t('chat.rail.references.dir') : size(reference.bytes),
            reference.touches > 1 ? t('chat.rail.references.reused', { count: reference.touches }) : '',
            reference.truncated ? t('chat.rail.references.truncated') : '',
            reference.outsideRoot ? t('chat.rail.references.outside') : '',
          ].filter(Boolean)
          return (
            <div
              key={reference.path}
              className="chat-file-row chat-ref-row"
              data-testid="chat-rail-reference"
              data-kind={reference.kind}
              data-turn={reference.turn}
              title={reference.path.replace(/^\\\\\?\\/, '')}
            >
              <span className="chat-file-ext">{referenceBadge(reference)}</span>
              <span className="chat-file-main">
                <span className="chat-file-name">{reference.label}</span>
                <span className={`chat-file-sub${reference.outsideRoot ? ' is-outside' : ''}`}>
                  {facts.join(' · ')}
                </span>
              </span>
              {reference.turn > 0 && onTurn ? (
                <button
                  type="button"
                  className="chat-btn chat-btn-sm chat-btn-ghost chat-ref-go"
                  onClick={() => onTurn(reference.runId, reference.turn)}
                >
                  {t('chat.rail.turnGo')}
                </button>
              ) : null}
            </div>
          )
        })
      )}
      <div className="chat-rail-section-head">
        <span>{t('chat.rail.sources.heading', { count: sources.length })}</span>
      </div>
      {sources.length === 0 ? (
        <div className="chat-rail-hint" data-testid="chat-rail-sources-empty">
          {t('chat.rail.sources.empty')}
        </div>
      ) : (
        sources.map((source) => (
          <button
            key={source.url}
            type="button"
            className="chat-source is-button"
            data-testid="chat-rail-source"
            data-number={source.number}
            data-run={source.runId}
            onClick={() => onTurn?.(source.runId, source.turn)}
          >
            <div className="chat-source-head">
              <span className="chat-source-num">{source.number}</span>
              <span className="chat-source-title">{source.host}</span>
              <span className="chat-source-turn">{t('chat.rail.sources.turn', { turn: source.turn })}</span>
            </div>
            <div className="chat-source-url">{source.url}</div>
          </button>
        ))
      )}
    </>
  )
}

/** The two-letter badge above a reference row. */
function referenceBadge(reference: { kind: string; label: string }): string {
  if (reference.kind === 'dir') return 'DIR'
  if (reference.kind === 'skill') return 'SKL'
  if (reference.kind === 'image') return 'IMG'
  const dot = reference.label.lastIndexOf('.')
  const ext = dot > 0 ? reference.label.slice(dot + 1) : ''
  return (ext || 'file').slice(0, 4).toUpperCase()
}

/** Browser: a real webview, or the local page this build read from the project.
 *
 *  A frame cannot show most of the web — a site may refuse to be embedded — so
 *  a typed address opens the native view instead. The view is placed by the
 *  host over `chat-browser-view`, which is why this component reports its rect
 *  whenever it moves: the pane is DOM, the page on top of it is WebView2. */
function LiveBrowser({
  view,
  browser,
}: {
  /** Null when no conversation is open: the browser still works, it just has
   *  no page the project produced to show. */
  view: RailView | null
  browser?: RailLive['browser']
}) {
  const { t } = useI18n()
  const viewRef = useRef<HTMLDivElement>(null)
  const [draft, setDraft] = useState(browser?.url ?? '')
  const [showLocal, setShowLocal] = useState(true)

  useEffect(() => {
    setDraft(browser?.url ?? '')
    if (browser?.url) setShowLocal(false)
  }, [browser?.url])

  // Report the pane's rect: on resize, on scroll (the sidebar scrolls under it)
  // and when the tab changes. A closed pane reports a zero size, which parks the
  // native view off-screen without losing the page.
  useEffect(() => {
    // Scroll fires this many times a second; only a changed rect is worth an
    // IPC round trip.
    let last = ''
    const report = () => {
      if (!browser) return
      const box = viewRef.current?.getBoundingClientRect()
      const rect =
        !box || box.width < 2 || box.height < 2
          ? { x: 0, y: 0, w: 0, h: 0 }
          : {
              x: Math.round(box.left),
              y: Math.round(box.top),
              w: Math.round(box.width),
              h: Math.round(box.height),
            }
      const key = `${rect.x}:${rect.y}:${rect.w}:${rect.h}`
      if (key === last) return
      last = key
      browser.bounds(rect)
    }
    report()
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(report) : null
    if (observer && viewRef.current) observer.observe(viewRef.current)
    window.addEventListener('resize', report)
    window.addEventListener('scroll', report, true)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', report)
      window.removeEventListener('scroll', report, true)
      browser?.bounds({ x: 0, y: 0, w: 0, h: 0 })
    }
  }, [browser])

  const openUrl = (raw: string) => {
    const url = raw.trim()
    if (!url || !browser) return
    setShowLocal(false)
    browser.open(url)
  }

  const page = view?.browser.page
  const localReady = view?.browser.state === 'local' && page && showLocal

  return (
    <div className="chat-browser" data-testid="chat-rail-browser">
      <form
        className="chat-browser-bar"
        onSubmit={(event) => {
          event.preventDefault()
          openUrl(draft)
        }}
      >
        {(['back', 'forward'] as const).map((step) => (
          <button
            key={step}
            type="button"
            className="chat-iconbtn"
            aria-label={t(step === 'back' ? 'chat.rail.browser.back' : 'chat.rail.browser.forward')}
            data-testid={`chat-browser-${step}`}
            onClick={() => browser?.action(step)}
          >
            <Icon name={step === 'back' ? 'arrowLeft' : 'chevron'} size={13} />
          </button>
        ))}
        <button
          type="button"
          className="chat-iconbtn"
          aria-label={t('chat.rail.browser.reload')}
          data-testid="chat-browser-reload"
          onClick={() => browser?.action('reload')}
        >
          <Icon name="refresh" size={13} />
        </button>
        <input
          className="chat-browser-input"
          data-testid="chat-browser-url"
          value={draft}
          placeholder={t('chat.rail.browser.placeholder')}
          aria-label={t('chat.rail.browser.placeholder')}
          onChange={(event) => setDraft(event.target.value)}
        />
      </form>

      <div className="chat-browser-view" ref={viewRef} data-testid="chat-browser-view">
        {localReady ? (
          <>
            <iframe
              className="chat-browser-frame"
              data-testid="chat-rail-browser-frame"
              title={page.relative}
              sandbox={page.sandbox}
              srcDoc={page.html}
            />
            <div className="chat-browser-actions">
              <span className="chat-browser-note">{t('chat.rail.browser.sandboxed')}</span>
              <span className="mono chat-browser-rendered">{size(page.bytes)}</span>
            </div>
          </>
        ) : null}
        {browser?.url ? null : (
          <div className="chat-rail-empty" data-testid="chat-rail-browser-unavailable">
            <Icon name="globe" size={20} />
            <p>{t('chat.rail.browser.unavailable')}</p>
            {view?.browser.reason ? <small className="mono">{view.browser.reason}</small> : null}
          </div>
        )}
      </div>
    </div>
  )
}

function timeOf(at: string): string {
  const match = /T(\d{2}:\d{2})/.exec(at)
  return match ? match[1] : ''
}

function size(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`
  return `${bytes} B`
}

function compact(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k`
  return String(value)
}
