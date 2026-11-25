// Provider accounts for the models page.
//
// Pi authenticates per provider, so a model list alone cannot explain why
// nothing answers: the account behind the model is the missing half. This panel
// lists every provider pi ships with, says which ones this machine already has a
// credential for, and offers the two ways pi accepts one — an API key typed here
// (`auth.json`), or the provider's own browser sign-in, streamed from the host
// while it waits on the browser.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { useI18n } from '../i18n/LanguageProvider'
import type { MessageKey } from '../i18n'
import { invokeHost, listenHost } from '../host/tauri'
import {
  PROVIDER_LOGIN_EVENT,
  cancelProviderLogin,
  loadProviders,
  logoutProvider,
  removeCustomProvider,
  saveCustomProvider,
  setProviderKey,
  startProviderLogin,
  submitProviderLogin,
  type ProviderAccount,
  type ProviderLoginEvent,
  type ProviderSnapshot,
} from './client'

type Filter = 'all' | 'connected' | 'login' | 'custom'

/** A sign-in in progress, reduced from the host's event stream. */
interface LoginState {
  provider: ProviderAccount
  sessionId: string | null
  status: 'starting' | 'waiting' | 'done' | 'error'
  prompt: ProviderLoginEvent | null
  answer: string
  url: string | null
  device: { code: string; uri: string | null } | null
  log: string[]
  message: string | null
}

export function providerInitial(account: ProviderAccount): string {
  const source = account.name.trim() || account.id
  return source.slice(0, 1).toUpperCase()
}

/** Providers that can be signed in with a browser, in catalogue order. */
export function oauthProviders(accounts: ProviderAccount[]): ProviderAccount[] {
  return accounts.filter((account) => Boolean(account.oauthLabel))
}

/** Connected first, then the ones that can sign in, then by name. */
export function sortProviders(accounts: ProviderAccount[]): ProviderAccount[] {
  return [...accounts].sort((left, right) => {
    const connected =
      Number(Boolean(right.credential)) - Number(Boolean(left.credential))
    if (connected !== 0) return connected
    const oauth = Number(Boolean(right.oauthLabel)) - Number(Boolean(left.oauthLabel))
    if (oauth !== 0) return oauth
    return left.name.localeCompare(right.name)
  })
}

/** Whether a row matches the search and the active filter. */
export function providerMatches(
  account: ProviderAccount,
  query: string,
  filter: Filter,
): boolean {
  if (filter === 'connected' && !account.credential) return false
  if (filter === 'login' && !account.oauthLabel) return false
  if (filter === 'custom' && account.source !== 'custom') return false
  const needle = query.trim().toLowerCase()
  if (!needle) return true
  return (
    account.name.toLowerCase().includes(needle) ||
    account.id.toLowerCase().includes(needle)
  )
}

/** Fold one host event into the running sign-in. */
function reduceLogin(current: LoginState, event: ProviderLoginEvent): LoginState {
  if (event.sessionId !== current.sessionId) return current
  const pushLog = (line: string | null) => {
    if (!line) return current.log
    return [...current.log, line].slice(-6)
  }
  switch (event.kind) {
    case 'prompt':
      return { ...current, prompt: event, answer: '', status: 'waiting' }
    case 'auth_url':
      return {
        ...current,
        url: event.url ?? current.url,
        log: pushLog(event.instructions),
        status: 'waiting',
      }
    case 'device_code':
      return {
        ...current,
        device: { code: event.userCode ?? '', uri: event.verificationUri },
        log: pushLog(event.message),
        status: 'waiting',
      }
    case 'done':
      return { ...current, status: 'done', prompt: null, message: event.message }
    case 'error':
      return {
        ...current,
        status: 'error',
        prompt: null,
        message: event.message ?? 'sign-in failed',
      }
    default:
      return { ...current, log: pushLog(event.message) }
  }
}

