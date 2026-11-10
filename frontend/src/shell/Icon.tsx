import type { ReactNode } from 'react'

const ICONS: Record<string, ReactNode> = {
  grid: (
    <>
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    </>
  ),
  message: <path d="M21 11.5a8.4 8.4 0 0 1-9 8.4 9.3 9.3 0 0 1-3.9-.8L3 21l1.9-4.9A8.4 8.4 0 0 1 12 3.1a8.4 8.4 0 0 1 9 8.4z" />,
  folder: <path d="M3.5 7.5a2 2 0 0 1 2-2h3.2l2 2.4h7.8a2 2 0 0 1 2 2v8.6a2 2 0 0 1-2 2H5.5a2 2 0 0 1-2-2z" />,
  bot: (
    <>
      <rect x="4" y="8" width="16" height="12" rx="3" />
      <path d="M12 4v4M9 14h.01M15 14h.01M9.5 17.5h5" />
    </>
  ),
  puzzle: (
    <>
      <path d="M10 4.5h2.2a1.4 1.4 0 0 1 1.4 1.4c0 .8.7 1.4 1.5 1.4s1.5-.6 1.5-1.5V4.5H20v4.2a1.4 1.4 0 0 1-2.8 0c0-.8-.6-1.4-1.4-1.4s-1.4.6-1.4 1.4V12h4.4v5.8h-3.9a1.4 1.4 0 0 0-1.4 1.4V21H10v-2.4a1.4 1.4 0 0 1 2.8 0" />
      <path d="M10 4.5H5.8A1.8 1.8 0 0 0 4 6.3v3.9h2.4a1.4 1.4 0 0 1 1.4 1.4 1.4 1.4 0 0 1-1.4 1.4H4v4.2a1.8 1.8 0 0 0 1.8 1.8H10" />
    </>
  ),
  plug: (
    <>
      <path d="M9 3v5M15 3v5M6.5 8h11v3.2a5.5 5.5 0 0 1-11 0z" />
      <path d="M12 16.7V21" />
    </>
  ),
  link: (
    <>
      <path d="M10.5 13.5a3.5 3.5 0 0 0 5 0l3-3a3.5 3.5 0 0 0-5-5l-1.2 1.2" />
      <path d="M13.5 10.5a3.5 3.5 0 0 0-5 0l-3 3a3.5 3.5 0 0 0 5 5l1.2-1.2" />
    </>
  ),
  clock: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.5V12l3 2" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 14a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-2.9-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.7 1.7 0 0 0 3.2 13H3a2 2 0 1 1 0-4h.2a1.7 1.7 0 0 0 1.2-2.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.7 1.7 0 0 0 10 3.2V3a2 2 0 1 1 4 0v.2a1.7 1.7 0 0 0 2.9 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0 1.2 2.9h.2a2 2 0 1 1 0 4h-.2a1.7 1.7 0 0 0-1.5.9z" />
    </>
  ),
  layers: (
    <>
      <path d="M12 3.5l8 4-8 4-8-4z" />
      <path d="M4 12l8 4 8-4M4 16.5l8 4 8-4" />
    </>
  ),
  search: (
    <>
      <circle cx="11" cy="11" r="6.5" />
      <path d="M16 16l4.5 4.5" />
    </>
  ),
  help: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M9.6 9.4a2.5 2.5 0 1 1 3.3 2.4c-.6.2-.9.8-.9 1.4v.4M12 17h.01" />
    </>
  ),
  arrowLeft: <path d="M19 12H5M11 6l-6 6 6 6" />,
  chevDown: <path d="M6 9l6 6 6-6" />,
  chevron: <path d="M9 6l6 6-6 6" />,
  plus: <path d="M12 5v14M5 12h14" />,
  more: <><circle cx="5" cy="12" r="1" /><circle cx="12" cy="12" r="1" /><circle cx="19" cy="12" r="1" /></>,
  download: <path d="M12 3v12M7 10l5 5 5-5M4 21h16" />,
  panel: <><rect x="3.5" y="4.5" width="17" height="15" rx="2" /><path d="M15 4.5v15" /></>,
  brain: <path d="M9.5 4.5a3 3 0 0 0-3 3 3.2 3.2 0 0 0-1.3 5.8A3.3 3.3 0 0 0 8 18.8a3 3 0 0 0 4 1.7V4.5a3 3 0 0 0-2.5 0zM14.5 4.5a3 3 0 0 1 3 3 3.2 3.2 0 0 1 1.3 5.8 3.3 3.3 0 0 1-2.8 5.5 3 3 0 0 1-4 1.7V4.5a3 3 0 0 1 2.5 0z" />,
  file: <><path d="M6 3.5h7l5 5v12H6z" /><path d="M13 3.5v5h5M9 13h6M9 16.5h6" /></>,
  terminal: <><rect x="3.5" y="4.5" width="17" height="15" rx="2" /><path d="M7 9l3 3-3 3M13 15h4" /></>,
  shield: <path d="M12 3.5l7 2.7v5.4c0 4-2.9 7.6-7 9-4.1-1.4-7-5-7-9V6.2z" />,
  pause: <><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></>,
  send: <path d="M20 4L3.8 10.4l6.4 2.4 2.4 6.4z" />,
  copy: <><rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" /></>,
  refresh: <><path d="M20 11a8 8 0 0 0-14-4L4 9M4 4v5h5M4 13a8 8 0 0 0 14 4l2-2M20 20v-5h-5" /></>,
  x: <path d="M6 6l12 12M18 6L6 18" />,
  check: <path d="M5 12l4 4L19 6" />,
  trash: <><path d="M4 7h16M9 7V4.5h6V7M6.5 7l1 13h9l1-13M10 11v5M14 11v5" /></>,
  archive: <><rect x="3.5" y="4.5" width="17" height="4" rx="1" /><path d="M5 8.5v10a1.5 1.5 0 0 0 1.5 1.5h11a1.5 1.5 0 0 0 1.5-1.5v-10M10 12.5h4" /></>,
  branch: <><circle cx="6" cy="5" r="2" /><circle cx="6" cy="19" r="2" /><circle cx="18" cy="7" r="2" /><path d="M6 7v10M8 12h4a6 6 0 0 0 6-6" /></>,
  external: <><path d="M14 4h6v6M20 4l-9 9" /><path d="M19 13v5a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h5" /></>,
  paperclip: <path d="M20 11.5l-8.2 8.2a5 5 0 0 1-7.1-7.1L13 4.3a3.4 3.4 0 0 1 4.8 4.8l-8.2 8.2a1.7 1.7 0 0 1-2.4-2.4l7.6-7.6" />,
  sparkle: <><path d="M12 3.5l1.8 5.2 5.2 1.8-5.2 1.8L12 17.5l-1.8-5.2L5 10.5l5.2-1.8z" /><path d="M18.5 16.5l.8 2 2 .8-2 .8-.8 2-.8-2-2-.8 2-.8z" /></>,
  globe: <><circle cx="12" cy="12" r="8.5" /><path d="M3.5 12h17M12 3.5c2.2 2.3 3.3 5.1 3.3 8.5S14.2 18.2 12 20.5M12 3.5c-2.2 2.3-3.3 5.1-3.3 8.5s1.1 6.2 3.3 8.5" /></>,
  alert: <><path d="M12 3.5L21 20H3z" /><path d="M12 9v5M12 17h.01" /></>,
  pin: <path d="M8 4.5h8l-1.2 5 2.7 2.7v1.3h-11v-1.3l2.7-2.7zM12 13.5v7" />,
}

export default function Icon({ name, size = 16 }: { name: string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {ICONS[name] ?? ICONS.grid}
    </svg>
  )
}
