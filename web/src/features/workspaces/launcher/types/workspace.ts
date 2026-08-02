import type { WorkspaceTodoSummary } from '../../todos/types'
import type { WorkspaceOverviewTopologyRoute } from './workspace-overview'

export type WorkspaceDefinitionStatus = '' | 'pending' | 'completed' | 'failed'

export interface WorkspaceDefinitionFields {
  definitionStatus: WorkspaceDefinitionStatus
  definition: string
  definitionError: string
  definitionSuggestion: string
  definitionAttempts: number
  definitionGeneration: number
  definitionUpdatedAt: number
}

type OptionalWorkspaceDefinitionFields = Partial<WorkspaceDefinitionFields>

export interface WorkspaceEntry extends OptionalWorkspaceDefinitionFields {
  path: string
  workspaceId?: string
  workspaceGeneration?: number
  state?: string
  localWorkspaceBindingId: string
  workspaceName: string
  themeId: string
  directories: string[]
  isGitRepo: boolean
  sortIndex: number
  addedAt: number
  updatedAt: number
  lastSelectedAt: number
  active: boolean
  worktreeEnabled: boolean
  gitBranch?: string
  gitHasGit?: boolean
  gitClean?: boolean
  gitDirtyCount?: number
  gitStagedCount?: number
  gitModifiedCount?: number
  gitUntrackedCount?: number
  gitConflictCount?: number
  gitAheadCount?: number
  gitBehindCount?: number
  gitCommittedFileCount?: number
  gitCommittedAdditions?: number
  gitCommittedDeletions?: number
  todoSummary?: WorkspaceTodoSummary
  topologyRoutes: WorkspaceOverviewTopologyRoute[]
}

export interface WorkspaceResolution extends OptionalWorkspaceDefinitionFields {
  requestedPath: string
  resolvedPath: string
  workspaceId?: string
  workspaceGeneration?: number
  state?: string
  localWorkspaceBindingId: string
  workspaceName: string
  themeId: string
}

export interface WorkspaceDiscoverEntry {
  path: string
  name: string
  isGitRepo: boolean
  hasClaude: boolean
  hasSwarm: boolean
  lastModified: number
}

export interface WorkspaceBrowseEntry {
  path: string
  name: string
  isDirectory: boolean
  isGitRepo: boolean
  hasClaude: boolean
  hasSwarm: boolean
}

export interface WorkspaceBrowseResult {
  requestedPath: string
  resolvedPath: string
  parentPath: string | null
  homePath: string
  rootPath: string
  entries: WorkspaceBrowseEntry[]
}

export interface ListWorkspacesResponse {
  ok: boolean
  workspaces: WorkspaceEntry[]
}

export interface DiscoverWorkspacesResponse {
  ok: boolean
  directories: WorkspaceDiscoverEntry[]
}

export interface WorkspaceBrowseResponse {
  ok: boolean
  browser: WorkspaceBrowseResultWire
}

export interface WorkspaceDefinitionWire {
  definition_status?: string
  definition?: string
  definition_error?: string
  definition_model_suggestion?: string
  definition_attempt_count?: number
  definition_generation?: number
  definition_updated_at?: number
}

export interface WorkspaceEntryWire extends WorkspaceDefinitionWire {
  path: string
  workspace_id?: string
  workspace_generation?: number
  state?: string
  local_workspace_binding_id?: string
  workspace_name: string
  theme_id?: string
  directories: string[]
  is_git_repo: boolean
  sort_index: number
  added_at: number
  updated_at: number
  last_selected_at: number
  active: boolean
  worktree_enabled: boolean
  git_branch?: string
  git_has_git?: boolean
  git_clean?: boolean
  git_dirty_count?: number
  git_staged_count?: number
  git_modified_count?: number
  git_untracked_count?: number
  git_conflict_count?: number
  git_ahead_count?: number
  git_behind_count?: number
  git_committed_file_count?: number
  git_committed_additions?: number
  git_committed_deletions?: number
}

export interface WorkspaceResolutionWire extends WorkspaceDefinitionWire {
  requested_path: string
  resolved_path: string
  workspace_id?: string
  workspace_generation?: number
  state?: string
  local_workspace_binding_id?: string
  workspace_name: string
  theme_id?: string
}

export interface WorkspaceDiscoverEntryWire {
  path: string
  name: string
  is_git_repo: boolean
  has_claude: boolean
  has_swarm: boolean
  last_modified: number
}

export interface WorkspaceBrowseEntryWire {
  path: string
  name: string
  is_directory: boolean
  is_git_repo: boolean
  has_claude: boolean
  has_swarm: boolean
}

export interface WorkspaceBrowseResultWire {
  requested_path: string
  resolved_path: string
  parent_path?: string
  home_path: string
  root_path: string
  entries: WorkspaceBrowseEntryWire[]
}

