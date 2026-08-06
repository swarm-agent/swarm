import { listWorkspaces } from '../queries/list-workspaces'
import type { WorkspaceDefinitionFields, WorkspaceEntry, WorkspaceResolution } from '../types/workspace'

export const WORKSPACE_DEFINITION_POLL_INTERVAL_MS = 2_000

export function workspaceDefinitionFailureMessage(workspace: Partial<WorkspaceDefinitionFields>): string {
  const error = workspace.definitionError || 'Router could not generate a workspace definition after three attempts.'
  const suggestion = workspace.definitionSuggestion || 'Change the Router model in Settings, then add this workspace again.'
  return `${error} ${suggestion}`.trim()
}

function matchesResolution(workspace: WorkspaceEntry, resolution: WorkspaceResolution): boolean {
  const resolvedPath = resolution.resolvedPath.trim()
  const workspaceID = resolution.workspaceId?.trim() ?? ''
  const bindingID = resolution.localWorkspaceBindingId?.trim() ?? ''
  if (workspaceID && workspace.workspaceId === workspaceID) {
    return true
  }
  if (bindingID && workspace.localWorkspaceBindingId === bindingID) {
    return true
  }
  return workspace.path === resolvedPath
}

export interface WaitForWorkspaceDefinitionOptions {
  load?: () => Promise<WorkspaceEntry[]>
  delay?: (milliseconds: number) => Promise<void>
  intervalMs?: number
}

export async function waitForWorkspaceDefinition(
  resolution: WorkspaceResolution,
  options: WaitForWorkspaceDefinitionOptions = {},
): Promise<WorkspaceEntry | WorkspaceResolution> {
  if (resolution.definitionStatus === 'completed') {
    return resolution
  }
  if (resolution.definitionStatus === 'failed') {
    throw new Error(workspaceDefinitionFailureMessage(resolution))
  }

  const load = options.load ?? (() => listWorkspaces())
  const intervalMs = options.intervalMs ?? WORKSPACE_DEFINITION_POLL_INTERVAL_MS
  const delay = options.delay ?? ((milliseconds: number) => new Promise<void>((resolve) => {
    const setTimeoutFn = typeof window !== 'undefined' ? window.setTimeout.bind(window) : setTimeout
    setTimeoutFn(resolve, milliseconds)
  }))

  while (true) {
    await delay(intervalMs)
    const workspaces = await load()
    const workspace = workspaces.find((entry) => matchesResolution(entry, resolution))
    if (!workspace) {
      continue
    }
    if (workspace.definitionStatus === 'completed') {
      return workspace
    }
    if (workspace.definitionStatus === 'failed') {
      throw new Error(workspaceDefinitionFailureMessage(workspace))
    }
  }
}
