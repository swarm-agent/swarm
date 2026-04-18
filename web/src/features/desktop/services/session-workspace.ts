function trimTrailingSeparators(value: string): string {
  return value.replace(/[\\/]+$/, '')
}

export function basenameFromWorkspacePath(value: string): string {
  const trimmed = trimTrailingSeparators(value.trim())
  if (!trimmed) {
    return ''
  }
  const segments = trimmed.split(/[/\\]/)
  return segments[segments.length - 1] ?? ''
}

export function inferHostWorkspacePathFromDetachedWorktree(workspacePath: string): string {
  const trimmed = trimTrailingSeparators(workspacePath.trim())
  if (!trimmed) {
    return ''
  }
  const normalized = trimmed.replace(/\\/g, '/')
  const marker = '/.swarm/worktrees/'
  const markerIndex = normalized.indexOf(marker)
  if (markerIndex <= 0) {
    return ''
  }
  return trimTrailingSeparators(trimmed.slice(0, markerIndex))
}

export function canonicalSessionWorkspacePath(input: {
  workspacePath: string
  hostedHostWorkspacePath?: string
  worktreeEnabled?: boolean
  worktreeRootPath?: string
}): string {
  const hostedHostWorkspacePath = input.hostedHostWorkspacePath?.trim() ?? ''
  if (hostedHostWorkspacePath) {
    return hostedHostWorkspacePath
  }

  const worktreeRootPath = input.worktreeRootPath?.trim() ?? ''
  if (input.worktreeEnabled && worktreeRootPath) {
    return worktreeRootPath
  }

  const inferredHostWorkspacePath = inferHostWorkspacePathFromDetachedWorktree(input.workspacePath)
  return inferredHostWorkspacePath || input.workspacePath.trim()
}

export function canonicalSessionWorkspaceName(workspaceName: string, workspacePath: string, canonicalWorkspacePath: string): string {
  const trimmedName = workspaceName.trim()
  const rawWorkspaceBaseName = basenameFromWorkspacePath(workspacePath)
  if (trimmedName && !(trimmedName === rawWorkspaceBaseName && /^ws_[a-z0-9]+$/i.test(trimmedName))) {
    return trimmedName
  }
  return basenameFromWorkspacePath(canonicalWorkspacePath) || trimmedName
}
