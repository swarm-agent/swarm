import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { queryClient } from '../../../app/query-client'
import { getDesktopSnapshot, mergeDesktopSnapshot } from './desktop-state-store'
import type { DesktopSessionRecord, DesktopStoreState } from '../types/realtime'
import { applyEnvelope, useDesktopStore } from './use-desktop-store'
import type { RunStreamEventMessage } from './run-stream-controller'
import type { DesktopChatRoute } from '../chat/services/chat-routing'
import { permissionRequiresApproval } from '../permissions/services/permission-payload'
import { buildStructuredToolMessage } from '../chat/services/tool-message'
import { buildLiveReasoningItem, buildLiveToolMessages } from '../chat/components/desktop-chat-panel'

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
    toolHistory: [],
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

function makeSession(input: Partial<DesktopSessionRecord> & Pick<DesktopSessionRecord, 'id'>): DesktopSessionRecord {
  return {
    id: input.id,
    title: input.title ?? 'V3 session',
    workspacePath: input.workspacePath ?? '/repo',
    workspaceName: input.workspaceName ?? 'repo',
    mode: input.mode ?? 'auto',
    metadata: input.metadata,
    sessionApi: input.sessionApi ?? 'v3',
    lastEventSeq: input.lastEventSeq ?? 0,
    projectionHighWatermarkSeq: input.projectionHighWatermarkSeq ?? 0,
    messageCount: input.messageCount ?? 0,
    updatedAt: input.updatedAt ?? 1,
    createdAt: input.createdAt ?? 1,
    permissionsHydrated: input.permissionsHydrated ?? false,
    lifecycle: input.lifecycle ?? null,
    live: input.live ?? emptyLiveState(),
    pendingPermissions: input.pendingPermissions ?? [],
    pendingPermissionCount: input.pendingPermissionCount ?? 0,
    usage: input.usage ?? null,
    runIntent: input.runIntent ?? null,
  }
}

function makeState(session: DesktopSessionRecord): DesktopStoreState {
  return {
    ...useDesktopStore.getState(),
    sessions: { [session.id]: session },
    lastGlobalSeq: 0,
  }
}

const primaryRoute: DesktopChatRoute = {
  id: 'host:binding:local-binding',
  label: 'primary',
  swarmId: 'primary-swarm',
  targetKind: 'host',
  targetRelationship: 'self',
  hostSwarmId: 'primary-swarm',
  hostSwarmName: 'primary',
  hostWorkspacePath: '/repo',
  hostWorkspaceName: 'swarm-go',
  runtimeWorkspacePath: '/repo',
  workspaceBindingId: 'local-binding',
}

afterEach(async () => {
  useDesktopStore.getState().disconnect()
  useDesktopStore.setState({
    sessions: {},
    notifications: [],
    lastGlobalSeq: 0,
    reconnectTimer: null,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: 0,
    realtimeDesired: false,
    connectionState: 'idle',
  })
  queryClient.clear()
  await new Promise((resolve) => setImmediate(resolve))
})

test('V3 stream frame application commits ordered durable message events and cursor state', () => {
  const session = makeSession({ id: 'session-v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.message.appended',
      ts_unix_ms: 10,
      payload: {
        session_id: 'session-v3',
        message: {
          id: 'msg-v3-2',
          session_id: 'session-v3',
          global_seq: 2,
          role: 'user',
          content: 'hello from v3 stream',
          created_at: 10,
        },
      },
    },
  }, 11)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.sessionApi, 'v3')
  assert.equal(updated.lastEventSeq, 2)
  assert.equal(updated.projectionHighWatermarkSeq, 2)
  assert.equal(updated.messageCount, 1)
  assert.equal(updated.live.lastEventType, 'session.message.appended')
  assert.equal(updated.live.lastEventAt, 11)
})

test('V3 stream applies provider usage updates from durable session events', () => {
  const session = makeSession({ id: 'session-v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'run.usage.updated',
      ts_unix_ms: 20,
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        usage_summary: {
          session_id: 'session-v3',
          provider: 'any-provider',
          model: 'provider-model',
          source: 'provider_api_usage',
          context_window: 1000,
          total_tokens: 250,
          remaining_tokens: 750,
          updated_at: 20,
        },
      },
    },
  }, 21)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.usage?.provider, 'any-provider')
  assert.equal(updated.usage?.model, 'provider-model')
  assert.equal(updated.usage?.contextWindow, 1000)
  assert.equal(updated.usage?.remainingTokens, 750)
  assert.equal(updated.live.lastEventType, 'run.usage.updated')
  assert.equal(updated.lastEventSeq, 2)

})

test('V3 stream maps committed assistant lifecycle events into live draft and final message state', () => {
  const originalWindow = globalThis.window
  const testWindow = originalWindow ?? {} as typeof window
  testWindow.setTimeout = ((callback: TimerHandler) => {
    if (typeof callback === 'function') callback()
    return 0
  }) as typeof window.setTimeout
  globalThis.window = testWindow
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  try {

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 2,
      event: {
        id: 'v3evt_session-v3_00000000000000000002',
        session_id: 'session-v3',
        seq: 2,
        event_type: 'session.assistant.started',
        ts_unix_ms: 20,
        payload: { session_id: 'session-v3', run_id: 'run-v3', status: 'running' },
      },
    }, 20)

    let updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.runId, 'run-v3')
    assert.equal(updated.live.status, 'running')
    assert.equal(updated.live.summary, 'Assistant responding…')

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 3,
      event: {
        id: 'v3evt_session-v3_00000000000000000003',
        session_id: 'session-v3',
        seq: 3,
        event_type: 'session.assistant.delta',
        ts_unix_ms: 21,
        payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'hel' },
      },
    }, 21)
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 4,
      event: {
        id: 'v3evt_session-v3_00000000000000000004',
        session_id: 'session-v3',
        seq: 4,
        event_type: 'session.assistant.delta',
        ts_unix_ms: 22,
        payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'lo' },
      },
    }, 22)

    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.assistantDraft, 'hello')
    assert.equal(updated.live.lastEventType, 'session.assistant.delta')

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 5,
      event: {
        id: 'v3evt_session-v3_00000000000000000005',
        session_id: 'session-v3',
        seq: 5,
        event_type: 'session.assistant.completed',
        ts_unix_ms: 23,
        payload: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          status: 'completed',
          message: {
            id: 'msg-assistant-v3',
            session_id: 'session-v3',
            global_seq: 5,
            role: 'assistant',
            content: 'hello',
            created_at: 23,
          },
          run_intent: { run_id: 'run-v3', status: 'completed' },
        },
      },
    }, 23)

    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.lastEventSeq, 5)
    assert.equal(updated.projectionHighWatermarkSeq, 5)
    assert.equal(updated.live.status, 'idle')
    assert.equal(updated.live.runId, null)
    assert.equal(updated.live.assistantDraft, '')
    assert.equal(updated.live.lastEventType, 'session.assistant.completed')
    assert.equal(updated.messageCount, 1)
  } finally {
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
})

test('V3 stream maps reasoning events into live thinking state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.reasoning.started',
      ts_unix_ms: 30,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step: 1, reasoning_key: 'summary-1' },
    },
  }, 30)
  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 3,
    event: {
      id: 'v3evt_session-v3_00000000000000000003',
      session_id: 'session-v3',
      seq: 3,
      event_type: 'session.reasoning.delta',
      ts_unix_ms: 31,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step: 1, reasoning_key: 'summary-1', delta: 'Inspecting files' },
    },
  }, 31)
  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 4,
    event: {
      id: 'v3evt_session-v3_00000000000000000004',
      session_id: 'session-v3',
      seq: 4,
      event_type: 'session.reasoning.completed',
      ts_unix_ms: 32,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step: 1, reasoning_key: 'summary-1', summary: 'Inspecting files' },
    },
  }, 32)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.status, 'running')
  assert.equal(updated.live.reasoningText, 'Inspecting files')
  assert.equal(updated.live.reasoningSummary, 'Inspecting files')
  assert.equal(updated.live.reasoningState, 'done')
  assert.equal(updated.live.reasoningSegment, 1)
  assert.deepEqual(buildLiveReasoningItem(updated, []), {
    type: 'live-reasoning',
    id: 'live-reasoning:run-v3:1',
    text: 'Inspecting files',
    summary: 'Inspecting files',
    state: 'done',
    startedAt: 30,
    timelineSeq: 4,
  })
  assert.equal(buildLiveReasoningItem(updated, [{
    id: 'reasoning-canonical',
    sessionId: 'session-v3',
    role: 'reasoning',
    content: 'Inspecting files',
    createdAt: 40,
    globalSeq: 4,
  }]), null)
  assert.equal(updated.live.sidebarToolName, null)
  assert.equal(updated.live.toolName, null)
  assert.equal(updated.live.toolOutput, '')
  assert.deepEqual(updated.live.toolHistory, [])
  assert.equal(updated.live.lastEventType, 'session.reasoning.completed')
})

