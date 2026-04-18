import type { WorkspaceDiscoverEntry, WorkspaceEntry, WorkspaceBrowseResult } from '../../types/workspace'

export type WorkspaceLauncherIterationId = '1'

export interface WorkspaceLauncherIterationDefinition {
  id: WorkspaceLauncherIterationId
  label: string
  tagline: string
}

export interface WorkspaceLauncherIterationProps {
  workspaces: WorkspaceEntry[]
  discovered: WorkspaceDiscoverEntry[]
  currentWorkspacePath: string | null
  defaultWorkspacePath: string | null
  lastWorkspacePath: string | null
  selectingPath: string | null
  savingPath: string | null
  draggingWorkspacePath: string | null
  browser: WorkspaceBrowseResult | null
  browserLoading: boolean
  browserError: string | null
  refreshing?: boolean
  onRefresh?: () => void
  onOpenWorkspace: (path: string) => void
  onUseFolderTemporarily: (path: string) => void
  onEditWorkspace: (path: string) => void
  onDeleteWorkspace: (path: string) => void
  onToggleWorktree: (path: string, enabled: boolean) => void
  onSetDefaultWorkspace: (path: string | null) => void
  onSaveDiscovered: (entry: WorkspaceDiscoverEntry) => void
  onBrowsePath: (path: string) => void
  onMoveWorkspaceToIndex: (path: string, index: number) => void
  onDraggingWorkspaceChange: (path: string | null) => void
}
