import { useEffect } from 'react'
import { useLocation } from 'react-router-dom'

interface Probe {
  viewport: string
  scrollWidth: number
  clientWidth: number
  overflow: boolean
}

function readProbe(): Probe {
  const root = document.documentElement
  return {
    viewport: `${window.innerWidth}x${window.innerHeight}`,
    scrollWidth: root.scrollWidth,
    clientWidth: root.clientWidth,
    overflow: root.scrollWidth > window.innerWidth,
  }
}

export function probeLine(probe: Probe): string {
  return `viewport=${probe.viewport} scrollWidth=${probe.scrollWidth} clientWidth=${probe.clientWidth} overflow=${probe.overflow}`
}

export async function reportLayoutProbe(): Promise<void> {
  const line = probeLine(readProbe())
  try {
    await fetch('/__anvil/log', {
      method: 'POST',
      headers: { 'Content-Type': 'text/plain' },
      body: line,
      keepalive: true,
    })
  } catch {
    // The probe is diagnostic only; a missing host endpoint must not break UI.
  }
}

// Report the real WebView viewport on mount and after every resize. The native
// acceptance case reads these lines from the application log and asserts
// overflow=false instead of trusting a hard-coded conclusion.
export function useLayoutProbe(): void {
  const location = useLocation()

  useEffect(() => {
    void reportLayoutProbe()
    let timer: number | undefined
    const onResize = () => {
      if (timer !== undefined) window.clearTimeout(timer)
      timer = window.setTimeout(() => {
        void reportLayoutProbe()
      }, 120)
    }
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [location.pathname])
}
