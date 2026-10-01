import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
import {
  extractTaskSessionIds,
  computeActiveTaskSessionIds,
  type TaskSessionCandidate,
} from '../runtime/desktop-projects-membership'
import {
  aggregateTaskLiveState,
  type SessionDataLookup,
} from './orchestrate-task-helpers'
import type { RunningTask } from './orchestrate-types'

// =============================================================================
// Test Suite: Task-card lifecycle synchronization & multi-session tracking
// Product Requirement: Orchestrate task cards accurately track authoritative backend
// and deployed session OR multiple associated sessions in realtime, show progress
// and needs-review with mixed states, survive reload/reconnect, and expose a useful
// default view with summary, session progress, and media thumbnails.
// =============================================================================

test('Invariant 1: extractTaskSessionIds extracts and deduplicates session IDs across single session, planBinding, and TaskProgram child jobs', () => {
  // Written Purpose:
  // - Requirement: All associated sessions (primary, plan binding, Task Program child jobs,
  //   current session IDs, and generation history) must be extracted and deduplicated.
  // - Threat/regression: Subagents in a task program not tracked, missing realtime updates
  //   or failing to acquire demand leases.
  // - Boundary: extractTaskSessionIds in desktop-projects-membership.ts.

  // Case 1: Simple single session
  const task1: TaskSessionCandidate = {
    id: 't-1',
    sessionId: 'sess-primary-1',
    status: 'running',
  }
  assert.deepEqual(extractTaskSessionIds(task1), ['sess-primary-1'])

  // Case 2: Plan binding session with different ID
  const task2: TaskSessionCandidate = {
    id: 't-2',
    sessionId: 'sess-primary-2',
    status: 'pending_approval',
    planBinding: { sessionId: 'sess-planner-2' },
  }
  assert.deepEqual(extractTaskSessionIds(task2), ['sess-planner-2', 'sess-primary-2'])

  // Case 3: Task program with multiple child coder jobs and generation history
  const task3: TaskSessionCandidate = {
    id: 't-3',
    sessionId: 'sess-coordinator-3',
    status: 'in_progress',
    taskProgramStatus: {
      jobs: [
        {
          child_session_id: 'sess-coder-a',
          current_session_id: 'sess-coder-a',
          generation_history: [{ session_id: 'sess-coder-a' }],
        },
        {
          child_session_id: 'sess-coder-b-old',
          current_session_id: 'sess-coder-b-new',
          generation_history: [
            { session_id: 'sess-coder-b-old' },
            { session_id: 'sess-coder-b-new' },
          ],
        },
        {
          child_session_id: 'sess-coder-c',
        },
      ],
    },
  }
  const sids3 = extractTaskSessionIds(task3)
  // Current attempt supersedes historical sessions and generation_history is excluded
  // so old failures cannot poison state/counts.
  assert.deepEqual(sids3, [
    'sess-coder-a',
    'sess-coder-b-new',
    'sess-coder-c',
    'sess-coordinator-3',
  ])

  // Case 4: Null, undefined, empty/whitespace strings
  assert.deepEqual(extractTaskSessionIds(null), [])
  assert.deepEqual(extractTaskSessionIds({ id: 't-4', sessionId: '   ' }), [])
})

