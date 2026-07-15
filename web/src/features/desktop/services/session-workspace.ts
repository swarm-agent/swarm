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

export function sessionMetadataString(metadata: Record<string, unknown> | null | undefined, key: string): string {
  return metadata && typeof metadata[key] === 'string' ? metadata[key].trim() : ''
}

export function sessionWorkspaceFactsFromMetadata(metadata: Record<string, unknown> | null | undefined): {
  sourceWorkspacePath: string
  runtimeWorkspacePath: string
  worktreeEnabled?: boolean
  worktreeRootPath: string
} {
  return {
    sourceWorkspacePath: sessionMetadataString(metadata, 'swarm_v3_source_workspace_path'),
    runtimeWorkspacePath: sessionMetadataString(metadata, 'swarm_v3_runtime_workspace_path'),
    worktreeEnabled: undefined,
    worktreeRootPath: '',
  }
}

export function canonicalSessionWorkspacePath(input: {
  workspacePath: string
  sourceWorkspacePath?: string
  runtimeWorkspacePath?: string
  preferRuntimeWorkspacePath?: boolean
  worktreeEnabled?: boolean
  worktreeRootPath?: string
}): string {
  const workspacePath = input.workspacePath.trim()
  const sourceWorkspacePath = input.sourceWorkspacePath?.trim() ?? ''
  const runtimeWorkspacePath = input.runtimeWorkspacePath?.trim() ?? ''
  if (input.preferRuntimeWorkspacePath && runtimeWorkspacePath) {
    return runtimeWorkspacePath
  }

  if (sourceWorkspacePath) {
    return sourceWorkspacePath
  }

  if (runtimeWorkspacePath && normalizeWorkspacePathForComparison(workspacePath) === normalizeWorkspacePathForComparison(runtimeWorkspacePath)) {
    return runtimeWorkspacePath
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
