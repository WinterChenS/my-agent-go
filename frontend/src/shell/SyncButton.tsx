// The manual sync control.
//
// Both the model list in settings and the model picker in the composer read a
// snapshot that the host builds by scanning the machine. A scan can miss a model
// that was installed (or a credential that was fixed) after it ran, so the user
// needs a way to ask again without restarting the app.
//
// The icon rotates while the request is in flight. It is the only busy
// affordance: the button disables itself and the spin says why.

import Icon from './Icon'

export default function SyncButton({
  label,
  busy,
  disabled,
  onClick,
  text,
  testId,
}: {
  /** Accessible name; also the tooltip. */
  label: string
  busy: boolean
  disabled?: boolean
  onClick: () => void
  /** Optional visible text next to the icon. */
  text?: string
  testId?: string
}) {
  return (
    <button
      type="button"
      className={`sync-btn${busy ? ' is-busy' : ''}${text ? ' has-text' : ''}`}
      aria-label={label}
      aria-busy={busy}
      title={label}
      disabled={disabled || busy}
      data-testid={testId}
      onClick={onClick}
    >
      <Icon name="refresh" size={13} />
      {text ? <span>{text}</span> : null}
    </button>
  )
}
