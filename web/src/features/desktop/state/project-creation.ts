export interface ProjectContextGeneration {
  status: 'pending' | 'running' | 'failed' | 'ready'
  attempt: number
  lease_until?: number
  error?: string
  router_alert?: string
}

export interface CreationWorkspace {
  workspace_id: string
  path: string
  label: string
  role: 'auxiliary'
}

export interface CreationProject {
  id: string
  name: string
  description?: string
  workspaces?: CreationWorkspace[]
  project_context?: string
  context_generation?: ProjectContextGeneration
}

// Missing generation state is a pre-lifecycle project, not a pending new project.
export function projectContextPending(project: { contextGeneration?: ProjectContextGeneration } | undefined): boolean {
  return !!project?.contextGeneration && project.contextGeneration.status !== 'ready'
}

export interface ProjectCreationDraft {
  client_request_id: string
  name: string
  description: string
  workspaces: CreationWorkspace[]
}

export interface ProjectCreationState {
  draft?: ProjectCreationDraft
  project?: CreationProject
  busy: boolean
  error: string
  sessionId?: string
}

export function creationProjectSummary(project: CreationProject): import('../orchestrate/orchestrate-types').ProjectSummary {
  return {
    id: project.id, name: project.name, slug: project.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
    description: project.description || '', repoPath: project.workspaces?.[0]?.path || '', branch: '', gitStatus: 'clean',
    workspaces: project.workspaces, linkedWorkspaces: project.workspaces?.map(w => w.path) || [],
    activeWorkersCount: 0, pendingDeliverablesCount: 0, runningTasksCount: 0,
    projectContext: project.project_context, contextGeneration: project.context_generation,
  }
}
