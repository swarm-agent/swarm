import test from 'node:test'
import ts from 'typescript'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  validateSelectedTaskForContext,
  buildSelectedTaskMessageEnvelope,
  buildSelectedTaskMessageMetadata,
  parseSelectedTaskMessageEnvelope,
  reconcileSelectedTaskId,
  type SelectedTaskContextSnapshot,
} from './orchestrate-task-helpers'
import type { RunningTask, ProjectSummary } from './orchestrate-types'
import { getDesktopSessionStopTarget, resolveDesktopChatRouteFromSession, sessionMetadataString } from '../chat/services/chat-routing'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

test('validateSelectedTaskForContext rejects missing project and missing task', () => {
  // Written test purpose:
  // - Requirement: Context forwarding requires an authenticated project and valid task.
  // - Threat/regression prevented: Submitting unanchored messages with missing project/task IDs.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const validTask: RunningTask = {
    id: 'task-100',
    title: 'Update OAuth Login',
    agentType: 'coder',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '1m',
    subtasks: [],
    revision: 1,
  }
  const validProject: ProjectSummary = {
    id: 'proj-1',
    name: 'Swarm Platform',
    slug: 'swarm-platform',
    description: 'Core platform',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: ['/workspace/repo'],
    activeWorkersCount: 1,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  // Missing project
  const resNoProject = validateSelectedTaskForContext(null, validTask, [validTask])
  assert.equal(resNoProject.valid, false)
  assert.equal(resNoProject.reason, 'missing_project')

  const resEmptyProject = validateSelectedTaskForContext({ id: '  ' }, validTask, [validTask])
  assert.equal(resEmptyProject.valid, false)
  assert.equal(resEmptyProject.reason, 'missing_project')

  // Missing task
  const resNoTask = validateSelectedTaskForContext(validProject, null, [validTask])
  assert.equal(resNoTask.valid, false)
  assert.equal(resNoTask.reason, 'missing_task')

  const resEmptyTask = validateSelectedTaskForContext(validProject, { id: '' } as RunningTask, [validTask])
  assert.equal(resEmptyTask.valid, false)
  assert.equal(resEmptyTask.reason, 'missing_task')
})

test('validateSelectedTaskForContext enforces exact task ID matching and never title inference', () => {
  // Written test purpose:
  // - Requirement: Exact task matching by ID; never infer task identity from task titles or partial strings.
  // - Threat/regression prevented: Title collision or fuzzy match accidentally hijacking context of another task.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-main',
    name: 'Swarm',
    slug: 'swarm',
    description: 'Swarm repository',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const existingTask: RunningTask = {
    id: 'task-real-123',
    title: 'Refactor Authentication Flow',
    agentType: 'coder',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '5m',
    subtasks: [],
    revision: 2,
  }

  // Attempting to validate a task with identical title but different ID (cross-project or spoofed)
  const candidateWithMatchingTitle: RunningTask = {
    id: 'task-foreign-999',
    title: 'Refactor Authentication Flow', // Identical title!
    agentType: 'coder',
    status: 'in_progress',
    workspaceTarget: '/workspace/other-repo',
    elapsed: '1m',
    subtasks: [],
    revision: 2,
  }

  const res = validateSelectedTaskForContext(project, candidateWithMatchingTitle, [existingTask])
  assert.equal(res.valid, false)
  assert.equal(res.reason, 'cross_project')
  assert.ok(
    res.error?.includes('missing from active project'),
    'Must reject when ID is not in active project, regardless of identical title'
  )
})

test('validateSelectedTaskForContext rejects stale task revisions', () => {
  // Written test purpose:
  // - Requirement: Selected task context must reject stale task states if revision has incremented.
  // - Threat/regression prevented: Chat referring to outdated task revision r1 when backend is at r2.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-1',
    name: 'Platform',
    slug: 'platform',
    description: '',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const liveTask: RunningTask = {
    id: 'task-auth',
    title: 'Fix CSRF Token Validation',
    agentType: 'coder',
    status: 'needs_review',
    workspaceTarget: '/workspace/repo',
    elapsed: '10m',
    subtasks: [],
    revision: 3, // Live revision is 3
  }

  const staleTask: RunningTask = {
    ...liveTask,
    revision: 2, // UI selection has stale revision 2
  }

  const res = validateSelectedTaskForContext(project, staleTask, [liveTask])
  assert.equal(res.valid, false)
  assert.equal(res.reason, 'stale_revision')
  assert.ok(res.error?.includes('revision r2 is stale (project task is at r3)'))
})

