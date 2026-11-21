/** Clipboard access that also works in a webview without the async API. */
export async function writeClipboard(value: string): Promise<boolean> {
  if (!value) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    // Fall through to the selection-based route.
  }
  try {
    const field = document.createElement('textarea')
    field.value = value
    field.setAttribute('readonly', '')
    field.style.cssText = 'position:fixed;top:-1000px;opacity:0'
    document.body.appendChild(field)
    field.select()
    const ok = typeof document.execCommand === 'function' ? document.execCommand('copy') : false
    field.remove()
    return ok
  } catch {
    return false
  }
}

export async function readClipboard(): Promise<string> {
  if (navigator.clipboard?.readText) return navigator.clipboard.readText()
  throw new Error('clipboard read is unavailable')
}
