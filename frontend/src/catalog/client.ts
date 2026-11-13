// Typed access to the phase-1 catalog commands.
//
// The desktop host answers these; a plain browser (or jsdom) has no catalog, so
// `loadCatalog` rejects with `catalogUnavailable` and the pages fall back to
// their honest empty state instead of inventing runtimes.

import { invokeHost, isTauriHost } from '../host/tauri'

export interface CatalogCapabilities {
  models: string
  customModels: string
  skills: string
  tools: string
  mcp: string
  sessions: string
}

export interface CatalogHealth {
  checkedAt: string
  latencyMs: number
  detail: string | null
}

export interface CatalogRuntime {
  id: string
  runtimeId: string
  displayName: string
  version: string | null
  installPath: string
  installSource: string
  configPath: string | null
  status: string
  capabilities: CatalogCapabilities
  health: CatalogHealth
  lastSeen: string
  firstSeen: string
  snapshotState: string
  adapterRegistered: boolean
}

export interface CatalogModel {
  runtimeId: string
  modelId: string
  displayName: string
  origin: string
  protocol: string | null
  endpoint: string | null
  verified: boolean
  verifiedAt: string | null
  isDefault: boolean
  updatedAt: string
  hasCredential: boolean
}

export interface CatalogSkill {
  runtimeId: string
  name: string
  description: string
  root: string
  path: string
  state: string
  reason: string | null
  updatedAt: string
}

export interface CatalogMcp {
  runtimeId: string
  name: string
  transport: string
  command: string | null
  args: string[]
  url: string | null
  envKeys: string[]
  headerKeys: string[]
  source: string
  state: string
  probedAt: string | null
}

export interface CatalogSync {
  runtimeId: string
  kind: string
  ok: boolean
  itemCount: number
  fetchedAt: string
  stale: boolean
  error: string | null
}

export interface CatalogFailure {
  runtimeId: string
  phase: string
  error: string
}

export interface CatalogSnapshot {
  runtimes: CatalogRuntime[]
  models: CatalogModel[]
  skills: CatalogSkill[]
  tools: unknown[]
  mcp: CatalogMcp[]
  syncs: CatalogSync[]
  adapterFailures: CatalogFailure[]
  scannedAt: string | null
}

export interface ModelVerification {
  ok: boolean
  detail: string | null
}

export interface McpProbeResult {
  ok: boolean
  tools: string[]
  detail: string | null
}

/** Marker error so callers can distinguish "no host" from a real failure. */
export class CatalogUnavailable extends Error {
  constructor() {
    super('catalog is not available outside the desktop host')
    this.name = 'CatalogUnavailable'
  }
}

function hostCall<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  if (!isTauriHost()) return Promise.reject(new CatalogUnavailable())
  return invokeHost<T>(command, args)
}

export function loadCatalog(): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_snapshot')
}

export function refreshCatalog(): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_refresh')
}

export function addCustomModel(input: {
  runtimeId: string
  modelId: string
  displayName: string
  protocol?: string
  endpoint?: string
  credential?: string
}): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_add_model', {
    runtimeId: input.runtimeId,
    modelId: input.modelId,
    displayName: input.displayName,
    protocol: input.protocol,
    endpoint: input.endpoint,
    credential: input.credential,
  })
}

export function verifyCatalogModel(runtimeId: string, modelId: string): Promise<ModelVerification> {
  return hostCall<ModelVerification>('catalog_verify_model', { runtimeId, modelId })
}

export function setDefaultCatalogModel(runtimeId: string, modelId: string): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_set_default_model', { runtimeId, modelId })
}

export function probeCatalogMcp(runtimeId: string, name: string): Promise<McpProbeResult> {
  return hostCall<McpProbeResult>('catalog_probe_mcp', { runtimeId, name })
}

/** Host runs the scan on the first call, so `loading` covers discovery too. */
export const CATALOG_STATUS = ['idle', 'loading', 'ready', 'error', 'unavailable'] as const
export type CatalogStatus = (typeof CATALOG_STATUS)[number]
