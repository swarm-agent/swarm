import { requestJson } from '../../../../app/api'

export interface SwarmTarget {
  swarm_id: string
  name: string
  role: string
  relationship: string
  kind: 'self' | 'local' | 'remote' | 'host' | 'mirrored'
  deployment_id?: string
  attach_status?: string
  host_swarm_id?: string
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

export async function fetchSwarmTargets(): Promise<SwarmTargetsResponse> {
  return requestJson<SwarmTargetsResponse>('/v1/swarm/targets', {
    cache: 'no-store',
  })
}