test('validateSelectedTaskForContext snapshots verified task with plan binding definition revision', () => {
  // Written test purpose:
  // - Requirement: Snapshot on send must capture exact plan definition revision and plan ID.
  // - Threat/regression prevented: Sending ambiguous or unversioned plan task references.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-apollo',
    name: 'Apollo',
    slug: 'apollo',
    description: '',
    repoPath: '/workspace/apollo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 1,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const taskWithPlan: RunningTask = {
    id: 'task-plan-42',
    title: 'Architecture Review & Re-plan',
    agentType: 'plan',
    status: 'in_progress',
    workspaceTarget: '/workspace/apollo',
    elapsed: '3m',
    subtasks: [],
    sessionId: 'session-child-99',
    revision: 2,
    planBinding: {
      planId: 'plan-arch-v1',
      definitionRevision: 4,
      sessionId: 'session-child-99',
    },
  }

  const res = validateSelectedTaskForContext(project, taskWithPlan, [taskWithPlan])
  assert.equal(res.valid, true)
  assert.ok(res.snapshot)
  assert.equal(res.snapshot.projectId, 'proj-apollo')
  assert.equal(res.snapshot.taskId, 'task-plan-42')
  assert.equal(res.snapshot.taskTitle, 'Architecture Review & Re-plan')
  assert.equal(res.snapshot.agentType, 'plan')
  assert.equal(res.snapshot.status, 'in_progress')
  assert.equal(res.snapshot.sessionId, 'session-child-99')
  assert.equal(res.snapshot.taskRevision, 2)
  assert.equal(res.snapshot.planId, 'plan-arch-v1')
  assert.equal(res.snapshot.planDefinitionRevision, 4)
  assert.ok(res.snapshot.snapshotTimestamp > 0)
})

test('buildSelectedTaskMessageEnvelope formats explicit user-message envelope and parseSelectedTaskMessageEnvelope extracts it', () => {
  // Written test purpose:
  // - Requirement: Format explicit user-message envelope without system prompt/schema changes.
  // - Threat/regression prevented: Ignored backend metadata leaving AI model with zero task context.
  // - Symbols: buildSelectedTaskMessageEnvelope & parseSelectedTaskMessageEnvelope.
  const snapshot: SelectedTaskContextSnapshot = {
    projectId: 'proj-v3',
    projectName: 'Desktop V3',
    taskId: 'task-77',
    taskTitle: 'Repair WebSocket Reconnect Race',
    agentType: 'coder',
    status: 'blocked',
    sessionId: 'sess-ws-1',
    taskRevision: 2,
    planId: 'plan-ws-fix',
    planDefinitionRevision: 1,
    snapshotTimestamp: Date.now(),
  }

  const userPrompt = 'Why is this task blocked? Please inspect the mutex lock order.'
  const enveloped = buildSelectedTaskMessageEnvelope(snapshot, userPrompt)

  assert.ok(enveloped.startsWith('[Task Context: task-77]'))
  assert.ok(enveloped.includes('project_id: proj-v3'))
  assert.ok(enveloped.includes('task_id: task-77'))
  assert.ok(enveloped.includes('task_title: Repair WebSocket Reconnect Race'))
  assert.ok(enveloped.includes('task_status: blocked'))
  assert.ok(enveloped.includes('task_revision: 2'))
  assert.ok(enveloped.includes('agent_type: coder'))
  assert.ok(enveloped.includes('session_id: sess-ws-1'))
  assert.ok(enveloped.includes('plan_id: plan-ws-fix'))
  assert.ok(enveloped.includes('plan_definition_revision: 1'))
  assert.ok(enveloped.includes('---\nWhy is this task blocked? Please inspect the mutex lock order.'))

  // Verify roundtrip parsing
  const parsed = parseSelectedTaskMessageEnvelope(enveloped)
  assert.equal(parsed.hasEnvelope, true)
  assert.equal(parsed.taskId, 'task-77')
  assert.equal(parsed.projectId, 'proj-v3')
  assert.equal(parsed.taskRevision, 2)
  assert.equal(parsed.planId, 'plan-ws-fix')
  assert.equal(parsed.planDefinitionRevision, 1)
  assert.equal(parsed.agentType, 'coder')
  assert.equal(parsed.taskStatus, 'blocked')
  assert.equal(parsed.userPrompt, userPrompt)

  // Plain message without envelope parses cleanly
  const plainParsed = parseSelectedTaskMessageEnvelope('Just a normal message without context')
  assert.equal(plainParsed.hasEnvelope, false)
  assert.equal(plainParsed.userPrompt, 'Just a normal message without context')
})

test('buildSelectedTaskMessageMetadata generates exact tracking metadata without invented backend fields', () => {
  // Written test purpose:
  // - Requirement: Generate audit metadata with exact IDs, not invented backend fields.
  // - Threat/regression prevented: Schema validation failures or silent metadata drops.
  // - Symbols: buildSelectedTaskMessageMetadata.
  const snapshot: SelectedTaskContextSnapshot = {
    projectId: 'proj-1',
    taskId: 'task-1',
    taskTitle: 'Fix Bug',
    status: 'in_progress',
    taskRevision: 1,
    sessionId: 'sess-1',
    snapshotTimestamp: Date.now(),
  }

  const meta = buildSelectedTaskMessageMetadata(snapshot, { orchestrate_view: true })
  assert.equal(meta.orchestrate_view, true)
  assert.equal(meta.project_id, 'proj-1')
  assert.equal(meta.task_id, 'task-1')
  assert.equal(meta.selected_task_id, 'task-1')
  assert.equal(meta.task_revision, 1)
  assert.equal(meta.task_session_id, 'sess-1')
})

