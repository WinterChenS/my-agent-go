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
  /** Offered in the chat picker. Discovery leaves this off on purpose. */
  enabled: boolean
  updatedAt: string
  hasCredential: boolean
}

/** A saved agent: its binding plus every setting the page edits. */
export interface CatalogAgent {
  id: string
  name: string
  slug: string
  description: string
  runtimeId: string
  modelId: string
  isDefault: boolean
  thinking: 'off' | 'low' | 'medium' | 'high'
  contextWindow: string
  memory: boolean
  autoCompact: boolean
  systemPrompt: string
  tools: string[]
  skills: string[]
  permission: 'readonly' | 'medium' | 'high' | 'full'
  writableDirs: string
  commandAllowlist: string
  network: boolean
  confirmDangerous: boolean
  groupKind: 'main' | 'sub'
  delegatedFrom: string
  delegationPolicy: 'auto' | 'explicit' | 'never'
  concurrency: number
  enabled: boolean
  updatedAt: string
}

/** What the page sends when it saves. `id` is absent for a new agent. */
export type CatalogAgentDraft = Omit<CatalogAgent, 'id' | 'isDefault' | 'updatedAt'> & {
  id?: string
}

/** Tool names pi exposes; the page labels them with the prototype's copy. */
export const BUILTIN_TOOLS = ['read', 'write', 'edit', 'bash', 'grep', 'find', 'ls'] as const

/** A blank draft, used by "新建 Agent" and by tests. */
export function blankAgentDraft(): CatalogAgentDraft {
  return {
    name: '',
    slug: '',
    description: '',
    runtimeId: '',
    modelId: '',
    thinking: 'medium',
    contextWindow: '400K',
    memory: false,
    autoCompact: true,
    systemPrompt: '',
    tools: [...BUILTIN_TOOLS],
    skills: [],
    permission: 'medium',
    writableDirs: '',
    commandAllowlist: '',
    network: true,
    confirmDangerous: true,
    groupKind: 'main',
    delegatedFrom: '',
    delegationPolicy: 'auto',
    concurrency: 4,
    enabled: true,
  }
}

/**
 * Fill in persona defaults for a row written by an older host (or a test
 * fixture). The desktop host always returns every field; this keeps the page
 * from handing `undefined` to a controlled input.
 */
export function normalizeAgent(raw: CatalogAgent): CatalogAgent {
  const base = blankAgentDraft()
  return {
    id: raw.id,
    name: raw.name ?? '',
    slug: raw.slug ?? raw.id,
    description: raw.description ?? '',
    runtimeId: raw.runtimeId ?? '',
    modelId: raw.modelId ?? '',
    isDefault: raw.isDefault ?? false,
    thinking: raw.thinking ?? base.thinking,
    contextWindow: raw.contextWindow ?? base.contextWindow,
    memory: raw.memory ?? base.memory,
    autoCompact: raw.autoCompact ?? base.autoCompact,
    systemPrompt: raw.systemPrompt ?? base.systemPrompt,
    tools: raw.tools ?? base.tools,
    skills: raw.skills ?? base.skills,
    permission: raw.permission ?? base.permission,
    writableDirs: raw.writableDirs ?? base.writableDirs,
    commandAllowlist: raw.commandAllowlist ?? base.commandAllowlist,
    network: raw.network ?? base.network,
    confirmDangerous: raw.confirmDangerous ?? base.confirmDangerous,
    groupKind: raw.groupKind ?? base.groupKind,
    delegatedFrom: raw.delegatedFrom ?? base.delegatedFrom,
    delegationPolicy: raw.delegationPolicy ?? base.delegationPolicy,
    concurrency: raw.concurrency ?? base.concurrency,
    enabled: raw.enabled ?? base.enabled,
    updatedAt: raw.updatedAt ?? '',
  }
}

/** Copy a stored agent into an editable draft. */
export function agentToDraft(agent: CatalogAgent): CatalogAgentDraft {
  const { id, isDefault: _isDefault, updatedAt: _updatedAt, ...rest } = agent
  return { ...rest, id }
}

/** One row of the agent's real run history, read from the session ledger. */
export interface AgentRun {
  sessionId: string
  title: string
  at: string
  turns: number
  toolCalls: number
  tokens: number
  status: string
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
  agents: CatalogAgent[]
  skills: CatalogSkill[]
  tools: unknown[]
  mcp: CatalogMcp[]
  syncs: CatalogSync[]
  adapterFailures: CatalogFailure[]
  scannedAt: string | null
  /**
   * Discovery is still running in the host. The rows above are what it last
   * committed, so "no runtime" only means "nothing read yet" while this is true.
   */
  scanning?: boolean
  /** The user finished first-run setup; kept in the data directory. */
  setupDone?: boolean
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

/**
 * Re-read local skills, tools and MCP declarations without probing models.
 * Cheap enough to run on a timer; `refreshCatalog` remains the full rescan.
 */
export function syncCatalogInventory(): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_sync_inventory')
}

/**
 * Re-read one runtime's model list.
 *
 * Every adapter reads its models from a local file, so a per-runtime sync is a
 * few milliseconds — unlike `refreshCatalog`, which first spawns each runtime
 * to read its version before syncing everything.
 */
