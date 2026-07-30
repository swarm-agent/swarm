import test from 'node:test'
import assert from 'node:assert/strict'

import { applyDesktopV3LivePatchBatch, applyToolLifecycleToRun, createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { selectDesktopToolActivities } from './desktop-v3-cache-selectors'
import type { DesktopToolActivity, LiveRunOverlay } from './desktop-v3-cache-types'
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

test('runtime started and terminal records reconcile the construction activity', () => {
  const liveRun = run()
  applyToolLifecycleToRun(liveRun, {
    call_id: 'call-edit', step: 2, output_index: 0, tool_name: 'edit', arguments_snapshot: '{"path":"a.ts"}',
  }, 'session.provider_tool_call.arguments.snapshot', 5, 1_005)
  applyToolLifecycleToRun(liveRun, {
    call_id: 'call-edit', tool_instance_id: 'step-2:call-edit', step: 2, tool_name: 'edit', started_at: 2_000,
  }, 'session.tool.started', 6, 2_000)
  applyToolLifecycleToRun(liveRun, {
    call_id: 'call-edit', tool_instance_id: 'step-2:call-edit', step: 2, tool_name: 'edit', output: '{"ok":true}', duration_ms: 25,
  }, 'session.tool.completed', 7, 2_025)

  const tool = activity(liveRun)
  assert.equal(tool.phase, 'completed')
  assert.equal(tool.label, 'Edit')
  assert.equal(tool.toolInstanceId, 'step-2:call-edit')
  assert.equal(tool.outputText, '{"ok":true}')
  assert.equal(tool.provenance.providerConstruction, true)
  assert.equal(tool.provenance.runtimeExecution, true)
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
