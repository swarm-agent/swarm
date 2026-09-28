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
  assert.deepEqual(sids3, [
    'sess-coder-a',
    'sess-coder-b-new',
    'sess-coder-b-old',
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

  const liveCohortFailed = aggregateTaskLiveState(taskProgramTask, mixedFailedLookup)
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
    { ...taskProgramTask, isIntegrated: false },
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
