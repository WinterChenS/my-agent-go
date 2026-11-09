import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'

// The prototype has no persistent back control in the shared topbar (only the
// chat session header owns one). Provide the platform history shortcuts so
// Alt+Left / Alt+Right still work in the WebView and in tests.
export function useHistoryShortcuts(): void {
  const navigate = useNavigate()

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.altKey && (event.key === 'ArrowLeft' || event.key === 'BrowserBack')) {
        event.preventDefault()
        navigate(-1)
      } else if (event.altKey && (event.key === 'ArrowRight' || event.key === 'BrowserForward')) {
        event.preventDefault()
        navigate(1)
      } else if (event.metaKey && event.key === '[') {
        event.preventDefault()
        navigate(-1)
      } else if (event.metaKey && event.key === ']') {
        event.preventDefault()
        navigate(1)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [navigate])
}
