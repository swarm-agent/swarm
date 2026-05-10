import { requestJson } from '../../../../app/api'

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
  target?: {
    swarm_id?: string
    name?: string
    online?: boolean
  }
  destination_root?: string
  workspaces?: ManagedWorkspacePlanWire[]
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
  target?: {
    swarm_id?: string
    name?: string
    online?: boolean
  }
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

function mapTarget(target: ManagedWorkspacePreflightWire['target'] | ManagedWorkspaceReplicateWire['target']) {
  return {
    swarmId: String(target?.swarm_id ?? '').trim(),
    name: String(target?.name ?? '').trim(),
    online: Boolean(target?.online),
  }
}

function selectionWire(selection: ManagedWorkspaceSelectionInput) {
  return {
    source_workspace_path: selection.sourceWorkspacePath,
    destination_path: selection.destinationPath?.trim() || undefined,
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
  confirmedPlans: ManagedWorkspaceConfirmedPlanInput[]
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
      confirmed_plans: input.confirmedPlans.map((plan) => ({
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
