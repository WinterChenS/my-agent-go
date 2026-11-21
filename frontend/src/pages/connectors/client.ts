// Typed access to connectors.
//
// A connector is an MCP server the runtime can reach. The catalog knows its
// transport, endpoint and last probe; the ledger knows how often its tools were
// actually called. Both are real observations — a server nobody has probed
// reports zero actions rather than a guessed number.

import { invokeHost, isTauriHost } from '../../host/tauri'

export interface ConnectorActionRow {
  tool: string
  calls: number
  failures: number
}

export interface Connector {
  runtimeId: string
  name: string
  transport: string
  endpoint: string
  /** `declared` | `connected` | `failed` | `unsupported_transport` | `missing_command`. */
  state: string
  probedAt: string | null
  tools: string[]
  calls: number
  failures: number
  lastUsed: string | null
  sessions: number
  actions: ConnectorActionRow[]
}

export interface ConnectorStats {
  total: number
  connected: number
  actions: number
  weekCalls: number
  weekFailures: number
  successRate: number | null
  lastProbe: string | null
  remoteUnprobeable: boolean
}

export interface ConnectorActivityRow {
  at: string
  connector: string
  tool: string
  ok: boolean
  sessionId: string
}

export interface ConnectorView {
  connectors: Connector[]
  stats: ConnectorStats
  activity: ConnectorActivityRow[]
  ready: boolean
}

export interface ProbeResult {
  ok: boolean
  tools: string[]
  detail?: string | null
}

export function hasConnectorHost(): boolean {
  return isTauriHost()
}

export async function loadConnectors(): Promise<ConnectorView> {
  return invokeHost<ConnectorView>('connectors_overview', {
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
  })
}

export async function verifyConnector(runtimeId: string, name: string): Promise<ProbeResult> {
  return invokeHost<ProbeResult>('connector_verify', { runtimeId, name })
}
