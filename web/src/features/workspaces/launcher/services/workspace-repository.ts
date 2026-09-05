export type WorkspaceRepositoryStateCode =
  | 'ready'
  | 'git_unavailable'
  | 'not_repository'
  | 'needs_initial_commit'
  | 'needs_assisted_setup'

export interface WorkspaceRepositoryState {
  state: WorkspaceRepositoryStateCode
  path: string
  repositoryRoot: string
  headCommit: string
  canSetup: boolean
  needsReview: boolean
  message: string
}

export interface WorkspaceRepositoryStateWire {
  state?: string
  path?: string
  repository_root?: string
  head_commit?: string
  can_setup?: boolean
  needs_review?: boolean
  message?: string
}

export class WorkspaceRepositoryPrerequisiteError extends Error {
  readonly code = 'workspace_repository_not_ready'
  readonly repository: WorkspaceRepositoryState

  constructor(repository: WorkspaceRepositoryState, message?: string) {
    super(message?.trim() || repository.message || 'Swarm requires a committed Git repository.')
    this.name = 'WorkspaceRepositoryPrerequisiteError'
    this.repository = repository
  }
}

export function mapWorkspaceRepositoryState(value: WorkspaceRepositoryStateWire): WorkspaceRepositoryState {
  const rawState = String(value.state ?? '').trim().toLowerCase()
  const state: WorkspaceRepositoryStateCode = rawState === 'ready'
    || rawState === 'git_unavailable'
    || rawState === 'not_repository'
    || rawState === 'needs_initial_commit'
    || rawState === 'needs_assisted_setup'
    ? rawState
    : 'needs_assisted_setup'
  return {
    state,
    path: String(value.path ?? '').trim(),
    repositoryRoot: String(value.repository_root ?? '').trim(),
    headCommit: String(value.head_commit ?? '').trim(),
    canSetup: Boolean(value.can_setup),
    needsReview: Boolean(value.needs_review),
    message: String(value.message ?? '').trim(),
  }
}

export function workspaceRepositorySetupPrompt(repository: WorkspaceRepositoryState): string {
  return [
    `Inspect and prepare ${repository.path || 'the selected folder'} as a committed Git repository so it can become a Swarm workspace.`,
    'Inspect the directory and review repository setup before changing anything. Review its existing files and applicable ignore rules, then explain what should or should not be tracked before proposing a first commit. Never silently stage or commit files. Request explicit permission before every Git mutation, including git init, git add, or git commit. After approval, verify that the selected directory is the repository root and that HEAD resolves to a commit.',
    '',
    `Current repository state: ${repository.state}.`,
    repository.message,
  ].filter(Boolean).join('\n')
}
