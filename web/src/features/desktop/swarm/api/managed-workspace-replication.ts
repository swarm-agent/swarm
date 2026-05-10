import { requestJson } from '../../../../app/api'
import {
  mapWorkspaceDiscoverEntry,
  mapWorkspaceEntry,
  type WorkspaceDiscoverEntry,
  type WorkspaceDiscoverEntryWire,
  type WorkspaceEntry,
  type WorkspaceEntryWire,
} from '../../../workspaces/launcher/types/workspace'

export type ManagedWorkspaceAction = 'import_bundle' | 'link_existing' | 'conflict'

export interface ManagedWorkspaceSelectionInput {
  sourceWorkspacePath: string
  destinationPath?: string
}

export interface ManagedWorkspacePlan {
  planId: string
  sourceWorkspacePath: string
  sourceWorkspaceName: string
  destinationRoot: string
  destinationPath: string
  action: ManagedWorkspaceAction | string
  gitWorkspace: boolean
  ok: boolean
  error: string
}

export interface ManagedWorkspacePreflightResponse {
  ok: boolean
  ready: boolean
  target: {
    swarmId: string
    name: string
    online: boolean
  }
  destinationRoot: string
  workspaces: ManagedWorkspacePlan[]
}

export interface ManagedWorkspaceActiveCWD {
  path: string
  workspacePath: string
  workspaceName: string
  sessionID: string
  sessionTitle: string
  active: boolean
  updatedAt: number
}

export interface ManagedWorkspaceInventoryResponse {
  ok: boolean
  target: {
    swarmId: string
    name: string
    online: boolean
  }
  managedHome: string
  savedWorkspaces: WorkspaceEntry[]
  discoveredDirectories: WorkspaceDiscoverEntry[]
  activeCWDs: ManagedWorkspaceActiveCWD[]
}

export interface ManagedWorkspaceConfirmedPlanInput {
  sourceWorkspacePath: string
  destinationPath: string
  action: string
  planId?: string
}

export interface ManagedWorkspaceResult {
  sourceWorkspacePath: string
  sourceWorkspaceName: string
  managedHostName: string
  destinationPath: string
  action: string
}

export interface ManagedWorkspaceReplicateResponse {
  ok: boolean
  target: {
    swarmId: string
    name: string
    online: boolean
  }
  workspaces: ManagedWorkspaceResult[]
}

interface ManagedWorkspacePlanWire {
  plan_id?: string
  source_workspace_path?: string
  source_workspace_name?: string
  destination_root?: string
  destination_path?: string
  action?: string
  git_workspace?: boolean
  ok?: boolean
  error?: string
}

interface ManagedWorkspacePreflightWire {
  ok?: boolean
  ready?: boolean
  target?: ManagedWorkspaceTargetWire
  destination_root?: string
  workspaces?: ManagedWorkspacePlanWire[]
}

interface ManagedWorkspaceTargetWire {
  swarm_id?: string
  name?: string
  online?: boolean
}

interface ManagedWorkspaceActiveCWDWire {
  path?: string
  workspace_path?: string
  workspace_name?: string
  session_id?: string
  session_title?: string
  active?: boolean
  updated_at?: number
}

interface ManagedWorkspaceInventoryWire {
  ok?: boolean
  target?: ManagedWorkspaceTargetWire
  managed_home?: string
  saved_workspaces?: WorkspaceEntryWire[]
  discovered_directories?: WorkspaceDiscoverEntryWire[]
  active_cwds?: ManagedWorkspaceActiveCWDWire[]
}

interface ManagedWorkspaceResultWire {
  source_workspace_path?: string
  source_workspace_name?: string
  managed_host_name?: string
  destination_path?: string
  action?: string
}

interface ManagedWorkspaceReplicateWire {
  ok?: boolean
  target?: ManagedWorkspaceTargetWire
  workspaces?: ManagedWorkspaceResultWire[]
}

function mapPlan(plan: ManagedWorkspacePlanWire): ManagedWorkspacePlan {
  return {
    planId: String(plan.plan_id ?? '').trim(),
    sourceWorkspacePath: String(plan.source_workspace_path ?? '').trim(),
    sourceWorkspaceName: String(plan.source_workspace_name ?? '').trim(),
    destinationRoot: String(plan.destination_root ?? '').trim(),
    destinationPath: String(plan.destination_path ?? '').trim(),
    action: String(plan.action ?? '').trim(),
    gitWorkspace: Boolean(plan.git_workspace),
    ok: Boolean(plan.ok),
    error: String(plan.error ?? '').trim(),
  }
}

