import { requestJson } from '../../../app/api'

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
    mode: 'tail',
    max_messages_per_session: DESKTOP_STARTUP_MESSAGE_LIMIT,
    manifest_policy: 'manifest',
  },
  resources: {
    messages: true,
    events: false,
    run_intents: true,
    active_plan: true,
    plan_revisions: false,
  },
  include_active: true,
}

export const DESKTOP_V3_INITIAL_HYDRATE_DEFAULT_RESOURCES: SyncResources = {
  messages: true,
  events: false,
  run_intents: true,
  active_plan: true,
  plan_revisions: false,
}

export function buildDesktopV3InitialHydrateInput(sessionIds: string[]): DesktopV3HydrateInput {
  return {
    surface: 'desktop',
    session_ids: sessionIds,
    history: {
      mode: 'tail',
      max_messages_per_session: DESKTOP_STARTUP_MESSAGE_LIMIT,
      manifest_policy: 'manifest',
    },
    resources: DESKTOP_V3_INITIAL_HYDRATE_DEFAULT_RESOURCES,
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

  return requestJson<SyncSnapshotResponse>('/v3/sync/bootstrap', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
  })
}

export async function postDesktopV3SyncHydrate(
  input: DesktopV3HydrateInput,
): Promise<SyncSnapshotResponse> {
  return requestJson<SyncSnapshotResponse>('/v3/sync/hydrate', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
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