test('OrchestrateView forwards explicit task attachments independently of detail selection', () => {
  // Written test purpose:
  // - Requirement: only explicitly attached tasks reach OrchestratorChatSidebar context;
  //   detail selection must not attach context or open execution chat.
  //   Preserve canonical approval/revision/reopen/complete paths.
  // - Threat/regression prevented: selecting details silently changes conversation context.
  // - Symbols: OrchestrateView.tsx, OrchestratorChatSidebar, OrchestratorChatComposer.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Detail selection remains separate from explicit conversation attachments.
  assert.ok(source.includes('selectedTask={null}'), 'Detail selection must not become composer context')
  const ast = ts.createSourceFile('OrchestrateView.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const view = ast.statements.find((node): node is ts.FunctionDeclaration =>
    ts.isFunctionDeclaration(node) && node.name?.text === 'OrchestrateView')
  assert.ok(view?.body)
  const selection = view.body.statements.flatMap(node =>
    ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
    .find(node => node.name.getText(ast) === 'handleSelectTask')
  assert.ok(selection?.initializer)
  const compiled = ts.transpileModule(`const select = ${selection.initializer.getText(ast)};`, {
    compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.None },
  }).outputText
  const selected: string[] = []
  const forbidden = () => assert.fail('Detail selection must not change session or attached context')
  new Function('setSelectedTaskId', 'setActiveSessionId', 'setActiveTaskId', 'setAttachedTaskIds',
    `${compiled}; select({ id: 'details-only', sessionId: 'execution-session' });`)(
    (id: string) => selected.push(id), forbidden, forbidden, forbidden)
  assert.deepEqual(selected, ['details-only'])
  assert.ok(
    source.includes('handleDeselectTask = useCallback('),
    'OrchestrateView must provide handleDeselectTask callback'
  )
  assert.ok(
    source.includes("setSelectedTaskId('')"),
    'handleDeselectTask must reset selectedTaskId'
  )

  // OrchestratorChatSidebar receives selectedTask and allTasks
  assert.ok(
    source.includes('attachedTasks={tasks.filter(task => attachedTaskIds.includes(task.id))}'),
    'OrchestratorChatSidebar receives only explicitly attached task context, not detail selection'
  )
  assert.ok(
    source.includes('allTasks={tasks}'),
    'OrchestratorChatSidebar must receive allTasks for validation'
  )
  assert.ok(
    source.includes('onDeselectTask={handleDeselectTask}'),
    'OrchestratorChatSidebar must receive onDeselectTask'
  )

  // OrchestratorChatComposer implementation
  assert.ok(
    source.includes('function OrchestratorChatComposer('),
    'OrchestrateView must define OrchestratorChatComposer'
  )
  assert.ok(
    source.includes('validateSelectedTaskForContext('),
    'Composer must validate selected task before sending'
  )
  assert.ok(
    source.includes('buildSelectedTaskMessageEnvelope('),
    'Composer must build explicit user-message envelope'
  )
  assert.ok(
    source.includes('buildSelectedTaskMessageMetadata('),
    'Composer must build tracking metadata'
  )
  assert.ok(
    source.includes('createDesktopV3ExistingMessageOperation('),
    'Composer must create canonical existing message operation'
  )
  assert.ok(
    source.includes('continueDesktopV3Conversation('),
    'Composer must submit message via continueDesktopV3Conversation'
  )

  // Composer UI elements
  assert.ok(
    source.includes('data-testid="orchestrator-chat-composer"'),
    'Composer must have orchestrator-chat-composer testid'
  )
  assert.ok(
    source.includes('data-testid="composer-task-context-badge"'),
    'Composer must render context badge when task is active'
  )
  assert.ok(
    source.includes('data-testid="orchestrator-chat-input"'),
    'Composer must have orchestrator-chat-input textarea'
  )
  assert.ok(
    source.includes('data-testid="orchestrator-chat-send-btn"'),
    'Composer must have orchestrator-chat-send-btn'
  )
  assert.ok(
    source.includes('data-testid="composer-clear-task-context-btn"'),
    'Composer must allow clearing selected task context'
  )

  // Selected Task Context Banner in sidebar header
  assert.ok(
    source.includes('data-testid="selected-task-context-banner"'),
    'Sidebar header must render selected task context banner'
  )
  assert.ok(
    source.includes('data-testid="clear-selected-task-context-btn"'),
    'Sidebar header must render clear context button'
  )

  // Execution chat requires a linked session; task context is a separate action.
  assert.ok(
    source.includes('data-testid="task-card-chat-btn"'),
    'Task card must have task-card-chat-btn'
  )
  assert.ok(
    source.includes('title="Open execution session"') && source.includes('taskSessionId && onOpenChat && ('),
    'Execution chat must be explicitly labeled and require a linked session'
  )

  assert.ok(source.includes('onAskOrchestrator'), 'Task context retains its separate Orchestrator action')

  // Canonical task action paths are preserved
  assert.ok(source.includes('handleApproveTask'), 'handleApproveTask must be preserved')
  assert.ok(source.includes('/approve'), 'Approve endpoint must be preserved')
  assert.ok(source.includes('handleRefineTask'), 'handleRefineTask must be preserved')
  assert.ok(source.includes('/refine'), 'Refine endpoint must be preserved')
  assert.ok(source.includes('handleReopenTask'), 'handleReopenTask must be preserved')
  assert.ok(source.includes('/reopen'), 'Reopen endpoint must be preserved')
  assert.ok(source.includes('handleCompleteTask'), 'handleCompleteTask must be preserved')
  assert.ok(source.includes('/complete'), 'Complete endpoint must be preserved')
  assert.ok(source.includes('handleRedeployJob'), 'handleRedeployJob must be preserved')
  assert.ok(source.includes('/program:redeploy-job'), 'Redeploy job endpoint must be preserved')
})

