import { requestJson } from '../../../../app/api'
import type { WorkspaceDefinitionStatus } from '../types/workspace'

interface WorkspaceDefinitionRefreshFailure {
  workspace_id?: string
  workspace_path: string
  error: string
}

interface WorkspaceDefinitionRefreshWorkspaceWire {
  path: string
  definition_status?: string
  definition?: string
  definition_error?: string
  definition_model_suggestion?: string
  definition_attempt_count?: number
  definition_generation?: number
  definition_updated_at?: number
}

interface WorkspaceDefinitionRefreshResponse {
  ok: boolean
  workspace_count: number
  launched_count: number
  failed_count: number
  workspaces: WorkspaceDefinitionRefreshWorkspaceWire[]
  failures: WorkspaceDefinitionRefreshFailure[]
}

export interface WorkspaceDefinitionRefreshWorkspace {
  path: string
  definitionStatus: WorkspaceDefinitionStatus
  definition: string
  definitionError: string
  definitionSuggestion: string
  definitionAttempts: number
  definitionGeneration: number
  definitionUpdatedAt: number
}

export interface WorkspaceDefinitionRefreshResult {
  workspaceCount: number
  launchedCount: number
  failedCount: number
  workspaces: WorkspaceDefinitionRefreshWorkspace[]
  failures: WorkspaceDefinitionRefreshFailure[]
}

export async function refreshWorkspaceDefinitions(): Promise<WorkspaceDefinitionRefreshResult> {
  const response = await requestJson<WorkspaceDefinitionRefreshResponse>('/v1/workspace/definitions/refresh', {
    method: 'POST',
  })
  return {
    workspaceCount: response.workspace_count,
    launchedCount: response.launched_count,
    failedCount: response.failed_count,
    workspaces: response.workspaces.map((workspace) => ({
      path: workspace.path,
      definitionStatus: workspace.definition_status === 'pending' || workspace.definition_status === 'completed' || workspace.definition_status === 'failed'
        ? workspace.definition_status
        : '',
      definition: workspace.definition?.trim() ?? '',
      definitionError: workspace.definition_error?.trim() ?? '',
      definitionSuggestion: workspace.definition_model_suggestion?.trim() ?? '',
      definitionAttempts: workspace.definition_attempt_count ?? 0,
      definitionGeneration: workspace.definition_generation ?? 0,
      definitionUpdatedAt: workspace.definition_updated_at ?? 0,
    })),
    failures: response.failures,
  }
}
