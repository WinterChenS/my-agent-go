import { useState } from 'react'
import Icon from '../../shell/Icon'
import { useI18n } from '../../i18n/LanguageProvider'
import type { UiAnswer, UiRequest } from '../../chat/client'

/**
 * A question the runtime asked on its own.
 *
 * A runtime that runs headless still has to ask the human something: pi's
 * extensions reach for `ctx.ui`, which in RPC mode blocks the run until the
 * answer is written back. The card is therefore not a decoration — until it is
 * answered, the turn cannot continue.
 */
export function ExtensionUiCard({
  request,
  answer,
  onAnswer,
}: {
  request: UiRequest
  answer: UiAnswer | null
  onAnswer?: (request: UiRequest, answer: UiAnswer) => void
}) {
  const { t } = useI18n()
  const [draft, setDraft] = useState('')
  const answered = answer !== null

  const send = (value: UiAnswer) => {
    if (answered || !onAnswer) return
    onAnswer(request, value)
  }

  // The fire-and-forget methods only want to be shown; they have no answer. A
  // card would claim the same weight as a question the reader must answer, so
  // they are one quiet line instead.
  if (request.method === 'notify' || request.method === 'setStatus' || request.method === 'setWidget' || request.method === 'setTitle' || request.method === 'set_editor_text') {
    return (
      <p
        className="chat-ui-note"
        data-testid="chat-extension-note"
        data-method={request.method}
      >
        <span className="chat-ui-note-mark" aria-hidden="true">
          <Icon name="sparkle" size={11} />
        </span>
        <span className="chat-ui-note-text">
          {request.method === 'notify'
            ? request.message
            : request.method === 'setStatus'
              ? `${t('chat.extension.status')}: ${request.message}`
              : request.method === 'setWidget'
                ? `${t('chat.extension.widget')}: ${request.message}`
                : request.message || request.title}
        </span>
      </p>
    )
  }

  return (
    <section
      className="chat-ui-card"
      data-testid="chat-extension-ui"
      data-method={request.method}
      data-request-id={request.id}
    >
      <header className="chat-ui-head">
        <span className="chat-ui-mark" aria-hidden="true">
          <Icon name="sparkle" size={13} />
        </span>
        <span className="chat-ui-kicker">{t('chat.extension.kicker')}</span>
        {answered ? (
          <span className="chat-ui-answered">
            {answer.cancelled
              ? t('chat.extension.cancelled')
              : answer.confirmed === true
                ? t('chat.extension.allowed')
                : answer.confirmed === false
                  ? t('chat.extension.denied')
                  : t('chat.extension.answered', { answer: answer.value ?? '' })}
          </span>
        ) : null}
      </header>

      <h4 className="chat-ui-title">{request.title || request.message}</h4>
      {request.message && request.message !== request.title ? (
        <p className="chat-ui-body">{request.message}</p>
      ) : null}

      {answered ? null : request.method === 'confirm' ? (
        <div className="chat-ui-actions">
          <button
            type="button"
            className="chat-btn chat-btn-sm"
            data-testid="chat-extension-allow"
            onClick={() => send({ confirmed: true })}
          >
            <Icon name="check" size={12} />
            {t('chat.extension.allow')}
          </button>
          <button
            type="button"
            className="chat-btn chat-btn-sm"
            data-testid="chat-extension-deny"
            onClick={() => send({ confirmed: false })}
          >
            {t('chat.extension.deny')}
          </button>
        </div>
      ) : request.method === 'select' ? (
        <div className="chat-ui-options">
          {request.options.map((option) => (
            <button
              key={option}
              type="button"
              className="chat-btn chat-btn-sm"
              data-testid="chat-extension-option"
              onClick={() => send({ value: option })}
            >
              {option}
            </button>
          ))}
        </div>
      ) : (
        <form
          className="chat-ui-form"
          onSubmit={(event) => {
            event.preventDefault()
            const value = draft.trim()
            if (value !== '') send({ value })
          }}
        >
          <textarea
            className="chat-ui-input"
            data-testid="chat-extension-input"
            rows={request.method === 'editor' ? 4 : 1}
            placeholder={request.placeholder}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
          <button
            type="submit"
            className="chat-btn chat-btn-sm"
            data-testid="chat-extension-submit"
            disabled={draft.trim() === ''}
          >
            {t('chat.extension.submit')}
          </button>
        </form>
      )}
    </section>
  )
}