test('validateSelectedTaskForContext rejects stale task session ID', () => {
  // Written test purpose:
  // - Requirement: Selected task context must reject stale task states if session ID has changed.
  // - Threat/regression prevented: Chat referring to previous session when backend has allocated a new session.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-1',
    name: 'Platform',
    slug: 'platform',
    description: '',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const liveTask: RunningTask = {
    id: 'task-auth',
    title: 'Fix CSRF Token Validation',
    agentType: 'coder',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '10m',
    subtasks: [],
    revision: 2,
    sessionId: 'session-live-2',
  }

  const staleSessionTask: RunningTask = {
    ...liveTask,
    sessionId: 'session-old-1',
  }

  const res = validateSelectedTaskForContext(project, staleSessionTask, [liveTask])
  assert.equal(res.valid, false)
  assert.equal(res.reason, 'stale_task')
  assert.ok(res.error?.includes('session session-old-1 is stale'))
})

test('validateSelectedTaskForContext rejects stale plan definition revision', () => {
  // Written test purpose:
  // - Requirement: Selected task context must reject stale plan states if plan definition revision incremented.
  // - Threat/regression prevented: Submitting prompt against outdated plan schema or plan step order.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-1',
    name: 'Platform',
    slug: 'platform',
    description: '',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const liveTask: RunningTask = {
    id: 'task-plan-sync',
    title: 'Multi-stage Task',
    agentType: 'plan',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '5m',
    subtasks: [],
    revision: 1,
    planBinding: {
      planId: 'plan-123',
      definitionRevision: 3,
    },
  }

  const stalePlanTask: RunningTask = {
    ...liveTask,
    planBinding: {
      planId: 'plan-123',
      definitionRevision: 2,
    },
  }

  const res = validateSelectedTaskForContext(project, stalePlanTask, liveTask)
  assert.equal(res.valid, false)
  assert.equal(res.reason, 'stale_revision')
  assert.ok(res.error?.includes('plan definition revision r2 is stale'))
})

test('validateSelectedTaskForContext supports authoritative single task verification and fails closed on missing task ID', () => {
  // Written test purpose:
  // - Requirement: Validate against authoritative task directly from canonical API; fail closed with task ID in error.
  // - Threat/regression prevented: Missing selected task ID silently dropping context into a general message.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-1',
    name: 'Platform',
    slug: 'platform',
    description: '',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const authoritativeTask: RunningTask = {
    id: 'task-authoritative',
    title: 'Real Authoritative Task',
    agentType: 'coder',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '1m',
    subtasks: [],
    revision: 1,
  }

  // Exact ID match with single authoritative task
  const resValid = validateSelectedTaskForContext(project, authoritativeTask, authoritativeTask)
  assert.equal(resValid.valid, true)
  assert.equal(resValid.snapshot?.taskId, 'task-authoritative')

  // Mismatched task ID (cross-project or missing from project)
  const candidateDifferentId: RunningTask = {
    ...authoritativeTask,
    id: 'task-other-id',
  }
  const resMismatch = validateSelectedTaskForContext(project, candidateDifferentId, authoritativeTask)
  assert.equal(resMismatch.valid, false)
  assert.equal(resMismatch.reason, 'cross_project')
  assert.ok(resMismatch.error?.includes('task-other-id'))
  assert.ok(resMismatch.error?.includes('missing from active project'))
})

test('OrchestratorChatComposer implements permissions, attachments, running states, and authoritative send validation', () => {
  // Written test purpose:
  // - Requirement: OrchestratorChatComposer must support file attachments, running states (active run stop/busy),
  //   pending permissions notifications, and canonical API task validation before send.
  // - Threat/regression prevented: Stripped custom composer lacking media, run abort, permissions, or stale validation.
  // - Symbols: OrchestratorChatComposer in OrchestrateView.tsx.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Permissions awareness
  assert.ok(
    source.includes('data-testid="composer-pending-permissions-notice"'),
    'Composer must render pending permissions notice when requests await approval'
  )

  // Attachments support
  assert.ok(
    source.includes('data-testid="orchestrator-chat-attach-btn"'),
    'Composer must render attach button'
  )
  assert.ok(
    source.includes('uploadDesktopV3MediaAsset'),
    'Composer must upload attachments via uploadDesktopV3MediaAsset'
  )
  assert.ok(
    source.includes('admitComposerFile'),
    'Composer must admit files via admitComposerFile'
  )

  // Running states and stop action
  assert.ok(
    source.includes('data-testid="orchestrator-chat-stop-btn"'),
    'Composer must render stop button when agent is running'
  )
  assert.ok(
    source.includes('stopSessionV3Run'),
    'Composer must stop active run via stopSessionV3Run'
  )

  // Authoritative API task fetch before send & fail-closed error
  assert.ok(
    source.includes('/v3/projects/${encodeURIComponent(project.id)}/tasks/${encodeURIComponent(effectiveTaskId)}'),
    'Composer must fetch authoritative task from canonical API before send'
  )
  assert.ok(
    source.includes('Context cannot be attached'),
    'Composer must fail closed with explicit message when task ID is not found on server'
  )
})