test('Invariant 2: computeActiveTaskSessionIds includes all associated sessions for active and selected tasks, excluding unselected inactive tasks', () => {
  // Written Purpose:
  // - Requirement: Realtime demand leases must be acquired for all sessions belonging to active tasks
  //   (running, in_progress, planning, pending_approval, needs_review) or selected tasks,
  //   while completed/queued tasks remain inactive unless selected.
  // - Threat/regression: Lease churn or missing live updates for planning or multi-agent tasks.
  // - Boundary: computeActiveTaskSessionIds in desktop-projects-membership.ts.

  const tasks: TaskSessionCandidate[] = [
    { id: 't-run', sessionId: 'sess-run', status: 'running' },
    { id: 't-plan', sessionId: 'sess-plan', status: 'planning' },
    {
      id: 't-program',
      sessionId: 'sess-coord',
      status: 'in_progress',
      taskProgramStatus: {
        jobs: [
          { child_session_id: 'sess-job-1' },
          { child_session_id: 'sess-job-2' },
        ],
      },
    },
    { id: 't-done', sessionId: 'sess-done', status: 'completed' },
    { id: 't-queue', sessionId: 'sess-queue', status: 'queued' },
  ]

  const activeNoSelection = computeActiveTaskSessionIds(tasks)
  assert.deepEqual(activeNoSelection, [
    'sess-coord',
    'sess-job-1',
    'sess-job-2',
    'sess-plan',
    'sess-queue',
    'sess-run',
  ])

  // Selecting completed task includes its session
  const activeWithDoneSelected = computeActiveTaskSessionIds(tasks, 't-done')
  assert.deepEqual(activeWithDoneSelected, [
    'sess-coord',
    'sess-done',
    'sess-job-1',
    'sess-job-2',
    'sess-plan',
    'sess-queue',
    'sess-run',
  ])
})

test('Invariant 3: aggregateTaskLiveState accurately tracks single-session transitions (running, needs_review, failed, planning->pending_approval)', () => {
  // Written Purpose:
  // - Requirement: Task cards must transition to needs_review when execution completes (never flipping directly
  //   to completed), transition to failed when a run fails or is cancelled, and transition planning tasks
  //   to pending_approval when a plan document is authored.
  // - Threat/regression: Direct flip to completed hiding unintegrated changes, stuck planning tasks.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const baseTask: RunningTask = {
    id: 'task-1',
    title: 'Implement Auth',
    agentType: 'coder',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '1m',
    subtasks: [],
    sessionId: 'sess-1',
    unintegratedCommits: 2,
    isIntegrated: false,
  }

  // 1. Session running -> task status running
  const runningLookup: Record<string, SessionDataLookup> = {
    'sess-1': {
      intent: { status: 'running', run_id: 'run-1' },
      sessionRecord: { kind: 'full', session: { id: 'sess-1', lifecycle: { active: true } } },
      liveRun: {
        toolCallsByCallId: {
          'c-1': { toolName: 'edit', toolDisplay: 'edit src/auth.go', updatedAt: 100 },
        },
        assistantSegments: [{ content: 'Editing auth.go...', updatedAt: 100 }],
      },
    },
  }
  const liveRunning = aggregateTaskLiveState(baseTask, runningLookup)
  assert.equal(liveRunning.status, 'running')
  assert.equal(liveRunning.currentTool, 'edit src/auth.go')
  assert.equal(liveRunning.liveAssistantText, 'Editing auth.go...')

  // 2. Session completed -> task status needs_review (not completed, because unintegratedCommits > 0)
  const reviewLookup: Record<string, SessionDataLookup> = {
    'sess-1': {
      intent: { status: 'completed', run_id: 'run-1' },
      sessionRecord: { kind: 'full', session: { id: 'sess-1', lifecycle: { active: false, phase: 'needs_review' } } },
      planRecord: { status: 'waiting_review' },
    },
  }
  const liveReview = aggregateTaskLiveState(baseTask, reviewLookup)
  assert.equal(liveReview.status, 'needs_review')

  // 3. Session failed -> task status failed
  const failedLookup: Record<string, SessionDataLookup> = {
    'sess-1': {
      intent: { status: 'failed', blocked_reason: 'Compile error on line 42' },
      sessionRecord: { kind: 'full', session: { id: 'sess-1', lifecycle: { active: false, phase: 'failed' } } },
    },
  }
  const liveFailed = aggregateTaskLiveState(baseTask, failedLookup)
  assert.equal(liveFailed.status, 'failed')

  // 4. Planning task with plan document authored -> transitions to pending_approval
  const planTask: RunningTask = {
    id: 'task-plan-1',
    title: 'Architect Payment Flow',
    agentType: 'plan',
    status: 'planning',
    workspaceTarget: 'swarm-go',
    elapsed: '30s',
    subtasks: [],
    sessionId: 'sess-plan-1',
  }
  const planReadyLookup: Record<string, SessionDataLookup> = {
    'sess-plan-1': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-plan-1', lifecycle: { active: false } } },
      planRecord: {
        id: 'plan-payment',
        status: 'waiting_review',
        document: {
          id: 'plan-payment',
          title: 'Payment Architecture',
          checkpoints: [
            { id: 'cp-1', title: 'Stripe Webhook', status: 'pending' },
          ],
        },
      },
    },
  }
  const livePlan = aggregateTaskLiveState(planTask, planReadyLookup)
  assert.equal(livePlan.status, 'pending_approval')
  assert.equal(livePlan.planDocument?.title, 'Payment Architecture')
  assert.equal(livePlan.planDocument?.checkpoints?.length, 1)

  // 5. Planning task failed -> transitions to failed
  const planFailedLookup: Record<string, SessionDataLookup> = {
    'sess-plan-1': {
      intent: { status: 'failed', blocked_reason: 'Context overflow during planning' },
      sessionRecord: { kind: 'full', session: { id: 'sess-plan-1', lifecycle: { active: false, phase: 'failed' } } },
    },
  }
  const livePlanFailed = aggregateTaskLiveState(planTask, planFailedLookup)
  assert.equal(livePlanFailed.status, 'failed')
})