test('V3 stream parses thinking events as timeline tool objects', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  const emitThinkingTool = (
    seq: number,
    eventType: 'session.tool.started' | 'session.tool.delta' | 'session.tool.completed',
    payload: Record<string, unknown>,
  ) => {
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: seq,
      event: {
        id: `v3evt_session-v3_${String(seq).padStart(20, '0')}`,
        session_id: 'session-v3',
        seq,
        event_type: eventType,
        ts_unix_ms: 30 + seq,
        payload: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          step: 1,
          step_id: 'step-1',
          tool_instance_id: 'step-1:thinking-1',
          tool_name: 'thinking',
          call_id: 'thinking-1',
          metadata: { synthetic_tool: true, timeline_kind: 'thinking', segment_kind: 'reasoning' },
          ...payload,
        },
      },
    }, 30 + seq)
  }

  emitThinkingTool(2, 'session.tool.started', { arguments: '{"reasoning_key":"summary-1"}' })

  let updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolName, 'thinking')
  assert.equal(updated.live.sidebarToolName, 'thinking')
  assert.equal(updated.live.toolCallId, 'thinking-1')
  assert.equal(updated.live.summary, 'thinking')
  assert.equal(updated.live.reasoningState, 'idle')
  assert.equal(updated.live.lastEventType, 'session.tool.started')
  assert.deepEqual(buildLiveToolMessages(updated).map((message) => [message.tool, message.state, message.timelineSeq]), [['thinking', 'running', 2]])

  emitThinkingTool(3, 'session.tool.delta', { output: 'Inspecting files' })

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolOutput, 'Inspecting files')
  assert.equal(updated.live.toolHistory?.[0]?.toolOutput, 'Inspecting files')
  assert.deepEqual(buildLiveToolMessages(updated).map((message) => [message.tool, message.state, message.output, message.timelineSeq]), [['thinking', 'running', 'Inspecting files', 2]])

  emitThinkingTool(4, 'session.tool.completed', { completed_output: 'Inspecting files' })

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.reasoningText, '')
  assert.equal(updated.live.reasoningSummary, '')
  assert.equal(updated.live.reasoningState, 'idle')
  assert.equal(updated.live.sidebarToolName, 'thinking')
  assert.equal(updated.live.toolName, null)
  assert.equal(updated.live.toolOutput, '')
  assert.equal(updated.live.retainedToolName, 'thinking')
  assert.equal(updated.live.retainedToolOutput, 'Inspecting files')
  assert.equal(updated.live.toolHistory?.length, 1)
  assert.equal(updated.live.toolHistory?.[0]?.toolName, 'thinking')
  assert.equal(updated.live.toolHistory?.[0]?.state, 'done')
  assert.equal(updated.live.toolHistory?.[0]?.toolOutput, 'Inspecting files')
  assert.equal(updated.live.lastEventType, 'session.tool.completed')
  assert.deepEqual(buildLiveToolMessages(updated).map((message) => [message.tool, message.state, message.completedOutput, message.timelineSeq]), [['thinking', 'done', 'Inspecting files', 2]])
})

test('V3 stream maps committed run failures into replayable error state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', live: { ...emptyLiveState(), status: 'running', runId: 'run-v3', assistantDraft: 'partial' } })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.run.failed',
      ts_unix_ms: 30,
      payload: { session_id: 'session-v3', run_id: 'run-v3', status: 'failed', error: 'provider unavailable' },
    },
  }, 30)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.status, 'error')
  assert.equal(updated.live.runId, null)
  assert.equal(updated.live.error, 'provider unavailable')
  assert.equal(updated.live.summary, 'provider unavailable')
  assert.equal(updated.live.lastEventType, 'session.run.failed')
})


test('V3 stream maps session.tool events into live tool state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.tool.started',
      ts_unix_ms: 20,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'tool-1', tool_name: 'bash', call_id: 'call-1', arguments: '{"command":"echo hi"}', step: 1 },
    },
  }, 20)

  let updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolName, 'bash')
  assert.equal(updated.live.sidebarToolName, 'bash')
  assert.equal(updated.live.toolCallId, 'call-1')
  assert.equal(updated.live.toolArguments, '{"command":"echo hi"}')
  assert.equal(updated.live.summary, 'bash')
  assert.equal(updated.live.lastEventType, 'session.tool.started')

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 3,
    event: {
      id: 'v3evt_session-v3_00000000000000000003',
      session_id: 'session-v3',
      seq: 3,
      event_type: 'session.tool.delta',
      ts_unix_ms: 21,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'tool-1', tool_name: 'bash', call_id: 'call-1', output: 'chunk' },
    },
  }, 21)

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolOutput, 'chunk')
  assert.equal(updated.live.sidebarToolName, 'bash')
  assert.equal(updated.live.lastEventType, 'session.tool.delta')

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 4,
    event: {
      id: 'v3evt_session-v3_00000000000000000004',
      session_id: 'session-v3',
      seq: 4,
      event_type: 'session.tool.completed',
      ts_unix_ms: 22,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'tool-1', tool_name: 'bash', call_id: 'call-1', output: 'done', raw_output: 'raw done' },
    },
  }, 22)

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolName, null)
  assert.equal(updated.live.sidebarToolName, 'bash')
  assert.equal(updated.live.retainedToolName, 'bash')
  assert.equal(updated.live.retainedToolOutput, 'raw done')
  assert.equal(updated.live.retainedToolState, 'done')
  assert.equal(updated.live.toolHistory?.length, 1)
  assert.equal(updated.live.toolHistory?.[0]?.callId, 'call-1')
  assert.equal(updated.live.toolHistory?.[0]?.toolInstanceId, 'tool-1')
  assert.equal(updated.live.toolHistory?.[0]?.toolOutput, 'raw done')
  assert.equal(updated.live.lastEventType, 'session.tool.completed')
})

test('V3 stream keeps reused provider call IDs as separate live tool history records', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  const emitToolEvent = (seq: number, eventType: 'session.tool.started' | 'session.tool.completed', step: number, rawOutput: string) => {
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: seq,
      event: {
        id: `v3evt_session-v3_${String(seq).padStart(20, '0')}`,
        session_id: 'session-v3',
        seq,
        event_type: eventType,
        ts_unix_ms: 20 + seq,
        payload: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          step_id: `step-${step}`,
          tool_instance_id: `step-${step}:call-reused`,
          tool_name: 'read',
          call_id: 'call-reused',
          arguments: JSON.stringify({ path: `${step}.txt` }),
          raw_output: rawOutput,
          step,
        },
      },
    }, 20 + seq)
  }

  emitToolEvent(2, 'session.tool.started', 1, '')
  emitToolEvent(3, 'session.tool.completed', 1, 'first')
  emitToolEvent(4, 'session.tool.started', 2, '')
  emitToolEvent(5, 'session.tool.completed', 2, 'second')

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolHistory?.length, 2)
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.callId), ['call-reused', 'call-reused'])
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.toolInstanceId), ['step-2:call-reused', 'step-1:call-reused'])
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.toolOutput), ['second', 'first'])
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.seq), [4, 2])
})

test('V3 stream retains sequence on interleaved assistant segments and live tools', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  const emit = (seq: number, eventType: string, payload: Record<string, unknown>) => {
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: seq,
      event: {
        id: `v3evt_session-v3_${String(seq).padStart(20, '0')}`,
        session_id: 'session-v3',
        seq,
        event_type: eventType,
        ts_unix_ms: 20 + seq,
        payload: { session_id: 'session-v3', run_id: 'run-v3', ...payload },
      },
    }, 20 + seq)
  }

  emit(2, 'session.assistant.delta', { delta: 'SEGMENT A' })
  emit(3, 'session.tool.started', { step_id: 'step-1', tool_instance_id: 'step-1:call-1', tool_name: 'list', call_id: 'call-1', arguments: '{}', step: 1 })
  emit(4, 'session.tool.completed', { step_id: 'step-1', tool_instance_id: 'step-1:call-1', tool_name: 'list', call_id: 'call-1', raw_output: 'first', step: 1 })
  emit(5, 'session.assistant.delta', { delta: 'SEGMENT B' })
  emit(6, 'session.tool.started', { step_id: 'step-2', tool_instance_id: 'step-2:call-2', tool_name: 'list', call_id: 'call-2', arguments: '{}', step: 2 })

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.deepEqual(updated.live.retainedAssistantSegments.map((segment) => [segment.content, segment.seq]), [['SEGMENT A', 2], ['SEGMENT B', 5]])
  assert.deepEqual(updated.live.toolHistory?.map((item) => [item.callId, item.seq]), [['call-2', 6], ['call-1', 3]])
  assert.equal(updated.live.sidebarToolName, 'list')
})

