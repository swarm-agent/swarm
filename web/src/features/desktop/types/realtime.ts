import type { VaultImportResult } from '../vault/types'

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

export interface DesktopSessionRecord {
  id: string
  title: string
  workspacePath: string
  workspaceName: string
  mode: string
  metadata?: Record<string, unknown>
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
  live: {
    runId: string | null
    agentName: string | null
    startedAt: number | null
    status: 'idle' | 'starting' | 'running' | 'blocked' | 'error'
    step: number
    toolName: string | null
    toolCallId: string | null
    toolArguments: string | null
    toolOutput: string
    retainedToolName: string | null
    retainedToolCallId: string | null
    retainedToolArguments: string | null
    retainedToolOutput: string
    retainedToolState: 'running' | 'done' | 'error' | null
    summary: string | null
    lastEventType: string | null
    lastEventAt: number | null
    error: string | null
    seq: number
    assistantDraft: string
    reasoningSummary: string
    reasoningText: string
    reasoningState: 'idle' | 'running' | 'done' | 'error'
    reasoningSegment: number
    reasoningStartedAt: number | null
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

export interface DesktopStoreState {
  hydrated: boolean
  hydrating: boolean
  connectionState: 'idle' | 'connecting' | 'open' | 'closed' | 'error'
  onboardingFlowRequested: boolean
  activeSessionId: string | null
  activeWorkspacePath: string | null
  sessions: Record<string, DesktopSessionRecord>
  notifications: DesktopNotificationRecord[]
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
  disconnect: () => void
  submitPrompt: (input: {
    sessionId: string | null
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
  stopRun: (sessionId: string) => Promise<void>
}