test('buildSelectedTaskMessageEnvelope and documentation make no server security claims', () => {
  // Written test purpose:
  // - Requirement: Documentation and comments must be honest; client message envelope must not claim
  //   to be a server security boundary or tamper-proof guarantee.
  // - Threat/regression prevented: False security claims regarding client-side message framing.
  // - Symbols: orchestrate-task-helpers.ts.
  const helpersPath = path.join(__dirname, 'orchestrate-task-helpers.ts')
  const helpersSource = fs.readFileSync(helpersPath, 'utf8')

  assert.ok(
    helpersSource.includes('not a server security boundary') || helpersSource.includes('does not constitute a server security boundary'),
    'Helpers documentation must honestly state that client envelope is not a server security boundary'
  )
})

test('validateSelectedTaskForContext rejects mismatched plan ID between caller and authoritative task', () => {
  // Written test purpose:
  // - Requirement: Selected task context must verify plan ID matches authoritative state.
  // - Threat/regression prevented: Stale or unaligned plan references attached to orchestrator prompts.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-1',
    name: 'Platform',
    slug: 'platform',
    description: '',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const liveTask: RunningTask = {
    id: 'task-plan-id-check',
    title: 'Plan ID Task',
    agentType: 'plan',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '1m',
    subtasks: [],
    revision: 1,
    planBinding: {
      planId: 'plan-live-abc',
      definitionRevision: 1,
    },
  }

  // Caller specifies different plan ID
  const mismatchedPlanTask: RunningTask = {
    ...liveTask,
    planBinding: {
      planId: 'plan-caller-xyz',
      definitionRevision: 1,
    },
  }

  const res = validateSelectedTaskForContext(project, mismatchedPlanTask, liveTask)
  assert.equal(res.valid, false)
  assert.equal(res.reason, 'stale_task')
  assert.ok(res.error?.includes('plan plan-caller-xyz is stale'))

  // Caller has no plan binding while live task has one
  const noPlanTask: RunningTask = {
    ...liveTask,
    planBinding: undefined,
  }
  const resNoPlan = validateSelectedTaskForContext(project, noPlanTask, liveTask)
  assert.equal(resNoPlan.valid, false)
  assert.equal(resNoPlan.reason, 'stale_task')

  // Live task has no plan binding while caller specifies one
  const liveWithoutPlan: RunningTask = { ...liveTask, planBinding: undefined }
  const resLiveNoPlan = validateSelectedTaskForContext(project, liveTask, liveWithoutPlan)
  assert.equal(resLiveNoPlan.valid, false)
  assert.equal(resLiveNoPlan.reason, 'stale_task')
})

test('validateSelectedTaskForContext preserves definitionRevision 0 and never defaults missing values silently to 0', () => {
  // Written test purpose:
  // - Requirement: Snapshot and validation must treat definitionRevision 0 as valid distinct revision,
  //   and must reject missing definitionRevision against 0 rather than silently coercing undefined to 0.
  // - Threat/regression prevented: Silent revision collision between unversioned and initial-revision tasks.
  // - Symbols: validateSelectedTaskForContext in orchestrate-task-helpers.ts.
  const project: ProjectSummary = {
    id: 'proj-1',
    name: 'Platform',
    slug: 'platform',
    description: '',
    repoPath: '/workspace/repo',
    branch: 'dev',
    gitStatus: 'clean',
    linkedWorkspaces: [],
    activeWorkersCount: 0,
    pendingDeliverablesCount: 0,
    runningTasksCount: 1,
  }

  const taskRevZero: RunningTask = {
    id: 'task-rev-0',
    title: 'Initial Plan Task',
    agentType: 'plan',
    status: 'in_progress',
    workspaceTarget: '/workspace/repo',
    elapsed: '10s',
    subtasks: [],
    revision: 1,
    planBinding: {
      planId: 'plan-init',
      definitionRevision: 0,
    },
  }

  // Matching revision 0 succeeds and snapshot retains 0 (not undefined or 1)
  const resZero = validateSelectedTaskForContext(project, taskRevZero, taskRevZero)
  assert.equal(resZero.valid, true)
  assert.equal(resZero.snapshot?.planDefinitionRevision, 0)

  // Revision 0 vs Revision 1 fails with stale_revision
  const taskRevOne: RunningTask = {
    ...taskRevZero,
    planBinding: {
      planId: 'plan-init',
      definitionRevision: 1,
    },
  }
  const resMismatch = validateSelectedTaskForContext(project, taskRevZero, taskRevOne)
  assert.equal(resMismatch.valid, false)
  assert.equal(resMismatch.reason, 'stale_revision')
  assert.ok(resMismatch.error?.includes('r0 is stale (project task plan is at r1)'))

  // Missing definitionRevision vs Revision 0 is detected as stale_revision, never silently matched
  const taskMissingRev: RunningTask = {
    ...taskRevZero,
    planBinding: {
      planId: 'plan-init',
    } as any,
  }
  const resMissing = validateSelectedTaskForContext(project, taskMissingRev, taskRevZero)
  assert.equal(resMissing.valid, false)
  assert.equal(resMissing.reason, 'stale_revision')
})

