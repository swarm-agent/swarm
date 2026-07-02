import { requestJson } from '../../../app/api'
import type {
  DesktopOnboardingStatusWire,
  SaveDesktopOnboardingInput,
} from './types'

export interface RemoteSwarmPendingPairing {
  request_id: string
  status: string
  manager_swarm_id?: string
  manager_name?: string
  manager_endpoint?: string
  managed_swarm_id?: string
  managed_name?: string
  managed_fingerprint?: string
  managed_endpoint?: string
  ceremony_code?: string
  transport_mode?: string
  created_at?: number
}

export interface RemoteSwarmPairingApprovalResult {
  ok?: boolean
  status: string
  request_id: string
  routing?: {
    managed_swarm_id?: string
    managed_name?: string
    backend_url?: string
    transport_mode?: string
    container_scope?: string
  }
}

export function buildDesktopOnboardingPayload(input: SaveDesktopOnboardingInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (Object.prototype.hasOwnProperty.call(input, 'username')) {
    payload.username = input.username
  }
  if (Object.prototype.hasOwnProperty.call(input, 'swarmName')) {
    payload.swarm_name = input.swarmName
  }
  if (Object.prototype.hasOwnProperty.call(input, 'desktopOnboardingComplete')) {
    payload.desktop_onboarding_complete = input.desktopOnboardingComplete
  }
  if (Object.prototype.hasOwnProperty.call(input, 'child')) {
    payload.child = input.child
  }
  if (Object.prototype.hasOwnProperty.call(input, 'mode')) {
    payload.mode = input.mode
  }
  if (Object.prototype.hasOwnProperty.call(input, 'port')) {
    payload.port = input.port
  }
  if (Object.prototype.hasOwnProperty.call(input, 'advertiseHost')) {
    payload.advertise_host = input.advertiseHost
  }
  if (Object.prototype.hasOwnProperty.call(input, 'advertisePort')) {
    payload.advertise_port = input.advertisePort
  }
  if (Object.prototype.hasOwnProperty.call(input, 'tailscaleURL')) {
    payload.tailscale_url = input.tailscaleURL
  }
  if (Object.prototype.hasOwnProperty.call(input, 'localTransportPort')) {
    payload.local_transport_port = input.localTransportPort
  }
  if (Object.prototype.hasOwnProperty.call(input, 'peerTransportPort')) {
    payload.peer_transport_port = input.peerTransportPort
  }
  return payload
}

export async function patchDesktopOnboarding(input: SaveDesktopOnboardingInput): Promise<DesktopOnboardingStatusWire> {
  return requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(buildDesktopOnboardingPayload(input)),
  })
}

export async function upgradeAccountToTeam(teamName: string): Promise<void> {
  await requestJson('/v1/account/team/upgrade', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ team_name: teamName }),
  })
}

export async function approveRemoteSwarmPairing(input: {
  requestID: string
  approve: boolean
  confirmed?: boolean
  ceremonyCode?: string
  reason?: string
}): Promise<RemoteSwarmPairingApprovalResult> {
  const response = await requestJson<RemoteSwarmPairingApprovalResult>('/v1/swarm/remote-pairing/approve', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      request_id: input.requestID,
      approve: input.approve,
      confirmed: input.confirmed,
      ceremony_code: input.ceremonyCode,
      reason: input.reason,
    }),
  })
  return response
}
