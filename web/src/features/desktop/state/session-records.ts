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
  const incomingHasToolDetails = liveHasToolDetails(incoming)
  const existingHasToolDetails = liveHasToolDetails(existing)
  const incomingHasAssistantDetails = liveHasAssistantDetails(incoming)
  const existingHasAssistantDetails = liveHasAssistantDetails(existing)
  const incomingHasReasoningDetails = liveHasReasoningDetails(incoming)
  const existingHasReasoningDetails = liveHasReasoningDetails(existing)
  const incomingClearsAssistantDetails = liveClearsAssistantDetails(incoming)

  return {
    ...incoming,
    seq: Math.max(existing.seq ?? 0, incoming.seq ?? 0),
    toolName: mergeLiveToolValue(existing.toolName, incoming.toolName, existingHasToolDetails, incomingHasToolDetails),
    sidebarToolName: mergeLiveToolValue(existing.sidebarToolName, incoming.sidebarToolName, existingHasToolDetails, incomingHasToolDetails),
    toolCallId: mergeLiveToolValue(existing.toolCallId, incoming.toolCallId, existingHasToolDetails, incomingHasToolDetails),
    toolArguments: mergeLiveToolValue(existing.toolArguments, incoming.toolArguments, existingHasToolDetails, incomingHasToolDetails),
    toolOutput: mergeLiveToolValue(existing.toolOutput, incoming.toolOutput, existingHasToolDetails, incomingHasToolDetails),
    retainedToolName: mergeLiveToolValue(existing.retainedToolName, incoming.retainedToolName, existingHasToolDetails, incomingHasToolDetails),
    retainedToolCallId: mergeLiveToolValue(existing.retainedToolCallId, incoming.retainedToolCallId, existingHasToolDetails, incomingHasToolDetails),
    retainedToolArguments: mergeLiveToolValue(existing.retainedToolArguments, incoming.retainedToolArguments, existingHasToolDetails, incomingHasToolDetails),
    retainedToolOutput: mergeLiveToolValue(existing.retainedToolOutput, incoming.retainedToolOutput, existingHasToolDetails, incomingHasToolDetails),
    retainedToolState: mergeLiveToolValue(existing.retainedToolState, incoming.retainedToolState, existingHasToolDetails, incomingHasToolDetails),
    summary: mergeLiveSummary(existing, incoming, existingHasToolDetails, incomingHasToolDetails),
    assistantDraft: incomingClearsAssistantDetails
      ? incoming.assistantDraft
      : incoming.assistantDraft || (!incomingHasAssistantDetails && existingHasAssistantDetails ? existing.assistantDraft : incoming.assistantDraft),
    retainedAssistantSegments: incomingClearsAssistantDetails
      ? incomingRetainedAssistantSegments
      : incomingRetainedAssistantSegments.length > 0
        ? incomingRetainedAssistantSegments
        : existingRetainedAssistantSegments,
    reasoningSummary: incoming.reasoningSummary || (!incomingHasReasoningDetails && existingHasReasoningDetails ? existing.reasoningSummary : incoming.reasoningSummary),
    reasoningText: incoming.reasoningText || (!incomingHasReasoningDetails && existingHasReasoningDetails ? existing.reasoningText : incoming.reasoningText),
    reasoningState: incomingHasReasoningDetails || !existingHasReasoningDetails ? incoming.reasoningState : existing.reasoningState,
    reasoningSegment: Math.max(existing.reasoningSegment ?? 0, incoming.reasoningSegment ?? 0),
    reasoningStartedAt: incoming.reasoningStartedAt || (!incomingHasReasoningDetails && existingHasReasoningDetails ? existing.reasoningStartedAt : incoming.reasoningStartedAt),
    toolHistory: incomingToolHistory.length > 0
      ? incomingToolHistory
      : existingToolHistory,
  }
}

function liveClearsAssistantDetails(live: DesktopSessionRecord['live']): boolean {
  switch (live.lastEventType) {
    case 'session.assistant.completed':
    case 'session.assistant.failed':
    case 'session.run.completed':
    case 'session.run.cancelled':
    case 'session.run.expired':
    case 'session.run.interrupted':
      return live.assistantDraft === '' && (live.retainedAssistantSegments?.length ?? 0) === 0
    default:
      return false
  }
}

function mergeLiveToolValue<T extends string | null>(
  existing: T,
  incoming: T,
  existingHasToolDetails: boolean,
  incomingHasToolDetails: boolean,
): T {
  return incoming || (!incomingHasToolDetails && existingHasToolDetails ? existing : incoming)
}

function liveHasToolDetails(live: DesktopSessionRecord['live']): boolean {
  return Boolean(
    live.toolName?.trim()
    || live.sidebarToolName?.trim()
    || live.toolCallId?.trim()
    || live.toolArguments?.trim()
    || live.toolOutput.trim()
    || live.retainedToolName?.trim()
    || live.retainedToolCallId?.trim()
    || live.retainedToolArguments?.trim()
    || live.retainedToolOutput.trim()
    || live.retainedToolState
    || (live.toolHistory?.length ?? 0) > 0,
  )
}

function liveHasAssistantDetails(live: DesktopSessionRecord['live']): boolean {
  return Boolean(live.assistantDraft.trim() || live.retainedAssistantSegments.length > 0)
}

function liveHasReasoningDetails(live: DesktopSessionRecord['live']): boolean {
  return Boolean(
    live.reasoningSummary.trim()
    || live.reasoningText.trim()
    || live.reasoningState !== 'idle'
    || live.reasoningSegment > 0
    || live.reasoningStartedAt !== null,
  )
}

function mergeLiveSummary(
  existing: DesktopSessionRecord['live'],
  incoming: DesktopSessionRecord['live'],
  existingHasToolDetails: boolean,
  incomingHasToolDetails: boolean,
): string | null {
  const incomingSummary = incoming.summary?.trim() ?? ''
  if (incomingSummary) {
    if (!incomingHasToolDetails && existingHasToolDetails && isGenericAssistantSummary(incomingSummary)) {
      return existing.summary || existing.toolName || existing.retainedToolName || incoming.summary
    }
    return incoming.summary
  }
  if (!incomingHasToolDetails && existingHasToolDetails) {
    return existing.summary
  }
  return incoming.summary
}

function isGenericAssistantSummary(summary: string): boolean {
  const normalized = summary.trim().toLowerCase()
  return normalized === 'assistant responding...' || normalized === 'assistant responding…' || normalized === 'streaming response...' || normalized === 'streaming response…'
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
    runIntent: incoming.runIntent === undefined ? existing.runIntent ?? null : incoming.runIntent,
    live: mergeSessionLiveState(existing.live, incoming.live),
    pendingPermissions: incoming.pendingPermissions,
    pendingPermissionCount: incoming.pendingPermissionCount,
    usage: incoming.usage ?? existing.usage,
  }
}
