import type { VaultImportResult } from '../vault/types'
import type { DesktopChatRoute } from '../chat/services/chat-routing'
import type { ResolvedSessionPreference } from '../chat/types/chat'

export interface DesktopSessionUsageRecord {
  sessionId: string
  provider: string
  model: string
  source: string
  contextWindow: number
  totalTokens: number
  remainingTokens: number
  updatedAt: number
}

export interface DesktopRunIntentRecord {
  sessionId: string
  runId: string
  status: string
  blockedReason: string
  createdAt: number
  updatedAt: number
  eventSeq: number
}

export interface DesktopLiveAssistantSegment {
  id: string
  content: string
  createdAt: number
  seq: number
}

export interface DesktopLiveToolRecord {
  key: string
  sessionId: string
  runId: string
  stepId: string
  callId: string
  toolInstanceId: string
  pathId?: 'run.tool-history.v2' | 'run.v3.provider-tool-result.v1'
  toolName: string | null
  toolArguments: string | null
  toolOutput: string
  state: 'running' | 'done' | 'error'
  step: number | null
  seq?: number
  startedAt: number
  updatedAt: number
  completedAt: number | null
}

export interface DesktopLiveReasoningRecord {
  key: string
  runId: string
  step: number
  stepId: string
  reasoningId: string
  reasoningKey: string
  text: string
  summary: string
  state: 'running' | 'done' | 'error'
  startedAt: number
  completedAt: number | null
  timelineSeq: number
  updatedSeq: number
}

export interface DesktopSessionRecord {
  id: string
  title: string
  workspacePath: string
  workspaceName: string
  mode: string
  metadata?: Record<string, unknown>
  sessionApi?: string
  lastEventSeq?: number
  projectionHighWatermarkSeq?: number
  messageCount: number
  updatedAt: number
  createdAt: number
  permissionsHydrated: boolean
  runtimeWorkspacePath?: string
  worktreeEnabled?: boolean
  worktreeRootPath?: string
  worktreeBaseBranch?: string
  worktreeBranch?: string
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
  gitCommitDetected?: boolean
  gitCommitCount?: number
  gitCommittedFileCount?: number
  gitCommittedAdditions?: number
  gitCommittedDeletions?: number
  lifecycle: {
    sessionId: string
    runId: string | null
    active: boolean
    phase: string
    startedAt: number
    endedAt: number
    updatedAt: number
    generation: number
    stopReason: string | null
    error: string | null
    ownerTransport: string | null
  } | null
  runIntent?: DesktopRunIntentRecord | null
  live: {
    runId: string | null
    terminalRunId?: string | null
    terminalEventSeq?: number
    agentName: string | null
    startedAt: number | null
    status: 'idle' | 'starting' | 'running' | 'blocked' | 'error'
    step: number
    toolName: string | null
    sidebarToolName: string | null
    toolCallId: string | null
    toolArguments: string | null
    toolOutput: string
    retainedToolName: string | null
    retainedToolCallId: string | null
    retainedToolArguments: string | null
    retainedToolOutput: string
    retainedToolState: 'running' | 'done' | 'error' | null
    toolHistory?: DesktopLiveToolRecord[]
    summary: string | null
    lastEventType: string | null
    lastEventAt: number | null
    error: string | null
    seq: number
    assistantDraft: string
    retainedAssistantSegments: DesktopLiveAssistantSegment[]
    reasoningSummary: string
    reasoningText: string
    reasoningState: 'idle' | 'running' | 'done' | 'error'
    reasoningSegment: number
    reasoningStartedAt: number | null
    reasoningCompletedAt?: number | null
    reasoningTimelineSeq?: number
    reasoningHistory?: DesktopLiveReasoningRecord[]
    awaitingAck: boolean
  }
  pendingPermissions: DesktopPermissionRecord[]
  pendingPermissionCount: number
  usage: DesktopSessionUsageRecord | null
}

export interface DesktopPermissionRecord {
  id: string
  sessionId: string
  runId: string
  callId: string
  toolName: string
  toolArguments: string
  approvedArguments?: string
  savedRule?: {
    id: string
    kind: string
    decision: string
    tool?: string
    pattern?: string
    createdAt?: number
    updatedAt?: number
  }
  status: string
  decision: string
  reason: string
  requirement: string
  mode: string
  createdAt: number
  updatedAt: number
  resolvedAt: number
  permissionRequestedAt: number
}

export interface DesktopNotificationRecord {
  id: string
  sessionId: string | null
  runId: string | null
  eventType: string
  title: string
  detail: string
  createdAt: number
  severity: 'info' | 'warning' | 'error'
  source?: 'session' | 'swarm'
  swarmEnrollmentId?: string | null
  swarmChildName?: string | null
}