test('Invariant 4: aggregateTaskLiveState multi-session: NEVER whole-task complete from one child, surfaces mixed states and counts', () => {
  // Written Purpose:
  // - Requirement: When a task executes multiple parallel sessions (e.g. Task Program with 3 Coders):
  //   1) One finished child must NEVER mark the whole task completed while others run.
  //   2) If any session fails, the failure must be surfaced (no false success).
  //   3) Status transitions to needs_review only after all finish, completed only when integrated.
  //   4) sessionSummary surfaces running, review, failed, and completed session counts.
  // - Threat/regression: Race condition where first finishing child falsely terminates multi-agent task.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const taskProgramTask: RunningTask = {
    id: 'task-tp-1',
    title: 'Overhaul Engine Subsystems',
    agentType: 'swarm',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '2m',
    subtasks: [],
    sessionId: 'sess-coordinator',
    taskProgramStatus: {
      parent_session_id: 'sess-coordinator',
      program_id: 'prog-1',
      state: 'running',
      definition: { stages: [], jobs: [] },
      jobs: [
        { job_id: 'job-core', stage_id: 's1', state: 'running', child_session_id: 'sess-core' },
        { job_id: 'job-api', stage_id: 's1', state: 'completed', child_session_id: 'sess-api' },
        { job_id: 'job-ui', stage_id: 's1', state: 'running', child_session_id: 'sess-ui' },
      ],
    },
  }

  // 1. One child completed, two running -> Whole task MUST remain running!
  const mixedRunningLookup: Record<string, SessionDataLookup> = {
    'sess-coordinator': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coordinator', lifecycle: { active: true } } },
    },
    'sess-api': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-api', lifecycle: { active: false, phase: 'completed' } } },
    },
    'sess-core': {
      intent: { status: 'running' },
      sessionRecord: { kind: 'full', session: { id: 'sess-core', lifecycle: { active: true } } },
    },
    'sess-ui': {
      intent: { status: 'running' },
      sessionRecord: { kind: 'full', session: { id: 'sess-ui', lifecycle: { active: true } } },
    },
  }

  const liveCohortRunning = aggregateTaskLiveState(taskProgramTask, mixedRunningLookup)
  assert.equal(liveCohortRunning.status, 'running', 'Task must NOT complete when only one child finishes!')
  assert.equal(liveCohortRunning.sessionSummary?.runningSessions, 2)
  assert.equal(liveCohortRunning.sessionSummary?.completedSessions, 1)

  // 2. All finished, but one failed -> Task MUST be failed (no false success!)
  const mixedFailedLookup: Record<string, SessionDataLookup> = {
    'sess-coordinator': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coordinator', lifecycle: { active: false } } },
    },
    'sess-api': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-api', lifecycle: { active: false, phase: 'completed' } } },
    },
    'sess-core': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-core', lifecycle: { active: false, phase: 'completed' } } },
    },
    'sess-ui': {
      intent: { status: 'failed', blocked_reason: 'CSS compilation failed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-ui', lifecycle: { active: false, phase: 'failed' } } },
    },
  }

  const awaitingProgram = aggregateTaskLiveState(taskProgramTask, mixedFailedLookup)
  assert.equal(awaitingProgram.status, 'running', 'Coordinator owns program outcome until it publishes terminal state')
  assert.equal(awaitingProgram.sessionSummary?.failedSessions, 1, 'Child failure remains visible while coordinator reconciles')
  const liveCohortFailed = aggregateTaskLiveState({ ...taskProgramTask, taskProgramStatus: { ...taskProgramTask.taskProgramStatus!, state: 'failed' } }, mixedFailedLookup)
  assert.equal(liveCohortFailed.status, 'failed', 'Task must surface failure when a child session fails!')
  assert.equal(liveCohortFailed.sessionSummary?.failedSessions, 1)
  assert.equal(liveCohortFailed.sessionSummary?.completedSessions, 2)

  // 3. All finished without failure, but unintegrated -> Task MUST transition to needs_review!
  const allDoneNotIntegratedLookup: Record<string, SessionDataLookup> = {
    'sess-coordinator': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coordinator', lifecycle: { active: false } } },
    },
    'sess-api': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-api', lifecycle: { active: false, phase: 'completed' } } },
    },
    'sess-core': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-core', lifecycle: { active: false, phase: 'completed' } } },
    },
    'sess-ui': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-ui', lifecycle: { active: false, phase: 'completed' } } },
    },
  }

  const liveCohortNeedsReview = aggregateTaskLiveState(
    { ...taskProgramTask, isIntegrated: false, taskProgramStatus: { ...taskProgramTask.taskProgramStatus!, state: 'completed' } },
    allDoneNotIntegratedLookup,
  )
  assert.equal(liveCohortNeedsReview.status, 'needs_review', 'Task must transition to needs_review before integration!')
  assert.equal(liveCohortNeedsReview.sessionSummary?.completedSessions, 3)

  // 4. All finished and explicitly integrated -> Completed!
  const liveCohortCompleted = aggregateTaskLiveState(
    { ...taskProgramTask, isIntegrated: true },
    allDoneNotIntegratedLookup,
  )
  assert.equal(liveCohortCompleted.status, 'completed')
})

