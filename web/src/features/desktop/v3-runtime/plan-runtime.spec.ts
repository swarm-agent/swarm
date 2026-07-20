import assert from 'node:assert/strict'
import test from 'node:test'
import {
  applyPlanRuntimeDelta,
  hydratePlanRuntimeState,
  type PlanRuntimeDeltaWire,
  type PlanRuntimeHydrationWire,
} from './plan-runtime'

function hydration(): PlanRuntimeHydrationWire {
  return {
    schema_version: 1,
    definition: { schema_version: 1, session_id: 'session-1', plan_id: 'plan-1', definition_revision: 3, title: 'Plan', checkpoint_order: ['cp-alpha'] },
    checkpoint_definitions: {
      'cp-alpha': { session_id: 'session-1', plan_id: 'plan-1', definition_revision: 3, checkpoint_id: 'cp-alpha', order: 1, title: 'Alpha', subtask_order: ['st-stable'] },
    },
    subtask_definitions: {
      'cp-alpha\u0000st-stable': { session_id: 'session-1', plan_id: 'plan-1', definition_revision: 3, checkpoint_id: 'cp-alpha', subtask_id: 'st-stable', order: 1, title: 'Stable' },
    },
    summary: { session_id: 'session-1', plan_id: 'plan-1', definition_revision: 3, execution_seq: 4, status: 'in_progress' },
    checkpoint_executions: {},
    subtask_executions: {},
  }
}

function delta(sequence: number): PlanRuntimeDeltaWire {
  return {
    protocol: 'v3.plan_execution', protocol_version: 1, kind: 'plan.execution.delta', schema_version: 1,
    session_id: 'session-1', plan_id: 'plan-1', definition_revision: 3, execution_seq: sequence,
    event_id: `event-${sequence}`, event_type: 'plan.subtasks_completed', checkpoint_id: 'cp-alpha', subtask_ids: ['st-stable'],
    summary_change: { session_id: 'session-1', plan_id: 'plan-1', definition_revision: 3, execution_seq: sequence, status: 'in_progress' },
    checkpoint_change: { session_id: 'session-1', plan_id: 'plan-1', checkpoint_id: 'cp-alpha', execution_seq: sequence, status: 'in_progress' },
    subtask_changes: [{ session_id: 'session-1', plan_id: 'plan-1', checkpoint_id: 'cp-alpha', subtask_id: 'st-stable', execution_seq: sequence, status: 'completed' }],
    created_at: 1,
  }
}

test('plan runtime applies only stable-ID delta targets', () => {
  const state = hydratePlanRuntimeState(hydration())
  assert.equal(applyPlanRuntimeDelta(state, delta(5)), 'applied')
  assert.equal(state.appliedExecutionSeq, 5)
  assert.equal(state.subtaskExecutionsById['cp-alpha\u0000st-stable']?.status, 'completed')
  assert.equal(Object.keys(state.subtaskExecutionsById).length, 1)
})

test('plan runtime rejects gaps and requests targeted hydrate', () => {
  const state = hydratePlanRuntimeState(hydration())
  assert.equal(applyPlanRuntimeDelta(state, delta(6)), 'stale')
  assert.equal(state.appliedExecutionSeq, 4)
  assert.equal(state.hydrateRequired, true)
  assert.equal(state.staleReason, 'execution_sequence_gap')
  assert.equal(Object.keys(state.subtaskExecutionsById).length, 0)
})

test('plan runtime ignores duplicate delivery and rejects unknown stable IDs', () => {
  const state = hydratePlanRuntimeState(hydration())
  assert.equal(applyPlanRuntimeDelta(state, delta(4)), 'duplicate')
  const invalid = delta(5)
  invalid.subtask_changes![0].subtask_id = 'position-0'
  invalid.subtask_ids = ['position-0']
  assert.equal(applyPlanRuntimeDelta(state, invalid), 'stale')
  assert.equal(state.staleReason, 'invalid_target')
})

test('plan runtime reducer is deterministic for ordered delivery and leaves hydration input immutable', () => {
  const raw = hydration()
  const original = structuredClone(raw)
  const first = hydratePlanRuntimeState(raw)
  const second = hydratePlanRuntimeState(structuredClone(raw))
  for (const sequence of [5, 6, 7]) {
    assert.equal(applyPlanRuntimeDelta(first, delta(sequence)), 'applied')
    assert.equal(applyPlanRuntimeDelta(second, structuredClone(delta(sequence))), 'applied')
  }
  assert.deepEqual(first, second)
  assert.deepEqual(raw, original)
})

test('plan runtime rejects definition identity mismatches before changing projections', () => {
  const state = hydratePlanRuntimeState(hydration())
  const invalid = delta(5)
  invalid.definition_revision = 4
  assert.equal(applyPlanRuntimeDelta(state, invalid), 'stale')
  assert.equal(state.appliedExecutionSeq, 4)
  assert.equal(state.summary?.execution_seq, 4)
  assert.deepEqual(state.checkpointExecutionsById, {})
  assert.deepEqual(state.subtaskExecutionsById, {})
})