function mapWorkspaceDefinition(entry: WorkspaceDefinitionWire): WorkspaceDefinitionFields {
  const rawStatus = String(entry.definition_status ?? '').trim().toLowerCase()
  const definitionStatus: WorkspaceDefinitionStatus = rawStatus === 'pending' || rawStatus === 'completed' || rawStatus === 'failed'
    ? rawStatus
    : ''
  return {
    definitionStatus,
    definition: String(entry.definition ?? '').trim(),
    definitionError: String(entry.definition_error ?? '').trim(),
    definitionSuggestion: String(entry.definition_model_suggestion ?? '').trim(),
    definitionAttempts: typeof entry.definition_attempt_count === 'number' ? entry.definition_attempt_count : 0,
    definitionGeneration: typeof entry.definition_generation === 'number' ? entry.definition_generation : 0,
    definitionUpdatedAt: typeof entry.definition_updated_at === 'number' ? entry.definition_updated_at : 0,
  }
}

export function mapWorkspaceEntry(entry: WorkspaceEntryWire): WorkspaceEntry {
  return {
    ...mapWorkspaceDefinition(entry),
    path: entry.path,
    workspaceId: String(entry.workspace_id ?? '').trim(),
    workspaceGeneration: typeof entry.workspace_generation === 'number' ? entry.workspace_generation : 0,
    state: String(entry.state ?? '').trim(),
    localWorkspaceBindingId: String(entry.local_workspace_binding_id ?? '').trim(),
    workspaceName: entry.workspace_name,
    themeId: entry.theme_id ?? '',
    directories: entry.directories,
    isGitRepo: Boolean(entry.is_git_repo),
    sortIndex: entry.sort_index,
    addedAt: entry.added_at,
    updatedAt: entry.updated_at,
    lastSelectedAt: entry.last_selected_at,
    active: entry.active,
    worktreeEnabled: entry.worktree_enabled,
    gitBranch: String(entry.git_branch ?? '').trim(),
    gitHasGit: Boolean(entry.git_has_git),
    gitClean: Boolean(entry.git_clean),
    gitDirtyCount: typeof entry.git_dirty_count === 'number' ? entry.git_dirty_count : 0,
    gitStagedCount: typeof entry.git_staged_count === 'number' ? entry.git_staged_count : 0,
    gitModifiedCount: typeof entry.git_modified_count === 'number' ? entry.git_modified_count : 0,
    gitUntrackedCount: typeof entry.git_untracked_count === 'number' ? entry.git_untracked_count : 0,
    gitConflictCount: typeof entry.git_conflict_count === 'number' ? entry.git_conflict_count : 0,
    gitAheadCount: typeof entry.git_ahead_count === 'number' ? entry.git_ahead_count : 0,
    gitBehindCount: typeof entry.git_behind_count === 'number' ? entry.git_behind_count : 0,
    gitCommittedFileCount: typeof entry.git_committed_file_count === 'number' ? entry.git_committed_file_count : 0,
    gitCommittedAdditions: typeof entry.git_committed_additions === 'number' ? entry.git_committed_additions : 0,
    gitCommittedDeletions: typeof entry.git_committed_deletions === 'number' ? entry.git_committed_deletions : 0,
    todoSummary: undefined,
    topologyRoutes: [],
  }
}

export function mapWorkspaceResolution(entry: WorkspaceResolutionWire): WorkspaceResolution {
  return {
    ...mapWorkspaceDefinition(entry),
    requestedPath: entry.requested_path,
    resolvedPath: entry.resolved_path,
    workspaceId: String(entry.workspace_id ?? '').trim(),
    workspaceGeneration: typeof entry.workspace_generation === 'number' ? entry.workspace_generation : 0,
    state: String(entry.state ?? '').trim(),
    localWorkspaceBindingId: entry.local_workspace_binding_id ?? '',
    workspaceName: entry.workspace_name,
    themeId: entry.theme_id ?? '',
  }
}

export function mapWorkspaceDiscoverEntry(entry: WorkspaceDiscoverEntryWire): WorkspaceDiscoverEntry {
  return {
    path: entry.path,
    name: entry.name,
    isGitRepo: entry.is_git_repo,
    hasClaude: entry.has_claude,
    hasSwarm: entry.has_swarm,
    lastModified: entry.last_modified,
  }
}

export function mapWorkspaceBrowseEntry(entry: WorkspaceBrowseEntryWire): WorkspaceBrowseEntry {
  return {
    path: entry.path,
    name: entry.name,
    isDirectory: entry.is_directory,
    isGitRepo: entry.is_git_repo,
    hasClaude: entry.has_claude,
    hasSwarm: entry.has_swarm,
  }
}

export function mapWorkspaceBrowseResult(entry: WorkspaceBrowseResultWire): WorkspaceBrowseResult {
  return {
    requestedPath: entry.requested_path,
    resolvedPath: entry.resolved_path,
    parentPath: entry.parent_path?.trim() ? entry.parent_path : null,
    homePath: entry.home_path,
    rootPath: entry.root_path,
    entries: Array.isArray(entry.entries) ? entry.entries.map(mapWorkspaceBrowseEntry) : [],
  }
}

export const DEFAULT_WORKSPACE_STORAGE_KEY = 'swarm.web.defaultWorkspacePath'
export const LAST_WORKSPACE_STORAGE_KEY = 'swarm.web.lastWorkspacePath'