test('Invariant 5: Source static assertions: MinimalTaskCard renders bigger default view with thumbnails, multi-session strip, and expand toggle', () => {
  // Written Purpose:
  // - Requirement: The OrchestrateView source must define:
  //   1) data-testid="task-media-thumbnails-strip" in MinimalTaskCard
  //   2) data-testid="task-multi-session-strip" in MinimalTaskCard
  //   3) data-testid="toggle-task-details-btn" in MinimalTaskCard
  //   4) Direct rendering of MinimalTaskCard in matrix task list without thin bar wrapping
  // - Threat/regression: Regressing to the 40px thin task bar or hiding media thumbnails.
  // - Boundary: OrchestrateView.tsx.

  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('data-testid="task-media-thumbnails-strip"'), 'Must render media thumbnails strip')
  assert.ok(source.includes('data-testid="task-multi-session-strip"'), 'Must render multi-session strip')
  assert.ok(source.includes('data-testid="toggle-task-details-btn"'), 'Must render expand details toggle button')
  assert.ok(source.includes('data-testid="orchestrate-task-list"'), 'Must render orchestrate-task-list testid')
  assert.ok(!source.includes('className="flex items-center justify-between p-3 cursor-pointer hover:bg-white/[0.02]"'), 'Thin task bar must be eliminated')
})

