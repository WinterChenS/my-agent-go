import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { startCatalogPrefetch } from './catalog/prefetch'
import './styles.css'

const container = document.getElementById('root')
if (!container) {
  throw new Error('root container is missing from index.html')
}

// Discovery probes every installed runtime, so start it before the first paint
// instead of on the first component that needs a model list. The result is
// cached; whoever reads the catalog next gets it without waiting again.
void startCatalogPrefetch()?.catch(() => {
  // The catalog surfaces its own failure with a retry; a rejected prefetch must
  // not become an unhandled rejection in the console.
})

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
