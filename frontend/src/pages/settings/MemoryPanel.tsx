// 设置 → 记忆.
//
// Two layers, on purpose:
//
// * 长期记忆 is injected into every turn. It is written by the user here, or by
//   the agent through `anvil-memory.mjs` when the user says 记住…, and the list
//   you see is exactly what the prompt will carry.
// * 会话记忆 is one conclusion per conversation, written by the host from the
//   assistant's own answer. It is a record to review, not an input: nothing in
//   this tab reaches the model until the user promotes it.
//
// That split is the answer to the usual failure of memory UIs — an invisible
// recall layer nobody can audit. Deleting here deletes the row the prompt reads.

import { useEffect, useMemo, useState } from 'react'
import { useI18n } from '../../i18n/LanguageProvider'
import type { MessageKey } from '../../i18n/catalogue'
import Icon from '../../shell/Icon'
import {
  clearMemories,
  deleteMemory,
  loadMemories,
  memoryError,
  saveMemory,
  setMemorySwitches,
  updateMemory,
  type MemoryKind,
  type MemoryOverview,
  type MemoryRow,
  type MemoryScope,
} from './memoryClient'

const KINDS: Array<{ id: MemoryKind; key: MessageKey }> = [
  { id: 'preference', key: 'memory.kind.preference' },
  { id: 'fact', key: 'memory.kind.fact' },
  { id: 'constraint', key: 'memory.kind.constraint' },
  { id: 'workflow', key: 'memory.kind.workflow' },
]

function day(value: string): string {
  return value.length >= 10 ? value.slice(0, 10) : value
}

