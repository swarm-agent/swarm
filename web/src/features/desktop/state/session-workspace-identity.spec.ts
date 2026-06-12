import assert from 'node:assert/strict'
import test from 'node:test'

import { applyEnvelope } from './use-desktop-store'
import type { DesktopSessionRecord, DesktopStoreState } from '../types/realtime'

function emptyLiveState(): DesktopSessionRecord['live'] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: 'idle',
    step: 0,
    toolName: null,
    sidebarToolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: '',
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: '',
    retainedToolState: null,
    summary: null,
    lastEventType: null,
    lastEventAt: null,
    error: null,
    seq: 0,
    assistantDraft: '',
    retainedAssistantSegments: [],
    reasoningSummary: '',
    reasoningText: '',
    reasoningState: 'idle',
    reasoningSegment: 0,
    reasoningStartedAt: null,
    awaitingAck: false,
  }
}

function makeState(): DesktopStoreState {
  return {
    hydrated: true,
    hydrating: false,
    connectionState: 'open',
    onboardingFlowRequested: false,
    activeSessionId: null,
    activeWorkspacePath: null,
    sessions: {},
    notifications: [],
    notificationCenter: {
      items: [],
      summary: { swarmID: '', totalCount: 0, unreadCount: 0, activeCount: 0, updatedAt: 0 },
      loading: false,
      hydrated: false,
    },
    reconnectTimer: null,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: 0,
    connectionGeneration: 0,
    realtimeDesired: true,
    lastGlobalSeq: 0,
    vault: {
      bootstrapped: false,
      loading: false,
      enabled: false,
      unlocked: false,
      unlockRequired: false,
      storageMode: '',
      warning: '',
      error: null,
      openSettingsOnUnlock: false,
    },
    sessionDrafts: {},
    sessionDraftModes: {},
    setActiveSession: () => undefined,
    setActiveWorkspacePath: () => undefined,
    upsertSession: () => undefined,
    refreshSessionPermissions: async () => undefined,
    refreshNotifications: async () => undefined,
    clearNotifications: async () => undefined,
    updateNotificationRecord: async () => undefined,
    setSessionDraft: () => undefined,
    setSessionDraftMode: () => undefined,
    getSessionDraft: () => '',
    getSessionDraftMode: () => 'auto',
    bootstrapVault: async () => undefined,
    refreshVaultStatus: async () => undefined,
    enableVault: async () => undefined,
    unlockVault: async () => undefined,
    lockVault: async () => undefined,
    disableVault: async () => undefined,
    exportVaultBundle: async () => ({ exported: 0, bundle: new Uint8Array() }),
    importVaultBundle: async () => ({
      imported: 0,
      vault: { enabled: false, unlocked: false, unlockRequired: false, storageMode: '', warning: '' },
    }),
    consumeVaultSettingsRequest: () => false,
    requestOnboardingFlow: () => undefined,
    clearOnboardingFlow: () => undefined,
    hydrate: async () => undefined,
    connect: async () => undefined,
    reconnectIfStale: async () => undefined,
    disconnect: () => undefined,
    submitPrompt: async () => undefined,
    ensureRunStream: async () => undefined,
    closeRunStream: () => undefined,
    stopRun: async () => undefined,
  }
}

test('session.created maps local-container v2 mirrored session back to source workspace from binding metadata', () => {
  const patch = applyEnvelope(makeState(), {
    global_seq: 1,
    event_type: 'session.created',
    entity_id: 'session-local',
    ts_unix_ms: 1,
    payload: {
      id: 'session-local',
      title: 'Local container',
      workspace_path: '/workspaces/swarm-go',
      workspace_name: 'swarm-go',
      mode: 'auto',
      metadata: {
        swarm_v2_workspace_binding_id: 'binding:replica:checkthis:/workspaces/source-swarm-go',
        swarm_v2_source_workspace_id: 'ws_primary',
        swarm_v2_source_workspace_name: 'swarm-go',
        swarm_v2_source_workspace_path: '/workspaces/source-swarm-go',
        swarm_v2_runtime_workspace_path: '/workspaces/swarm-go',
      },
      created_at: 1,
      updated_at: 1,
    },
  })

  const session = patch.sessions?.['session-local']
  assert.ok(session)
  assert.equal(session.workspacePath, '/workspaces/source-swarm-go')
  assert.equal(session.runtimeWorkspacePath, '/workspaces/swarm-go')
  assert.equal(session.workspaceName, 'swarm-go')
})

test('session.updated repairs a placeholder runtime workspace path when v2 binding metadata arrives', () => {
  const state = makeState()
  state.sessions['session-local'] = {
    id: 'session-local',
    title: 'Local container',
    workspacePath: '/workspaces/swarm-go',
    workspaceName: 'swarm-go',
    mode: 'auto',
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: false,
    lifecycle: null,
    live: emptyLiveState(),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }

  const patch = applyEnvelope(state, {
    global_seq: 2,
    event_type: 'session.updated',
    entity_id: 'session-local',
    ts_unix_ms: 2,
    payload: {
      id: 'session-local',
      title: 'Local container',
      workspace_path: '/workspaces/swarm-go',
      workspace_name: 'swarm-go',
      mode: 'auto',
      metadata: {
        swarm_v2_workspace_binding_id: 'binding:replica:checkthis:/workspaces/source-swarm-go',
        swarm_v2_source_workspace_path: '/workspaces/source-swarm-go',
        swarm_v2_runtime_workspace_path: '/workspaces/swarm-go',
      },
      updated_at: 2,
    },
  })

  const session = patch.sessions?.['session-local']
  assert.ok(session)
  assert.equal(session.workspacePath, '/workspaces/source-swarm-go')
  assert.equal(session.runtimeWorkspacePath, '/workspaces/swarm-go')
})
