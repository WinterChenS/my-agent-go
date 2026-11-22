import { useEffect } from 'react'

// The main window is a desktop surface, not a web page, so WebView2's default
// menu has to go: it offers reload, print, "save page as" and inspect — actions
// that either duplicate the app's own controls or expose machinery the reader
// should never see. The event is taken in the capture phase on the document so a
// later handler cannot hand the menu back, and every route (including the
// prototype samples) sits under the shell that installs it.
//
// The built-in browser pane is a separate child webview with its own document
// and its own menu, so it is deliberately out of reach here.
//
// Cost of this: right-click paste in the composer is gone; Ctrl+V still works.
export function useContextMenuGuard(): void {
  useEffect(() => {
    const swallow = (event: MouseEvent) => {
      event.preventDefault()
    }
    document.addEventListener('contextmenu', swallow, true)
    return () => document.removeEventListener('contextmenu', swallow, true)
  }, [])
}