test('V3 assistant draft promotion ignores stale scheduled flushes after tool start', () => {
  const originalWindow = globalThis.window
  const scheduled: Array<() => void> = []
  const canceled = new Set<number>()
  const testWindow = (originalWindow ?? {}) as typeof window
  testWindow.requestAnimationFrame = ((callback: FrameRequestCallback) => {
    const id = scheduled.length + 1
    scheduled.push(() => {
      if (!canceled.has(id)) callback(0)
    })
    return id
  }) as typeof window.requestAnimationFrame
  testWindow.cancelAnimationFrame = ((id: number) => {
    canceled.add(id)
  }) as typeof window.cancelAnimationFrame
  testWindow.setTimeout = ((callback: TimerHandler) => {
    if (typeof callback === 'function') callback()
    return 0
  }) as typeof window.setTimeout
  globalThis.window = testWindow

  try {
    const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
    useDesktopStore.setState(makeState(session), true)

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 2,
      event: {
        id: 'v3evt_session-v3_00000000000000000002',
        session_id: 'session-v3',
        seq: 2,
        event_type: 'session.assistant.delta',
        ts_unix_ms: 20,
        payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'First message' },
      },
    }, 20)

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 3,
      event: {
        id: 'v3evt_session-v3_00000000000000000003',
        session_id: 'session-v3',
        seq: 3,
        event_type: 'session.tool.started',
        ts_unix_ms: 21,
        payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'step-1:call-1', tool_name: 'list', call_id: 'call-1', arguments: '{}', step: 1 },
      },
    }, 21)

    scheduled.forEach((callback) => callback())

    const updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.assistantDraft, '')
    assert.equal(updated.live.retainedAssistantSegments.length, 1)
    assert.equal(updated.live.retainedAssistantSegments[0]?.content, 'First message')
  } finally {
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
})

test('V3 replay control frames update cursor state without V2 resume semantics', () => {
  const session = makeSession({ id: 'session-v3', lastEventSeq: 2, projectionHighWatermarkSeq: 2 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'replay.complete',
    ok: true,
    session_id: 'session-v3',
    last_seq: 4,
    high_watermark_seq: 4,
    next_seq: 4,
  }, 22)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.lastEventSeq, 4)
  assert.equal(updated.projectionHighWatermarkSeq, 4)
  assert.equal(updated.live.lastEventType, 'replay.complete')
  assert.equal(updated.live.awaitingAck, false)
})