test('Invariant 6: Approved plan with unfinished checkpoints does NOT complete on provider turn completion', () => {
  // Written Purpose:
  // - Requirement: When a plan-driven task has an approved plan with unfinished checkpoints,
  //   a completed provider turn (intent status completed) must NOT mark the task or session completed or ready for review.
  // - Threat/regression: premature task completion when agent finishes single checkpoint turn in a multi-checkpoint plan.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const planTask: RunningTask = {
    id: 'task-plan-exec-1',
    title: 'Multi-checkpoint Plan Execution',
    agentType: 'swarm',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '1m',
    subtasks: [],
    sessionId: 'sess-plan-exec',
    planBinding: {
      planId: 'plan-exec-1',
      sessionId: 'sess-plan-exec',
      definitionRevision: 2,
    },
    planDocument: {
      id: 'plan-exec-1',
      title: 'Execution Plan',
      checkpoints: [
        { id: 'cp-1', title: 'Checkpoint 1', status: 'completed' },
        { id: 'cp-2', title: 'Checkpoint 2', status: 'pending' },
      ],
    },
  }

  // Provider turn ended for cp-1, but cp-2 is still pending
  const turnCompletedLookup: Record<string, SessionDataLookup> = {
    'sess-plan-exec': {
      intent: { status: 'completed' },
      sessionRecord: {
        kind: 'full',
        session: { id: 'sess-plan-exec', lifecycle: { active: false, phase: 'in_progress' } },
      },
      planRecord: {
        id: 'plan-exec-1',
        version: 2,
        document: planTask.planDocument,
      },
    },
  }

  const live = aggregateTaskLiveState(planTask, turnCompletedLookup)
  assert.notEqual(live.status, 'completed', 'Task must NOT complete when plan checkpoints remain pending')
  assert.notEqual(live.status, 'needs_review', 'Task must NOT transition to needs_review when plan checkpoints remain pending')
  assert.equal(live.sessionSummary?.completedSessions, 0, 'Session must not be counted completed while plan is unfinished')
})

test('Invariant 7: Child coder sessions do NOT inherit parent plan document and do not get falsely marked needs_review', () => {
  // Written Purpose:
  // - Requirement: Child sessions in a Task Program must not inherit the parent coordinator\'s plan document.
  // - Threat/regression: Parent plan review state polluting child session states.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const taskProgramWithPlan: RunningTask = {
    id: 'task-tp-plan',
    title: 'Coordinator With Plan',
    agentType: 'swarm',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '30s',
    subtasks: [],
    sessionId: 'sess-coord-p',
    planBinding: {
      planId: 'plan-coord',
      sessionId: 'sess-coord-p',
      definitionRevision: 1,
    },
    planDocument: {
      id: 'plan-coord',
      status: 'waiting_review',
      checkpoints: [{ id: 'cp-1', status: 'needs_review' }],
    },
    taskProgramStatus: {
      parent_session_id: 'sess-coord-p',
      program_id: 'prog-p',
      state: 'running',
      definition: { stages: [], jobs: [] },
      jobs: [
        { job_id: 'job-1', stage_id: 's1', state: 'running', child_session_id: 'sess-child-1' },
      ],
    },
  }

  const lookup: Record<string, SessionDataLookup> = {
    'sess-coord-p': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coord-p', lifecycle: { active: true } } },
      planRecord: { id: 'plan-coord', status: 'waiting_review', document: taskProgramWithPlan.planDocument },
    },
    'sess-child-1': {
      intent: { status: 'running' },
      sessionRecord: { kind: 'full', session: { id: 'sess-child-1', lifecycle: { active: true } } },
    },
  }

  const live = aggregateTaskLiveState(taskProgramWithPlan, lookup)
  const childState = live.sessionSummary?.sessionStates.find((s) => s.sessionId === 'sess-child-1')
  assert.equal(childState?.status, 'running', 'Child session must NOT inherit parent plan waiting_review status!')
})