export function CatalogProvidersPanel({ onChanged }: { onChanged?: () => void }) {
  const { t } = useI18n()
  const [snapshot, setSnapshot] = useState<ProviderSnapshot | null>(null)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState('')
  const [keyTarget, setKeyTarget] = useState<ProviderAccount | null>(null)
  const [customOpen, setCustomOpen] = useState(false)
  const [login, setLogin] = useState<LoginState | null>(null)
  const openedUrl = useRef('')

  const reload = useCallback(async () => {
    try {
      const next = await loadProviders()
      setSnapshot(next)
      setError('')
      return next
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      return null
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // One listener for the sign-in in flight. Re-subscribed per session so a
  // closed dialog cannot receive another provider's events.
  useEffect(() => {
    const sessionId = login?.sessionId
    if (!sessionId) return
    let disposed = false
    let unlisten: (() => void) | null = null
    void listenHost<ProviderLoginEvent>(PROVIDER_LOGIN_EVENT, (event) => {
      if (!event || event.sessionId !== sessionId) return
      if (event.kind === 'auth_url' && event.url && openedUrl.current !== event.url) {
        openedUrl.current = event.url
        // pi's CLI opens the browser itself; here the page owns that decision.
        void invokeHost('open_external', { url: event.url }).catch(() => undefined)
      }
      setLogin((current) => (current ? reduceLogin(current, event) : current))
      if (event.kind === 'done') {
        void reload()
        onChanged?.()
      }
    })
      .then((off) => {
        if (disposed) off()
        else unlisten = off
      })
      .catch(() => undefined)
    return () => {
      disposed = true
      unlisten?.()
    }
    // A session id is the whole identity of a sign-in.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [login?.sessionId])

  const accounts = snapshot?.accounts ?? []
  const connected = accounts.filter((account) => account.credential).length
  const visible = useMemo(
    () => sortProviders(accounts.filter((account) => providerMatches(account, query, filter))),
    [accounts, query, filter],
  )

  const startLogin = async (account: ProviderAccount) => {
    setNotice('')
    setBusy(`login:${account.id}`)
    openedUrl.current = ''
    setLogin({
      provider: account,
      sessionId: null,
      status: 'starting',
      prompt: null,
      answer: '',
      url: null,
      device: null,
      log: [],
      message: null,
    })
    try {
      const sessionId = await startProviderLogin(account.id)
      setLogin((current) =>
        current && current.provider.id === account.id
          ? { ...current, sessionId, status: 'waiting' }
          : current,
      )
    } catch (cause) {
      setLogin((current) =>
        current
          ? {
              ...current,
              status: 'error',
              message: cause instanceof Error ? cause.message : String(cause),
            }
          : current,
      )
    } finally {
      setBusy('')
    }
  }

  const answerPrompt = async () => {
    if (!login?.prompt?.promptId || !login.sessionId) return
    const value = login.answer.trim()
    if (!value) return
    setBusy('answer')
    try {
      await submitProviderLogin(login.sessionId, login.prompt.promptId, value)
      setLogin((current) => (current ? { ...current, prompt: null, answer: '' } : current))
    } catch (cause) {
      setLogin((current) =>
        current
          ? {
              ...current,
              status: 'error',
              message: cause instanceof Error ? cause.message : String(cause),
            }
          : current,
      )
    } finally {
      setBusy('')
    }
  }

  const closeLogin = async () => {
    const sessionId = login?.sessionId
    setLogin(null)
    if (sessionId) await cancelProviderLogin(sessionId).catch(() => undefined)
  }

  const logout = async (account: ProviderAccount) => {
    setBusy(`logout:${account.id}`)
    setNotice('')
    try {
      const next = await logoutProvider(account.id)
      setSnapshot(next)
      setNotice(t('catalog.providers.loggedOut', { name: account.name }))
      onChanged?.()
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  const removeCustom = async (account: ProviderAccount) => {
    setBusy(`remove:${account.id}`)
    setNotice('')
    try {
      const next = await removeCustomProvider(account.id)
      setSnapshot(next)
      setNotice(t('catalog.providers.customRemoved', { name: account.name }))
      onChanged?.()
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy('')
    }
  }

  return (
    <section className="cat-panel cat-providers" data-testid="catalog-providers">
      <div className="cat-panel-head">
        <div>
          <h3>{t('catalog.providers.title')}</h3>
          <p className="cat-sub">
            {t('catalog.providers.sub', {
              connected,
              total: accounts.length,
            })}
          </p>
        </div>
        <button
          type="button"
          className="cat-btn"
          data-testid="catalog-providers-refresh"
          disabled={Boolean(busy)}
          onClick={() => void reload()}
        >
          {t('catalog.providers.refresh')}
        </button>
        <button
          type="button"
          className="cat-btn cat-btn-primary"
          data-testid="catalog-providers-custom"
          onClick={() => setCustomOpen(true)}
        >
          {t('catalog.providers.addCustom')}
        </button>
      </div>

      {snapshot?.warnings.map((warning) => (
        <p key={warning} className="cat-notice is-warn" data-testid="catalog-providers-warning">
          {warning}
        </p>
      ))}
      {error && (
        <p className="cat-notice is-warn" data-testid="catalog-providers-error">
          {error}
        </p>
      )}
      {notice && (
        <p className="cat-notice" role="status">
          {notice}
        </p>
      )}

      <div className="cat-provider-tools">
        <input
          className="cat-provider-search"
          value={query}
          aria-label={t('catalog.providers.search')}
          placeholder={t('catalog.providers.searchPlaceholder')}
          onChange={(event) => setQuery(event.target.value)}
        />
        <div className="cat-chips" role="group" aria-label={t('catalog.providers.filter')}>
          {(
            [
              ['all', 'catalog.providers.filterAll'],
              ['connected', 'catalog.providers.filterConnected'],
              ['login', 'catalog.providers.filterLogin'],
              ['custom', 'catalog.providers.filterCustom'],
            ] as [Filter, MessageKey][]
          ).map(([value, key]) => (
            <button
              key={value}
              type="button"
              className={`cat-chip${filter === value ? ' is-active' : ''}`}
              aria-pressed={filter === value}
              onClick={() => setFilter(value)}
            >
              {t(key)}
            </button>
          ))}
        </div>
      </div>

      {snapshot === null && !error ? (
        <p className="cat-empty">{t('catalog.providers.loading')}</p>
      ) : visible.length === 0 ? (
        <p className="cat-empty">{t('catalog.providers.empty')}</p>
      ) : (
        <div className="cat-table-wrap cat-provider-wrap">
          <table className="cat-table cat-provider-table" aria-label={t('catalog.providers.title')}>
            <thead>
              <tr>
                <th>{t('catalog.providers.provider')}</th>
                <th>{t('catalog.providers.method')}</th>
                <th>{t('catalog.providers.state')}</th>
                <th className="cat-num">{t('catalog.providers.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((account) => {
                const key = `key:${account.id}`
                const signIn = `login:${account.id}`
                const out = `logout:${account.id}`
                const custom = account.source === 'custom'
                return (
                  <tr key={account.id} data-testid={`provider-row-${account.id}`}>
                    <td>
                      <span className="cat-provider-name">
                        <span className="cat-provider-avatar" aria-hidden="true">
                          {providerInitial(account)}
                        </span>
                        <span>
                          <strong>{account.name}</strong>
                          <span className="cat-sub mono">{account.id}</span>
                          {account.baseUrlOverride && (
                            <span className="cat-sub mono" title={account.baseUrlOverride}>
                              {t('catalog.providers.endpointOverride', {
                                url: account.baseUrlOverride,
                              })}
                            </span>
                          )}
                          {custom && account.models.length > 0 && (
                            <span className="cat-sub mono">
                              {t('catalog.providers.customModelsList', {
                                models: account.models.join(', '),
                              })}
                            </span>
                          )}
                        </span>
                      </span>
                    </td>
                    <td>
                      <span className="cat-methods">
                        {account.supportsKey && (
                          <span className="cat-badge">{t('catalog.providers.methodKey')}</span>
                        )}
                        {account.oauthLabel && (
                          <span className="cat-badge is-accent">
                            {account.oauthLabel}
                            {account.oauthSubscription ? ' · Pro' : ''}
                          </span>
                        )}
                        {custom && (
                          <span className="cat-badge is-success">
                            {t('catalog.providers.custom')}
                          </span>
                        )}
                      </span>
                      {account.keyEnv && (
                        <span className="cat-sub mono">{account.keyEnv}</span>
                      )}
                    </td>
                    <td>
                      {account.credential === 'oauth' ? (
                        <span className="cat-badge is-success">
                          <i />
                          {t('catalog.providers.signedIn')}
                        </span>
                      ) : account.credential ? (
                        <span className="cat-badge is-success">
                          <i />
                          {account.keyHint
                            ? t('catalog.providers.keyStored', { hint: account.keyHint })
                            : t('catalog.providers.keyStoredPlain')}
                        </span>
                      ) : (
                        <span className="cat-sub">{t('catalog.providers.notConfigured')}</span>
                      )}
                    </td>
                    <td className="cat-num">
                      {account.supportsKey && (
                        <button
                          type="button"
                          className="cat-btn"
                          data-testid={`provider-key-${account.id}`}
                          disabled={busy === key}
                          onClick={() => setKeyTarget(account)}
                        >
                          {account.credential
                            ? t('catalog.providers.replaceKey')
                            : t('catalog.providers.setKey')}
                        </button>
                      )}
                      {account.oauthLabel && (
                        <button
                          type="button"
                          className="cat-btn"
                          data-testid={`provider-login-${account.id}`}
                          disabled={busy === signIn}
                          onClick={() => void startLogin(account)}
                        >
                          {busy === signIn
                            ? t('catalog.providers.starting')
                            : t('catalog.providers.signIn')}
                        </button>
                      )}
                      {account.credential && (
                        <button
                          type="button"
                          className="cat-btn"
                          data-testid={`provider-logout-${account.id}`}
                          disabled={busy === out}
                          onClick={() => void logout(account)}
                        >
                          {t('catalog.providers.logout')}
                        </button>
                      )}
                      {custom && (
                        <button
                          type="button"
                          className="cat-btn"
                          data-testid={`provider-remove-${account.id}`}
                          disabled={busy === `remove:${account.id}`}
                          onClick={() => void removeCustom(account)}
                        >
                          {t('catalog.providers.remove')}
                        </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      <p className="cat-sub cat-provider-foot">{t('catalog.providers.hint')}</p>
      <p className="cat-sub cat-provider-foot">
        {t('catalog.providers.files', {
          auth: snapshot?.authPath ?? '',
          models: snapshot?.modelsPath ?? '',
        })}
      </p>

      {keyTarget && (
        <KeyDialog
          account={keyTarget}
          onClose={() => setKeyTarget(null)}
          onSaved={(next) => {
            setSnapshot(next)
            setKeyTarget(null)
            setNotice(t('catalog.providers.keySaved', { name: keyTarget.name }))
            onChanged?.()
          }}
        />
      )}

      {customOpen && (
        <CustomProviderDialog
          apis={snapshot?.customApis ?? ['openai-completions']}
          onClose={() => setCustomOpen(false)}
          onSaved={(next) => {
            setSnapshot(next)
            setCustomOpen(false)
            setNotice(t('catalog.providers.customSaved'))
            onChanged?.()
          }}
        />
      )}

      {login && (
        <LoginDialog
          state={login}
          busy={busy === 'answer'}
          onAnswer={setLogin}
          onSubmit={() => void answerPrompt()}
          onClose={() => void closeLogin()}
          onRetry={() => {
            void closeLogin().then(() => startLogin(login.provider))
          }}
        />
      )}
    </section>
  )
}

/** Backdrop + dialog shell shared by the three provider dialogs. */
function Shell({
  title,
  description,
  width,
  testId,
  onClose,
  children,
  foot,
}: {
  title: string
  description?: string
  width?: number
  testId: string
  onClose: () => void
  children: ReactNode
  foot?: ReactNode
}) {
  return (
    <div
      className="cat-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.stopPropagation()
          onClose()
        }
      }}
    >
      <div
        className="cat-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        data-testid={testId}
        style={width ? { width: `min(${width}px, calc(100vw - 32px))` } : undefined}
      >
        <div className="cat-dialog-head">
          <div>
            <h2>{title}</h2>
            {description && <p>{description}</p>}
          </div>
          <button type="button" className="cat-btn" aria-label="close" onClick={onClose}>
            ✕
          </button>
        </div>
        <div className="cat-dialog-body">{children}</div>
        {foot && <div className="cat-dialog-foot">{foot}</div>}
      </div>
    </div>
  )
}

function KeyDialog({
  account,
  onClose,
  onSaved,
}: {
  account: ProviderAccount
  onClose: () => void
  onSaved: (snapshot: ProviderSnapshot) => void
}) {
  const { t } = useI18n()
  const [key, setKey] = useState('')
  const [extra, setExtra] = useState<Record<string, string>>(() =>
    Object.fromEntries(account.extra.map((field) => [field.env, ''])),
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    if (!key.trim()) {
      setError(t('catalog.providers.keyRequired'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const values = account.extra
        .map((field) => ({ name: field.env, value: (extra[field.env] ?? '').trim() }))
        .filter((item) => item.value.length > 0)
      const next = await setProviderKey(account.id, key.trim(), values)
      onSaved(next)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Shell
      title={t('catalog.providers.keyTitle', { name: account.name })}
      description={
        account.keyEnv
          ? t('catalog.providers.keyDesc', { env: account.keyEnv })
          : t('catalog.providers.keyDescPlain')
      }
      testId="provider-key-dialog"
      onClose={onClose}
      foot={
        <>
          <button type="button" className="cat-btn" onClick={onClose}>
            {t('catalog.providers.cancel')}
          </button>
          <button
            type="button"
            className="cat-btn cat-btn-primary"
            data-testid="provider-key-save"
            disabled={busy}
            onClick={() => void save()}
          >
            {busy ? t('catalog.providers.saving') : t('catalog.providers.save')}
          </button>
        </>
      }
    >
      <div className="cat-form-stack">
        <label>
          <span>{t('catalog.providers.apiKey')}</span>
          <input
            type="password"
            value={key}
            autoFocus
            autoComplete="off"
            aria-label={t('catalog.providers.apiKey')}
            placeholder="sk-…"
            onChange={(event) => setKey(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') void save()
            }}
          />
        </label>
        {account.extra.map((field) => (
          <label key={field.env}>
            <span>
              {field.label}
              {field.required ? ' *' : ''}
            </span>
            <input
              value={extra[field.env] ?? ''}
              aria-label={field.label}
              placeholder={field.env}
              onChange={(event) =>
                setExtra((current) => ({ ...current, [field.env]: event.target.value }))
              }
            />
            <small className="cat-sub mono">{field.env}</small>
          </label>
        ))}
        {account.oauthLabel && (
          <p className="cat-sub">{t('catalog.providers.keyOrSignIn', { label: account.oauthLabel })}</p>
        )}
        {error && <p className="cat-notice is-warn">{error}</p>}
      </div>
    </Shell>
  )
}

function CustomProviderDialog({
  apis,
  onClose,
  onSaved,
}: {
  apis: string[]
  onClose: () => void
  onSaved: (snapshot: ProviderSnapshot) => void
}) {
  const { t } = useI18n()
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [api, setApi] = useState(apis[0] ?? 'openai-completions')
  const [models, setModels] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    if (!id.trim() || !baseUrl.trim()) {
      setError(t('catalog.providers.customRequired'))
      return
    }
    const list = models
      .split(/[\n,]+/)
      .map((item) => item.trim())
      .filter(Boolean)
    if (list.length === 0) {
      setError(t('catalog.providers.customModelsRequired'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const next = await saveCustomProvider({
        id: id.trim(),
        name: name.trim() || undefined,
        baseUrl: baseUrl.trim(),
        api,
        apiKey: apiKey.trim() || undefined,
        models: list,
      })
      onSaved(next)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Shell
      title={t('catalog.providers.customTitle')}
      description={t('catalog.providers.customDesc')}
      width={560}
      testId="provider-custom-dialog"
      onClose={onClose}
      foot={
        <>
          <button type="button" className="cat-btn" onClick={onClose}>
            {t('catalog.providers.cancel')}
          </button>
          <button
            type="button"
            className="cat-btn cat-btn-primary"
            data-testid="provider-custom-save"
            disabled={busy}
            onClick={() => void save()}
          >
            {busy ? t('catalog.providers.saving') : t('catalog.providers.save')}
          </button>
        </>
      }
    >
      <div className="cat-form-stack">
        <label>
          <span>{t('catalog.providers.customId')}</span>
          <input
            value={id}
            autoFocus
            aria-label={t('catalog.providers.customId')}
            placeholder="my-gateway"
            onChange={(event) => setId(event.target.value)}
          />
          <small className="cat-sub">{t('catalog.providers.customIdHint')}</small>
        </label>
        <label>
          <span>{t('catalog.providers.customName')}</span>
          <input
            value={name}
            aria-label={t('catalog.providers.customName')}
            placeholder={t('catalog.providers.customNamePlaceholder')}
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        <label>
          <span>{t('catalog.providers.customBaseUrl')}</span>
          <input
            value={baseUrl}
            aria-label={t('catalog.providers.customBaseUrl')}
            placeholder="https://api.example.com/v1"
            onChange={(event) => setBaseUrl(event.target.value)}
          />
        </label>
        <label>
          <span>{t('catalog.providers.customApi')}</span>
          <select
            value={api}
            aria-label={t('catalog.providers.customApi')}
            onChange={(event) => setApi(event.target.value)}
          >
            {apis.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>
        <label>
          <span>{t('catalog.providers.customModels')}</span>
          <textarea
            value={models}
            rows={3}
            aria-label={t('catalog.providers.customModels')}
            placeholder={'gpt-5.6-sol\nqwen3-coder'}
            onChange={(event) => setModels(event.target.value)}
          />
          <small className="cat-sub">{t('catalog.providers.customModelsHint')}</small>
        </label>
        <label>
          <span>{t('catalog.providers.customKey')}</span>
          <input
            type="password"
            value={apiKey}
            autoComplete="off"
            aria-label={t('catalog.providers.customKey')}
            placeholder="sk-…"
            onChange={(event) => setApiKey(event.target.value)}
          />
          <small className="cat-sub">{t('catalog.providers.customKeyHint')}</small>
        </label>
        {error && <p className="cat-notice is-warn">{error}</p>}
      </div>
    </Shell>
  )
}

function LoginDialog({
  state,
  busy,
  onAnswer,
  onSubmit,
  onClose,
  onRetry,
}: {
  state: LoginState
  busy: boolean
  onAnswer: (next: (current: LoginState | null) => LoginState | null) => void
  onSubmit: () => void
  onClose: () => void
  onRetry: () => void
}) {
  const { t } = useI18n()
  const prompt = state.prompt
  const secret = prompt?.promptKind === 'secret' || prompt?.promptKind === 'manual_code'

  return (
    <Shell
      title={t('catalog.providers.loginTitle', { name: state.provider.name })}
      description={state.provider.oauthLabel ?? undefined}
      width={520}
      testId="provider-login-dialog"
      onClose={onClose}
      foot={
        <>
          {state.status === 'error' && (
            <button type="button" className="cat-btn" onClick={onRetry}>
              {t('catalog.providers.retry')}
            </button>
          )}
          <button
            type="button"
            className="cat-btn"
            data-testid="provider-login-close"
            onClick={onClose}
          >
            {state.status === 'done' ? t('catalog.providers.close') : t('catalog.providers.cancel')}
          </button>
          {prompt && (
            <button
              type="button"
              className="cat-btn cat-btn-primary"
              data-testid="provider-login-submit"
              disabled={busy || state.answer.trim().length === 0}
              onClick={onSubmit}
            >
              {busy ? t('catalog.providers.submitting') : t('catalog.providers.submit')}
            </button>
          )}
        </>
      }
    >
      <div className="cat-login">
        {state.status === 'starting' && (
          <p className="cat-login-status" data-testid="provider-login-starting">
            <span className="cat-spinner" aria-hidden="true" />
            {t('catalog.providers.loginStarting')}
          </p>
        )}

        {state.status === 'waiting' && !prompt && (
          <p className="cat-login-status">
            <span className="cat-spinner" aria-hidden="true" />
            {t('catalog.providers.loginWaiting')}
          </p>
        )}

        {state.url && (
          <div className="cat-login-block">
            <p>{t('catalog.providers.loginBrowser')}</p>
            <button
              type="button"
              className="cat-btn cat-btn-primary"
              data-testid="provider-login-open"
              onClick={() => void invokeHost('open_external', { url: state.url! }).catch(() => undefined)}
            >
              {t('catalog.providers.openBrowser')}
            </button>
          </div>
        )}

        {state.device && (
          <div className="cat-login-block">
            <p>{t('catalog.providers.loginDevice')}</p>
            <code className="cat-login-code" data-testid="provider-login-code">
              {state.device.code}
            </code>
            {state.device.uri && (
              <button
                type="button"
                className="cat-btn"
                onClick={() =>
                  void invokeHost('open_external', { url: state.device!.uri! }).catch(() => undefined)
                }
              >
                {t('catalog.providers.openVerification')}
              </button>
            )}
          </div>
        )}

        {prompt && (
          <div className="cat-login-block">
            <p>{prompt.message}</p>
            {prompt.options.length > 0 ? (
              <div className="cat-chips">
                {prompt.options.map((option) => (
                  <button
                    key={option.id}
                    type="button"
                    className={`cat-chip${state.answer === option.id ? ' is-active' : ''}`}
                    data-testid={`provider-login-option-${option.id}`}
                    onClick={() =>
                      onAnswer((current) => (current ? { ...current, answer: option.id } : current))
                    }
                    title={option.description ?? undefined}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
            ) : (
              <input
                type={secret ? 'password' : 'text'}
                value={state.answer}
                autoFocus
                autoComplete="off"
                aria-label={prompt.message ?? undefined}
                data-testid="provider-login-answer"
                placeholder={prompt.placeholder ?? ''}
                onChange={(event) =>
                  onAnswer((current) =>
                    current ? { ...current, answer: event.target.value } : current,
                  )
                }
                onKeyDown={(event) => {
                  if (event.key === 'Enter') onSubmit()
                }}
              />
            )}
            {prompt.promptKind === 'manual_code' && (
              <small className="cat-sub">{t('catalog.providers.manualCodeHint')}</small>
            )}
          </div>
        )}

        {state.status === 'done' && (
          <p className="cat-login-done" data-testid="provider-login-done">
            {t('catalog.providers.loginDone', { name: state.provider.name })}
          </p>
        )}

        {state.status === 'error' && (
          <p className="cat-notice is-warn" data-testid="provider-login-error">
            {state.message ?? t('catalog.providers.loginFailed')}
          </p>
        )}

        {state.log.length > 0 && (
          <ul className="cat-login-log">
            {state.log.map((line, index) => (
              <li key={`${index}-${line}`}>{line}</li>
            ))}
          </ul>
        )}
      </div>
    </Shell>
  )
}
