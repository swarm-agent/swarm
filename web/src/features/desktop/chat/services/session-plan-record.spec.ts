import test from 'node:test'
import assert from 'node:assert/strict'
import { normalizeDesktopSessionPlan } from './session-plan-record'

test('normalizeDesktopSessionPlan preserves execution policy state review attempts and run linkage', () => {
  const plan = normalizeDesktopSessionPlan({
    id: 'plan-1',
    title: 'Execution plan',
    status: 'approved',
    document: {
      id: 'plan-1',
      title: 'Execution plan',
      status: 'approved',
      revision_id: 'plan-1:v3',
      info: { goal: 'Ship checkpoint execution' },
      execution_policy: { mode: 'automatic', shape: 'checkpointed' },
      execution_state: {
        status: 'in_progress',
        active_attempt_id: 'cp-2:attempt-1',
        parent_session_id: 'parent-session',
        current_session_id: 'run-session',
        current_run_id: 'run-123',
        last_checkpoint_id: 'cp-1',
        last_attempt_id: 'cp-1:attempt-1',
        last_outcome: 'completed',
        started_at: 11,
        updated_at: 22,
        completed_at: 0,
      },
      active_checkpoint_id: 'cp-2',
      checkpoints: [{
        id: 'cp-2',
        title: 'Expose data',
        status: 'in_progress',
        attempt_id: 'cp-2:attempt-1',
        run_id: 'run-123',
        session_id: 'run-session',
        started_at: 11,
        review: { status: 'pending', reviewer_id: 'user-1', reviewer_type: 'user', notes: 'check it', reviewed_at: 0 },
        attempts: [{
          id: 'cp-2:attempt-1',
          checkpoint_id: 'cp-2',
          status: 'in_progress',
          run_id: 'run-123',
          session_id: 'run-session',
          parent_session_id: 'parent-session',
          started_at: 11,
          changed_files: ['web/src/features/desktop/chat/services/session-plan-record.ts'],
          validation: ['not run'],
        }],
        order: 2,
      }],
    },
  })

  assert.equal(plan.document?.executionPolicy?.mode, 'automatic')
  assert.equal(plan.document?.executionPolicy?.shape, 'checkpointed')
  assert.equal(plan.document?.executionState?.status, 'in_progress')
  assert.equal(plan.document?.executionState?.activeAttemptId, 'cp-2:attempt-1')
  assert.equal(plan.document?.executionState?.currentRunId, 'run-123')
  assert.equal(plan.document?.executionState?.lastOutcome, 'completed')

  const checkpoint = plan.document?.checkpoints[0]
  assert.equal(checkpoint?.attemptId, 'cp-2:attempt-1')
  assert.equal(checkpoint?.runId, 'run-123')
  assert.equal(checkpoint?.sessionId, 'run-session')
  assert.equal(checkpoint?.review?.status, 'pending')
  assert.equal(checkpoint?.review?.reviewerId, 'user-1')
  assert.equal(checkpoint?.attempts[0]?.parentSessionId, 'parent-session')
  assert.deepEqual(checkpoint?.attempts[0]?.changedFiles, ['web/src/features/desktop/chat/services/session-plan-record.ts'])
})
