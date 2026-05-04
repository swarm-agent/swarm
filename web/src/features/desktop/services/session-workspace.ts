function trimTrailingSeparators(value: string): string {
  return value.replace(/[\\/]+$/, '')
}

function normalizeWorkspacePathForComparison(value: string): string {
  return trimTrailingSeparators(value.trim()).replace(/\\/g, '/')
}

export function basenameFromWorkspacePath(value: string): string {
  const trimmed = trimTrailingSeparators(value.trim())
  if (!trimmed) {
    return ''
  }
  const segments = trimmed.split(/[/\\]/)
  return segments[segments.length - 1] ?? ''
}

export function canonicalSessionWorkspacePath(input: {
  workspacePath: string
  hostedHostWorkspacePath?: string
  hostedRuntimeWorkspacePath?: string
  preferHostedRuntimeWorkspacePath?: boolean
  worktreeEnabled?: boolean
  worktreeRootPath?: string
}): string {
  const workspacePath = input.workspacePath.trim()
  const hostedRuntimeWorkspacePath = input.hostedRuntimeWorkspacePath?.trim() ?? ''
  if (input.preferHostedRuntimeWorkspacePath && hostedRuntimeWorkspacePath) {
    return hostedRuntimeWorkspacePath
  }

  const hostedHostWorkspacePath = input.hostedHostWorkspacePath?.trim() ?? ''
  if (hostedHostWorkspacePath) {
    return hostedHostWorkspacePath
  }

  if (hostedRuntimeWorkspacePath && normalizeWorkspacePathForComparison(workspacePath) === normalizeWorkspacePathForComparison(hostedRuntimeWorkspacePath)) {
    return hostedRuntimeWorkspacePath
  }

  const worktreeRootPath = input.worktreeRootPath?.trim() ?? ''
  if (input.worktreeEnabled && worktreeRootPath) {
    return worktreeRootPath
  }

  return workspacePath
}

export function canonicalSessionWorkspaceName(workspaceName: string, workspacePath: string, canonicalWorkspacePath: string): string {
  const trimmedName = workspaceName.trim()
  const rawWorkspaceBaseName = basenameFromWorkspacePath(workspacePath)
  if (trimmedName && !(trimmedName === rawWorkspaceBaseName && /^ws_[a-z0-9]+$/i.test(trimmedName))) {
    return trimmedName
  }
  return basenameFromWorkspacePath(canonicalWorkspacePath) || trimmedName
}
