/**
 * The connectors API.
 *
 * A connector is a service the user has an account on (mail, Notion, DingTalk
 * docs): it has credentials, it can be tested against the real service, and it
 * exposes a few tools to the agent. That is a different thing from an MCP
 * server, which is why this file shares nothing with the MCP client.
 */
import { invokeHost, isTauriHost } from '../../host/tauri'

export type ConnectorFieldKind = 'text' | 'password' | 'number' | 'select' | 'tags'

export interface ConnectorFieldOption {
  value: string
  label: string
}

export interface ConnectorField {
  key: string
  label: string
  hint: string
  placeholder: string
  kind: ConnectorFieldKind
  /** The value never reaches the UI; only `secretsSet` says one is stored. */
  secret: boolean
  required: boolean
  options: ConnectorFieldOption[]
}

export interface ConnectorToolView {
  name: string
  description: string
  /** Writes or sends: shown as a badge so the user knows what they allow. */
  write: boolean
  calls: number
  failures: number
}

export interface ConnectorType {
  id: string
  name: string
  description: string
  capabilities: string[]
  fields: ConnectorField[]
  tools: ConnectorToolView[]
  count: number
}

export interface ConnectorRecord {
  id: string
  kind: string
  kindName: string
  name: string
  enabled: boolean
  /** `declared` (never probed) · `connected` · `failed` */
  state: string
  probedAt: string | null
  lastError: string | null
  config: Record<string, string>
  secretsSet: string[]
  capabilities: string[]
  tools: ConnectorToolView[]
  calls: number
  failures: number
  lastCallAt: string | null
}

export interface ConnectorStats {
  total: number
  connected: number
  unverified: number
  failed: number
  calls: number
  failures: number
  tools: number
}

export interface ConnectorActivity {
  connectorId: string
  name: string
  kindName: string
  tool: string
  ok: boolean
  at: string
  detail: string | null
}

export interface ConnectorOverview {
  types: ConnectorType[]
  connectors: ConnectorRecord[]
  stats: ConnectorStats
  activity: ConnectorActivity[]
  updatedAt: string
}

export interface ConnectorProbeOutcome {
  ok: boolean
  message: string
  steps: string[]
}

export interface ConnectorDraft {
  id?: string
  kind: string
  name: string
  config: Record<string, string>
}

/** Whether the page runs inside the app (a plain browser has no host). */
export function hasConnectorHost(): boolean {
  return isTauriHost()
}

export function loadConnectors(): Promise<ConnectorOverview> {
  return invokeHost<ConnectorOverview>('connectors_overview', {
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
  })
}

export function saveConnector(draft: ConnectorDraft): Promise<ConnectorOverview> {
  return invokeHost<ConnectorOverview>('connector_save', { draft })
}

export function deleteConnector(id: string): Promise<ConnectorOverview> {
  return invokeHost<ConnectorOverview>('connector_delete', { id })
}

export function toggleConnector(id: string, enabled: boolean): Promise<ConnectorOverview> {
  return invokeHost<ConnectorOverview>('connector_toggle', { id, enabled })
}

/** Test one connector against the real service (network, credentials). */
export function probeConnector(id: string): Promise<ConnectorProbeOutcome> {
  return invokeHost<ConnectorProbeOutcome>('connector_probe', { id })
}
