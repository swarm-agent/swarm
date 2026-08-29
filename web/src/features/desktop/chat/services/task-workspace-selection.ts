export interface DesktopTaskWorkspaceCandidate {
  path: string
  workspaceName: string
  workspaceId?: string
  localWorkspaceBindingId?: string
  state?: string
}

export interface ResolveDesktopTaskWorkspaceInput<TWorkspace extends DesktopTaskWorkspaceCandidate> {
  selector?: string
  activeWorkspace: TWorkspace | null
  savedWorkspaces: readonly TWorkspace[]
}

function selectorLooksLikeFilesystemPath(selector: string): boolean {
  return selector.startsWith('/')
    || selector.startsWith('~')
    || selector === '.'
    || selector === '..'
    || selector.startsWith('./')
    || selector.startsWith('../')
    || selector.includes('\\')
    || selector.includes('/')
    || /^[a-z]:[\\/]/i.test(selector)
}

function workspaceHasCanonicalAuthority(workspace: DesktopTaskWorkspaceCandidate): boolean {
  return workspace.path.trim() !== '' && (workspace.localWorkspaceBindingId?.trim() ?? '') !== ''
}

function workspaceSelectorMatches(workspace: DesktopTaskWorkspaceCandidate, selector: string): boolean {
  const normalizedName = workspace.workspaceName.trim().toLowerCase()
  const normalizedSelector = selector.toLowerCase()
  return normalizedName === normalizedSelector
    || workspace.workspaceId?.trim().toLowerCase() === normalizedSelector
    || workspace.localWorkspaceBindingId?.trim().toLowerCase() === normalizedSelector
}

export function resolveDesktopTaskWorkspace<TWorkspace extends DesktopTaskWorkspaceCandidate>(
  input: ResolveDesktopTaskWorkspaceInput<TWorkspace>,
): TWorkspace {
  const selector = input.selector?.trim() ?? ''
  if (!selector) {
    if (!input.activeWorkspace || !workspaceHasCanonicalAuthority(input.activeWorkspace) || input.activeWorkspace.state === 'unavailable') {
      throw new Error('Background Router session requires the active workspace authority')
    }
    return input.activeWorkspace
  }
  if (selectorLooksLikeFilesystemPath(selector)) {
    throw new Error('Task workspace must be a saved workspace name or ID, not a filesystem path')
  }

  const matchesByPath = new Map<string, TWorkspace>()
  for (const workspace of input.savedWorkspaces) {
    if (workspaceSelectorMatches(workspace, selector)) matchesByPath.set(workspace.path, workspace)
  }
  const matches = [...matchesByPath.values()]
  if (matches.length === 0) throw new Error(`Unknown saved workspace “${selector}”`)
  if (matches.length > 1) throw new Error(`Ambiguous saved workspace “${selector}”; use its workspace ID`)
  const workspace = matches[0]
  if (!workspace || workspace.state === 'unavailable' || !workspaceHasCanonicalAuthority(workspace)) {
    throw new Error(`Saved workspace “${selector}” is unavailable`)
  }
  return workspace
}
