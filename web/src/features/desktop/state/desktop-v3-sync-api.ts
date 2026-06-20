import { requestJson } from '../../../app/api'

import type { SessionsReconnectResponse, SyncHistory, SyncResources, SyncSelector, SyncSnapshotResponse } from './desktop-v3-cache-types'

export interface DesktopV3BootstrapInput {
  surface: 'desktop' | string
  selector: SyncSelector
  history: SyncHistory
  resources: SyncResources
  include_active: boolean
}

export interface DesktopV3HydrateInput {
  surface: 'desktop' | string
  session_ids: string[]
  history: SyncHistory
  resources: SyncResources
  include_active: boolean
}

export const DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT: DesktopV3BootstrapInput = {
  surface: 'desktop',
  selector: {
    kind: 'recent',
    global: true,
    recent: {
      limit: 50,
    },
  },
  history: {
    mode: 'none',
  },
  resources: {
    messages: false,
    events: false,
    run_intents: true,
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
      max_messages_per_session: 200,
    },
    resources: DESKTOP_V3_INITIAL_HYDRATE_DEFAULT_RESOURCES,
    include_active: true,
  }
}

export async function postDesktopV3SyncBootstrap(
  input: Partial<DesktopV3BootstrapInput> = {},
): Promise<SyncSnapshotResponse> {
  const body: DesktopV3BootstrapInput = {
    ...DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT,
    ...input,
    selector: input.selector ?? DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.selector,
    history: input.history ?? DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.history,
    resources: input.resources ?? DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT.resources,
  }

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
