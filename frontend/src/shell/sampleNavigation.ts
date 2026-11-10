import { useLocation } from 'react-router-dom'

// Isolated shell fixture used only by the replayable prototype entries. The
// normal product shell never reads these counts.
export const SHELL_SAMPLE_FIXTURE = {
  chatCount: 4,
  toolCounts: {
    library: 28,
    skills: 9,
    mcp: 3,
    connectors: 5,
    schedules: 6,
  } as Record<string, number>,
}

export function useShellSample(): boolean {
  const { pathname, search } = useLocation()
  if (pathname === '/__sample/workbench' || pathname === '/__sample/chat' || pathname === '/__sample/agents') return true
  const sample = new URLSearchParams(search).get('sample')
  return sample === 'workbench' || sample === 'chat' || sample === 'agents'
}
