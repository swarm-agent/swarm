import { requestJson } from '../../../../app/api'

export interface SwarmTarget {
  swarm_id: string
  name: string
  role: string
  relationship: string
  kind: 'self' | 'local' | 'remote'
  deployment_id?: string
  attach_status?: string
  online: boolean
  selectable: boolean
  current: boolean
  backend_url?: string
  desktop_url?: string
  last_error?: string
}

export interface SwarmTargetsResponse {
  ok: boolean
  targets: SwarmTarget[]
}

export interface SwarmCurrentTargetResponse {
  ok: boolean
  target?: SwarmTarget | null
}

export async function fetchSwarmTargets(): Promise<SwarmTargetsResponse> {
  return requestJson<SwarmTargetsResponse>('/v1/swarm/targets', {
    cache: 'no-store',
  })
}

export async function fetchCurrentSwarmTarget(): Promise<SwarmCurrentTargetResponse> {
  return requestJson<SwarmCurrentTargetResponse>('/v1/swarm/target/current', {
    cache: 'no-store',
  })
}

export async function selectSwarmTarget(swarmID: string): Promise<SwarmCurrentTargetResponse> {
  return requestJson<SwarmCurrentTargetResponse>('/v1/swarm/target/select', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ swarm_id: swarmID }),
  })
}
