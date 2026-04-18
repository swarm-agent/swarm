export interface AuthMethodWire {
  id?: string
  label?: string
  credential_type?: string
  description?: string
}

export interface ProviderStatusWire {
  id?: string
  ready?: boolean
  runnable?: boolean
  reason?: string
  run_reason?: string
  default_model?: string
  default_thinking?: string
  auth_methods?: AuthMethodWire[]
}

export interface AuthConnectionStatusWire {
  connected?: boolean
  method?: string
  message?: string
  verified_at?: number
}

export interface AutoDefaultsStatusWire {
  applied?: boolean
  error?: string
  provider?: string
  model?: string
  thinking?: string
  global_model?: boolean
  agents?: string[]
  subagents?: string[]
  utility_provider?: string
  utility_model?: string
  utility_thinking?: string
}

export interface AuthCredentialWire {
  id?: string
  provider?: string
  active?: boolean
  auth_type?: string
  label?: string
  tags?: string[]
  updated_at?: number
  created_at?: number
  expires_at?: number
  last4?: string
  has_refresh_token?: boolean
  has_account_id?: boolean
  storage_mode?: string
  auto_defaults?: AutoDefaultsStatusWire
  connection?: AuthConnectionStatusWire
}

export interface AuthMethod {
  id: string
  label: string
  credentialType: string
  description: string
}

export interface ProviderStatus {
  id: string
  ready: boolean
  runnable: boolean
  reason: string
  runReason: string
  defaultModel: string
  defaultThinking: string
  authMethods: AuthMethod[]
}

export interface AuthConnectionStatus {
  connected: boolean
  method: string
  message: string
  verifiedAt: number
}

export interface AutoDefaultsStatus {
  applied: boolean
  error: string
  provider: string
  model: string
  thinking: string
  globalModel: boolean
  agents: string[]
  subagents: string[]
  utilityProvider: string
  utilityModel: string
  utilityThinking: string
}

export interface AuthCredential {
  id: string
  provider: string
  active: boolean
  authType: string
  label: string
  tags: string[]
  updatedAt: number
  createdAt: number
  expiresAt: number
  last4: string
  hasRefresh: boolean
  hasAccountID: boolean
  storageMode: string
  autoDefaults?: AutoDefaultsStatus
  connection?: AuthConnectionStatus
}

export interface AuthCredentialListResponseWire {
  provider?: string
  query?: string
  total?: number
  records?: AuthCredentialWire[]
  providers?: string[]
}

export interface AuthCredentialListResponse {
  provider: string
  query: string
  total: number
  records: AuthCredential[]
  providers: string[]
}

export interface ProvidersResponseWire {
  providers?: ProviderStatusWire[]
}

export interface VerifyAuthCredentialResponseWire {
  provider?: string
  id?: string
  connection?: AuthConnectionStatusWire
}

export interface VerifyAuthCredentialResponse {
  provider: string
  id: string
  connection: AuthConnectionStatus
}

export interface UpsertAuthCredentialInput {
  id?: string
  provider: string
  type: string
  label?: string
  tags?: string[]
  api_key?: string
  access_token?: string
  refresh_token?: string
  expires_at?: number
  account_id?: string
  active: boolean
}

export interface AuthCredentialActionInput {
  provider: string
  id: string
}

export interface CodexOAuthSessionWire {
  session_id?: string
  provider?: string
  method?: string
  label?: string
  active?: boolean
  auth_url?: string
  status?: string
  error?: string
  credential?: AuthCredentialWire
}

export interface CodexOAuthSession {
  sessionID: string
  provider: string
  method: string
  label: string
  active: boolean
  authURL: string
  status: string
  error: string
  credential?: AuthCredential
}

export interface StartCodexOAuthInput {
  provider?: string
  label?: string
  active: boolean
  method: 'browser' | 'manual'
}

export interface CompleteCodexOAuthInput {
  session_id: string
  callback_input: string
}

export function mapAuthMethod(method: AuthMethodWire): AuthMethod {
  return {
    id: String(method.id ?? '').trim(),
    label: String(method.label ?? '').trim(),
    credentialType: String(method.credential_type ?? '').trim(),
    description: String(method.description ?? '').trim(),
  }
}

export function mapProviderStatus(provider: ProviderStatusWire): ProviderStatus {
  return {
    id: String(provider.id ?? '').trim(),
    ready: Boolean(provider.ready),
    runnable: Boolean(provider.runnable),
    reason: String(provider.reason ?? '').trim(),
    runReason: String(provider.run_reason ?? '').trim(),
    defaultModel: String(provider.default_model ?? '').trim(),
    defaultThinking: String(provider.default_thinking ?? '').trim(),
    authMethods: Array.isArray(provider.auth_methods) ? provider.auth_methods.map(mapAuthMethod) : [],
  }
}

export function mapAuthConnectionStatus(status: AuthConnectionStatusWire | undefined): AuthConnectionStatus | undefined {
  if (!status) {
    return undefined
  }
  return {
    connected: Boolean(status.connected),
    method: String(status.method ?? '').trim(),
    message: String(status.message ?? '').trim(),
    verifiedAt: typeof status.verified_at === 'number' ? status.verified_at : 0,
  }
}

export function mapAutoDefaultsStatus(status: AutoDefaultsStatusWire | undefined): AutoDefaultsStatus | undefined {
  if (!status) {
    return undefined
  }
  return {
    applied: Boolean(status.applied),
    error: String(status.error ?? '').trim(),
    provider: String(status.provider ?? '').trim(),
    model: String(status.model ?? '').trim(),
    thinking: String(status.thinking ?? '').trim(),
    globalModel: Boolean(status.global_model),
    agents: Array.isArray(status.agents) ? status.agents.map((value) => String(value)) : [],
    subagents: Array.isArray(status.subagents) ? status.subagents.map((value) => String(value)) : [],
    utilityProvider: String(status.utility_provider ?? '').trim(),
    utilityModel: String(status.utility_model ?? '').trim(),
    utilityThinking: String(status.utility_thinking ?? '').trim(),
  }
}

export function mapAuthCredential(record: AuthCredentialWire): AuthCredential {
  return {
    id: String(record.id ?? '').trim(),
    provider: String(record.provider ?? '').trim(),
    active: Boolean(record.active),
    authType: String(record.auth_type ?? '').trim(),
    label: String(record.label ?? '').trim(),
    tags: Array.isArray(record.tags) ? record.tags.map((value) => String(value)) : [],
    updatedAt: typeof record.updated_at === 'number' ? record.updated_at : 0,
    createdAt: typeof record.created_at === 'number' ? record.created_at : 0,
    expiresAt: typeof record.expires_at === 'number' ? record.expires_at : 0,
    last4: String(record.last4 ?? '').trim(),
    hasRefresh: Boolean(record.has_refresh_token),
    hasAccountID: Boolean(record.has_account_id),
    storageMode: String(record.storage_mode ?? '').trim(),
    autoDefaults: mapAutoDefaultsStatus(record.auto_defaults),
    connection: mapAuthConnectionStatus(record.connection),
  }
}

export function mapCodexOAuthSession(record: CodexOAuthSessionWire): CodexOAuthSession {
  return {
    sessionID: String(record.session_id ?? '').trim(),
    provider: String(record.provider ?? '').trim(),
    method: String(record.method ?? '').trim(),
    label: String(record.label ?? '').trim(),
    active: Boolean(record.active),
    authURL: String(record.auth_url ?? '').trim(),
    status: String(record.status ?? '').trim(),
    error: String(record.error ?? '').trim(),
    credential: record.credential ? mapAuthCredential(record.credential) : undefined,
  }
}