test('DesktopV3UserMessage extracts and renders task context badge from user message envelope', () => {
  // Written test purpose:
  // - Requirement: DesktopV3UserMessage must detect enveloped task context headers and render
  //   a dedicated context badge, showing clean user prompt text without raw envelope metadata.
  // - Threat/regression prevented: Visual clutter from raw YAML/bracketed metadata leaked into conversation pane.
  // - Symbols: DesktopV3UserMessage in desktop-v3-existing-conversation-pane.tsx.
  const panePath = path.join(__dirname, '../chat/components/desktop-v3-existing-conversation-pane.tsx')
  const paneSource = fs.readFileSync(panePath, 'utf8')
  assert.ok(
    paneSource.includes('data-testid="user-message-task-context-badge"'),
    'DesktopV3UserMessage must render user-message-task-context-badge for enveloped messages'
  )
  assert.ok(
    paneSource.includes('Task Context:'),
    'DesktopV3UserMessage must display Task Context label'
  )
})

test('reconcileSelectedTaskId never auto-selects or implicitly defaults to first task', () => {
  // Written test purpose:
  // - Requirement: Reconciling task selection against a non-empty task list when currentSelectedTaskId
  //   is empty or whitespace must return ''. Never implicitly auto-select the first task or any default task.
  // - Threat/regression prevented: Ordinary orchestrator chat being hijacked by auto-selected tasks
  //   (such as completed image or coder tasks) without operator selection.
  // - Symbols: reconcileSelectedTaskId in orchestrate-task-helpers.ts.
  // - Narrowest layer: Direct pure function unit test verifying task selection lifecycle across task arrays.
  const tasks = [
    { id: 'task-c4f309d71fe91aa3', title: 'Panda Riding a Giraffe' },
    { id: 'task-auth-2', title: 'Refactor Authentication' },
    { id: 'task-ui-3', title: 'Design System Header' },
  ]

  // Empty string selection preserves empty string (no implicit first-task selection)
  assert.equal(reconcileSelectedTaskId('', tasks), '')
  assert.equal(reconcileSelectedTaskId('   ', tasks), '')
  assert.equal(reconcileSelectedTaskId(null as any, tasks), '')
  assert.equal(reconcileSelectedTaskId(undefined as any, tasks), '')
})

test('reconcileSelectedTaskId preserves deliberate user selection when task exists', () => {
  // Written test purpose:
  // - Requirement: When an operator explicitly selects a task, and that task exists in the current project's
  //   task list, reconcileSelectedTaskId must preserve that exact task ID.
  // - Threat/regression prevented: Dropping intentional task context while the operator is discussing or refining a specific task.
  // - Symbols: reconcileSelectedTaskId in orchestrate-task-helpers.ts.
  // - Narrowest layer: Direct pure function unit test verifying deliberate selection retention.
  const tasks = [
    { id: 'task-100', title: 'Task One' },
    { id: 'task-200', title: 'Task Two' },
    { id: 'task-300', title: 'Task Three' },
  ]

  assert.equal(reconcileSelectedTaskId('task-100', tasks), 'task-100')
  assert.equal(reconcileSelectedTaskId('task-200', tasks), 'task-200')
  assert.equal(reconcileSelectedTaskId('task-300', tasks), 'task-300')
  // Trims whitespace around selected ID
  assert.equal(reconcileSelectedTaskId('  task-200  ', tasks), 'task-200')
})

test('reconcileSelectedTaskId clears stale selection across project and deletion boundaries', () => {
  // Written test purpose:
  // - Requirement: When a selected task is deleted, pruned, or when switching to an empty or different
  //   project where the task does not exist, reconcileSelectedTaskId must return ''.
  // - Threat/regression prevented: Stale task context leaking into subsequent messages or cross-project context pollution.
  // - Symbols: reconcileSelectedTaskId in orchestrate-task-helpers.ts.
  // - Narrowest layer: Direct pure function unit test verifying deleted task IDs and mismatched project task lists.
  const activeTasks = [
    { id: 'task-live-1', title: 'Live Task' },
    { id: 'task-live-2', title: 'Another Live Task' },
  ]

  // Task was deleted from current project -> clears to '' (never falls back to task-live-1)
  assert.equal(reconcileSelectedTaskId('task-deleted', activeTasks), '')
  // Stale selection from a previous project -> clears to ''
  assert.equal(reconcileSelectedTaskId('task-from-old-project', activeTasks), '')
  // Current project has zero tasks -> clears to ''
  assert.equal(reconcileSelectedTaskId('task-live-1', []), '')
})

