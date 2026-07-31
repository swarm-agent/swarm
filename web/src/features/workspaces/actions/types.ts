import { requestJson } from '../../../app/api'

export type WorkspaceActionInputKind = 'text' | 'secret'
export type WorkspaceActionRunStatus = 'running' | 'succeeded' | 'failed' | 'timed_out' | 'cancelled'

export interface WorkspaceActionInput {
  id: string
  label: string
  description: string
  kind: WorkspaceActionInputKind
  required: boolean
  placeholder: string
  defaultValue: string
  arguments: string[]
}

export interface WorkspaceAction {
  id: string
  workspaceId: string
  workspacePath: string
  name: string
  description: string
  icon: string
  entrypoint: string
  arguments: string[]
  inputs: WorkspaceActionInput[]
  sortIndex: number
}

export interface WorkspaceActionRun {
  id: string
  actionId: string
  actionName: string
  status: WorkspaceActionRunStatus
  output: string
  outputTruncated: boolean
  outputBytes: number
  startedAt: number
  completedAt: number
  durationMs: number
  exitCode: number | null
  terminationSignal: string
  error: string
}

interface WorkspaceActionInputWire {
  id: string
  label: string
  description?: string
  kind?: string
  required?: boolean
  placeholder?: string
  default?: string
  arguments?: string[]
}

interface WorkspaceActionWire {
  id: string
  workspace_id: string
  workspace_path: string
  name: string
  description?: string
  icon?: string
  entrypoint: string
  arguments?: string[]
  inputs?: WorkspaceActionInputWire[]
  sort_index?: number
}

interface WorkspaceActionRunWire {
  id: string
  action_id: string
  action_name: string
  status: string
  output?: string
  output_truncated?: boolean
  output_bytes?: number
  started_at?: number
  completed_at?: number
  duration_ms?: number
  exit_code?: number
  termination_signal?: string
  error?: string
}

interface WorkspaceActionsResponseWire {
  actions?: WorkspaceActionWire[]
}

interface WorkspaceActionRunResponseWire {
  run?: WorkspaceActionRunWire
}

function mapWorkspaceActionInput(input: WorkspaceActionInputWire): WorkspaceActionInput {
  return {
    id: input.id.trim(),
    label: input.label.trim(),
    description: input.description?.trim() ?? '',
    kind: input.kind?.trim().toLowerCase() === 'secret' ? 'secret' : 'text',
    required: Boolean(input.required),
    placeholder: input.placeholder?.trim() ?? '',
    defaultValue: input.default ?? '',
    arguments: Array.isArray(input.arguments) ? input.arguments : [],
  }
}

function mapWorkspaceAction(action: WorkspaceActionWire): WorkspaceAction {
  return {
    id: action.id.trim(),
    workspaceId: action.workspace_id.trim(),
    workspacePath: action.workspace_path.trim(),
    name: action.name.trim(),
    description: action.description?.trim() ?? '',
    icon: action.icon?.trim() ?? '',
    entrypoint: action.entrypoint.trim(),
    arguments: Array.isArray(action.arguments) ? action.arguments : [],
    inputs: Array.isArray(action.inputs) ? action.inputs.map(mapWorkspaceActionInput) : [],
    sortIndex: typeof action.sort_index === 'number' ? action.sort_index : 0,
  }
}

function mapWorkspaceActionRun(run: WorkspaceActionRunWire): WorkspaceActionRun {
  const status = run.status.trim().toLowerCase()
  const normalizedStatus: WorkspaceActionRunStatus = status === 'succeeded' || status === 'failed' || status === 'timed_out' || status === 'cancelled'
    ? status
    : 'running'
  return {
    id: run.id.trim(),
    actionId: run.action_id.trim(),
    actionName: run.action_name.trim(),
    status: normalizedStatus,
    output: run.output ?? '',
    outputTruncated: Boolean(run.output_truncated),
    outputBytes: typeof run.output_bytes === 'number' ? run.output_bytes : 0,
    startedAt: typeof run.started_at === 'number' ? run.started_at : 0,
    completedAt: typeof run.completed_at === 'number' ? run.completed_at : 0,
    durationMs: typeof run.duration_ms === 'number' ? run.duration_ms : 0,
    exitCode: typeof run.exit_code === 'number' ? run.exit_code : null,
    terminationSignal: run.termination_signal?.trim() ?? '',
    error: run.error?.trim() ?? '',
  }
}

export async function fetchWorkspaceActions(workspacePath: string, signal?: AbortSignal): Promise<WorkspaceAction[]> {
  const search = new URLSearchParams({ workspace_path: workspacePath })
  const response = await requestJson<WorkspaceActionsResponseWire>(`/v1/workspace/actions?${search.toString()}`, { signal })
  return Array.isArray(response.actions) ? response.actions.map(mapWorkspaceAction) : []
}

export async function deleteWorkspaceAction(workspacePath: string, actionId: string): Promise<void> {
  await requestJson('/v1/workspace/actions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'delete', workspace_path: workspacePath, id: actionId }),
  })
}

export async function startWorkspaceAction(workspacePath: string, actionId: string, inputs: Record<string, string>): Promise<WorkspaceActionRun> {
  const response = await requestJson<WorkspaceActionRunResponseWire>('/v1/workspace/actions/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, action_id: actionId, inputs }),
  })
  if (!response.run) throw new Error('Action launch returned no run')
  return mapWorkspaceActionRun(response.run)
}

export async function fetchWorkspaceActionRun(workspacePath: string, runId: string, signal?: AbortSignal): Promise<WorkspaceActionRun> {
  const search = new URLSearchParams({ workspace_path: workspacePath, run_id: runId })
  const response = await requestJson<WorkspaceActionRunResponseWire>(`/v1/workspace/actions/runs?${search.toString()}`, { signal })
  if (!response.run) throw new Error('Action status returned no run')
  return mapWorkspaceActionRun(response.run)
}

export async function cancelWorkspaceActionRun(workspacePath: string, runId: string): Promise<WorkspaceActionRun> {
  const response = await requestJson<WorkspaceActionRunResponseWire>('/v1/workspace/actions/runs/cancel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, run_id: runId }),
  })
  if (!response.run) throw new Error('Action cancellation returned no run')
  return mapWorkspaceActionRun(response.run)
}
