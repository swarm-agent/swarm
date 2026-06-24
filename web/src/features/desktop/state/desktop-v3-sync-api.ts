import { apiFetch, requestJson } from '../../../app/api'

import type { SessionsReconnectResponse, SyncHistory, SyncResources, SyncSelector, SyncSnapshotResponse } from './desktop-v3-cache-types'

export interface DesktopV3BootstrapInput {
  surface: 'desktop' | string
  selector: SyncSelector
  history: SyncHistory
  resources: SyncResources
  include_active: boolean
}

export interface DesktopV3KnownSessionState {
  endpoint_cursor?: string
  applied_seq?: number
  high_watermark?: number
}

export interface DesktopV3HydrateInput {
  surface: 'desktop' | string
  session_ids: string[]
  history: SyncHistory
  resources: SyncResources
  include_active: boolean
  known_sessions?: Record<string, DesktopV3KnownSessionState>
}

export const DESKTOP_STARTUP_SESSION_LIMIT = 50
export const DESKTOP_STARTUP_MESSAGE_LIMIT = 200
export const DESKTOP_SELECTED_HYDRATE_RESPONSE_BYTE_BUDGET = 512 * 1024

export const DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT: DesktopV3BootstrapInput = {
  surface: 'desktop',
  selector: {
    kind: 'recent',
    global: true,
    recent: {
      limit: DESKTOP_STARTUP_SESSION_LIMIT,
    },
  },
  history: {
    mode: 'none',
  },
  resources: {
    messages: false,
    events: false,
    run_intents: false,
    current_run_state: true,
    session_view: false,
    active_plan: false,
    plan_revisions: false,
  },
  include_active: true,
}

export const DESKTOP_V3_INITIAL_HYDRATE_DEFAULT_RESOURCES: SyncResources = {
  messages: true,
  events: false,
  run_intents: true,
  current_run_state: true,
  session_view: true,
  active_plan: false,
  plan_revisions: false,
}

export function buildDesktopV3SelectedSessionHydrateInput(sessionId: string): DesktopV3HydrateInput {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Desktop V3 selected-session hydrate requires session_id')
  }
  return {
    surface: 'desktop',
    session_ids: [normalizedSessionId],
    history: {
      mode: 'tail',
      max_messages_per_session: DESKTOP_STARTUP_MESSAGE_LIMIT,
      manifest_policy: 'manifest',
    },
    resources: { ...DESKTOP_V3_INITIAL_HYDRATE_DEFAULT_RESOURCES },
    include_active: true,
  }
}

export function buildDesktopV3BootstrapInput(
  input: Partial<DesktopV3BootstrapInput> = {},
  preferredSessionId?: string | null,
): DesktopV3BootstrapInput {
  const sourceSelector = input.selector ?? DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.selector
  const preferred = preferredSessionId?.trim()
  const sessionIds = [...(sourceSelector.session_ids ?? [])]
  if (preferred && !sessionIds.includes(preferred)) {
    sessionIds.unshift(preferred)
  }
  const selector: SyncSelector = {
    ...sourceSelector,
    workspace_paths: sourceSelector.workspace_paths ? [...sourceSelector.workspace_paths] : undefined,
    session_ids: sessionIds.length > 0 ? sessionIds : undefined,
    recent: sourceSelector.recent ? { ...sourceSelector.recent } : undefined,
  }

  return {
    ...DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT,
    ...input,
    selector,
    history: { ...(input.history ?? DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.history) },
    resources: { ...(input.resources ?? DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.resources) },
  }
}