test('reconcileSelectedTaskId guarantees deselect / clear is stable and never resurrected', () => {
  // Written test purpose:
  // - Requirement: Once cleared/deselected (currentSelectedTaskId is ''), subsequent reconciliation
  //   with updated task lists must remain '' and never resurrect or auto-select tasks[0].
  // - Threat/regression prevented: Deselect button failing because an effect or helper immediately restores the first task.
  // - Symbols: reconcileSelectedTaskId in orchestrate-task-helpers.ts.
  // - Narrowest layer: Direct pure function unit test verifying multi-turn state transitions.
  const initialTasks = [{ id: 'task-img-1', title: 'Initial Image Task' }]
  // Step 1: User explicitly selected task
  let currentSelection = reconcileSelectedTaskId('task-img-1', initialTasks)
  assert.equal(currentSelection, 'task-img-1')

  // Step 2: User clicks deselect -> selection reset to ''
  currentSelection = ''

  // Step 3: Tasks update (e.g. background sync adds new tasks)
  const updatedTasks = [
    { id: 'task-img-1', title: 'Initial Image Task' },
    { id: 'task-coder-2', title: 'New Coder Task' },
  ]
  const reconciledAfterUpdate = reconcileSelectedTaskId(currentSelection, updatedTasks)
  assert.equal(reconciledAfterUpdate, '', 'Must remain empty and never resurrect task-img-1')
})

test('OrchestrateView enforces explicit-only task selection lifecycle and clears across boundaries', () => {
  // Written test purpose:
  // - Requirement: OrchestrateView.tsx must never implicitly auto-select tasks[0], must use reconcileSelectedTaskId,
  //   must clear selectedTaskId on deselect, back-to-orchestrator, orchestrator session reset, context clear, and project switch,
  //   while preserving intentional deliberate selection and task action routes.
  // - Threat/regression prevented: Silent auto-selection of tasks[0] hijacking orchestrator composer, broken deselect action,
  //   and stale selection retention across session/project boundaries.
  // - Symbols: OrchestrateView.tsx, reconcileSelectedTaskId, handleDeselectTask, handleBackToOrchestrator, handleOrchestratorSessionReset.
  // - Narrowest layer: Source contract inspection proving production invariants and absence of auto-select anti-patterns.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Invariant 1: No implicit auto-selection of tasks[0]
  assert.ok(
    !source.includes('setSelectedTaskId(tasks[0].id)'),
    'OrchestrateView must NOT auto-select tasks[0].id'
  )
  assert.ok(
    source.includes('reconcileSelectedTaskId(selectedTaskId, tasks)'),
    'OrchestrateView must use reconcileSelectedTaskId to manage selection lifecycle'
  )

  // Invariant 2: Split view inspector must not fall back to liveTasks[0]
  assert.ok(
    !source.includes('liveTasks.find((t) => t.id === selectedTaskId) || liveTasks[0]'),
    'selectedTaskForSplit must NOT fall back to liveTasks[0]'
  )

  // Invariant 3: Explicit clear / deselect resets selectedTaskId
  assert.ok(
    source.includes('handleDeselectTask = useCallback('),
    'OrchestrateView must provide handleDeselectTask'
  )
  assert.ok(
    source.includes("setSelectedTaskId('')"),
    'handleDeselectTask must reset selectedTaskId to empty string'
  )

  // Invariant 4: Session boundary - back to orchestrator resets selectedTaskId
  assert.ok(
    source.includes('const handleBackToOrchestrator = () => {'),
    'OrchestrateView must define handleBackToOrchestrator'
  )
  assert.ok(
    source.includes("const handleBackToOrchestrator = () => {\n    setActiveTaskId(null)\n    setSelectedTaskId('')"),
    'handleBackToOrchestrator must clear selectedTaskId when returning to orchestrator'
  )

  // Invariant 5: Session boundary - orchestrator session reset clears selectedTaskId
  assert.ok(
    source.includes("const handleOrchestratorSessionReset = useCallback((newSessionId: string) => {\n    setActiveSessionId(newSessionId)\n    setSelectedTaskId('')"),
    'handleOrchestratorSessionReset must clear selectedTaskId'
  )

  // Invariant 6: Sidebar clear-context action triggers onDeselectTask
  assert.ok(
    source.includes("const handleClearContext = async () => {\n    if (!project?.id || clearingContext) return\n    setClearingContext(true)\n    setClearSuccess(false)\n    try {\n      onDeselectTask?.()"),
    'handleClearContext must call onDeselectTask to clear composer state immediately'
  )

  // Invariant 7: Project boundary - project switch clears selectedTaskId and activeTaskId
  assert.ok(
    source.includes('[selectedProjectId]'),
    'OrchestrateView must have an effect dependent on selectedProjectId'
  )

  // Invariant 8: Preserves intentional task action paths
  assert.ok(source.includes('handleApproveTask'), 'handleApproveTask must be preserved')
  assert.ok(source.includes('handleRefineTask'), 'handleRefineTask must be preserved')
  assert.ok(source.includes('handleReopenTask'), 'handleReopenTask must be preserved')
  assert.ok(source.includes('handleCompleteTask'), 'handleCompleteTask must be preserved')
  assert.ok(source.includes('handleIntegrateTask'), 'handleIntegrateTask must be preserved')
  assert.ok(source.includes('handleRedeployJob'), 'handleRedeployJob must be preserved')
})