export function syncCatalogRuntime(runtimeId: string): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_sync', { runtimeId })
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

export function setCatalogModelEnabled(
  runtimeId: string,
  modelId: string,
  enabled: boolean,
): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_set_model_enabled', { runtimeId, modelId, enabled })
}

/**
 * Record that first-run setup is finished.
 *
 * The wizard keeps a localStorage flag too, but that lives in the WebView and
 * a forced quit or a storage reset drops it. This one lives in the data
 * directory, so a configured profile does not open on the wizard again.
 */
export function markCatalogSetupDone(): Promise<void> {
  return hostCall<void>('catalog_mark_setup_done')
}

/** Save the whole agent draft; the host persists every field it edits. */
export function saveCatalogAgent(draft: CatalogAgentDraft): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_save_agent', { agent: draft })
}

/** The agent's run records, read from the real session ledger. */
export function loadAgentRuns(agentId: string, limit = 20): Promise<AgentRun[]> {
  return hostCall<AgentRun[]>('agent_runs', { agentId, limit })
}

export function deleteCatalogAgent(id: string): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_delete_agent', { id })
}

export function probeCatalogMcp(runtimeId: string, name: string): Promise<McpProbeResult> {
  return hostCall<McpProbeResult>('catalog_probe_mcp', { runtimeId, name })
}

/**
 * Delete one declared server.
 *
 * This edits the runtime's own MCP config (JSON or TOML) and rescans, so the
 * server is really gone: a "delete" that only hid the row would come back on
 * the next sync. The returned snapshot is the catalogue after the removal.
 */
export function removeCatalogMcp(runtimeId: string, name: string): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_remove_mcp', { runtimeId, name })
}

/** What the add-service form sends to the host. */
export interface McpDraft {
  runtimeId: string
  name: string
  transport: string
  command: string | null
  args: string[]
  url: string | null
  env: Array<[string, string]>
}

/** Write the declaration into the runtime's own config and rescan it. */
export function addCatalogMcp(draft: McpDraft): Promise<CatalogSnapshot> {
  return hostCall<CatalogSnapshot>('catalog_add_mcp', { ...draft })
}

/** Try a declaration without saving it. */
export function testCatalogMcp(draft: McpDraft): Promise<McpProbeResult> {
  return hostCall<McpProbeResult>('catalog_test_mcp', {
    transport: draft.transport,
    command: draft.command,
    args: draft.args,
    url: draft.url,
  })
}

/**
 * Host answers `catalog_snapshot` from disk and scans in the background, so
 * `loading` means "nothing read yet", never "waiting on a probe".
 */
export const CATALOG_STATUS = ['idle', 'loading', 'ready', 'error', 'unavailable'] as const
export type CatalogStatus = (typeof CATALOG_STATUS)[number]

/**
 * Whether pi's `subagent` extension is active for this user.
 *
 * `canInstall` says this build found the extension inside the installed pi
 * package; `installed` says pi will actually load it. A runtime capability, so
 * it lives with the catalog client rather than with chat.
 */
export interface PiSubagentStatus {
  installed: boolean
  agentDir: string
  extensionDir: string
  source?: string
  agents: number
  prompts: number
  canInstall: boolean
  detail?: string
}

/** What one install wrote, plus the status read back after it. */
export interface PiSubagentInstall {
  written: number
  skipped: number
  status: PiSubagentStatus
}

export function piSubagentStatus(): Promise<PiSubagentStatus> {
  return hostCall<PiSubagentStatus>('pi_subagent_status')
}

export function installPiSubagent(): Promise<PiSubagentInstall> {
  return hostCall<PiSubagentInstall>('pi_subagent_install')
}

/** What one runtime install attempt did (npm's own words included). */
export interface RuntimeInstallReport {
  runtimeId: string
  ok: boolean
  package: string
  prefix: string
  installedPath: string | null
  output: string
  elapsedMs: number
}

export interface RuntimeInstallResult {
  report: RuntimeInstallReport
  snapshot: CatalogSnapshot
}

/**
 * Install one runtime CLI through npm, then rescan.
 *
 * The host runs npm into the same shared prefix the adapters scan, so the
 * returned snapshot already shows the runtime as available — no manual
 * re-detect and no restart.
 */
export function installRuntime(runtimeId: string): Promise<RuntimeInstallResult> {
  return hostCall<RuntimeInstallResult>('runtime_install', { runtimeId })
}

/** What npm says about a runtime package, compared with the installed CLI. */
export interface RuntimeUpdateCheck {
  runtimeId: string
  package: string
  installed: string | null
  latest: string | null
  updateAvailable: boolean
  checkedAt: string
  error: string | null
}

/**
 * Ask the registry for the latest published version.
 *
 * Read-only and explicit: opening settings never calls npm, so this runs only
 * when the user asks. The reply says which version is installed, which is
 * published, and whether the published one is newer.
 */
export function checkRuntimeUpdate(runtimeId: string): Promise<RuntimeUpdateCheck> {
  return hostCall<RuntimeUpdateCheck>('runtime_update_check', { runtimeId })
}