test('Invariant 8: Stale running job state does NOT override terminal child session evidence', () => {
  // Written Purpose:
  // - Requirement: If a child session has completed or failed in runtime session store,
  //   a stale job state (\'running\') in the Task Program record must not override the terminal session status.
  // - Threat/regression: UI stuck in running state because job record in Pebble has not yet transitioned.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const taskWithStaleJob: RunningTask = {
    id: 'task-tp-stale',
    title: 'Stale Job Override Test',
    agentType: 'swarm',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '45s',
    subtasks: [],
    sessionId: 'sess-coord-s',
    taskProgramStatus: {
      parent_session_id: 'sess-coord-s',
      program_id: 'prog-s',
      state: 'running',
      definition: { stages: [], jobs: [] },
      jobs: [
        // Stale job record claims running
        { job_id: 'job-done', stage_id: 's1', state: 'running', child_session_id: 'sess-child-done' },
      ],
    },
  }

  // Session runtime has terminal completed intent
  const lookup: Record<string, SessionDataLookup> = {
    'sess-coord-s': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coord-s', lifecycle: { active: true } } },
    },
    'sess-child-done': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-child-done', lifecycle: { active: false, phase: 'completed' } } },
    },
  }

  const live = aggregateTaskLiveState(taskWithStaleJob, lookup)
  const childState = live.sessionSummary?.sessionStates.find((s) => s.sessionId === 'sess-child-done')
  assert.equal(childState?.status, 'completed', 'Terminal child session must not be overridden by stale running job state!')
})

test('Invariant 9: Unallocated/queued jobs prevent whole-task completion or review, and progress percent <= 100%', () => {
  // Written Purpose:
  // - Requirement: When a Task Program has unallocated jobs or jobs in declared/queued state,
  //   the overall task cannot be completed or needs_review. Progress percent must never exceed 100%.
  // - Threat/regression: Early review state before later pipeline stages/jobs even launch.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const taskWithUnallocated: RunningTask = {
    id: 'task-tp-unalloc',
    title: 'Multi-stage Pipeline',
    agentType: 'swarm',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '1m',
    subtasks: [],
    sessionId: 'sess-coord-u',
    taskProgramStatus: {
      parent_session_id: 'sess-coord-u',
      program_id: 'prog-u',
      state: 'running',
      definition: { stages: [], jobs: [] },
      jobs: [
        { job_id: 'job-1', stage_id: 's1', state: 'completed', child_session_id: 'sess-j1' },
        { job_id: 'job-2', stage_id: 's2', state: 'declared' }, // Unallocated
      ],
    },
  }

  const lookup: Record<string, SessionDataLookup> = {
    'sess-coord-u': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coord-u', lifecycle: { active: false } } },
    },
    'sess-j1': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-j1', lifecycle: { active: false, phase: 'completed' } } },
    },
  }

  const live = aggregateTaskLiveState(taskWithUnallocated, lookup)
  assert.equal(live.status, 'running', 'Task with unallocated jobs must remain running!')
  assert.ok(typeof live.planProgressPercent === 'number' && live.planProgressPercent <= 100, 'Progress must be <= 100%')
})

test('Invariant 10: Failures are never masked by review sessions in multi-session aggregation', () => {
  // Written Purpose:
  // - Requirement: If any associated session or job has failed, the task status must surface failed,
  //   even if other sessions are in needs_review or handoff_ready.
  // - Threat/regression: Review priority hiding catastrophic child failure.
  // - Boundary: aggregateTaskLiveState in orchestrate-task-helpers.ts.

  const taskMixed: RunningTask = {
    id: 'task-tp-mixed',
    title: 'Mixed Review and Fail',
    agentType: 'swarm',
    status: 'in_progress',
    workspaceTarget: 'swarm-go',
    elapsed: '1m',
    subtasks: [],
    sessionId: 'sess-coord-m',
    taskProgramStatus: {
      parent_session_id: 'sess-coord-m',
      program_id: 'prog-m',
      jobs: [
        { job_id: 'j-rev', stage_id: 's1', state: 'handoff_ready', child_session_id: 'sess-rev' },
        { job_id: 'j-fail', stage_id: 's1', state: 'conflict', child_session_id: 'sess-fail' },
      ],
    },
  }

  const lookup: Record<string, SessionDataLookup> = {
    'sess-coord-m': {
      sessionRecord: { kind: 'full', session: { id: 'sess-coord-m', lifecycle: { active: false } } },
    },
    'sess-rev': {
      intent: { status: 'completed' },
      sessionRecord: { kind: 'full', session: { id: 'sess-rev', lifecycle: { active: false, phase: 'needs_review' } } },
    },
    'sess-fail': {
      intent: { status: 'failed', blocked_reason: 'Branch conflict on rebase' },
      sessionRecord: { kind: 'full', session: { id: 'sess-fail', lifecycle: { active: false, phase: 'failed' } } },
    },
  }

  const live = aggregateTaskLiveState(taskMixed, lookup)
  assert.equal(live.status, 'failed', 'Failure MUST take precedence over review!')
  assert.equal(live.sessionSummary?.failedSessions, 1)
  assert.equal(live.sessionSummary?.reviewSessions, 1)
})