export default function MemoryPanel() {
  const { t } = useI18n()
  const [overview, setOverview] = useState<MemoryOverview | null>(null)
  const [query, setQuery] = useState('')
  const [tab, setTab] = useState<MemoryScope>('user')
  const [text, setText] = useState('')
  const [kind, setKind] = useState<MemoryKind>('preference')
  const [pinned, setPinned] = useState(false)
  const [editing, setEditing] = useState<{ id: string; text: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [confirmClear, setConfirmClear] = useState(false)

  const apply = (next: MemoryOverview) => {
    setOverview(next)
    setError('')
  }

  useEffect(() => {
    loadMemories()
      .then(apply)
      .catch((cause) => setError(memoryError(cause)))
  }, [])

  // Searching re-reads the ledger instead of filtering a stale copy: the agent
  // can add a memory mid-conversation, and the list has to be able to find it.
  const runSearch = (value: string) => {
    setQuery(value)
    loadMemories(value.trim())
      .then(apply)
      .catch((cause) => setError(memoryError(cause)))
  }

  const rows = useMemo(
    () => (tab === 'user' ? overview?.user ?? [] : overview?.session ?? []),
    [overview, tab],
  )

  const run = async (work: () => Promise<MemoryOverview>, message: string) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      apply(await work())
      setNotice(message)
    } catch (cause) {
      setError(memoryError(cause))
    } finally {
      setBusy(false)
    }
  }

  const add = () => {
    const value = text.trim()
    if (!value) return
    void run(
      () => saveMemory({ scope: 'user', kind, text: value, pinned }, query.trim() || undefined),
      t('memory.notice.saved'),
    ).then(() => {
      setText('')
      setPinned(false)
    })
  }

  /** Promote = copy into 长期记忆, then drop the session row it came from. */
  const promote = async (row: MemoryRow) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      apply(await saveMemory({ scope: 'user', kind: 'fact', text: row.text }, query.trim() || undefined))
      apply(await deleteMemory(row.id, query.trim() || undefined))
      setTab('user')
      setNotice(t('memory.notice.promoted'))
    } catch (cause) {
      setError(memoryError(cause))
    } finally {
      setBusy(false)
    }
  }

  if (!overview) {
    return (
      <div className="card" data-testid="memory-panel">
        <div className="card-head">
          <h3>{t('memory.title')}</h3>
        </div>
        <div className="st-empty" data-testid="memory-unavailable">
          {error || t('memory.loading')}
        </div>
      </div>
    )
  }

  const switches = overview.settings

  return (
    <div className="st-stack" data-testid="memory-panel">
      {notice ? (
        <div className="st-notice-bar" role="status" data-testid="memory-notice">
          {notice}
        </div>
      ) : null}
      {error ? (
        <div className="st-notice-bar is-error" role="alert" data-testid="memory-error">
          {error}
        </div>
      ) : null}

      <div className="card">
        <div className="card-head">
          <h3>{t('memory.title')}</h3>
          <span className="card-head-sub">{t('memory.sub')}</span>
        </div>
        <div className="st-rows">
          <div className="st-row">
            <div className="st-row-main">
              <div className="st-row-title">{t('memory.switch.inject')}</div>
              <div className="st-row-sub">{t('memory.switch.injectSub')}</div>
            </div>
            <div className="st-row-control">
              <button
                type="button"
                className="st-switch"
                role="switch"
                aria-checked={switches.inject}
                aria-label={t('memory.switch.inject')}
                data-testid="memory-switch-inject"
                disabled={busy}
                onClick={() =>
                  void run(
                    () => setMemorySwitches({ inject: !switches.inject }),
                    t('memory.notice.switched'),
                  )
                }
              >
                <span />
              </button>
            </div>
          </div>
          <div className="st-row">
            <div className="st-row-main">
              <div className="st-row-title">{t('memory.switch.record')}</div>
              <div className="st-row-sub">{t('memory.switch.recordSub')}</div>
            </div>
            <div className="st-row-control">
              <button
                type="button"
                className="st-switch"
                role="switch"
                aria-checked={switches.recordSessions}
                aria-label={t('memory.switch.record')}
                data-testid="memory-switch-record"
                disabled={busy}
                onClick={() =>
                  void run(
                    () => setMemorySwitches({ recordSessions: !switches.recordSessions }),
                    t('memory.notice.switched'),
                  )
                }
              >
                <span />
              </button>
            </div>
          </div>
        </div>
      </div>

      <div className="card" data-testid="memory-list-card">
        <div className="card-head">
          <h3>{t('memory.list.title')}</h3>
          <div className="st-card-actions">
            <input
              className="st-input st-input-sm"
              value={query}
              placeholder={t('memory.search')}
              aria-label={t('memory.search')}
              data-testid="memory-search"
              onChange={(event) => runSearch(event.target.value)}
            />
            <button
              type="button"
              className="st-btn st-btn-sm"
              data-testid="memory-clear"
              disabled={busy || rows.length === 0}
              onClick={() => {
                if (!confirmClear) {
                  setConfirmClear(true)
                  return
                }
                setConfirmClear(false)
                void run(
                  () => clearMemories(tab, query.trim() || undefined),
                  t('memory.notice.cleared'),
                )
              }}
            >
              {confirmClear ? t('memory.clearConfirm') : t('memory.clear')}
            </button>
          </div>
        </div>

        <div className="memory-tabs" role="tablist" aria-label={t('memory.list.title')}>
          {(['user', 'session'] as MemoryScope[]).map((scope) => (
            <button
              key={scope}
              type="button"
              role="tab"
              aria-selected={tab === scope}
              className={`memory-tab${tab === scope ? ' is-active' : ''}`}
              data-testid={`memory-tab-${scope}`}
              onClick={() => {
                setTab(scope)
                setConfirmClear(false)
              }}
            >
              {t(scope === 'user' ? 'memory.tab.user' : 'memory.tab.session', {
                count: scope === 'user' ? overview.counts.user : overview.counts.session,
              })}
            </button>
          ))}
          <span className="memory-tabs-hint">{t('memory.countHint', { enabled: overview.counts.enabled })}</span>
        </div>

        {tab === 'user' ? (
          <div className="memory-compose">
            <textarea
              className="st-input memory-textarea"
              value={text}
              placeholder={t('memory.placeholder')}
              aria-label={t('memory.placeholder')}
              data-testid="memory-draft-text"
              rows={2}
              maxLength={500}
              onChange={(event) => setText(event.target.value)}
            />
            <div className="memory-compose-row">
              <select
                className="st-select"
                value={kind}
                aria-label={t('memory.kindLabel')}
                data-testid="memory-draft-kind"
                onChange={(event) => setKind(event.target.value as MemoryKind)}
              >
                {KINDS.map((entry) => (
                  <option key={entry.id} value={entry.id}>{t(entry.key)}</option>
                ))}
              </select>
              <label className="memory-pin">
                <input
                  type="checkbox"
                  checked={pinned}
                  data-testid="memory-draft-pinned"
                  onChange={(event) => setPinned(event.target.checked)}
                />
                {t('memory.pin')}
              </label>
              <button
                type="button"
                className="st-btn st-btn-sm st-btn-primary"
                data-testid="memory-save"
                disabled={busy || !text.trim()}
                onClick={add}
              >
                <Icon name="plus" size={12} />
                {t('memory.save')}
              </button>
            </div>
            <p className="st-hint">{t('memory.hint.user')}</p>
          </div>
        ) : (
          <p className="st-hint memory-hint">{t('memory.hint.session')}</p>
        )}

        {rows.length === 0 ? (
          <div className="st-empty" data-testid="memory-empty">
            {t(tab === 'user' ? 'memory.empty.user' : 'memory.empty.session')}
          </div>
        ) : (
          <ul className="memory-list" data-testid="memory-rows">
            {rows.map((row) => (
              <li key={row.id} className="memory-row" data-testid="memory-row" data-kind={row.kind}>
                <span className={`memory-kind is-${row.kind}`}>{t(`memory.kind.${row.kind}` as MessageKey)}</span>
                <div className="memory-main">
                  {editing?.id === row.id ? (
                    <div className="memory-edit">
                      <textarea
                        className="st-input memory-textarea"
                        rows={2}
                        value={editing.text}
                        aria-label={t('memory.edit')}
                        data-testid="memory-edit-text"
                        onChange={(event) => setEditing({ id: row.id, text: event.target.value })}
                      />
                      <div className="memory-edit-actions">
                        <button
                          type="button"
                          className="st-btn st-btn-sm"
                          onClick={() => setEditing(null)}
                        >
                          {t('memory.cancel')}
                        </button>
                        <button
                          type="button"
                          className="st-btn st-btn-sm st-btn-primary"
                          data-testid="memory-edit-save"
                          disabled={busy || !editing.text.trim()}
                          onClick={() =>
                            void run(
                              () => updateMemory(row.id, { text: editing.text }, query.trim() || undefined),
                              t('memory.notice.updated'),
                            ).then(() => setEditing(null))
                          }
                        >
                          {t('memory.save')}
                        </button>
                      </div>
                    </div>
                  ) : (
                    <p className={`memory-text${row.enabled ? '' : ' is-off'}`}>{row.text}</p>
                  )}
                  <div className="memory-meta">
                    {row.pinned ? <span className="memory-chip">{t('memory.pinned')}</span> : null}
                    {!row.enabled ? <span className="memory-chip">{t('memory.disabled')}</span> : null}
                    <span>{t(`memory.source.${row.source}` as MessageKey)}</span>
                    {row.sessionTitle ? <span>· {row.sessionTitle}</span> : null}
                    <span>· {day(row.updatedAt)}</span>
                  </div>
                </div>
                <div className="memory-actions">
                  {tab === 'session' ? (
                    <button
                      type="button"
                      className="st-btn st-btn-sm"
                      data-testid="memory-promote"
                      disabled={busy}
                      onClick={() => void promote(row)}
                    >
                      {t('memory.promote')}
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className="memory-icon-btn"
                    aria-label={t('memory.pin')}
                    aria-pressed={row.pinned}
                    title={t('memory.pin')}
                    disabled={busy}
                    onClick={() =>
                      void run(
                        () => updateMemory(row.id, { pinned: !row.pinned }, query.trim() || undefined),
                        t('memory.notice.updated'),
                      )
                    }
                  >
                    <Icon name="pin" size={13} />
                  </button>
                  <button
                    type="button"
                    className="memory-icon-btn"
                    aria-label={row.enabled ? t('memory.disable') : t('memory.enable')}
                    title={row.enabled ? t('memory.disable') : t('memory.enable')}
                    disabled={busy}
                    onClick={() =>
                      void run(
                        () => updateMemory(row.id, { enabled: !row.enabled }, query.trim() || undefined),
                        t('memory.notice.updated'),
                      )
                    }
                  >
                    <Icon name={row.enabled ? 'pause' : 'eye'} size={13} />
                  </button>
                  <button
                    type="button"
                    className="memory-icon-btn"
                    aria-label={t('memory.edit')}
                    title={t('memory.edit')}
                    disabled={busy}
                    onClick={() => setEditing({ id: row.id, text: row.text })}
                  >
                    <Icon name="pen" size={13} />
                  </button>
                  <button
                    type="button"
                    className="memory-icon-btn is-danger"
                    aria-label={t('memory.delete')}
                    title={t('memory.delete')}
                    disabled={busy}
                    onClick={() =>
                      void run(
                        () => deleteMemory(row.id, query.trim() || undefined),
                        t('memory.notice.deleted'),
                      )
                    }
                  >
                    <Icon name="trash" size={13} />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <p className="st-hint memory-path" title={overview.database}>
          {t('memory.path', { path: overview.database })}
        </p>
      </div>
    </div>
  )
}
