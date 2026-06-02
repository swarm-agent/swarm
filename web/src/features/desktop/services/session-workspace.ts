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

export function sessionMetadataBoolean(metadata: Record<string, unknown> | null | undefined, key: string): boolean | undefined {
  const value = metadata?.[key]
  if (typeof value === 'boolean') {
    return value
  }
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase()
    if (normalized === 'true') return true
    if (normalized === 'false') return false
  }
  return undefined
}

export function sessionWorkspaceFactsFromMetadata(metadata: Record<string, unknown> | null | undefined): {
  sourceWorkspacePath: string
  runtimeWorkspacePath: string
  worktreeEnabled?: boolean
  worktreeRootPath: string
} {
  return {
    sourceWorkspacePath: sessionMetadataString(metadata, 'swarm_v2_source_workspace_path')
      || sessionMetadataString(metadata, 'swarm_routed_host_workspace_path'),
    runtimeWorkspacePath: sessionMetadataString(metadata, 'swarm_v2_runtime_workspace_path')
      || sessionMetadataString(metadata, 'swarm_routed_runtime_workspace_path'),
    worktreeEnabled: sessionMetadataBoolean(metadata, 'swarm_v2_worktree_enabled'),
    worktreeRootPath: sessionMetadataString(metadata, 'swarm_v2_worktree_root_path'),
  }
}

export function canonicalSessionWorkspacePath(input: {
  workspacePath: string
  hostedHostWorkspacePath?: string
  hostedRuntimeWorkspacePath?: string
  sourceWorkspacePath?: string
  runtimeWorkspacePath?: string
  preferHostedRuntimeWorkspacePath?: boolean
  worktreeEnabled?: boolean
  worktreeRootPath?: string
}): string {
  const workspacePath = input.workspacePath.trim()
  const sourceWorkspacePath = input.sourceWorkspacePath?.trim() ?? ''
  const runtimeWorkspacePath = input.runtimeWorkspacePath?.trim() ?? ''
  const hostedRuntimeWorkspacePath = input.hostedRuntimeWorkspacePath?.trim() || runtimeWorkspacePath
  if (input.preferHostedRuntimeWorkspacePath && hostedRuntimeWorkspacePath) {
    return hostedRuntimeWorkspacePath
  }

  const hostedHostWorkspacePath = input.hostedHostWorkspacePath?.trim() || sourceWorkspacePath
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
