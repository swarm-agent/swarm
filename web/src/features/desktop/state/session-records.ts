import type { DesktopSessionRecord } from '../types/realtime'

function metadataString(metadata: Record<string, unknown> | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function isFlowSessionMetadata(metadata: Record<string, unknown> | undefined): boolean {
  return metadataString(metadata, 'source').toLowerCase() === 'flow'
    || metadataString(metadata, 'lineage_kind').toLowerCase() === 'flow'
    || metadataString(metadata, 'flow_id') !== ''
}

function isPlaceholderSessionTitle(title: string): boolean {
  const normalized = title.trim().toLowerCase()
  return normalized === 'new session' || normalized === 'new conversation'
}

function mergeSessionTitle(existing: DesktopSessionRecord, incoming: DesktopSessionRecord): string {
  const incomingTitle = incoming.title.trim()
  const existingTitle = existing.title.trim()
  if (!incomingTitle) {
    return existing.title
  }
  if ((isFlowSessionMetadata(existing.metadata) || isFlowSessionMetadata(incoming.metadata))
    && isPlaceholderSessionTitle(incomingTitle)
    && existingTitle
    && !isPlaceholderSessionTitle(existingTitle)) {
    return existing.title
  }
  return incoming.title
}

function mergeSessionLiveState(
  existing: DesktopSessionRecord['live'],
  incoming: DesktopSessionRecord['live'],
): DesktopSessionRecord['live'] {
  const incomingRetainedAssistantSegments = incoming.retainedAssistantSegments ?? []
  const existingRetainedAssistantSegments = existing.retainedAssistantSegments ?? []
  const incomingToolHistory = incoming.toolHistory ?? []
  const existingToolHistory = existing.toolHistory ?? []

  return {
    ...incoming,
    seq: Math.max(existing.seq ?? 0, incoming.seq ?? 0),
    retainedAssistantSegments: incomingRetainedAssistantSegments.length > 0
      ? incomingRetainedAssistantSegments
      : existingRetainedAssistantSegments,
    toolHistory: incomingToolHistory.length > 0
      ? incomingToolHistory
      : existingToolHistory,
  }
}

export function mergeSessionRecords(existing: DesktopSessionRecord | null, incoming: DesktopSessionRecord): DesktopSessionRecord {
  if (!existing) {
    return incoming
  }

  return {
    ...incoming,
    title: mergeSessionTitle(existing, incoming),
    workspacePath: incoming.workspacePath || existing.workspacePath,
    workspaceName: incoming.workspaceName || existing.workspaceName,
    mode: incoming.mode || existing.mode || 'auto',
    messageCount: Math.max(existing.messageCount, incoming.messageCount),
    updatedAt: Math.max(existing.updatedAt, incoming.updatedAt),
    createdAt:
      existing.createdAt > 0 && incoming.createdAt > 0
        ? Math.min(existing.createdAt, incoming.createdAt)
        : Math.max(existing.createdAt, incoming.createdAt),
    permissionsHydrated: incoming.permissionsHydrated || existing.permissionsHydrated,
    runtimeWorkspacePath: incoming.runtimeWorkspacePath || existing.runtimeWorkspacePath || incoming.workspacePath || existing.workspacePath,
    worktreeEnabled: incoming.worktreeEnabled ?? existing.worktreeEnabled ?? false,
    worktreeRootPath: incoming.worktreeRootPath || existing.worktreeRootPath || '',
    worktreeBaseBranch: incoming.worktreeBaseBranch || existing.worktreeBaseBranch || '',
    worktreeBranch: incoming.worktreeBranch || existing.worktreeBranch || '',
    gitBranch: incoming.gitBranch || existing.gitBranch || '',
    metadata: incoming.metadata ?? existing.metadata,
    sessionApi: incoming.sessionApi || existing.sessionApi || '',
    lastEventSeq: Math.max(incoming.lastEventSeq ?? 0, existing.lastEventSeq ?? 0),
    projectionHighWatermarkSeq: Math.max(incoming.projectionHighWatermarkSeq ?? 0, existing.projectionHighWatermarkSeq ?? 0),
    gitHasGit: incoming.gitHasGit ?? existing.gitHasGit ?? false,
    gitClean: incoming.gitClean ?? existing.gitClean ?? false,
    gitDirtyCount: incoming.gitDirtyCount ?? existing.gitDirtyCount ?? 0,
    gitStagedCount: incoming.gitStagedCount ?? existing.gitStagedCount ?? 0,
    gitModifiedCount: incoming.gitModifiedCount ?? existing.gitModifiedCount ?? 0,
    gitUntrackedCount: incoming.gitUntrackedCount ?? existing.gitUntrackedCount ?? 0,
    gitConflictCount: incoming.gitConflictCount ?? existing.gitConflictCount ?? 0,
    gitAheadCount: incoming.gitAheadCount ?? existing.gitAheadCount ?? 0,
    gitBehindCount: incoming.gitBehindCount ?? existing.gitBehindCount ?? 0,
    gitCommitDetected: incoming.gitCommitDetected ?? existing.gitCommitDetected ?? false,
    gitCommitCount: incoming.gitCommitCount ?? existing.gitCommitCount ?? 0,
    gitCommittedFileCount: incoming.gitCommittedFileCount ?? existing.gitCommittedFileCount ?? 0,
    gitCommittedAdditions: incoming.gitCommittedAdditions ?? existing.gitCommittedAdditions ?? 0,
    gitCommittedDeletions: incoming.gitCommittedDeletions ?? existing.gitCommittedDeletions ?? 0,
    lifecycle: incoming.lifecycle ?? existing.lifecycle,
    live: mergeSessionLiveState(existing.live, incoming.live),
    pendingPermissions: incoming.pendingPermissions,
    pendingPermissionCount: incoming.pendingPermissionCount,
    usage: incoming.usage ?? existing.usage,
  }
}
