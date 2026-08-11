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
      artifacts: [{ path: 'docs/plan-input.json', role: 'input', description: 'Shared input', media_type: 'application/json' }],
      checkpoints: [{
        id: 'cp-2',
        title: 'Expose data',
        status: 'in_progress',
        attempt_id: 'cp-2:attempt-1',
        run_id: 'run-123',
        session_id: 'run-session',
        started_at: 11,
        review: { status: 'pending', reviewer_id: 'user-1', reviewer_type: 'user', notes: 'check it', reviewed_at: 0 },
        artifacts: [{ path: 'out/visible-list.md', role: 'deliverable', description: 'Visible list', media_type: 'text/markdown' }],
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
  assert.deepEqual(plan.document?.artifacts, [{ path: 'docs/plan-input.json', role: 'input', description: 'Shared input', mediaType: 'application/json' }])

  const checkpoint = plan.document?.checkpoints[0]
  assert.equal(checkpoint?.attemptId, 'cp-2:attempt-1')
  assert.equal(checkpoint?.runId, 'run-123')
  assert.equal(checkpoint?.sessionId, 'run-session')
  assert.equal(checkpoint?.review?.status, 'pending')
  assert.equal(checkpoint?.review?.reviewerId, 'user-1')
  assert.deepEqual(checkpoint?.artifacts, [{ path: 'out/visible-list.md', role: 'deliverable', description: 'Visible list', mediaType: 'text/markdown' }])
  assert.equal(checkpoint?.attempts[0]?.parentSessionId, 'parent-session')
  assert.deepEqual(checkpoint?.attempts[0]?.changedFiles, ['web/src/features/desktop/chat/services/session-plan-record.ts'])
})

test('normalizeDesktopSessionPlan decodes the versioned final handoff projection without parsing report Markdown', () => {
  const plan = normalizeDesktopSessionPlan({
    id: 'plan-final',
    document: {
      title: 'Final plan',
      info: { goal: 'Ship it' },
      checkpoints: [{
        id: 'cp-1',
        title: 'Finish',
        final_handoff: {
          schema_version: 1,
          title: 'Ready to ship',
          overview: 'The requested Desktop behavior is complete.',
          impact_bullets: ['Compact by default', 'Evidence remains available'],
          recommendation: { decision: 'ship', action: 'review', reason: 'All criteria met', action_state: 'ready' },
          suggested_prompts: [{ label: 'Review changes', prompt: 'Review the completed changes.' }],
          pull_request_url: 'https://github.com/swarm/repository/pull/42',
          artifacts: [
            { artifact_id: 'artifact-html', label: 'Interactive gallery', description: 'Rendered iteration', media_type: 'text/html', previewable: true },
            { id: 'artifact-image', description: 'Overview image', media_type: 'image/png', previewable: true },
            { path: 'changed/file.ts', description: 'Changed file is not an artifact' },
          ],
          details: {
            report: '## Report\nFull **Markdown** evidence.',
            result: 'done',
            changed_files: ['web/src/final.tsx'],
            validation: ['focused test passed'],
          },
        },
        order: 1,
      }],
    },
  })

  const handoff = plan.document?.checkpoints[0]?.finalHandoff
  assert.equal(handoff?.schemaVersion, 1)
  assert.equal(handoff?.overview, 'The requested Desktop behavior is complete.')
  assert.deepEqual(handoff?.impactBullets, ['Compact by default', 'Evidence remains available'])
  assert.equal(handoff?.recommendation?.actionState, 'ready')
  assert.deepEqual(handoff?.suggestedPrompts, [{ label: 'Review changes', prompt: 'Review the completed changes.' }])
  assert.equal(handoff?.pullRequestUrl, 'https://github.com/swarm/repository/pull/42')
  assert.deepEqual(handoff?.artifacts, [
    { artifactId: 'artifact-html', label: 'Interactive gallery', description: 'Rendered iteration', mediaType: 'text/html', previewable: true },
    { artifactId: 'artifact-image', label: 'Overview image', description: 'Overview image', mediaType: 'image/png', previewable: true },
  ])
  assert.equal(handoff?.details.report, '## Report\nFull **Markdown** evidence.')
  assert.deepEqual(handoff?.details.changedFiles, ['web/src/final.tsx'])
})

test('normalizeDesktopSessionPlan omits unsafe final handoff pull request URLs', () => {
  const plan = normalizeDesktopSessionPlan({
    id: 'plan-unsafe-pr',
    document: {
      title: 'Unsafe PR',
      info: { goal: 'Stay safe' },
      checkpoints: [{
        id: 'cp-1',
        title: 'Finish',
        final_handoff: {
          schema_version: 1,
          title: 'Done',
          overview: 'Complete.',
          pull_request_url: 'javascript:alert(1)',
          details: {},
        },
      }],
    },
  })

  assert.equal(plan.document?.checkpoints[0]?.finalHandoff?.pullRequestUrl, '')
  assert.deepEqual(plan.document?.checkpoints[0]?.finalHandoff?.artifacts, [])
})