function mapTarget(target: ManagedWorkspaceTargetWire | undefined) {
  return {
    swarmId: String(target?.swarm_id ?? '').trim(),
    name: String(target?.name ?? '').trim(),
    online: Boolean(target?.online),
  }
}

function mapActiveCWD(entry: ManagedWorkspaceActiveCWDWire): ManagedWorkspaceActiveCWD {
  return {
    path: String(entry.path ?? '').trim(),
    workspacePath: String(entry.workspace_path ?? '').trim(),
    workspaceName: String(entry.workspace_name ?? '').trim(),
    sessionID: String(entry.session_id ?? '').trim(),
    sessionTitle: String(entry.session_title ?? '').trim(),
    active: Boolean(entry.active),
    updatedAt: typeof entry.updated_at === 'number' ? entry.updated_at : 0,
  }
}

function selectionWire(selection: ManagedWorkspaceSelectionInput) {
  return {
    source_workspace_path: selection.sourceWorkspacePath,
    destination_path: selection.destinationPath?.trim() || undefined,
  }
}

export async function fetchManagedWorkspaceInventory(input: {
  targetSwarmID: string
  limit?: number
}): Promise<ManagedWorkspaceInventoryResponse> {
  const params = new URLSearchParams()
  params.set('target_swarm_id', input.targetSwarmID)
  if (typeof input.limit === 'number' && input.limit > 0) {
    params.set('limit', String(input.limit))
  }
  const payload = await requestJson<ManagedWorkspaceInventoryWire>(`/v1/swarm/managed-workspaces/inventory?${params.toString()}`, {
    method: 'GET',
    headers: {
      Accept: 'application/json',
    },
  })
  return {
    ok: Boolean(payload.ok),
    target: mapTarget(payload.target),
    managedHome: String(payload.managed_home ?? '').trim(),
    savedWorkspaces: Array.isArray(payload.saved_workspaces) ? payload.saved_workspaces.map(mapWorkspaceEntry) : [],
    discoveredDirectories: Array.isArray(payload.discovered_directories) ? payload.discovered_directories.map(mapWorkspaceDiscoverEntry) : [],
    activeCWDs: Array.isArray(payload.active_cwds) ? payload.active_cwds.map(mapActiveCWD) : [],
  }
}

export async function preflightManagedWorkspaces(input: {
  targetSwarmID: string
  destinationRoot: string
  workspaces: ManagedWorkspaceSelectionInput[]
}): Promise<ManagedWorkspacePreflightResponse> {
  const payload = await requestJson<ManagedWorkspacePreflightWire>('/v1/swarm/managed-workspaces/preflight', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      target_swarm_id: input.targetSwarmID,
      destination_root: input.destinationRoot,
      workspaces: input.workspaces.map(selectionWire),
    }),
  })
  return {
    ok: Boolean(payload.ok),
    ready: Boolean(payload.ready),
    target: mapTarget(payload.target),
    destinationRoot: String(payload.destination_root ?? '').trim(),
    workspaces: Array.isArray(payload.workspaces) ? payload.workspaces.map(mapPlan) : [],
  }
}

export async function replicateManagedWorkspaces(input: {
  targetSwarmID: string
  destinationRoot: string
  workspaces: ManagedWorkspaceSelectionInput[]
  confirmedPlans?: ManagedWorkspaceConfirmedPlanInput[]
}): Promise<ManagedWorkspaceReplicateResponse> {
  const payload = await requestJson<ManagedWorkspaceReplicateWire>('/v1/swarm/managed-workspaces/replicate', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      target_swarm_id: input.targetSwarmID,
      destination_root: input.destinationRoot,
      workspaces: input.workspaces.map(selectionWire),
      confirmed_plans: input.confirmedPlans?.map((plan) => ({
        source_workspace_path: plan.sourceWorkspacePath,
        destination_path: plan.destinationPath,
        action: plan.action,
        plan_id: plan.planId || undefined,
      })),
    }),
  })
  return {
    ok: Boolean(payload.ok),
    target: mapTarget(payload.target),
    workspaces: Array.isArray(payload.workspaces) ? payload.workspaces.map((workspace) => ({
      sourceWorkspacePath: String(workspace.source_workspace_path ?? '').trim(),
      sourceWorkspaceName: String(workspace.source_workspace_name ?? '').trim(),
      managedHostName: String(workspace.managed_host_name ?? '').trim(),
      destinationPath: String(workspace.destination_path ?? '').trim(),
      action: String(workspace.action ?? '').trim(),
    })) : [],
  }
}
