// How the chat pane shows a page, decided by the host before the first frame.
//
// Windows and macOS float a real child webview over the pane, so the page only
// has to report where the pane is. Linux cannot do that: Tauri packs every
// webview into the window's own `GtkBox`, so the second one becomes a sibling
// of the app instead of a layer above it — the app gets pushed aside and
// `set_bounds` moves nothing. There the pane embeds the page in a frame and
// never calls the native browser commands at all.
//
// The value comes from `html[data-browser-pane]`, injected by the host; a page
// built without that attribute (tests, a browser dev server) keeps the
// historical native path.

export type BrowserPaneMode = 'native' | 'iframe'

/** Which pane this build uses: `native` unless the host says `iframe`. */
export function browserPaneMode(): BrowserPaneMode {
  if (typeof document === 'undefined') return 'native'
  return document.documentElement.dataset.browserPane === 'iframe' ? 'iframe' : 'native'
}
