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

export default function ChatRail({
  sample,
  open,
  tab,
  onTab,
  onClose,
  onNotice,
}: {
  sample: boolean
  open: boolean
  tab: RailTab
  onTab: (tab: RailTab) => void
  onClose: () => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  const counts = sample
    ? { browser: 0, artifacts: 3, sources: 4, exec: 0 }
    : { browser: 0, artifacts: 0, sources: 0, exec: 0 }

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
        {!sample ? (
          <div className="chat-rail-empty">
            <Icon name="panel" size={20} />
            <p>{t('chat.rail.empty')}</p>
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