test('parent V3 stream child relation frames update canonical child session live state', () => {
  const parent = makeSession({ id: 'parent-v3', sessionApi: 'v3', workspacePath: '/repo', workspaceName: 'repo', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(parent), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('parent-v3', {
    type: 'event',
    ok: true,
    session_id: 'child-v3',
    parent_session_id: 'parent-v3',
    relation: 'child',
    lineage_kind: 'delegated_subagent',
    after_seq: 1,
    last_seq: 1,
    event: {
      id: 'v3evt_child-v3_00000000000000000001',
      session_id: 'child-v3',
      seq: 1,
      event_type: 'session.tool.started',
      ts_unix_ms: 40,
      payload: {
        session_id: 'child-v3',
        run_id: 'child-run-v3',
        step_id: 'child-step-1',
        tool_instance_id: 'child-tool-1',
        tool_name: 'bash',
        call_id: 'child-call-1',
        arguments: '{"command":"echo child"}',
        step: 1,
      },
    },
  }, 41)

  const state = useDesktopStore.getState()
  const parentAfter = state.sessions['parent-v3']
  const child = state.sessions['child-v3']
  assert.equal(parentAfter.lastEventSeq, 1)
  assert.equal(parentAfter.live.lastEventType, null)
  assert.equal(state.lastGlobalSeq, 0)
  assert.equal(child.sessionApi, 'v3')
  assert.equal(child.workspacePath, '/repo')
  assert.equal(child.metadata?.parent_session_id, 'parent-v3')
  assert.equal(child.metadata?.lineage_kind, 'delegated_subagent')
  assert.equal(child.lastEventSeq, 1)
  assert.equal(child.projectionHighWatermarkSeq, 1)
  assert.equal(child.live.runId, 'child-run-v3')
  assert.equal(child.live.status, 'running')
  assert.equal(child.live.toolName, 'bash')
  assert.equal(child.live.toolCallId, 'child-call-1')
  assert.equal(child.live.lastEventType, 'session.tool.started')
})

test('parent V3 task stream card remains renderable while child relation tool frames stream', () => {
  const parent = makeSession({ id: 'parent-v3', sessionApi: 'v3', workspacePath: '/repo', workspaceName: 'repo', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(parent), true)

  const emitParentTaskDelta = (seq: number, currentTool: string, previewText: string) => {
    useDesktopStore.getState().__testApplyRunStreamFrame?.('parent-v3', {
      type: 'event',
      ok: true,
      session_id: 'parent-v3',
      last_seq: seq,
      event: {
        id: `v3evt_parent-v3_${String(seq).padStart(20, '0')}`,
        session_id: 'parent-v3',
        seq,
        event_type: 'session.tool.delta',
        ts_unix_ms: 60 + seq,
        payload: {
          session_id: 'parent-v3',
          run_id: 'parent-run-v3',
          step_id: 'parent-step-1',
          tool_instance_id: 'parent-task-tool-1',
          tool_name: 'task',
          call_id: 'parent-task-call-1',
          output: JSON.stringify({
            tool: 'task',
            path_id: 'tool.task.stream.v1',
            status: 'running',
            phase: 'tool.delta',
            launches: [{
              launch_index: 1,
              child_session_id: 'child-v3',
              subagent: 'explorer',
              status: 'running',
              current_tool: currentTool,
              current_preview_kind: 'tool',
              current_preview_text: previewText,
            }],
          }),
        },
      },
    }, 60 + seq)
  }

  emitParentTaskDelta(2, 'search', 'searching repo')
  useDesktopStore.getState().__testApplyRunStreamFrame?.('parent-v3', {
    type: 'event',
    ok: true,
    session_id: 'child-v3',
    parent_session_id: 'parent-v3',
    relation: 'child',
    lineage_kind: 'delegated_subagent',
    after_seq: 2,
    last_seq: 2,
    event: {
      id: 'v3evt_child-v3_00000000000000000001',
      session_id: 'child-v3',
      seq: 1,
      event_type: 'session.tool.started',
      ts_unix_ms: 70,
      payload: {
        session_id: 'child-v3',
        run_id: 'child-run-v3',
        step_id: 'child-step-1',
        tool_instance_id: 'child-tool-1',
        tool_name: 'bash',
        call_id: 'child-call-1',
        arguments: '{"command":"echo child"}',
        step: 1,
      },
    },
  }, 70)
  emitParentTaskDelta(3, 'read', 'reading file')

  const state = useDesktopStore.getState()
  const parentAfter = state.sessions['parent-v3']
  const child = state.sessions['child-v3']
  assert.equal(parentAfter.live.toolName, 'task')
  assert.equal(parentAfter.live.toolCallId, 'parent-task-call-1')
  assert.equal(child.live.toolName, 'bash')

  const taskHistory = parentAfter.live.toolHistory?.find((entry) => entry.toolName === 'task')
  assert.ok(taskHistory, 'expected parent task live tool history to remain present')
  assert.doesNotThrow(() => JSON.parse(taskHistory.toolOutput), 'parent task stream output must remain a single JSON payload')
  const toolMessage = buildStructuredToolMessage({
    tool: 'task',
    callId: taskHistory.callId,
    toolInstanceId: taskHistory.toolInstanceId,
    outputText: taskHistory.toolOutput,
    state: 'running',
  })
  assert.equal(toolMessage?.taskRows.length, 1)
  assert.equal(toolMessage?.taskRows[0]?.childSessionId, 'child-v3')
  assert.equal(toolMessage?.taskRows[0]?.tool, 'read')
  assert.equal(toolMessage?.taskRows[0]?.previewText, 'reading file')
})


test('parent V3 stream child cursor errors do not refetch or poison parent session state', async () => {
  const originalWindow = globalThis.window
  const originalFetch = globalThis.fetch
  const fetchCalls: Array<RequestInfo | URL> = []
  globalThis.window = {
    ...(originalWindow ?? {}),
    setTimeout: ((callback: TimerHandler) => {
      if (typeof callback === 'function') callback()
      return 0
    }) as typeof window.setTimeout,
  } as Window & typeof globalThis
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    fetchCalls.push(input)
    return new Response(JSON.stringify({ ok: false }), { status: 500 })
  }) as typeof fetch

  try {
    const parent = makeSession({ id: 'parent-v3', sessionApi: 'v3', workspacePath: '/repo', workspaceName: 'repo', lastEventSeq: 7, projectionHighWatermarkSeq: 7 })
    const child = makeSession({ id: 'child-v3', sessionApi: 'v3', workspacePath: '/repo', workspaceName: 'repo', lastEventSeq: 2, projectionHighWatermarkSeq: 2 })
    useDesktopStore.setState({ ...makeState(parent), sessions: { [parent.id]: parent, [child.id]: child } }, true)

    useDesktopStore.getState().__testApplyRunStreamFrame?.('parent-v3', {
      type: 'cursor.error',
      ok: false,
      session_id: 'child-v3',
      parent_session_id: 'parent-v3',
      relation: 'child',
      after_seq: 7,
      next_seq: 5,
      error: 'child event sequence gap at 5, want 3; child refetch required',
    }, 50)
    await new Promise((resolve) => setImmediate(resolve))

    const state = useDesktopStore.getState()
    assert.equal(state.sessions['parent-v3'].lastEventSeq, 7)
    assert.equal(state.sessions['parent-v3'].live.lastEventType, null)
    assert.equal(state.sessions['child-v3'].lastEventSeq, 2)
    assert.deepEqual(fetchCalls.map(String), [])
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
})

test('desktop V3 realtime applies canonical event frames once and ignores diagnostic provider deltas', () => {
  const sessionId = 'session-dump-shape'
  const runId = 'v3run-dump-shape'
  const session = makeSession({ id: sessionId, sessionApi: 'v3', workspacePath: '/repo', workspaceName: 'repo' })
  useDesktopStore.setState(makeState(session), true)
  mergeDesktopSnapshot({
    rev: getDesktopSnapshot().rev + 1,
    sessionsById: { [sessionId]: session },
    sessionOrder: [sessionId],
    messagesBySessionId: { [sessionId]: [] },
  })

  const eventFrame = (endpointCursor: string, seq: number, eventType: string, payload: Record<string, unknown>) => ({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    type: 'event',
    session_id: sessionId,
    endpoint_cursor: endpointCursor,
    last_seq: seq,
    high_watermark_seq: seq,
    event_type: eventType,
    event: {
      id: `v3evt_${sessionId}_${String(seq).padStart(20, '0')}`,
      session_id: sessionId,
      seq,
      event_type: eventType,
      ts_unix_ms: 1000 + seq,
      payload: { session_id: sessionId, seq, ...payload },
    },
  })

  const apply = (endpointCursor: string, seq: number, eventType: string, payload: Record<string, unknown>) => {
    useDesktopStore.getState().__testApplyV3RealtimeFrame?.(sessionId, eventFrame(endpointCursor, seq, eventType, payload), 1000 + seq)
  }

  apply('cursor-25531', 2, 'session.diagnostic.provider.stream', {
    diagnostic: true,
    payload: { type: 'response.output_text.delta', delta: 'DIAGNOSTIC-TEXT-SHOULD-NOT-RENDER' },
    run_id: runId,
  })
  apply('cursor-25536', 7, 'session.assistant.started', {
    kind: 'run_intent.record',
    run_id: runId,
    run_intent: { session_id: sessionId, run_id: runId, status: 'running', created_at: 1007, updated_at: 1007, event_seq: 7 },
  })
  apply('cursor-25636', 106, 'session.assistant.delta', { run_id: runId, delta: 'Hey! 👋\n\n' })
  apply('cursor-25650', 120, 'session.assistant.delta', { run_id: runId, delta: "I'm your Swarm coding assistant, ready to help" })
  apply('cursor-25664', 134, 'session.assistant.delta', { run_id: runId, delta: " with whatever you're working on in the `swarm" })
  apply('cursor-25682', 152, 'session.assistant.delta', { run_id: runId, delta: '-go` workspace. What can I do for you today?' })

  let live = useDesktopStore.getState().sessions[sessionId].live
  const expected = "Hey! 👋\n\nI'm your Swarm coding assistant, ready to help with whatever you're working on in the `swarm-go` workspace. What can I do for you today?"
  assert.equal(live.assistantDraft, expected)
  assert.equal(live.assistantDraft.includes('DIAGNOSTIC-TEXT-SHOULD-NOT-RENDER'), false)

  apply('cursor-25693', 163, 'session.assistant.completed', {
    kind: 'message.append',
    status: 'completed',
    run_id: runId,
    message: { id: 'msg-assistant-final', session_id: sessionId, global_seq: 163, role: 'assistant', content: expected, created_at: 1163 },
    run_intent: { session_id: sessionId, run_id: runId, status: 'completed', updated_at: 1163, event_seq: 163 },
  })

  live = useDesktopStore.getState().sessions[sessionId].live
  assert.equal(live.assistantDraft, '')
  assert.equal(live.status, 'idle')

  const canonicalMessages = getDesktopSnapshot().messagesBySessionId[sessionId] ?? []
  assert.equal(canonicalMessages.filter((message) => message.role === 'assistant' && message.content === expected).length, 1)
  assert.equal(canonicalMessages.some((message) => message.content.includes('DIAGNOSTIC-TEXT-SHOULD-NOT-RENDER')), false)
  assert.equal(useDesktopStore.getState().sessions[sessionId].lastEventSeq, 163)
})

test('desktop V3 realtime controller consumes backend stream frames and renders assistant output once', async () => {
  const websocketURLs: string[] = []
  const sent: Array<{ url: string; message: Record<string, unknown> }> = []
  const sockets: FakeRealtimeSocket[] = []
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  class FakeRealtimeSocket extends EventTarget {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeRealtimeSocket.OPEN
    url: string

    constructor(input: string | URL) {
      super()
      this.url = String(input)
      websocketURLs.push(this.url)
      sockets.push(this)
    }

    close() {
      this.readyState = FakeRealtimeSocket.CLOSED
      this.dispatchEvent(new Event('close'))
    }

    send(payload: string) {
      sent.push({ url: this.url, message: JSON.parse(payload) as Record<string, unknown> })
    }

    serverMessage(payload: Record<string, unknown>) {
      const event = new Event('message') as MessageEvent
      Object.defineProperty(event, 'data', { value: JSON.stringify(payload) })
      this.dispatchEvent(event)
    }
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    addEventListener() {},
    dispatchEvent: (() => true) as typeof window.dispatchEvent,
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeRealtimeSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/sessions:workset') {
      return new Response(JSON.stringify({
        rev: getDesktopSnapshot().rev + 1,
        snapshot_endpoint_cursor: 'cursor-31005',
        sessions_by_id: {},
        messages_by_session: {},
        permissions_by_session: {},
        session_order: [],
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const sessionId = 'session-v3-live-stream'
    const runId = 'run-v3-live-stream'
    const session = makeSession({ id: sessionId, sessionApi: 'v3', workspacePath: '/repo', workspaceName: 'repo' })
    useDesktopStore.setState(makeState(session), true)
    mergeDesktopSnapshot({
      rev: getDesktopSnapshot().rev + 1,
      sessionsById: { [sessionId]: session },
      sessionOrder: [sessionId],
      messagesBySessionId: { [sessionId]: [] },
    })

    await useDesktopStore.getState().ensureRunStream(sessionId)
    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))

    const v3Socket = sockets.find((socket) => socket.url.includes('/v3/realtime/stream'))
    assert.ok(v3Socket, `expected canonical V3 realtime socket, got ${websocketURLs.join(', ')}`)
    assert.equal(websocketURLs.some((url) => /\/v3\/sessions\/[^/]+\/stream/.test(url)), false)

    const subscribeMessages = sent
      .filter((entry) => entry.url === v3Socket.url)
      .map((entry) => entry.message)
      .filter((message) => message.kind === 'subscribe.session')
    assert.equal(subscribeMessages.length, 1)
    assert.equal(subscribeMessages[0]?.session_id, sessionId)
    assert.equal('after_seq' in (subscribeMessages[0] ?? {}), false)
    assert.equal('after_rev' in (subscribeMessages[0] ?? {}), false)
    assert.equal(sent.some((entry) => entry.message.type === 'subscribe' && entry.message.channel === 'session:*'), false)

    const eventFrame = (endpointCursor: string, seq: number, eventType: string, payload: Record<string, unknown>) => ({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'event',
      type: 'event',
      session_id: sessionId,
      endpoint_cursor: endpointCursor,
      last_seq: seq,
      high_watermark_seq: seq,
      event_type: eventType,
      event: {
        id: `v3evt_${sessionId}_${String(seq).padStart(20, '0')}`,
        session_id: sessionId,
        seq,
        event_type: eventType,
        ts_unix_ms: 2000 + seq,
        payload: { session_id: sessionId, run_id: runId, ...payload },
      },
    })

    v3Socket.serverMessage(eventFrame('cursor-31001', 2, 'session.diagnostic.provider.stream', {
      diagnostic: true,
      payload: { type: 'response.output_text.delta', delta: 'DIAGNOSTIC-SHOULD-NOT-RENDER' },
    }))
    v3Socket.serverMessage(eventFrame('cursor-31002', 3, 'session.assistant.started', { status: 'running' }))
    v3Socket.serverMessage(eventFrame('cursor-31003', 4, 'session.assistant.delta', { delta: 'hello ' }))
    v3Socket.serverMessage(eventFrame('cursor-31004', 5, 'session.assistant.delta', { delta: 'world' }))

    let live = useDesktopStore.getState().sessions[sessionId].live
    assert.equal(live.assistantDraft, 'hello world')
    assert.equal(live.assistantDraft.includes('DIAGNOSTIC-SHOULD-NOT-RENDER'), false)
    assert.equal(live.status, 'running')

    v3Socket.serverMessage(eventFrame('cursor-31005', 6, 'session.assistant.completed', {
      status: 'completed',
      message: { id: 'msg-v3-live-stream-assistant', session_id: sessionId, global_seq: 6, role: 'assistant', content: 'hello world', created_at: 2006 },
      run_intent: { session_id: sessionId, run_id: runId, status: 'completed', created_at: 2003, updated_at: 2006, event_seq: 6 },
    }))

    live = useDesktopStore.getState().sessions[sessionId].live
    assert.equal(live.assistantDraft, '')
    assert.equal(live.status, 'idle')
    assert.equal(live.runId, null)
    assert.equal(useDesktopStore.getState().sessions[sessionId].lastEventSeq, 6)

    const canonicalMessages = getDesktopSnapshot().messagesBySessionId[sessionId] ?? []
    assert.equal(canonicalMessages.filter((message) => message.role === 'assistant' && message.content === 'hello world').length, 1)
    assert.equal(canonicalMessages.some((message) => message.content.includes('DIAGNOSTIC-SHOULD-NOT-RENDER')), false)

    useDesktopStore.getState().syncV3RealtimeSessions()
    await new Promise((resolve) => setImmediate(resolve))
    const subscribeAfterSync = sent
      .filter((entry) => entry.url === v3Socket.url)
      .map((entry) => entry.message)
      .filter((message) => message.kind === 'subscribe.session')
    assert.equal(subscribeAfterSync.length, 1, 'store sync must not duplicate V3 realtime subscriptions')
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})

test('desktop V3 realtime subscribes from active top-level snapshot run intent when session is temporarily idle', async () => {
  const sent: Array<{ url: string; message: Record<string, unknown> }> = []
  const websocketURLs: string[] = []
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  class FakeWebSocket extends EventTarget {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeWebSocket.OPEN
    url: string

    constructor(input: string | URL) {
      super()
      this.url = String(input)
      websocketURLs.push(this.url)
      queueMicrotask(() => this.dispatchEvent(new Event('open')))
    }

    close() {
      this.readyState = FakeWebSocket.CLOSED
      this.dispatchEvent(new Event('close'))
    }

    send(payload: string) {
      sent.push({ url: this.url, message: JSON.parse(payload) as Record<string, unknown> })
    }
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
      removeItem: () => undefined,
    },
    addEventListener() {},
    dispatchEvent: (() => true) as typeof window.dispatchEvent,
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v3/sessions:workset') {
      return new Response(JSON.stringify({ rev: getDesktopSnapshot().rev + 1, sessions_by_id: {}, session_order: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const sessionId = 'session-top-level-intent-subscribe'
    mergeDesktopSnapshot({
      rev: getDesktopSnapshot().rev + 1,
      snapshotEndpointCursor: 'cursor-500',
      runIntentsBySessionId: {
        [sessionId]: {
          sessionId,
          runId: 'run-top-level-intent-subscribe',
          status: 'running',
          blockedReason: '',
          createdAt: 10,
          updatedAt: 11,
          eventSeq: 2,
        },
      },
    })
    useDesktopStore.setState({
      sessions: {
        [sessionId]: makeSession({ id: sessionId, sessionApi: 'v3', live: emptyLiveState(), runIntent: null }),
      },
      realtimeDesired: false,
      connectionState: 'idle',
    })

    useDesktopStore.getState().syncV3RealtimeSessions({ force: true })
    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))

    const v3Socket = websocketURLs.find((url) => url.includes('/v3/realtime/stream'))
    assert.ok(v3Socket, `expected canonical V3 realtime socket, got ${websocketURLs.join(', ')}`)
    assert.equal(sent.some((entry) => entry.message.kind === 'subscribe.session' && entry.message.session_id === sessionId), true)
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})

test('desktop V3 realtime ignores persisted localStorage subscription intent authority', async () => {
  const sent: Array<{ url: string; message: Record<string, unknown> }> = []
  const websocketURLs: string[] = []
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  const localStorageData = new Map<string, string>()
  class FakeWebSocket extends EventTarget {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeWebSocket.OPEN
    url: string

    constructor(input: string | URL) {
      super()
      this.url = String(input)
      websocketURLs.push(this.url)
      queueMicrotask(() => this.dispatchEvent(new Event('open')))
    }

    close() {
      this.readyState = FakeWebSocket.CLOSED
      this.dispatchEvent(new Event('close'))
    }

    send(payload: string) {
      sent.push({ url: this.url, message: JSON.parse(payload) as Record<string, unknown> })
    }
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    localStorage: {
      getItem: (key: string) => localStorageData.get(key) ?? null,
      setItem: (key: string, value: string) => { localStorageData.set(key, value) },
      removeItem: (key: string) => { localStorageData.delete(key) },
    },
    addEventListener() {},
    dispatchEvent: (() => true) as typeof window.dispatchEvent,
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v3/sessions:workset') {
      return new Response(JSON.stringify({
        rev: getDesktopSnapshot().rev + 1,
        snapshot_endpoint_cursor: 'cursor-31005',
        sessions_by_id: {},
        session_order: [],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const sessionId = 'session-refresh-resync'
    const session = makeSession({
      id: sessionId,
      sessionApi: 'v3',
      lifecycle: {
        sessionId,
        runId: 'run-refresh-resync',
        active: true,
        phase: 'running',
        startedAt: 10,
        endedAt: 0,
        updatedAt: 11,
        generation: 1,
        stopReason: null,
        error: null,
        ownerTransport: null,
      },
      live: { ...emptyLiveState(), runId: 'run-refresh-resync', status: 'running' },
    })
    useDesktopStore.setState(makeState(session), true)
    useDesktopStore.getState().ensureRunStream(sessionId)
    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(sent.some((entry) => entry.message.kind === 'subscribe.session' && entry.message.session_id === sessionId), true)

    const socketCountBeforeRefresh = websocketURLs.length
    useDesktopStore.setState({
      sessions: {},
      realtimeDesired: false,
      connectionState: 'idle',
    })
    useDesktopStore.getState().syncV3RealtimeSessions({ force: true })
    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))

    const subscribeMessages = sent
      .map((entry) => entry.message)
      .filter((message) => message.kind === 'subscribe.session' && message.session_id === sessionId)
    assert.equal(subscribeMessages.length, 1, `localStorage subscription intent must not create refreshed resubscribe: ${JSON.stringify(sent)}`)
    assert.equal(websocketURLs.length, socketCountBeforeRefresh, 'localStorage subscription intent must not open a replacement V3 socket')
    assert.equal(websocketURLs.some((url) => url.includes('/v3/realtime/stream')), true)
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})

test('desktop V3 canonical session updates use /v3/realtime/stream and leave run stream compatibility off', async () => {
  const { readFile } = await import('node:fs/promises')
  const controllerSource = await readFile(new URL('./run-stream-controller.ts', import.meta.url), 'utf8')
  const querySource = await readFile(new URL('../chat/queries/chat-queries.ts', import.meta.url), 'utf8')
  const panelSource = await readFile(new URL('../chat/components/desktop-chat-panel.tsx', import.meta.url), 'utf8')
  const storeSource = await readFile(new URL('./desktop-ui-store.ts', import.meta.url), 'utf8')

  assert.match(querySource, /\/v3\/sessions\/\$\{encodeURIComponent\(normalizedSessionId\)\}\/stream/)
  assert.match(storeSource, /DesktopV3RealtimeController/)
  assert.match(storeSource, /requireV3RealtimeController/)
  assert.match(storeSource, /applyDesktopV3RealtimeFrame/)
  assert.match(storeSource, /subscribeDesktopV3RealtimeSession\(targetSessionId, desktopV3RealtimeEndpointCursor\)/)
  assert.match(storeSource, /syncV3RealtimeSessions/)
  assert.match(panelSource, /liveSession\?\.sessionApi\?\.trim\(\)\.toLowerCase\(\) === 'v3'/)
  assert.match(panelSource, /session\.tool\.started/)
  assert.match(panelSource, /session\.tool\.delta/)
  assert.match(panelSource, /session\.tool\.completed/)
  assert.doesNotMatch(controllerSource, /\/v3\/sessions\/[^`]+\/stream/)
  assert.doesNotMatch(querySource, /\/v3\/sessions\/[^`]+\/run\/stream/)
})

test('desktop store submitPrompt for V3 primary sessions commits through Sessions API v3 and subscribes to canonical realtime', async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const websocketURLs: string[] = []
  let websocketCloseCount = 0
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  class FakeWebSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeWebSocket.OPEN
    url: string

    constructor(input: string | URL) {
      this.url = String(input)
      websocketURLs.push(this.url)
    }

    addEventListener(type: string, callback: EventListenerOrEventListenerObject) {
      if (type === 'open') {
        queueMicrotask(() => {
          if (typeof callback === 'function') {
            callback(new Event('open'))
          } else {
            callback.handleEvent(new Event('open'))
          }
        })
      }
    }
    close() {
      websocketCloseCount += 1
      this.readyState = FakeWebSocket.CLOSED
    }
    send() {}
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    addEventListener() {},
    dispatchEvent: (() => true) as typeof window.dispatchEvent,
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/sessions/session-v3/run/stop') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/sessions:workset') {
      return new Response(JSON.stringify({
        rev: getDesktopSnapshot().rev + 1,
        snapshot_endpoint_cursor: 'cursor-4',
        sessions_by_id: {},
        session_order: [],
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url !== '/v3/sessions/session-v3/messages') {
      throw new Error(`unexpected fetch: ${url}`)
    }
    return new Response(JSON.stringify({
      ok: true,
      session: {
        id: 'session-v3',
        title: 'V3 session',
        workspace_path: '/repo',
        workspace_name: 'repo',
        mode: 'auto',
        session_api: 'v3',
        message_count: 1,
        updated_at: 20,
        created_at: 1,
      },
      projection: {
        session_id: 'session-v3',
        last_event_seq: 3,
        projection_high_watermark_seq: 3,
        updated_at: 20,
      },
      message: {
        id: 'msg-v3-submit',
        session_id: 'session-v3',
        global_seq: 2,
        role: 'user',
        content: 'hello primary',
        created_at: 19,
      },
      run_intent: {
        session_id: 'session-v3',
        run_id: 'v3run-session-v3-2',
        status: 'pending_executor',
        event_seq: 3,
        created_at: 20,
        updated_at: 20,
      },
      messages: [],
      events: [],
      realtime_outbox: {
        endpoint_seq: 4,
        endpoint_cursor: 'cursor-4',
        session_id: 'session-v3',
      },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
    useDesktopStore.setState(makeState(session), true)

    await useDesktopStore.getState().submitPrompt({
      sessionId: 'session-v3',
      sessionApi: 'v3',
      clientRequestId: 'desktop-v3-message:test-submit',
      workspacePath: '/repo',
      workspaceName: 'repo',
      prompt: 'hello primary',
      agentName: 'swarm',
    })

    const urls = calls.map((entry) => String(entry.input)).sort()
    assert.deepEqual(urls, ['/v3/sessions/session-v3/messages', '/v3/sessions:workset', '/v1/auth/desktop/session', '/v1/auth/desktop/session'].sort())
    assert.equal(urls.some((url) => url.startsWith('/v1/swarm/managed-hosts/sessions')), false)
    assert.equal(urls.some((url) => url.startsWith('/v2/sessions')), false)
    assert.deepEqual(websocketURLs.sort(), ['ws://127.0.0.1:7777/v3/realtime/stream?endpoint_cursor=cursor-4', 'ws://127.0.0.1:7777/ws'].sort())
    const body = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.deepEqual(body, {
      client_request_id: 'desktop-v3-message:test-submit',
      role: 'user',
      content: 'hello primary',
    })

    let updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.sessionApi, 'v3')
    assert.equal(updated.lastEventSeq, 3)
    assert.equal(updated.projectionHighWatermarkSeq, 3)
    assert.equal(updated.live.runId, 'v3run-session-v3-2')
    assert.equal(updated.live.status, 'starting')
    assert.equal(updated.live.startedAt, 20)
    assert.equal(updated.live.lastEventType, 'run.pending_executor')

    await useDesktopStore.getState().stopRun('session-v3', primaryRoute)
    const stopCall = calls.find((entry) => String(entry.input) === '/v3/sessions/session-v3/run/stop')
    assert.ok(stopCall)
    assert.deepEqual(JSON.parse(String(stopCall.init?.body ?? '{}')), { type: 'run.stop', run_id: 'v3run-session-v3-2', target_swarm_id: 'primary-swarm' })
    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.status, 'starting')
    assert.equal(updated.live.runId, 'v3run-session-v3-2')
    assert.equal(websocketCloseCount, 0)
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})

test('V3 stop sends request even when no run id is hydrated so backend returns the stop error', async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v3/sessions/session-v3/run/stop') {
      return new Response(JSON.stringify({ error: 'run_id is required' }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
    useDesktopStore.setState(makeState(session), true)

    await assert.rejects(
      () => useDesktopStore.getState().stopRun('session-v3', primaryRoute),
      /run_id is required/,
    )

    const stopCall = calls.find((entry) => String(entry.input) === '/v3/sessions/session-v3/run/stop')
    assert.ok(stopCall)
    assert.deepEqual(JSON.parse(String(stopCall.init?.body ?? '{}')), { type: 'run.stop', run_id: '', target_swarm_id: 'primary-swarm' })
    assert.equal(calls.length, 1)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('V3 stop sends explicit run id from caller when store state has no hydrated run id', async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v3/sessions/session-v3/run/stop') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
    useDesktopStore.setState(makeState(session), true)

    await useDesktopStore.getState().stopRun('session-v3', primaryRoute, 'run-from-stop-button')

    const stopCall = calls.find((entry) => String(entry.input) === '/v3/sessions/session-v3/run/stop')
    assert.ok(stopCall)
    assert.deepEqual(JSON.parse(String(stopCall.init?.body ?? '{}')), { type: 'run.stop', run_id: 'run-from-stop-button', target_swarm_id: 'primary-swarm' })
    assert.equal(calls.length, 1)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('V3 stop resolves active lifecycle run id when live run id is not hydrated', async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v3/sessions/session-v3/run/stop') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const session = makeSession({
      id: 'session-v3',
      sessionApi: 'v3',
      lifecycle: {
        sessionId: 'session-v3',
        runId: 'run-lifecycle-only',
        active: true,
        phase: 'running',
        startedAt: 10,
        endedAt: 0,
        updatedAt: 20,
        generation: 1,
        stopReason: null,
        error: null,
        ownerTransport: 'background_api',
      },
      live: { ...emptyLiveState(), status: 'running', startedAt: 10, runId: null },
    })
    useDesktopStore.setState(makeState(session), true)

    await useDesktopStore.getState().stopRun('session-v3', primaryRoute)

    const stopCall = calls.find((entry) => String(entry.input) === '/v3/sessions/session-v3/run/stop')
    assert.ok(stopCall)
    assert.deepEqual(JSON.parse(String(stopCall.init?.body ?? '{}')), { type: 'run.stop', run_id: 'run-lifecycle-only', target_swarm_id: 'primary-swarm' })
    assert.equal(calls.length, 1)
  } finally {
    globalThis.fetch = originalFetch
  }
})

// Keep the V3 stream path compatible with existing session envelope handling.
test('global /ws V3 session title envelope updates Desktop title from canonical payload', () => {
  const session = makeSession({ id: 'session-v3', title: 'New chat', sessionApi: 'v3' })
  const patch = applyEnvelope(makeState(session), {
    global_seq: 10,
    stream: 'session:session-v3',
    event_type: 'session.title.updated',
    entity_id: 'session-v3',
    ts_unix_ms: 100,
    payload: {
      session_id: 'session-v3',
      title: 'Generated title',
      updated_at: 100,
      session: {
        id: 'session-v3',
        title: 'Generated title',
        workspace_path: '/repo',
        workspace_name: 'repo',
        mode: 'auto',
        session_api: 'v3',
        message_count: 1,
        created_at: 1,
        updated_at: 100,
      },
    },
  })

  const updated = patch.sessions?.['session-v3']
  assert.equal(updated?.title, 'Generated title')
  assert.equal(updated?.sessionApi, 'v3')
  assert.equal(updated?.live.lastEventType, null)
  assert.equal(patch.lastGlobalSeq, 10)
})

test('global /ws V3 message and run-intent envelopes update Desktop canonical state', async () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 11,
    stream: 'session:session-v3',
    event_type: 'session.message.appended',
    entity_id: 'session-v3',
    ts_unix_ms: 101,
    payload: {
      session_id: 'session-v3',
      message: {
        id: 'msg-user-v3',
        session_id: 'session-v3',
        global_seq: 11,
        role: 'user',
        content: 'hello global',
        created_at: 101,
      },
    },
  }))
  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 12,
    stream: 'session:session-v3',
    event_type: 'session.run_intent.recorded',
    entity_id: 'session-v3',
    ts_unix_ms: 102,
    payload: {
      session_id: 'session-v3',
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'pending_executor',
        event_seq: 12,
        created_at: 102,
        updated_at: 102,
      },
    },
  }))

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.messageCount, 1)
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.status, 'starting')
  assert.equal(updated.live.summary, 'Pending executor…')
  assert.equal(updated.live.lastEventType, 'session.run_intent.recorded')
  assert.equal(useDesktopStore.getState().lastGlobalSeq, 12)
  await new Promise((resolve) => setTimeout(resolve, 0))

})

