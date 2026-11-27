// A one-line bus for "the catalog changed under you".
//
// Settings turns a model on (or installs an extension) while the chat page may
// already be mounted with a list read before that happened. Rather than have
// the page reach into chat state, it announces the change and interested pages
// re-read the catalog (a cheap local read once the startup scan is done).

const EVENT = 'anvil:catalog-changed'

export function notifyCatalogChanged(): void {
  window.dispatchEvent(new CustomEvent(EVENT))
}

/** Subscribe; returns the unsubscribe function. */
export function onCatalogChanged(handler: () => void): () => void {
  window.addEventListener(EVENT, handler)
  return () => window.removeEventListener(EVENT, handler)
}
