import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { Link } from 'react-router-dom'
import { useI18n } from '../../i18n/LanguageProvider'
import LanguageQuickSwitch from '../../i18n/LanguageQuickSwitch'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import { ROUTES_BY_ID } from '../../shell/routes'
import ChatDialogs, { type ChatDialogKind } from './ChatDialogs'
import ChatHistoryPanel from './ChatHistoryPanel'
import ChatRail, { type RailTab } from './ChatRail'
import { sampleChat, useChatSample, type ChatProse, type ChatTool, type SampleMessage } from './sampleData'
import './chat.css'

type Attachment = { id: string; name: string; kind: 'file' | 'skill'; size?: string }

function useNarrow() {
  const [narrow, setNarrow] = useState(() => {
    if (typeof window.matchMedia !== 'function') return false
    return window.matchMedia('(max-width: 900px)').matches
  })
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const query = window.matchMedia('(max-width: 900px)')
    const update = () => setNarrow(query.matches)
    update()
    query.addEventListener?.('change', update)
    return () => query.removeEventListener?.('change', update)
  }, [])
  return narrow
}

export default function ChatPage() {
  const { t, language } = useI18n()
  const sample = useChatSample()
  const narrow = useNarrow()
  const [railOpen, setRailOpen] = useState(!narrow)
  const [railTab, setRailTab] = useState<RailTab>('browser')
  const [dialog, setDialog] = useState<ChatDialogKind>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [notice, setNotice] = useState('')
  const [attachments, setAttachments] = useState<Attachment[]>(sample ? sampleChat.attachments : [])
  const [mentionOpen, setMentionOpen] = useState(false)
  const [attachmentOpen, setAttachmentOpen] = useState(false)
  const dialogTrigger = useRef<HTMLElement | null>(null)
  const railToggle = useRef<HTMLButtonElement>(null)
  const historyToggle = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    setRailOpen(!narrow)
  }, [narrow])

  useEffect(() => {
    if (!narrow || !railOpen) return
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      setRailOpen(false)
      window.setTimeout(() => railToggle.current?.focus(), 0)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [narrow, railOpen])

  useEffect(() => {
    if (!historyOpen) return
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      setHistoryOpen(false)
      window.setTimeout(() => historyToggle.current?.focus(), 0)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [historyOpen])

  const showNotice = (message: string) => setNotice(message)

  const openDialog = (kind: ChatDialogKind, trigger: HTMLElement) => {
    dialogTrigger.current = trigger
    setDialog(kind)
  }
  const closeDialog = () => {
    setDialog(null)
    const trigger = dialogTrigger.current
    window.setTimeout(() => trigger?.focus(), 0)
  }

  const openRail = (tab: RailTab) => {
    setRailTab(tab)
    setRailOpen(true)
  }

  return (
    <section className="chat-page" data-testid="chat-page" data-sample={sample ? 'true' : 'false'}>
      <span className="sr-only" id="route-marker" data-testid="page-marker">
        {ROUTES_BY_ID.get('chat')?.marker}
      </span>
      <header className="chat-header">
        <Link className="chat-iconbtn chat-back" to="/" aria-label={t('chat.back')} title={t('chat.back')}>
          <Icon name="arrowLeft" size={16} />
        </Link>
        <div className="chat-head-main">
          <h1 className="chat-title">{sample ? sampleChat.title : t('chat.normal.title')}</h1>
          <div className="chat-subline">
            {sample ? (
              <>
                <span className="chat-project-mark">
                  <Icon name="folder" size={10} />
                  {sampleChat.project}
                </span>
                <span className="sep">/</span>
                <span>{sampleChat.agent}</span>
                <span className="sep">/</span>
                <span className="mono">{sampleChat.model}</span>
                <span className="sep">/</span>
                <span className="mono">{t(`chat.composer.thinking.${sampleChat.thinkingLevel}` as MessageKey)}</span>
                <span className="sep">/</span>
                <span className="mono">{t(`chat.composer.permission.${sampleChat.permission}` as MessageKey)}</span>
              </>
            ) : (
              <span>{t('chat.normal.subtitle')}</span>
            )}
          </div>
        </div>
        <div className="chat-head-actions">
          <button
            type="button"
            className="chat-iconbtn chat-history-toggle"
            aria-label={t('chat.history.title')}
            aria-expanded={historyOpen}
            onClick={() => setHistoryOpen(true)}
            ref={historyToggle}
          >
            <Icon name="layers" size={15} />
          </button>
          {sample ? (
            <>
              <button
                type="button"
                className="chat-badge chat-thil-trigger"
                onClick={(event) => openDialog('thil', event.currentTarget)}
              >
                <Icon name="message" size={11} />
                <span>{t('chat.badge.clarification')}</span>
              </button>
              <span className="chat-badge chat-tone-warn">
                <i className="chat-dot" aria-hidden="true" />
                {t('chat.badge.pendingAuth')}
              </span>
            </>
          ) : null}
          <LanguageQuickSwitch />
          <button
            type="button"
            className="chat-iconbtn chat-export"
            aria-label={t('chat.export')}
            title={sample ? t('chat.export') : t('chat.action.unavailable')}
            disabled={!sample}
            onClick={(event) => openDialog('export', event.currentTarget)}
          >
            <Icon name="download" size={15} />
          </button>
          <button
            type="button"
            className={`chat-iconbtn${railOpen ? ' is-on' : ''}`}
            data-testid="chat-rail-toggle"
            aria-label={railOpen ? t('chat.rail.collapse') : t('chat.rail.expand')}
            aria-expanded={railOpen}
            aria-controls="chat-rail"
            onClick={() => setRailOpen((open) => !open)}
            ref={railToggle}
          >
            <Icon name="panel" size={16} />
          </button>
          <button
            type="button"
            className="chat-iconbtn chat-more"
            aria-label={t('chat.more')}
            title={sample ? t('chat.thread.title') : t('chat.action.unavailable')}
            disabled={!sample}
            onClick={(event) => openDialog('thread', event.currentTarget)}
          >
            <Icon name="more" size={16} />
          </button>
        </div>
      </header>

      {sample ? (
        <div className="chat-sample-banner" data-testid="sample-banner" role="status">
          <Icon name="sparkle" size={12} />
          {t('chat.sampleBanner')}
        </div>
      ) : null}
      {notice ? (
        <div className="chat-notice" data-testid="chat-notice" role="status">
          {notice}
        </div>
      ) : null}

      <div className={`chat-layout${railOpen ? ' rail-open' : ''}`} data-testid="chat-layout">
        <div className="chat-thread-col">
          <div className="chat-thread" data-testid="chat-thread">
            {sample ? (
              <div className="chat-thread-inner" data-testid="chat-message-list" aria-label={sampleChat.title}>
                {sampleChat.messages.map((message) => (
                  <MessageView
                    key={message.id}
                    message={message}
                    onOpenRail={openRail}
                    onDialog={(kind, trigger) => openDialog(kind, trigger)}
                    onNotice={showNotice}
                  />
                ))}
              </div>
            ) : (
              <div className="chat-empty" data-testid="chat-empty" role="status">
                <span className="chat-empty-icon">
                  <Icon name="message" size={22} />
                </span>
                <h2>{t('chat.empty.title')}</h2>
                <p>{t('chat.empty.body')}</p>
              </div>
            )}
          </div>

          <Composer
            sample={sample}
            language={language}
            attachments={attachments}
            setAttachments={setAttachments}
            mentionOpen={mentionOpen}
            setMentionOpen={setMentionOpen}
            attachmentOpen={attachmentOpen}
            setAttachmentOpen={setAttachmentOpen}
            onNotice={showNotice}
            onProject={(trigger) => openDialog('project', trigger)}
          />
        </div>

        {railOpen ? (
          <button
            type="button"
            className="chat-rail-scrim"
            aria-label={t('chat.rail.close')}
            tabIndex={-1}
            onClick={() => {
              setRailOpen(false)
              railToggle.current?.focus()
            }}
          />
        ) : null}
        <ChatRail sample={sample} open={railOpen} tab={railTab} onTab={setRailTab} onClose={() => {
          setRailOpen(false)
          railToggle.current?.focus()
        }} onNotice={showNotice} />
      </div>

      {historyOpen ? (
        <div className="chat-drawer-backdrop" onMouseDown={(event) => event.target === event.currentTarget && setHistoryOpen(false)}>
          <div className="chat-history-drawer" role="dialog" aria-modal="true" aria-label={t('chat.history.title')}>
            <ChatHistoryPanel sample={sample} variant="drawer" onClose={() => {
              setHistoryOpen(false)
              historyToggle.current?.focus()
            }} />
          </div>
        </div>
      ) : null}

      <ChatDialogs kind={dialog} onClose={closeDialog} onNotice={showNotice} />
    </section>
  )
}

function MessageView({
  message,
  onOpenRail,
  onDialog,
  onNotice,
}: {
  message: SampleMessage
  onOpenRail: (tab: RailTab) => void
  onDialog: (kind: ChatDialogKind, trigger: HTMLElement) => void
  onNotice: (message: string) => void
}) {
  const { t } = useI18n()
  if (message.kind === 'user') {
    return (
      <article className="chat-msg chat-msg-user" data-testid="chat-message-user">
        <div className="chat-bubble">{message.text}</div>
        <div className="chat-msg-meta">
          <span>{message.author}</span>
          <span>·</span>
          <span>{message.time}</span>
        </div>
      </article>
    )
  }
  if (message.kind === 'clarification') {
    return (
      <article className="chat-msg" data-testid="chat-message-clarification">
        <section className="chat-inline-clarification" role="status" aria-label={t('chat.clarification.kicker')}>
          <div className="chat-inline-mark">
            <Icon name="message" size={15} />
          </div>
          <div className="chat-inline-main">
            <div className="chat-inline-kicker">{t('chat.clarification.kicker')}</div>
            <h3>{message.title}</h3>
            <p>{message.body}</p>
            <div className="chat-inline-note">
              <Icon name="shield" size={11} />
              {message.note}
            </div>
          </div>
          <button
            type="button"
            className="chat-btn chat-btn-sm"
            onClick={(event) => onDialog('thil', event.currentTarget)}
          >
            {t('chat.clarification.view')}
          </button>
        </section>
      </article>
    )
  }
  if (message.kind === 'state') {
    const actionKeys: Record<NonNullable<Extract<SampleMessage, { kind: 'state' }>['actions']>[number], MessageKey> = {
      detail: 'chat.state.detail',
      retry: 'chat.state.retry',
      login: 'chat.state.login',
      compress: 'chat.state.compress',
      branch: 'chat.state.branch',
      continue: 'chat.state.continue',
    }
    return (
      <section
        className={`chat-state-card chat-tone-${message.tone}`}
        role={message.role}
        aria-label={message.title}
        data-testid="chat-state-card"
      >
        <span className="chat-state-icon">
          <Icon name={message.tone === 'danger' ? 'alert' : message.tone === 'warn' ? 'pause' : 'refresh'} size={14} />
        </span>
        <div className="chat-state-copy">
          <b>{message.title}</b>
          <span>{message.body}</span>
        </div>
        {message.actions?.map((action) => (
          <button key={action} type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.sample.notice'))}>
            {t(actionKeys[action])}
          </button>
        ))}
      </section>
    )
  }

  return (
    <article className="chat-msg" data-testid="chat-message-agent">
      <div className="chat-agent-head">
        <span className="chat-agent-badge">{message.agent.slice(0, 1)}</span>
        <span className="chat-agent-name">{message.agent}</span>
        <span className="chat-agent-model">{message.model}</span>
        {message.badge === 'generating' ? (
          <span className="chat-badge">
            <i className="chat-dot" aria-hidden="true" />
            {t('chat.status.generating')}
          </span>
        ) : null}
        {message.badge === 'failed' ? <span className="chat-badge chat-tone-danger">{t('chat.tool.failed')}</span> : null}
      </div>

      {message.thinking ? (
        <ThinkingBlock title={message.thinking.title} steps={message.thinking.steps} initialOpen={message.thinking.open} />
      ) : null}

      {message.tools?.map((tool) => <ToolBlock key={tool.name} tool={tool} />)}

      {message.prose ? <Prose prose={message.prose} /> : null}

      {message.meta ? (
        <div className="chat-msg-meta">
          <span className="mono">{message.meta.tokens}</span>
          <span>·</span>
          <span>{message.meta.time}</span>
          <span className="chat-msg-actions">
            <button type="button" className="chat-iconbtn" aria-label={t('chat.message.copy')} onClick={() => onNotice(t('chat.notice.copy'))}>
              <Icon name="copy" size={13} />
            </button>
            <button
              type="button"
              className="chat-iconbtn"
              aria-label={t('chat.message.regenerate')}
              onClick={() => onNotice(t('chat.sample.notice'))}
            >
              <Icon name="refresh" size={13} />
            </button>
          </span>
        </div>
      ) : null}

      {message.artifacts ? (
        <button type="button" className="chat-turn-artifacts" data-testid="chat-turn-artifacts" onClick={() => onOpenRail('artifacts')}>
          <span className="chat-turn-artifacts-icon">
            <Icon name="file" size={13} />
          </span>
          <span>
            <b>{t('chat.message.artifacts', { count: message.artifacts.count })}</b>
            <small>{message.artifacts.files}</small>
          </span>
          <span className="chat-turn-artifacts-open">
            {t('chat.message.openArtifacts')} <Icon name="chevron" size={11} />
          </span>
        </button>
      ) : null}

      {message.confirm ? <ConfirmCard onNotice={onNotice} /> : null}

      {message.stream ? (
        <div className="chat-stream-status" role="status">
          <span className="chat-live-dot" aria-hidden="true" />
          <span>{t('chat.status.paused', { tokens: '412' })}</span>
          <span className="chat-caret" aria-hidden="true" />
        </div>
      ) : null}
    </article>
  )
}

function ThinkingBlock({ title, steps, initialOpen = false }: { title: string; steps: string[]; initialOpen?: boolean }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(initialOpen)
  return (
    <section className={`chat-thinking${open ? ' is-open' : ''}`} data-testid="chat-thinking" aria-expanded={open}>
      <button type="button" className="chat-thinking-head" aria-label={t('chat.thinking.toggle')} onClick={() => setOpen((value) => !value)}>
        <Icon name="brain" size={14} />
        <span>{title}</span>
        <span className="chat-thinking-chevron" aria-hidden="true">
          <Icon name="chevron" size={12} />
        </span>
      </button>
      {open ? (
        <div className="chat-thinking-body">
          {steps.map((step, index) => (
            <div className="chat-thinking-step" key={step}>
              <span className="chat-thinking-num">{index + 1}</span>
              <span>{step}</span>
            </div>
          ))}
        </div>
      ) : null}
    </section>
  )
}

function ToolBlock({ tool }: { tool: ChatTool }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  return (
    <section className={`chat-tool${open ? ' is-open' : ''}`} data-testid="chat-tool" aria-expanded={open}>
      <button type="button" className="chat-tool-row" aria-label={t('chat.tool.toggle')} onClick={() => setOpen((value) => !value)}>
        <span className="chat-tool-icon">
          <Icon name={tool.icon} size={12} />
        </span>
        <span className="chat-tool-name">{tool.name}</span>
        <span className="chat-tool-arg">{tool.arg}</span>
        {tool.status === 'pending' ? <span className="chat-badge chat-tone-warn">{t('chat.tool.pending')}</span> : null}
        <span className="chat-tool-duration">{tool.duration}</span>
        <span className="chat-tool-caret" aria-hidden="true">
          <Icon name="chevron" size={12} />
        </span>
      </button>
      {open ? <pre className="chat-tool-body">{tool.body}</pre> : null}
    </section>
  )
}

function Prose({ prose }: { prose: ChatProse }) {
  const { t } = useI18n()
  return (
    <div className="chat-prose">
      <p>{prose.intro}</p>
      {prose.bullets ? (
        <ul>
          {prose.bullets.map((bullet) => (
            <li key={bullet.lead}>
              <strong>{bullet.lead}</strong>
              {bullet.text ? `：${bullet.text}` : null}
              {bullet.code ? <code>{bullet.code}</code> : null}
              {bullet.cite ? (
                <button type="button" className="chat-cite" aria-label={t('chat.message.cite', { num: bullet.cite })}>
                  {bullet.cite}
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {prose.closing ? <p>{prose.closing}</p> : null}
    </div>
  )
}

function ConfirmCard({ onNotice }: { onNotice: (message: string) => void }) {
  const { t } = useI18n()
  return (
    <section className="chat-confirm" role="alert" aria-label={t('chat.confirm.title')}>
      <div className="chat-confirm-head">
        <Icon name="shield" size={14} />
        <span>{t('chat.confirm.title')}</span>
        <span className="chat-badge chat-tone-warn">{t('chat.confirm.risk')}</span>
      </div>
      <div className="chat-confirm-body">
        <p>{t('chat.confirm.desc')}</p>
        <pre className="chat-confirm-code">{`write  docs/migration/v1-to-v2-plan.md   3.4 KB
scope docs/**                               允许
副作用 新增文件（无覆盖）`}</pre>
      </div>
      <div className="chat-confirm-foot">
        <span>{t('chat.confirm.hint')}</span>
        <button type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.notice.approval'))}>
          {t('chat.confirm.deny')}
        </button>
        <button type="button" className="chat-btn chat-btn-sm" onClick={() => onNotice(t('chat.notice.approval'))}>
          {t('chat.confirm.always')}
        </button>
        <button type="button" className="chat-btn chat-btn-sm chat-btn-primary" onClick={() => onNotice(t('chat.notice.approval'))}>
          {t('chat.confirm.once')}
        </button>
      </div>
    </section>
  )
}

function Composer({
  sample,
  language,
  attachments,
  setAttachments,
  mentionOpen,
  setMentionOpen,
  attachmentOpen,
  setAttachmentOpen,
  onNotice,
  onProject,
}: {
  sample: boolean
  language: string
  attachments: Attachment[]
  setAttachments: (next: Attachment[]) => void
  mentionOpen: boolean
  setMentionOpen: (open: boolean) => void
  attachmentOpen: boolean
  setAttachmentOpen: (open: boolean) => void
  onNotice: (message: string) => void
  onProject: (trigger: HTMLElement) => void
}) {
  const { t } = useI18n()
  const [text, setText] = useState('')

  const submit = (event?: FormEvent) => {
    event?.preventDefault()
    if (!sample) return
    onNotice(t('chat.notice.send'))
  }

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault()
      submit()
    }
    if (event.key === 'Escape') {
      setMentionOpen(false)
      setAttachmentOpen(false)
    }
  }

  const shown = sample ? text : ''

  return (
    <div className="chat-composer-wrap">
      <div className="chat-composer-project-row">
        <button
          type="button"
          className="chat-project-trigger"
          data-testid="chat-project-trigger"
          aria-haspopup="dialog"
          disabled={!sample}
          title={sample ? t('chat.composer.project.change') : t('chat.action.unavailable')}
          onClick={(event) => onProject(event.currentTarget)}
        >
          <Icon name="folder" size={12} />
          <span>{sample ? sampleChat.project : t('chat.composer.project.none')}</span>
          <Icon name="chevDown" size={11} />
        </button>
      </div>
      <form className="chat-composer" onSubmit={submit} aria-label={t('chat.composer.placeholder')}>
        <textarea
          data-testid="chat-composer"
          rows={2}
          value={shown}
          disabled={!sample}
          placeholder={sample ? t('chat.composer.placeholder') : t('chat.composer.normalPlaceholder')}
          aria-label={t('chat.composer.placeholder')}
          onChange={(event) => {
            if (!sample) return
            setText(event.target.value)
            setMentionOpen(event.target.value.endsWith('@'))
          }}
          onKeyDown={onKeyDown}
        />
        {attachments.length > 0 ? (
          <div className="chat-composer-attachments" data-testid="chat-attachments" aria-live="polite">
            {attachments.map((attachment) => (
              <span key={attachment.id} className={`chat-attachment${attachment.kind === 'skill' ? ' is-skill' : ''}`}>
                <Icon name={attachment.kind === 'skill' ? 'puzzle' : 'file'} size={12} />
                <b>{attachment.name}</b>
                <small>{attachment.kind === 'skill' ? 'Skill' : attachment.size}</small>
                <button
                  type="button"
                  aria-label={t('chat.composer.remove', { name: attachment.name })}
                  onClick={() => {
                    setAttachments(attachments.filter((item) => item.id !== attachment.id))
                    onNotice(t('chat.notice.attachment'))
                  }}
                >
                  <Icon name="x" size={11} />
                </button>
              </span>
            ))}
          </div>
        ) : null}
        {mentionOpen ? (
          <div className="chat-mention-list" role="listbox" aria-label={t('chat.composer.mention')}>
            {[sampleChat.attachments[0].name, sampleChat.agent].map((name) => (
              <button
                key={name}
                type="button"
                role="option"
                aria-selected="false"
                onClick={() => {
                  setText((current) => current.replace(/@$/, `@${name} `))
                  setMentionOpen(false)
                  onNotice(t('chat.notice.mention'))
                }}
              >
                {name}
              </button>
            ))}
          </div>
        ) : null}
        {attachmentOpen ? (
          <div className="chat-attachment-drawer" role="menu" aria-label={t('chat.composer.addContext')}>
            <div className="chat-attachment-group">
              <button type="button" role="menuitem" disabled title={t('chat.action.unavailable')}>
                <Icon name="file" size={14} />
                <span>
                  <b>{t('chat.composer.chooseFiles')}</b>
                  <small>{t('chat.composer.chooseFilesDesc')}</small>
                </span>
              </button>
              <button type="button" role="menuitem" disabled title={t('chat.action.unavailable')}>
                <Icon name="folder" size={14} />
                <span>
                  <b>{t('chat.composer.chooseDir')}</b>
                  <small>{t('chat.composer.chooseDirDesc')}</small>
                </span>
              </button>
            </div>
            <div className="chat-attachment-group">
              <button type="button" role="menuitem" disabled title={t('chat.action.unavailable')}>
                <Icon name="puzzle" size={14} />
                <span>
                  <b>{t('chat.composer.skill')}</b>
                  <small>{t('chat.composer.skillDesc')}</small>
                </span>
              </button>
            </div>
          </div>
        ) : null}
        <div className="chat-composer-bar">
          <button
            type="button"
            className="chat-composer-plus"
            aria-label={t('chat.composer.add')}
            aria-expanded={attachmentOpen}
            disabled={!sample}
            onClick={() => setAttachmentOpen(!attachmentOpen)}
          >
            <Icon name="plus" size={14} />
          </button>
          <label className="chat-ctl" title={t('chat.composer.model')}>
            <Icon name="sparkle" size={13} />
            <select aria-label={t('chat.composer.model')} disabled={!sample}>
              {sample ? <option>{sampleChat.model}</option> : <option>{t('chat.composer.normalPlaceholder')}</option>}
            </select>
          </label>
          <label className="chat-ctl" title={t('chat.composer.thinking')}>
            <Icon name="brain" size={13} />
            <select aria-label={t('chat.composer.thinking')} disabled={!sample} defaultValue={sample ? sampleChat.thinkingLevel : undefined}>
              {sample ? (
                <>
                  <option value="off">{t('chat.composer.thinking.off')}</option>
                  <option value="low">{t('chat.composer.thinking.low')}</option>
                  <option value="medium">{t('chat.composer.thinking.medium')}</option>
                  <option value="high">{t('chat.composer.thinking.high')}</option>
                </>
              ) : (
                <option>{t('chat.composer.normalPlaceholder')}</option>
              )}
            </select>
          </label>
          <label className="chat-ctl" title={t('chat.composer.permission')}>
            <Icon name="shield" size={13} />
            <select aria-label={t('chat.composer.permission')} disabled={!sample} defaultValue={sample ? sampleChat.permission : undefined}>
              {sample ? (
                <>
                  <option value="readonly">{t('chat.composer.permission.readonly')}</option>
                  <option value="medium">{t('chat.composer.permission.medium')}</option>
                  <option value="high">{t('chat.composer.permission.high')}</option>
                  <option value="full">{t('chat.composer.permission.full')}</option>
                </>
              ) : (
                <option>{t('chat.composer.normalPlaceholder')}</option>
              )}
            </select>
          </label>
          <button type="button" className="chat-ctl" disabled={!sample} onClick={() => onNotice(t('chat.notice.attachment'))}>
            <Icon name="paperclip" size={13} />
            <span>{t('chat.composer.context', { count: attachments.length })}</span>
          </button>
          <div className="chat-composer-actions">
            <button type="button" className="chat-btn chat-btn-sm" data-testid="chat-stop" disabled={!sample} onClick={() => onNotice(t('chat.notice.stop'))}>
              <Icon name="pause" size={12} />
              {t('chat.composer.stop')}
            </button>
            <button
              type="submit"
              className="chat-send"
              data-testid="chat-send"
              aria-label={t('chat.composer.send')}
              disabled={!sample}
            >
              <Icon name="send" size={14} />
            </button>
          </div>
        </div>
      </form>
      <div className="chat-composer-foot">
        <span>{sample ? t('chat.composer.pending') : t('chat.composer.normalHint')}</span>
        <span className="chat-kbdset">
          <span className="kbd">{navigator.platform.includes('Mac') ? '⌘' : 'Ctrl'}</span>
          <span className="kbd">↵</span> {t('chat.composer.sendHint')}
        </span>
        <span className="chat-kbdset">
          <span className="kbd">⇧</span>
          <span className="kbd">↵</span> {t('chat.composer.newlineHint')}
        </span>
      </div>
      <span className="sr-only">{language}</span>
    </div>
  )
}
