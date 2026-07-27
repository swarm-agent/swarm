import { requestJson } from '../../../../app/api'

export interface SwarmTarget {
  swarm_id: string
  name: string
  role: string
  relationship: string
  kind: string
  online: boolean
  selectable: boolean
  current: boolean
}

export interface SwarmTargetsResponse {
  ok: boolean
  targets: SwarmTarget[]
}

export async function fetchSwarmTargets(): Promise<SwarmTargetsResponse> {
  return requestJson<SwarmTargetsResponse>('/v1/swarm/targets', {
    cache: 'no-store',
  })
}
