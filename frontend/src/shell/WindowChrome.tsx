// Window chrome for the platforms where the app owns it.
//
// Windows keeps its system title bar, so this renders nothing there. macOS keeps
// its traffic lights (a transparent overlay title bar) and only needs the drag
// strip the lights sit on. Linux runs borderless, so the page has to draw
// minimize / maximize / close itself and provide the drag strip.
//
// The platform comes from the first-frame injection (`html[data-window-chrome]`),
// so this is decided before React runs and nothing flashes.

import type { ReactNode } from 'react'
import { useI18n } from '../i18n/LanguageProvider'
import { invokeHost } from '../host/tauri'

/** Which chrome this build draws: `macos`, `linux`, `windows`, or '' in tests. */
export function windowChromeKind(): string {
  if (typeof document === 'undefined') return ''
  return document.documentElement.dataset.windowChrome ?? ''
}

function WindowButton({
  label,
  command,
  danger,
  children,
}: {
  label: string
  command: string
  danger?: boolean
  children: ReactNode
}) {
  return (
    <button
      type="button"
      className={`window-control${danger ? ' is-danger' : ''}`}
      aria-label={label}
      title={label}
      data-window-command={command}
      onClick={() => {
        // No label argument: Tauri resolves the window the caller runs in.
        void invokeHost(`plugin:window|${command}`).catch(() => undefined)
      }}
    >
      <svg
        width="12"
        height="12"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        aria-hidden="true"
        focusable="false"
      >
        {children}
      </svg>
    </button>
  )
}

export default function WindowChrome() {
  const { t } = useI18n()
  const kind = windowChromeKind()
  if (!kind || kind === 'windows') return null

  return (
    <>
      {/* The page owns the top edge now, so this strip is what drags the window
          and what a double click maximizes. It must stay above the page and
          below the window buttons. */}
      <div className="window-drag-strip" data-tauri-drag-region aria-hidden="true" />
      {kind === 'linux' ? (
        <div className="window-controls" role="group" aria-label={t('window.controls')}>
          <WindowButton label={t('window.minimize')} command="minimize">
            <path d="M5 12h14" />
          </WindowButton>
          <WindowButton label={t('window.maximize')} command="toggle_maximize">
            <rect x="5.5" y="5.5" width="13" height="13" rx="1.5" />
          </WindowButton>
          <WindowButton label={t('window.close')} command="close" danger>
            <path d="M6 6l12 12M18 6L6 18" />
          </WindowButton>
        </div>
      ) : null}
    </>
  )
}