export interface DesktopNotificationCenterRecord {
  id: string
  swarmID: string
  originSwarmID: string | null
  sessionId: string | null
  runId: string | null
  category: string
  severity: 'info' | 'warning' | 'error' | string
  title: string
  body: string
  status: string
  sourceEventType: string | null
  permissionId: string | null
  toolName: string | null
  requirement: string | null
  sessionTitle: string | null
  sessionLabel: string | null
  workspacePath: string | null
  workspaceName: string | null
  originLabel: string | null
  actionURL: string | null
  readAt: number | null
  ackedAt: number | null
  mutedAt: number | null
  createdAt: number
  updatedAt: number
}

export interface DesktopNotificationSummary {
  swarmID: string
  totalCount: number
  unreadCount: number
  activeCount: number
  updatedAt: number
}

export interface DesktopVaultState {
  bootstrapped: boolean
  loading: boolean
  enabled: boolean
  unlocked: boolean
  unlockRequired: boolean
  storageMode: string
  warning: string
  error: string | null
  openSettingsOnUnlock: boolean
}

export type DesktopConnectionState = 'idle' | 'connecting' | 'open' | 'closed' | 'error'

export interface DesktopStoreState {
  hydrated: boolean
  hydrating: boolean
  connectionState: DesktopConnectionState
  onboardingFlowRequested: boolean
  activeSessionId: string | null
  activeWorkspacePath: string | null
  sessions: Record<string, DesktopSessionRecord>
  notifications: DesktopNotificationRecord[]
  notificationCenter: {
    items: DesktopNotificationCenterRecord[]
    summary: DesktopNotificationSummary
    loading: boolean
    hydrated: boolean
  }
  reconnectTimer: number | null
  heartbeatTimer: number | null
  livenessTimer: number | null
  reconnectAttempt: number
  connectionGeneration: number
  realtimeDesired: boolean
  lastGlobalSeq: number
  vault: DesktopVaultState
  sessionDrafts: Record<string, string>
  sessionDraftModes: Record<string, 'plan' | 'auto' | 'read' | 'readwrite'>
  setActiveSession: (sessionId: string | null) => void
  setActiveWorkspacePath: (workspacePath: string | null) => void
  upsertSession: (session: DesktopSessionRecord) => void
  refreshSessionPermissions: (sessionId: string) => Promise<void>
  refreshNotifications: () => Promise<void>
  clearNotifications: () => Promise<void>
  updateNotificationRecord: (id: string, patch: { read?: boolean; acked?: boolean; muted?: boolean; status?: string }) => Promise<void>
  setSessionDraft: (sessionId: string, draft: string) => void
  setSessionDraftMode: (sessionId: string, mode: 'plan' | 'auto' | 'read' | 'readwrite') => void
  getSessionDraft: (sessionId: string | null, workspacePath?: string | null) => string
  getSessionDraftMode: (sessionId: string | null, workspacePath?: string | null) => 'plan' | 'auto' | 'read' | 'readwrite'
  bootstrapVault: () => Promise<void>
  refreshVaultStatus: () => Promise<void>
  enableVault: (password: string) => Promise<void>
  unlockVault: (password: string, options?: { openSettingsOnUnlock?: boolean }) => Promise<void>
  lockVault: () => Promise<void>
  disableVault: (password: string) => Promise<void>
  exportVaultBundle: (password: string, vaultPassword?: string) => Promise<{ exported: number; bundle: Uint8Array }>
  importVaultBundle: (password: string, bundle: Uint8Array, vaultPassword?: string) => Promise<VaultImportResult>
  consumeVaultSettingsRequest: () => boolean
  requestOnboardingFlow: () => void
  clearOnboardingFlow: () => void
  hydrate: () => Promise<void>
  connect: () => Promise<void>
  reconnectIfStale: (reason: string) => Promise<void>
  syncV3RealtimeSessions: (options?: { force?: boolean }) => void
  disconnect: () => void
  createSession: (input: {
    title?: string
    workspacePath: string
    workspaceName: string
    mode: string
    agentName?: string
    metadata?: Record<string, unknown>
    preference: ResolvedSessionPreference['preference']
    route?: DesktopChatRoute | null
    worktreeMode?: string
    worktreeUseCurrentBranch?: boolean
    worktreeBaseBranch?: string
    worktreeBranchName?: string
  }) => Promise<DesktopSessionRecord>
  submitPrompt: (input: {
    sessionId: string | null
    route?: DesktopChatRoute | null
    sessionApi?: string | null
    clientRequestId?: string | null
    workspacePath: string
    workspaceName: string
    prompt: string
    agentName: string
    compact?: boolean
    targetKind?: string
    targetName?: string
  }) => Promise<void>
  ensureRunStream: (sessionId: string, runId?: string | null) => Promise<void>
  closeRunStream: (sessionId: string) => void
  stopRun: (sessionId: string, route?: DesktopChatRoute | null, runId?: string | null) => Promise<void>
  __testApplyRunStreamFrame?: (sessionId: string, payload: Record<string, unknown>, ts?: number) => void
  __testApplyV3RealtimeFrame?: (sessionId: string, payload: Record<string, unknown>, ts?: number) => void
}