test('global /ws V3 assistant lifecycle envelopes update Desktop live and message state', () => {
  const originalWindow = globalThis.window
  const testWindow = originalWindow ?? {} as typeof window
  testWindow.setTimeout = ((callback: TimerHandler) => {
    if (typeof callback === 'function') callback()
    return 0
  }) as typeof window.setTimeout
  globalThis.window = testWindow
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  try {
    useDesktopStore.setState((state) => applyEnvelope(state, {
      global_seq: 13,
      stream: 'session:session-v3',
      event_type: 'session.assistant.delta',
      entity_id: 'session-v3',
      ts_unix_ms: 103,
      payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'hi' },
    }))
    let updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.assistantDraft, 'hi')
    assert.equal(updated.live.status, 'running')
    assert.equal(updated.live.lastEventType, 'session.assistant.delta')

    useDesktopStore.setState((state) => applyEnvelope(state, {
      global_seq: 14,
      stream: 'session:session-v3',
      event_type: 'session.assistant.completed',
      entity_id: 'session-v3',
      ts_unix_ms: 104,
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        message: {
          id: 'msg-assistant-v3',
          session_id: 'session-v3',
          global_seq: 14,
          role: 'assistant',
          content: 'hi',
          created_at: 104,
        },
        run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'completed' },
      },
    }))

    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.status, 'idle')
    assert.equal(updated.live.runId, null)
    assert.equal(updated.live.assistantDraft, '')
    assert.equal(updated.live.lastEventType, 'session.assistant.completed')
    assert.equal(updated.messageCount, 1)
    assert.equal(useDesktopStore.getState().lastGlobalSeq, 14)
  } finally {
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
})