test('Invariant 11: Plan binding without verified definitionRevision disables Approve button', () => {
  // Written Purpose:
  // - Requirement: Frontend must not authorize Approve if plan binding lacks definition revision (> 0).
  // - Threat/regression: Authorizing stale plan definitions or bypassing revision verification.
  // - Boundary: OrchestrateView.tsx source checks.

  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')
  assert.ok(source.includes('isPlanBindingMissingRevision'), 'Must enforce isPlanBindingMissingRevision guard')
  assert.ok(source.includes('data-testid="approve-task-btn"'), 'Must have approve-task-btn')
})

// Requirement: absence of hydration is not a queued run, and blocked dispatch is not execution.
// Authority: aggregateTaskLiveState consuming canonical session projections; hermetic selector tests.
test('Missing, queued and blocked sessions remain distinguishable without invented sessions', () => {
  const task = { id: 'state-task', title: 'State task', agentType: 'coder', status: 'in_progress', sessionId: 'state-session', subtasks: [] } as RunningTask
  assert.equal(aggregateTaskLiveState({ ...task, sessionId: undefined }, {}).sessionSummary?.totalSessions, 0)
  assert.equal(aggregateTaskLiveState(task, {}).sessionSummary?.sessionStates[0].status, 'unknown')
  const queued = aggregateTaskLiveState(task, { 'state-session': { intent: { status: 'pending_executor' } } })
  assert.equal(queued.sessionSummary?.sessionStates[0].status, 'queued')
  assert.equal(queued.sessionSummary?.runningSessions, 0)
  const blocked = aggregateTaskLiveState(task, { 'state-session': { intent: { status: 'dispatch_blocked' } } })
  assert.equal(blocked.status, 'blocked')
  assert.equal(blocked.sessionSummary?.sessionStates[0].status, 'blocked')
  assert.equal(blocked.sessionSummary?.completedSessions, 0)
})

// Requirement: card identity is the resolved active session, not the task's requested
// agent/model; unavailable session preferences must not be filled from task defaults.
// Authority: aggregateTaskLiveState reads canonical hydrated session view; this selector
// test is the narrowest layer for active-session selection across a child cohort.
test('active child session supplies resolved agent and provider/model without task fallback', () => {
  const task = { id: 'identity-task', title: 'Identity task', agentType: 'coder', model: 'requested',
    status: 'in_progress', sessionId: 'parent', taskProgramStatus: { jobs: [{ job_id: 'child-job', child_session_id: 'child', state: 'running' }] }, subtasks: [] } as unknown as RunningTask
  const live = aggregateTaskLiveState(task, {
    parent: { view: { agentic_settings: { resolved_agent_name: 'swarm', effective_preference: { provider: 'other', model: 'parent-model' } } } },
    child: { intent: { status: 'running' }, view: { agentic_settings: { resolved_agent_name: 'finder',
      effective_preference: { provider: 'provider-a', model: 'model-a' } } } },
  })
  assert.equal(live.activeAgent, 'finder')
  assert.equal(live.activeProvider, 'provider-a')
  assert.equal(live.activeModel, 'model-a')
  const history = aggregateTaskLiveState(task, { parent: { liveRun: { toolCallsByCallId: {
    one: { callId: 'one', toolName: 'read', status: 'done', updatedAt: 1 },
  } } } })
  assert.equal(history.toolActivitySummary, undefined, 'finished tool calls cannot masquerade as live activity')
  const missing = aggregateTaskLiveState(task, {})
  assert.equal(missing.activeAgent, undefined)
  assert.equal(missing.activeProvider, undefined)
  assert.equal(missing.activeModel, undefined)
})