export async function postDesktopV3SyncBootstrap(
  input: Partial<DesktopV3BootstrapInput> = {},
): Promise<SyncSnapshotResponse> {
  const body = buildDesktopV3BootstrapInput(input)
  const requestStartedAt = desktopV3Now()
  const response = await apiFetch('/v3/sync/bootstrap', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
  })
  const headersReceivedAt = desktopV3Now()
  const text = await response.text()
  const bodyReadAt = desktopV3Now()

  if (!response.ok) {
    throw new Error(errorMessageFromResponseText(response.status, text))
  }

  const parseStartedAt = desktopV3Now()
  const parsed = JSON.parse(text) as SyncSnapshotResponse
  const parsedAt = desktopV3Now()
  logDesktopV3BootstrapTiming('network_json', {
    status: response.status,
    header_ms: roundTiming(headersReceivedAt - requestStartedAt),
    body_ms: roundTiming(bodyReadAt - headersReceivedAt),
    fetch_total_ms: roundTiming(bodyReadAt - requestStartedAt),
    json_parse_ms: roundTiming(parsedAt - parseStartedAt),
    total_with_parse_ms: roundTiming(parsedAt - requestStartedAt),
    response_bytes: byteLength(text),
    sessions: Object.keys(parsed.sessions_by_id ?? {}).length,
    session_order: parsed.session_order?.length ?? 0,
    messages: countArrayMapItems(parsed.messages_by_session),
    run_intents: countArrayMapItems(parsed.run_intents_by_session),
    current_run_states: Object.keys(parsed.current_run_state_by_session ?? {}).length,
    active_sessions: parsed.active_session_ids?.length ?? 0,
    include_active: body.include_active,
    recent_limit: body.selector.recent?.limit,
  })
  return parsed
}

export async function postDesktopV3SyncHydrate(
  input: DesktopV3HydrateInput,
  signal?: AbortSignal,
): Promise<SyncSnapshotResponse> {
  const requestStartedAt = desktopV3Now()
  const response = await apiFetch('/v3/sync/hydrate', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
    signal,
  })
  const headersReceivedAt = desktopV3Now()
  const text = await response.text()
  const bodyReadAt = desktopV3Now()

  if (!response.ok) {
    throw new Error(errorMessageFromResponseText(response.status, text))
  }

  const parseStartedAt = desktopV3Now()
  const parsed = JSON.parse(text) as SyncSnapshotResponse
  const parsedAt = desktopV3Now()
  const responseBytes = byteLength(text)
  logDesktopV3BootstrapTiming('hydrate_network_json', {
    status: response.status,
    header_ms: roundTiming(headersReceivedAt - requestStartedAt),
    body_ms: roundTiming(bodyReadAt - headersReceivedAt),
    fetch_total_ms: roundTiming(bodyReadAt - requestStartedAt),
    json_parse_ms: roundTiming(parsedAt - parseStartedAt),
    total_with_parse_ms: roundTiming(parsedAt - requestStartedAt),
    response_bytes: responseBytes,
    response_byte_budget: DESKTOP_SELECTED_HYDRATE_RESPONSE_BYTE_BUDGET,
    response_byte_budget_exceeded: responseBytes > DESKTOP_SELECTED_HYDRATE_RESPONSE_BYTE_BUDGET,
    sessions: Object.keys(parsed.sessions_by_id ?? {}).length,
    session_order: parsed.session_order?.length ?? 0,
    messages: countArrayMapItems(parsed.messages_by_session),
    run_intents: countArrayMapItems(parsed.run_intents_by_session),
    current_run_states: Object.keys(parsed.current_run_state_by_session ?? {}).length,
    requested_sessions: input.session_ids.length,
  })
  return parsed
}

export interface DesktopV3ReconnectInput {
  surface: 'desktop'
  client_id: string
  workset: {
    workset_id: string
    selector: SyncSelector
    history: SyncHistory
    resources: SyncResources
    include_active: boolean
    auto_subscribe_sessions: boolean
  }
}

export async function postDesktopV3Reconnect(
  input: DesktopV3ReconnectInput,
): Promise<SessionsReconnectResponse> {
  return requestJson<SessionsReconnectResponse>('/v3/sessions:reconnect', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export function logDesktopV3BootstrapTiming(stage: string, detail: Record<string, unknown>): void {
  if (typeof console === 'undefined') return
  console.info('[desktop-v3] bootstrap timing', {
    stage,
    ...detail,
  })
}

export function countArrayMapItems(value: Record<string, unknown[] | undefined> | undefined): number {
  if (!value) return 0
  let total = 0
  for (const items of Object.values(value)) {
    if (Array.isArray(items)) total += items.length
  }
  return total
}

function errorMessageFromResponseText(status: number, text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return `Request failed with status ${status}`
  try {
    const payload = JSON.parse(trimmed) as { error?: unknown }
    if (typeof payload.error === 'string' && payload.error.trim() !== '') return payload.error
  } catch {
    // Fall through to raw body.
  }
  return trimmed
}

function desktopV3Now(): number {
  return globalThis.performance?.now?.() ?? Date.now()
}

function roundTiming(value: number): number {
  return Math.round(value * 1000) / 1000
}

function byteLength(value: string): number {
  if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(value).byteLength
  return value.length
}