test('global /ws V3 permission envelopes hydrate canonical modal state without polling', () => {
  const session = makeSession({
    id: 'session-v3',
    sessionApi: 'v3',
    live: { ...emptyLiveState(), status: 'running', runId: 'run-v3', startedAt: 100 },
  })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 15,
    stream: 'session:session-v3',
    event_type: 'permission.requested',
    entity_id: 'session-v3',
    ts_unix_ms: 105,
    payload: {
      session_id: 'session-v3',
      permission: {
        id: 'perm-v3-stream',
        session_id: 'session-v3',
        run_id: 'run-v3',
        call_id: 'call-v3',
        tool_name: 'bash',
        tool_arguments: '{"command":"git status"}',
        status: 'pending',
        requirement: 'tool_call',
        mode: 'auto',
        created_at: 105,
        updated_at: 105,
        permission_requested_at: 105,
      },
    },
  }))

  let updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.sessionApi, 'v3')
  assert.equal(updated.permissionsHydrated, true)
  assert.equal(updated.pendingPermissions.length, 1)
  assert.equal(updated.pendingPermissionCount, 1)
  assert.equal(updated.live.status, 'blocked')
  assert.equal(updated.live.lastEventType, 'permission.requested')
  assert.equal(updated.live.lastEventAt, 105)
  assert.equal(useDesktopStore.getState().lastGlobalSeq, 15)

  const activePermission = updated.permissionsHydrated
    ? updated.pendingPermissions.find((permission) => permission.status === 'pending' && permissionRequiresApproval(permission, updated.mode)) ?? null
    : null
  assert.ok(activePermission, 'expected the canonical store state to make DesktopPermissionModal open')
  assert.equal(activePermission.id, 'perm-v3-stream')

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 16,
    stream: 'session:session-v3',
    event_type: 'permission.updated',
    entity_id: 'session-v3',
    ts_unix_ms: 106,
    payload: {
      session_id: 'session-v3',
      permission: {
        id: 'perm-v3-stream',
        session_id: 'session-v3',
        run_id: 'run-v3',
        call_id: 'call-v3',
        tool_name: 'bash',
        tool_arguments: '{"command":"git status"}',
        status: 'approved',
        decision: 'approve',
        requirement: 'tool_call',
        mode: 'auto',
        created_at: 105,
        updated_at: 106,
        resolved_at: 106,
        permission_requested_at: 105,
      },
    },
  }))

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.permissionsHydrated, true)
  assert.deepEqual(updated.pendingPermissions, [])
  assert.equal(updated.pendingPermissionCount, 0)
  assert.equal(updated.live.status, 'running')
  assert.equal(updated.live.lastEventType, 'permission.updated')
  assert.equal(updated.live.lastEventAt, 106)
  assert.equal(useDesktopStore.getState().lastGlobalSeq, 16)
})