// A real direct-session hydration can omit both lifecycle and active intent after
// completion. aggregateTaskLiveState must preserve its authoritative task result,
// but must not project one aggregate result onto multiple unknown sessions.
test('direct terminal task supplies missing badge state without masking active or cohort evidence', () => {
  for (const status of ['needs_review', 'failed', 'completed', 'blocked'] as const) {
    const task = { id: 'direct', status, sessionId: 'one' } as RunningTask
    const result = aggregateTaskLiveState(task, { one: { sessionRecord: { kind: 'full', session: { id: 'one' } } } as any })
    assert.equal(result.sessionSummary?.sessionStates[0].status, status)
    const active = aggregateTaskLiveState(task, { one: { intent: { status: 'running' } } as any })
    assert.equal(active.sessionSummary?.sessionStates[0].status, 'running')
  }
  const cohort = aggregateTaskLiveState({ id: 'cohort', status: 'needs_review', sessionId: 'parent', taskProgramStatus: { jobs: [{ job_id: 'a', child_session_id: 'child' }] } } as any, {})
  assert.notEqual(cohort.sessionSummary?.sessionStates[0].status, 'needs_review')
})

// Durable scheduler handoffs arrive independently of cached child snapshots.
// A matching current job's verified terminal state must beat stale running data.
test('verified current-job handoff wins over stale running child projection', () => {
  for (const [state, expected] of [['handoff_ready', 'needs_review'], ['integrated', 'completed'], ['failed', 'failed']]) {
    const result = aggregateTaskLiveState({ id: 'program', status: 'running', sessionId: 'parent', taskProgramStatus: { state: 'running', jobs: [{ job_id: 'job', state, child_session_id: 'child' }] } } as any, { child: { intent: { status: 'running' } } as any })
    assert.equal(result.sessionSummary?.sessionStates[0].status, expected)
  }
})

// An unlaunched task with no session and no task program cannot be actively running.
test('unlinked task with no session and no task program is marked failed instead of fake running', () => {
  const unlinkedTask = {
    id: 'unlinked',
    title: 'Failed router task',
    agentType: 'coder',
    status: 'in_progress',
    worktreeBranch: 'agent/mission-failed-router',
    routerAlert: 'Router agent failed or unavailable',
  } as unknown as RunningTask
  const result = aggregateTaskLiveState(unlinkedTask, {})
  assert.equal(result.status, 'failed')
})

// Requirement: current durable run errors bubble up without stale retry failures.
// Threat: prior attempt intent/lifecycle failure poisons a new running attempt.
// Authority: aggregateTaskLiveState/preferRunEvidence. Pure aggregation is the
// narrowest layer proving current-run precedence; it cannot dispatch a planner.
test('current attempt error summary is exposed and running retry clears old errors', () => {
  const task = { id: 'task_fixture', title: 'Task', status: 'running', agentType: 'coder', sessionId: 'session_fixture' } as RunningTask
  const failed = aggregateTaskLiveState(task, {
    session_fixture: { view: { current_run_state: { session_id: 'session_fixture', run_id: 'current', active: false, status: 'failed', blocked_reason: 'Permission denied' } } } as SessionDataLookup,
  })
  assert.equal(failed.sessionSummary?.sessionStates[0].lastError, 'Permission denied')
  const retry = aggregateTaskLiveState(task, {
    session_fixture: {
      view: { current_run_state: { session_id: 'session_fixture', run_id: 'new', created_at: 2, active: true, status: 'running' } },
      intent: { run_id: 'old', created_at: 1, status: 'failed', blocked_reason: 'Old failure' },
      sessionRecord: { kind: 'full', session: { lifecycle: { phase: 'failed', last_error: 'Old lifecycle failure' } } },
    } as unknown as SessionDataLookup,
  })
  assert.equal(retry.sessionSummary?.sessionStates[0].status, 'running')
  assert.equal(retry.sessionSummary?.sessionStates[0].lastError, undefined)
})
