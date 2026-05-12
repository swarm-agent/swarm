import { requestJson } from '../../../../app/api'
import type { WorkspaceReplicationLink, WorkspaceReplicationLinkWire } from '../types/workspace'
import { mapWorkspaceReplicationLink } from '../types/workspace'

interface ManagedLinkTargetWire {
  swarm_id?: string
  name?: string
  online?: boolean
}

interface ManagedLinkResponseWire {
  ok?: boolean
  target?: ManagedLinkTargetWire
  workspace_path?: string
  destination_path?: string
  exists?: boolean
  created?: boolean
  registered?: boolean
  link?: WorkspaceReplicationLinkWire
}

export interface ManagedLinkResponse {
  ok: boolean
  target: {
    swarmId: string
    name: string
    online: boolean
  }
  workspacePath: string
  destinationPath: string
  exists: boolean
  created: boolean
  registered: boolean
  link: WorkspaceReplicationLink | null
}

function mapManagedLinkResponse(payload: ManagedLinkResponseWire): ManagedLinkResponse {
  return {
    ok: Boolean(payload.ok),
    target: {
      swarmId: String(payload.target?.swarm_id ?? '').trim(),
      name: String(payload.target?.name ?? '').trim(),
      online: Boolean(payload.target?.online),
    },
    workspacePath: String(payload.workspace_path ?? '').trim(),
    destinationPath: String(payload.destination_path ?? '').trim(),
    exists: Boolean(payload.exists),
    created: Boolean(payload.created),
    registered: Boolean(payload.registered),
    link: payload.link ? mapWorkspaceReplicationLink(payload.link) : null,
  }
}

export async function upsertWorkspaceManagedLink(input: {
  workspacePath: string
  targetSwarmID: string
  destinationRoot?: string
  destinationPath?: string
  workspaceName?: string
  provision?: boolean
}): Promise<ManagedLinkResponse> {
  const payload = await requestJson<ManagedLinkResponseWire>('/v1/workspace/managed-links/upsert', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: input.workspacePath,
      target_swarm_id: input.targetSwarmID,
      destination_root: input.destinationRoot?.trim() || undefined,
      destination_path: input.destinationPath?.trim() || undefined,
      workspace_name: input.workspaceName?.trim() || undefined,
      provision: input.provision ?? true,
    }),
  })
  return mapManagedLinkResponse(payload)
}

export async function removeWorkspaceManagedLink(input: {
  workspacePath: string
  linkID: string
}): Promise<ManagedLinkResponse> {
  const payload = await requestJson<ManagedLinkResponseWire>('/v1/workspace/managed-links/remove', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: input.workspacePath,
      link_id: input.linkID,
    }),
  })
  return mapManagedLinkResponse(payload)
}