test('global /ws V3 interleaved live timeline uses session source sequence instead of global stream sequence', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  const emit = (globalSeq: number, sourceSeq: number, eventType: string, payload: Record<string, unknown>) => {
    useDesktopStore.setState((state) => applyEnvelope(state, {
      global_seq: globalSeq,
      source_seq: sourceSeq,
      stream: 'session:session-v3',
      event_type: eventType,
      entity_id: 'session-v3',
      ts_unix_ms: globalSeq,
      payload: { session_id: 'session-v3', run_id: 'run-v3', ...payload },
    }))
  }

  emit(1960, 15, 'session.assistant.delta', { delta: 'HELLO' })
  emit(1961, 16, 'session.message.appended', {
    message: { id: 'msg-assistant-1', session_id: 'session-v3', global_seq: 16, role: 'assistant', content: 'HELLO', created_at: 1961 },
  })
  emit(1962, 17, 'session.tool.started', { tool_name: 'list', call_id: 'call-list', step_id: 'step-1', tool_instance_id: 'step-1:call-list', arguments: '{}', step: 1 })
  emit(1963, 18, 'session.tool.completed', { tool_name: 'list', call_id: 'call-list', step_id: 'step-1', tool_instance_id: 'step-1:call-list', output: 'listed', raw_output: 'listed', step: 1 })
  emit(1964, 19, 'session.assistant.delta', { delta: 'SENTENCE TWO' })
  emit(1965, 20, 'session.message.appended', {
    message: { id: 'msg-assistant-2', session_id: 'session-v3', global_seq: 20, role: 'assistant', content: 'SENTENCE TWO', created_at: 1965 },
  })
  emit(1966, 21, 'session.tool.started', { tool_name: 'read', call_id: 'call-read', step_id: 'step-2', tool_instance_id: 'step-2:call-read', arguments: '{}', step: 2 })

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.deepEqual(updated.live.toolHistory.map((item) => [item.callId, item.seq]), [['call-read', 21], ['call-list', 17]])
  assert.equal(updated.live.seq, 21)
  assert.equal(useDesktopStore.getState().lastGlobalSeq, 1966)
})

