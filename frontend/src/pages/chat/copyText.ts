/**
 * Copy through the async clipboard when it is available, and fall back to a
 * hidden selection for webviews that only expose the old command. The fallback
 * restores the previous selection so copying an answer does not move the
 * reader's caret in the composer.
 */
export async function copyText(value: string): Promise<boolean> {
  if (!value) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    // Denied or unavailable in this webview; try the selection route.
  }
  try {
    const active = document.activeElement as HTMLElement | null
    const selection = document.getSelection()
    const saved = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null
    const field = document.createElement('textarea')
    field.value = value
    field.setAttribute('readonly', '')
    field.style.position = 'fixed'
    field.style.top = '-1000px'
    field.style.opacity = '0'
    document.body.appendChild(field)
    field.select()
    const ok = typeof document.execCommand === 'function' ? document.execCommand('copy') : false
    field.remove()
    if (saved && selection) {
      selection.removeAllRanges()
      selection.addRange(saved)
    } else {
      active?.focus?.()
    }
    return ok
  } catch {
    return false
  }
}
