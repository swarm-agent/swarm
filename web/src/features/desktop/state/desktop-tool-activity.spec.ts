import test from 'node:test'
import assert from 'node:assert/strict'

import { applyDesktopV3LivePatchBatch, applyToolLifecycleToRun, createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { selectDesktopToolActivities } from './desktop-v3-cache-selectors'
import type { CacheEvent, DesktopToolActivity, DesktopToolActivityPhase, DesktopToolActivitySemanticKind, LiveRunOverlay } from './desktop-v3-cache-types'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'

const encoder = new TextEncoder()
const sessionId = 'session-tool-activity'
const runId = 'run-tool-activity'

function run(): LiveRunOverlay {
  return {
    sessionId,
    runId,
    status: 'running',
    toolActivitiesById: {},
    toolCallsByCallId: {},
  }
}

function providerPatch(type: string, payload: Record<string, unknown>, eventIndex: number): SessionV3RealtimeLivePatchWire {
  const text = JSON.stringify({
    path_id: 'run.v3.provider-tool-construction.v1',
    type,
    run_id: runId,
    step: 2,
    event_index: eventIndex,
    recorded_at: 2_000 + eventIndex,
    ...payload,
  })
  return {
    session_id: sessionId,
    run_id: runId,
    stream_id: `provider-tool:${runId}:step:2:event:${eventIndex}`,
    stream_kind: 'provider_tool_call',
    operation: 'append',
    step: 2,
    step_id: 'provider-step-2',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: 0,
    offset_end: encoder.encode(text).byteLength,
    text,
    recorded_at: 2_000 + eventIndex,
  }
}

function activity(liveRun: LiveRunOverlay): DesktopToolActivity {
  const values = Object.values(liveRun.toolActivitiesById ?? {})
  assert.equal(values.length, 1)
  return values[0]
}

function activityByCall(liveRun: LiveRunOverlay, callId: string): DesktopToolActivity {
  const values = Object.values(liveRun.toolActivitiesById ?? {}).filter((tool) => tool.callId === callId)
  assert.equal(values.length, 1)
  return values[0]
}

interface ToolLifecycleFixture {
  callId: string
  toolName: 'edit' | 'plan_manage' | 'task'
  argumentsText: string
  semanticKind: DesktopToolActivitySemanticKind
  terminalEvent: 'session.tool.completed' | 'session.tool.failed' | 'session.tool.cancelled'
  terminalPhase: Extract<DesktopToolActivityPhase, 'completed' | 'failed' | 'cancelled'>
  terminalLabel: string
  terminalPayload: Record<string, unknown>
}

const toolLifecycleFixtures: ToolLifecycleFixture[] = [
  {
    callId: 'call-edit-success',
    toolName: 'edit',
    argumentsText: '{"path":"web/src/app.tsx"}',
    semanticKind: 'edit',
    terminalEvent: 'session.tool.completed',
    terminalPhase: 'completed',
    terminalLabel: 'Edit',
    terminalPayload: { output: '{"ok":true}', duration_ms: 25 },
  },
  {
    callId: 'call-plan-failure',
    toolName: 'plan_manage',
    argumentsText: '{"action":"complete_checkpoint","checkpoint_id":"cp-1"}',
    semanticKind: 'plan',
    terminalEvent: 'session.tool.failed',
    terminalPhase: 'failed',
    terminalLabel: 'Plan failed',
    terminalPayload: { error: 'checkpoint rejected' },
  },
  {
    callId: 'call-task-cancelled',
    toolName: 'task',
    argumentsText: '{"description":"Inspect lifecycle fixtures"}',
    semanticKind: 'task',
    terminalEvent: 'session.tool.cancelled',
    terminalPhase: 'cancelled',
    terminalLabel: 'Subagents cancelled',
    terminalPayload: {},
  },
]

function durableToolEvent(
  eventType: string,
  payload: Record<string, unknown>,
  seq: number,
): CacheEvent {
  const eventPayload = { run_id: runId, recorded_at: 4_000 + seq, ...payload }
  return {
    source: 'outbox',
    sessionId,
    eventType,
    payload: eventPayload,
    sessionEvent: {
      id: `event-${seq}-${eventType}-${String(payload.call_id ?? '')}`,
      session_id: sessionId,
      seq,
      event_type: eventType,
      payload: eventPayload,
      ts_unix_ms: 4_000 + seq,
    },
  }
}

test('construction live patch exists before execution output and repairs late fields', () => {
  let state = createEmptyDesktopV3CacheState()
  state = applyDesktopV3LivePatchBatch(state, [
    providerPatch('session.provider_tool_call.started', { call_id: 'call-edit', output_index: 0 }, 1),
  ])
  let selected = selectDesktopToolActivities(state, sessionId, runId)
  assert.equal(selected.length, 1)
  assert.equal(selected[0].phase, 'constructing')
  assert.equal(selected[0].label, 'Starting tool')
  assert.equal(selected[0].provenance.providerConstruction, true)

  state = applyDesktopV3LivePatchBatch(state, [
    providerPatch('session.provider_tool_call.arguments.delta', { call_id: 'call-edit', output_index: 0, arguments_delta: '{"path":' }, 2),
    providerPatch('session.provider_tool_call.arguments.snapshot', { call_id: 'call-edit', output_index: 0, tool_name: 'edit', arguments_snapshot: '{"path":"a.ts"}' }, 3),
    providerPatch('session.provider_tool_call.arguments.delta', { call_id: 'call-edit', output_index: 0, arguments_delta: '{"path":' }, 2),
    providerPatch('session.provider_tool_call.completed', { call_id: 'call-edit', output_index: 0, tool_name: 'edit', arguments: '{"path":"a.ts"}', status: 'completed' }, 4),
  ])
  selected = selectDesktopToolActivities(state, sessionId, runId)
  assert.equal(selected.length, 1)
  assert.equal(selected[0].phase, 'ready')
  assert.equal(selected[0].argumentsText, '{"path":"a.ts"}')
  assert.equal(selected[0].semanticKind, 'edit')
  assert.equal(selected[0].label, 'Editing')
})

test('edit, plan_manage, and task fixtures reconcile construction into one runtime card and explicit terminals', () => {
  for (const [index, fixture] of toolLifecycleFixtures.entries()) {
    const liveRun = run()
    applyToolLifecycleToRun(liveRun, {
      call_id: fixture.callId,
      step: 2,
      output_index: index,
    }, 'session.provider_tool_call.started', 1, 1_001)

    const construction = activity(liveRun)
    const activityId = construction.activityId
    assert.equal(construction.phase, 'constructing', fixture.toolName)
    assert.equal(construction.outputText, undefined, fixture.toolName)
    assert.equal(construction.provenance.providerConstruction, true, fixture.toolName)
    assert.equal(construction.provenance.runtimeExecution, false, fixture.toolName)

    applyToolLifecycleToRun(liveRun, {
      call_id: fixture.callId,
      step: 2,
      output_index: index,
      tool_name: fixture.toolName,
      arguments_snapshot: fixture.argumentsText,
    }, 'session.provider_tool_call.arguments.snapshot', 2, 1_002)
    applyToolLifecycleToRun(liveRun, {
      call_id: fixture.callId,
      tool_instance_id: `step-2:${fixture.callId}`,
      step: 2,
      tool_name: fixture.toolName,
      started_at: 2_000,
    }, 'session.tool.started', 3, 2_000)

    const running = activity(liveRun)
    assert.equal(running.activityId, activityId, fixture.toolName)
    assert.equal(running.phase, 'running', fixture.toolName)
    assert.equal(running.argumentsText, fixture.argumentsText, fixture.toolName)
    assert.equal(running.semanticKind, fixture.semanticKind, fixture.toolName)
    assert.equal(running.provenance.providerConstruction, true, fixture.toolName)
    assert.equal(running.provenance.runtimeExecution, true, fixture.toolName)

    applyToolLifecycleToRun(liveRun, {
      call_id: fixture.callId,
      tool_instance_id: `step-2:${fixture.callId}`,
      step: 2,
      tool_name: fixture.toolName,
      ...fixture.terminalPayload,
    }, fixture.terminalEvent, 4, 2_025)

    const terminal = activity(liveRun)
    assert.equal(terminal.activityId, activityId, fixture.toolName)
    assert.equal(terminal.phase, fixture.terminalPhase, fixture.toolName)
    assert.equal(terminal.label, fixture.terminalLabel, fixture.toolName)
    assert.equal(terminal.toolInstanceId, `step-2:${fixture.callId}`, fixture.toolName)
    if (fixture.terminalPhase === 'completed') {
      assert.equal(terminal.outputText, fixture.terminalPayload.output, fixture.toolName)
      assert.equal(terminal.durationMs, fixture.terminalPayload.duration_ms, fixture.toolName)
    } else if (fixture.terminalPhase === 'failed') {
      assert.equal(terminal.errorText, fixture.terminalPayload.error, fixture.toolName)
    }
    assert.equal(Object.keys(liveRun.toolCallsByCallId).length, 1, fixture.toolName)
  }
})

test('bounded fallback repairs late call identity and rejects ambiguous same-step merging', () => {
  const late = run()
  applyToolLifecycleToRun(late, { step: 2, output_index: 2 }, 'session.provider_tool_call.started', 1, 1)
  applyToolLifecycleToRun(late, {
    step: 2, output_index: 2, call_id: 'call-late', tool_name: 'task', arguments_snapshot: '{"description":"Map UI"}',
  }, 'session.provider_tool_call.arguments.snapshot', 2, 2)
  assert.equal(activity(late).callId, 'call-late')

  const unique = run()
  applyToolLifecycleToRun(unique, { step: 2, output_index: 3, tool_name: 'task' }, 'session.provider_tool_call.started', 1, 1)
  applyToolLifecycleToRun(unique, {
    step: 2, call_id: 'call-runtime-late', tool_instance_id: 'step-2:call-runtime-late', tool_name: 'task',
  }, 'session.tool.started', 2, 2)
  assert.equal(activity(unique).callId, 'call-runtime-late')

  const ambiguous = run()
  applyToolLifecycleToRun(ambiguous, { step: 2, output_index: 0 }, 'session.provider_tool_call.started', 1, 1)
  applyToolLifecycleToRun(ambiguous, { step: 2, output_index: 1 }, 'session.provider_tool_call.started', 2, 2)
  applyToolLifecycleToRun(ambiguous, {
    step: 2, call_id: 'call-third', tool_instance_id: 'step-2:call-third', tool_name: 'read',
  }, 'session.tool.started', 3, 3)
  assert.equal(Object.keys(ambiguous.toolActivitiesById ?? {}).length, 3)
})

test('runtime failures and cancellations expose explicit terminal phases', () => {
  const failed = run()
  applyToolLifecycleToRun(failed, { call_id: 'call-failed', tool_name: 'read' }, 'session.tool.started', 1, 1)
  applyToolLifecycleToRun(failed, { call_id: 'call-failed', tool_name: 'read', error: 'denied' }, 'session.tool.failed', 2, 2)
  const failedActivity = activity(failed)
  assert.equal(failedActivity.phase, 'failed')
  assert.equal(failedActivity.label, 'Read failed')

  const cancelled = run()
  applyToolLifecycleToRun(cancelled, { call_id: 'call-cancelled', tool_name: 'task' }, 'session.tool.started', 1, 1)
  applyToolLifecycleToRun(cancelled, { call_id: 'call-cancelled', tool_name: 'task' }, 'session.tool.cancelled', 2, 2)
  const cancelledActivity = activity(cancelled)
  assert.equal(cancelledActivity.phase, 'cancelled')
  assert.equal(cancelledActivity.label, 'Subagents cancelled')
})

test('reconnect repair and stale construction replay leave one non-running card per call', () => {
  let state = createEmptyDesktopV3CacheState()
  state = applyDesktopV3LivePatchBatch(state, toolLifecycleFixtures.map((fixture, index) => providerPatch(
    'session.provider_tool_call.started',
    { call_id: fixture.callId, output_index: index },
    index + 1,
  )))

  const beforeRepair = selectDesktopToolActivities(state, sessionId, runId)
  assert.equal(beforeRepair.length, toolLifecycleFixtures.length)
  assert.ok(beforeRepair.every((tool) => tool.phase === 'constructing'))
  assert.ok(beforeRepair.every((tool) => tool.outputText === undefined))

  const repairEvents = toolLifecycleFixtures.flatMap((fixture, index) => {
    const startedSeq = 10 + index
    const terminalSeq = 20 + index
    const identity = {
      call_id: fixture.callId,
      tool_instance_id: `step-2:${fixture.callId}`,
      step: 2,
      tool_name: fixture.toolName,
    }
    return [
      durableToolEvent(fixture.terminalEvent, { ...identity, ...fixture.terminalPayload }, terminalSeq),
      durableToolEvent('session.tool.started', identity, startedSeq),
      durableToolEvent(fixture.terminalEvent, { ...identity, ...fixture.terminalPayload }, terminalSeq),
    ]
  })

  desktopV3CacheReducer(state, {
    type: 'liveRun.mergeRepairEvents',
    sessionId,
    runId,
    events: repairEvents.reverse(),
  })
  state = applyDesktopV3LivePatchBatch(state, toolLifecycleFixtures.flatMap((fixture, index) => [
    providerPatch(
      'session.provider_tool_call.arguments.snapshot',
      {
        call_id: fixture.callId,
        output_index: index,
        tool_name: fixture.toolName,
        arguments_snapshot: fixture.argumentsText,
      },
      index + 10,
    ),
    providerPatch(
      'session.provider_tool_call.started',
      { call_id: fixture.callId, output_index: index },
      index + 1,
    ),
  ]))

  const repairedRun = state.liveRunsBySession[sessionId][runId]
  const repaired = selectDesktopToolActivities(state, sessionId, runId)
  assert.equal(repaired.length, toolLifecycleFixtures.length)
  assert.equal(Object.keys(repairedRun.toolCallsByCallId).length, toolLifecycleFixtures.length)
  for (const fixture of toolLifecycleFixtures) {
    const tool = activityByCall(repairedRun, fixture.callId)
    assert.equal(tool.phase, fixture.terminalPhase, fixture.toolName)
    assert.equal(tool.label, fixture.terminalLabel, fixture.toolName)
    assert.equal(tool.argumentsText, fixture.argumentsText, fixture.toolName)
    assert.equal(tool.provenance.providerConstruction, true, fixture.toolName)
    assert.equal(tool.provenance.runtimeExecution, true, fixture.toolName)
  }
  assert.ok(repaired.every((tool) => tool.phase !== 'constructing' && tool.phase !== 'ready' && tool.phase !== 'running'))
})

test('reordered hydration and terminal repair converge monotonically', () => {
  const liveRun = run()
  applyToolLifecycleToRun(liveRun, {
    step: 2, call_id: 'call-plan', output_index: 1, tool_name: 'plan_manage', status: 'building',
  }, 'session.provider_tool_call.started', 5, 5)
  applyToolLifecycleToRun(liveRun, {
    step: 2, call_id: 'call-plan', tool_instance_id: 'step-2:call-plan', tool_name: 'plan_manage',
  }, 'session.tool.started', 6, 6)
  applyToolLifecycleToRun(liveRun, {
    step: 2, call_id: 'call-plan', tool_instance_id: 'step-2:call-plan', tool_name: 'plan_manage',
  }, 'session.tool.completed', 8, 8)
  applyToolLifecycleToRun(liveRun, {
    step: 2, call_id: 'call-plan', output_index: 1, tool_name: 'plan_manage', arguments_snapshot: '{"action":"start_session_checkpoint"}', event_index: 7,
  }, 'session.provider_tool_call.arguments.snapshot', 7, 7)
  applyToolLifecycleToRun(liveRun, {
    step: 2, call_id: 'call-plan', output_index: 1, tool_name: 'plan_manage', arguments_snapshot: '{"action":"stale"}', event_index: 6,
  }, 'session.provider_tool_call.arguments.snapshot', 6, 6)
  applyToolLifecycleToRun(liveRun, {
    step: 2, call_id: 'call-plan', tool_instance_id: 'step-2:call-plan', tool_name: 'plan_manage',
  }, 'session.tool.completed', 8, 8)

  const tool = activity(liveRun)
  assert.equal(tool.phase, 'completed')
  assert.equal(tool.timelineSeq, 8)
  assert.equal(tool.semanticKind, 'plan')
  assert.equal(tool.argumentsText, '{"action":"start_session_checkpoint"}')
  assert.equal(tool.provenance.providerConstruction, true)
  assert.equal(tool.provenance.runtimeExecution, true)
})