test('global /ws V3 lifecycle and tool envelopes update Desktop canonical live state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 15,
    stream: 'session:session-v3',
    event_type: 'session.lifecycle.updated',
    entity_id: 'session-v3',
    ts_unix_ms: 105,
    payload: {
      session_id: 'session-v3',
      lifecycle: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        active: true,
        status: 'running',
        started_at: 105,
        updated_at: 105,
      },
    },
  }))
  let updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.lifecycle?.active, true)
  assert.equal(updated.live.status, 'running')
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.lastEventType, 'session.lifecycle.updated')

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 16,
    stream: 'session:session-v3',
    event_type: 'session.tool.started',
    entity_id: 'session-v3',
    ts_unix_ms: 106,
    payload: {
      session_id: 'session-v3',
      run_id: 'run-v3',
      tool_name: 'read',
      call_id: 'call-v3',
      step_id: 'step-v3',
      tool_instance_id: 'tool-v3',
      arguments: '{"path":"README.md"}',
      step: 1,
    },
  }))
  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 17,
    stream: 'session:session-v3',
    event_type: 'session.tool.completed',
    entity_id: 'session-v3',
    ts_unix_ms: 107,
    payload: {
      session_id: 'session-v3',
      run_id: 'run-v3',
      tool_name: 'read',
      call_id: 'call-v3',
      step_id: 'step-v3',
      tool_instance_id: 'tool-v3',
      output: 'done',
      raw_output: 'done',
      step: 1,
    },
  }))

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.retainedToolName, 'read')
  assert.equal(updated.live.retainedToolCallId, 'call-v3')
  assert.equal(updated.live.retainedToolOutput, 'done')
  assert.equal(updated.live.toolHistory.length, 1)
  assert.equal(updated.live.toolHistory[0]?.toolInstanceId, 'tool-v3')
  assert.equal(updated.live.toolHistory[0]?.state, 'done')
  assert.equal(updated.live.lastEventType, 'session.tool.completed')
  assert.equal(useDesktopStore.getState().lastGlobalSeq, 17)
})

test('V3 session.created payload nesting maps through applyEnvelope', () => {
  const patch = applyEnvelope({ ...useDesktopStore.getState(), sessions: {}, lastGlobalSeq: 0 }, {
    event_type: 'session.created',
    entity_id: 'session-created',
    ts_unix_ms: 1,
    payload: {
      id: 'session-created',
      session_id: 'session-created',
      title: 'created',
      workspace_path: '/repo',
      workspace_name: 'repo',
      mode: 'auto',
      session_api: 'v3',
      created_at: 1,
      updated_at: 1,
    },
  })
  assert.equal(patch.sessions?.['session-created']?.workspacePath, '/repo')
})

test('V3 ensureRunStream opens canonical realtime stream and keeps global /ws session wildcard off', async () => {
  const websocketURLs: string[] = []
  const sent: Array<Record<string, unknown>> = []
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  class FakeWebSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeWebSocket.OPEN
    url: string

    constructor(input: string | URL) {
      this.url = String(input)
      websocketURLs.push(this.url)
    }

    addEventListener(type: string, callback: EventListenerOrEventListenerObject) {
      if (type === 'open') {
        queueMicrotask(() => {
          if (typeof callback === 'function') {
            callback(new Event('open'))
          } else {
            callback.handleEvent(new Event('open'))
          }
        })
      }
    }

    close() {
      this.readyState = FakeWebSocket.CLOSED
    }

    send(payload: string) {
      sent.push(JSON.parse(payload) as Record<string, unknown>)
    }
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    addEventListener() {},
    dispatchEvent: (() => true) as typeof window.dispatchEvent,
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/sessions:workset') {
      return new Response(JSON.stringify({
        rev: getDesktopSnapshot().rev + 1,
        snapshot_endpoint_cursor: 'cursor-4',
        sessions_by_id: {},
        session_order: [],
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    useDesktopStore.setState(makeState(makeSession({ id: 'session-v3-a', sessionApi: 'v3' })), true)

    await useDesktopStore.getState().ensureRunStream('session-v3-a')
    await new Promise((resolve) => setImmediate(resolve))

    assert.deepEqual(websocketURLs.sort(), ['ws://127.0.0.1:7777/v3/realtime/stream?endpoint_cursor=cursor-4', 'ws://127.0.0.1:7777/ws'].sort())
    assert.equal(sent.some((message) => message.type === 'subscribe' && message.channel === 'session:*'), false)
    assert.equal(sent.some((message) => message.kind === 'subscribe.session'), true)
    assert.equal(websocketURLs.some((url) => url.includes('/v3/realtime/stream')), true)
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})


test('V3 assistant completed without durable run intent does not unlock Desktop live state', async () => {
  const session = makeSession({
    id: 'session-v3',
    sessionApi: 'v3',
    live: { ...emptyLiveState(), status: 'running', runId: 'run-v3', startedAt: 100, assistantDraft: 'hello' },
  })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.assistant.completed',
      ts_unix_ms: 200,
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        message: {
          id: 'msg-assistant-v3',
          session_id: 'session-v3',
          global_seq: 2,
          role: 'assistant',
          content: 'hello',
          created_at: 200,
        },
      },
    },
  }, 200)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.status, 'running')
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.startedAt, 100)
  assert.equal(updated.live.lastEventType, 'session.assistant.completed')
  assert.equal(updated.messageCount, 1)
  await new Promise((resolve) => setImmediate(resolve))
})

test('V3 assistant completed with durable terminal run intent unlocks Desktop live state', async () => {
  const session = makeSession({
    id: 'session-v3',
    sessionApi: 'v3',
    runIntent: {
      sessionId: 'session-v3',
      runId: 'run-v3',
      status: 'running',
      createdAt: 100,
      updatedAt: 120,
      eventSeq: 2,
    },
    live: { ...emptyLiveState(), status: 'running', runId: 'run-v3', startedAt: 100, assistantDraft: 'all done!' },
  })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 25,
    event: {
      id: 'v3evt_session-v3_00000000000000000025',
      session_id: 'session-v3',
      seq: 25,
      event_type: 'session.assistant.completed',
      ts_unix_ms: 300,
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'completed',
        run_intent: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          status: 'completed',
          created_at: 100,
          updated_at: 300,
          event_seq: 25,
        },
        message: {
          id: 'msg-assistant-v3',
          session_id: 'session-v3',
          global_seq: 25,
          role: 'assistant',
          content: 'all done!',
          created_at: 300,
        },
      },
    },
  }, 300)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.runIntent, null)
  assert.equal(updated.live.status, 'idle')
  assert.equal(updated.live.runId, null)
  assert.equal(updated.live.startedAt, null)
  assert.equal(updated.live.lastEventType, 'session.assistant.completed')
  await new Promise((resolve) => setImmediate(resolve))
})

test('V3 active run-intent record preserves durable created_at for active Desktop run state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 20,
    source_seq: 20,
    stream: 'session:session-v3',
    event_type: 'session.run_intent.recorded',
    entity_id: 'session-v3',
    ts_unix_ms: 300,
    payload: {
      session_id: 'session-v3',
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'running',
        created_at: 100,
        updated_at: 300,
        event_seq: 20,
      },
    },
  }))

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.runIntent?.runId, 'run-v3')
  assert.equal(updated.runIntent?.status, 'running')
  assert.equal(updated.live.status, 'running')
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.startedAt, 100)
  assert.equal(updated.live.lastEventType, 'session.run_intent.recorded')
})

test('V3 terminal run-intent record clears active Desktop lifecycle state', () => {
  const session = makeSession({
    id: 'session-v3',
    sessionApi: 'v3',
    lifecycle: {
      sessionId: 'session-v3',
      runId: 'run-v3',
      active: true,
      phase: 'running',
      startedAt: 100,
      endedAt: 0,
      updatedAt: 100,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: null,
    },
    live: { ...emptyLiveState(), status: 'running', runId: 'run-v3', startedAt: 100 },
  })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 20,
    source_seq: 20,
    stream: 'session:session-v3',
    event_type: 'session.run_intent.recorded',
    entity_id: 'session-v3',
    ts_unix_ms: 300,
    payload: {
      session_id: 'session-v3',
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'completed',
        event_seq: 20,
        updated_at: 300,
      },
    },
  }))

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.lifecycle, null)
  assert.equal(updated.runIntent, null)
  assert.equal(updated.live.status, 'idle')
  assert.equal(updated.live.runId, null)
  assert.equal(updated.live.startedAt, null)
  assert.equal(updated.live.lastEventType, 'session.run_intent.recorded')
})