// Requirement: Orchestrator stop uses the same session-derived target as chat, never the UI alias "host".
// Regression: missing session props or differing runtime/authority IDs must not misroute the stop POST.
// Authority: OrchestratorChatComposer.handleStopRun, resolveDesktopV3StopRunRequest, and chat-routing.
// Layer: execute the production handler/resolver extracted from their ASTs, replacing only cache/network
// boundaries to keep this Node test independent of the full Desktop browser dependency graph.
// This proves request construction and failure/no-request behavior, not backend authorization or rendering.
test('OrchestratorChatComposer stops the session-derived runtime with cached fallback and no host alias', async () => {
  const source = fs.readFileSync(path.join(__dirname, 'OrchestrateView.tsx'), 'utf8')
  const ast = ts.createSourceFile('OrchestrateView.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const composer = ast.statements.find((node): node is ts.FunctionDeclaration =>
    ts.isFunctionDeclaration(node) && node.name?.text === 'OrchestratorChatComposer')
  assert.ok(composer?.body)
  const declarations = composer.body.statements.flatMap((node) =>
    ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
  const cache = declarations.find((node) => node.name.getText(ast) === 'cacheSession')
  assert.ok(cache?.initializer)
  assert.match(cache.initializer.getText(ast), /useDesktopV3CacheSelector\(/)
  // Execute the canonical selector: only full cache records expose session routing.
  const selectorCode = ts.transpileModule(`const cached = ${cache.initializer.getText(ast)};`, {
    compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.None },
  }).outputText
  const selectCache = (record: unknown) => new Function('useDesktopV3CacheSelector', 'useCallback', 'sessionId',
    `${selectorCode}; return cached;`)(
    (selector: (state: unknown) => unknown) => selector({ sessionsById: { fixture: record } }),
    (callback: unknown) => callback, 'fixture')
  const fullSession = { id: 'fixture', metadata: { swarm_v3_runtime_swarm_id: 'runtime' } }
  assert.equal(selectCache({ kind: 'full', session: fullSession }), fullSession)
  assert.equal(selectCache({ kind: 'summary', session: fullSession }), null)
  assert.equal(selectCache(undefined), null)
  const stop = declarations.find((node) => node.name.getText(ast) === 'handleStopRun')
  assert.ok(stop?.initializer)
  assert.doesNotMatch(stop.initializer.getText(ast), /targetSwarmId\s*:/)

  const paneSource = fs.readFileSync(path.join(__dirname, '../chat/components/desktop-v3-existing-conversation-pane.tsx'), 'utf8')
  const paneAst = ts.createSourceFile('pane.tsx', paneSource, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const resolver = paneAst.statements.find((node): node is ts.FunctionDeclaration =>
    ts.isFunctionDeclaration(node) && node.name?.text === 'resolveDesktopV3StopRunRequest')
  assert.ok(resolver)
  const compiled = ts.transpileModule(
    `${resolver.getText(paneAst).replace(/^export\s+/, '')}\nconst handleStopRun = ${stop.initializer.getText(ast)};`,
    { compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.None } }
  ).outputText
  const calls: unknown[] = []
  const errors: string[] = []
  const invoke = new Function(
    'session', 'cacheSession', 'activeRun', 'sessionId', 'resolveDesktopChatRouteFromSession',
    'getDesktopSessionStopTarget', 'sessionMetadataString', 'stopSessionV3Run', 'setSendError',
    `${compiled}\nreturn handleStopRun();`
  )
  const session = (metadata: Record<string, unknown>) => ({ metadata } as SessionSnapshot)
  const passed = session({ swarm_v3_runtime_swarm_id: 'node-runtime', swarm_v3_authority_host_swarm_id: 'node-authority' })
  const cached = session({ swarm_v3_authority_host_swarm_id: 'node-cached' })
  const run = async (prop: SessionSnapshot | null | undefined, cache: SessionSnapshot | null, activeRun: { runId: string } | null, fail = false) => {
    await invoke(prop, cache, activeRun, 'session-stop-fixture', resolveDesktopChatRouteFromSession,
      getDesktopSessionStopTarget, sessionMetadataString,
      async (id: string, request: unknown) => {
        calls.push({ id, request })
        if (fail) throw new Error('Stop rejected')
      }, (error: string) => errors.push(error))
  }
  await run(passed, cached, { runId: 'run-prop' })
  await run(undefined, cached, { runId: 'run-cache' })
  assert.deepEqual(calls, [
    { id: 'session-stop-fixture', request: { runId: 'run-prop', targetSwarmId: 'node-runtime' } },
    { id: 'session-stop-fixture', request: { runId: 'run-cache', targetSwarmId: 'node-cached' } },
  ])
  await run(null, null, { runId: 'run-missing-target' })
  assert.equal(calls.length, 2, 'Missing routing metadata must not send a stop request')
  assert.match(errors[0], /requires a selected primary swarm_id/)
  await run(passed, cached, null)
  assert.equal(calls.length, 2, 'No active run must not send a stop request')
  await run(passed, cached, { runId: 'run-rejected' }, true)
  assert.equal(calls.length, 3)
  assert.equal(errors[1], 'Stop rejected', 'Network failures must remain visible')
})
