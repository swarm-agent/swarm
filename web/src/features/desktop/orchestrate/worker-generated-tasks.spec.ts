import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { mapBackendTask } from '../state/desktop-projects-state'
import type { RunningTask } from './orchestrate-types'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

// Requirement 1: Backend concurrently adds existing project task fields worker_id,
// worker_run_id, automation_id, and worker_name (exact pinned name).
// mapBackendTask must map those fields into RunningTask while preserving regular tasks.
test('mapBackendTask maps worker_id, worker_run_id, automation_id, and worker_name into RunningTask', () => {
  // Test case 1: Worker-generated automation task with snake_case backend fields
  const rawWorkerTask = {
    id: 'task-worker-1',
    title: 'Automated Nightly CI',
    agent: 'coder',
    status: 'running',
    workspace_path: '/repo/swarm',
    created_at: Date.now() - 120_000,
    worker_id: 'worker_ci_sentinel',
    worker_run_id: 'wrun_98765',
    automation_id: 'auto_ci_job',
    worker_name: 'CI Sentinel',
  }
  const mappedWorkerTask = mapBackendTask(rawWorkerTask)

  assert.equal(mappedWorkerTask.id, 'task-worker-1')
  assert.equal(mappedWorkerTask.title, 'Automated Nightly CI')
  assert.equal(mappedWorkerTask.workerId, 'worker_ci_sentinel')
  assert.equal(mappedWorkerTask.worker_id, 'worker_ci_sentinel')
  assert.equal(mappedWorkerTask.workerRunId, 'wrun_98765')
  assert.equal(mappedWorkerTask.worker_run_id, 'wrun_98765')
  assert.equal(mappedWorkerTask.automationId, 'auto_ci_job')
  assert.equal(mappedWorkerTask.automation_id, 'auto_ci_job')
  assert.equal(mappedWorkerTask.workerName, 'CI Sentinel')
  assert.equal(mappedWorkerTask.worker_name, 'CI Sentinel')

  // Test case 2: Worker-generated automation task with camelCase input compatibility
  const rawCamelWorkerTask = {
    id: 'task-worker-2',
    title: 'Automated Dependency Audit',
    agent: 'finder',
    status: 'completed',
    workerId: 'worker_audit_bot',
    workerRunId: 'wrun_11223',
    automationId: 'auto_audit_job',
    workerName: 'Dependency Auditor',
  }
  const mappedCamelWorkerTask = mapBackendTask(rawCamelWorkerTask)

  assert.equal(mappedCamelWorkerTask.workerId, 'worker_audit_bot')
  assert.equal(mappedCamelWorkerTask.worker_id, 'worker_audit_bot')
  assert.equal(mappedCamelWorkerTask.workerRunId, 'wrun_11223')
  assert.equal(mappedCamelWorkerTask.worker_run_id, 'wrun_11223')
  assert.equal(mappedCamelWorkerTask.automationId, 'auto_audit_job')
  assert.equal(mappedCamelWorkerTask.automation_id, 'auto_audit_job')
  assert.equal(mappedCamelWorkerTask.workerName, 'Dependency Auditor')
  assert.equal(mappedCamelWorkerTask.worker_name, 'Dependency Auditor')

  // Test case 3: Regular non-worker task must NOT have workerId / worker_id
  const rawRegularTask = {
    id: 'task-regular-1',
    title: 'Refactor Auth Middleware',
    agent: 'coder',
    status: 'in_progress',
  }
  const mappedRegularTask = mapBackendTask(rawRegularTask)

  assert.equal(mappedRegularTask.workerId, undefined)
  assert.equal(mappedRegularTask.worker_id, undefined)
  assert.equal(mappedRegularTask.workerRunId, undefined)
  assert.equal(mappedRegularTask.worker_run_id, undefined)
  assert.equal(mappedRegularTask.automationId, undefined)
  assert.equal(mappedRegularTask.automation_id, undefined)
  assert.equal(mappedRegularTask.worker_name, undefined)
  // Regular task workerName gets default generic agent label
  assert.equal(mappedRegularTask.workerName, '@coder Worker')
})

// Requirement 2: All tasks / Worker tasks filter must be based strictly on durable
// worker_id (NOT generic worker_name, as ordinary tasks use workerName).
// Must include tasks with identical worker names but different IDs.
// Must exclude regular tasks even if their workerName contains "Worker" or custom strings.
test('Worker tasks filter strictly isolates durable worker_id from generic workerName', () => {
  const tasks: RunningTask[] = [
    // Regular task 1: default workerName
    mapBackendTask({
      id: 'task-reg-1',
      title: 'Fix Button Hover State',
      agent: 'coder',
      status: 'running',
    }),
    // Regular task 2: custom workerName containing "Worker" and "Bot"
    mapBackendTask({
      id: 'task-reg-2',
      title: 'Analyze Performance Metrics',
      agent: 'finder',
      status: 'completed',
      worker_name: 'Autonomous Worker Bot', // No worker_id!
    }),
    // Worker task A: Worker 1
    mapBackendTask({
      id: 'task-w-1',
      title: 'Nightly Build Step A',
      agent: 'coder',
      status: 'running',
      worker_id: 'worker-build-alpha',
      worker_name: 'Build Sentinel',
      worker_run_id: 'run-1',
      automation_id: 'auto-build',
    }),
    // Worker task B: Worker 2 with IDENTICAL name but DIFFERENT ID
    mapBackendTask({
      id: 'task-w-2',
      title: 'Nightly Build Step B',
      agent: 'coder',
      status: 'completed',
      worker_id: 'worker-build-beta',
      worker_name: 'Build Sentinel', // Identical worker_name!
      worker_run_id: 'run-2',
      automation_id: 'auto-build',
    }),
    // Worker task C: Designer worker
    mapBackendTask({
      id: 'task-w-3',
      title: 'Generate Assets',
      agent: 'designer',
      status: 'needs_review',
      worker_id: 'worker-designer-1',
      worker_name: 'Asset Synthesizer',
    }),
  ]

  // Filter function matching OrchestrateView logic:
  // worker filter predicate: Boolean(t.workerId?.trim() || t.worker_id?.trim())
  const filterBySource = (sourceFilter: 'all' | 'worker') => {
    if (sourceFilter === 'all') return tasks
    return tasks.filter((t) => Boolean(t.workerId?.trim() || t.worker_id?.trim()))
  }

  // 1. All tasks filter includes all 5 tasks
  const allTasks = filterBySource('all')
  assert.equal(allTasks.length, 5)

  // 2. Worker tasks filter includes exactly the 3 worker-generated tasks
  const workerTasks = filterBySource('worker')
  assert.equal(workerTasks.length, 3)

  // 3. Regular tasks are strictly excluded even with "Worker" in workerName
  assert.ok(!workerTasks.some((t) => t.id === 'task-reg-1'), 'Regular task 1 must be excluded')
  assert.ok(!workerTasks.some((t) => t.id === 'task-reg-2'), 'Regular task 2 with custom workerName must be excluded')

  // 4. Both worker tasks with identical name "Build Sentinel" but different IDs are included
  const buildSentinelTasks = workerTasks.filter((t) => t.workerName === 'Build Sentinel')
  assert.equal(buildSentinelTasks.length, 2, 'Both tasks with identical worker_name must be included')
  assert.equal(buildSentinelTasks[0].workerId, 'worker-build-alpha')
  assert.equal(buildSentinelTasks[1].workerId, 'worker-build-beta')

  // 5. Worker task with different agentType (designer) is also included
  assert.ok(workerTasks.some((t) => t.id === 'task-w-3' && t.agentType === 'designer'))

  // 6. Combined with status filtering
  const runningWorkerTasks = workerTasks.filter((t) => t.status === 'running')
  assert.equal(runningWorkerTasks.length, 1)
  assert.equal(runningWorkerTasks[0].id, 'task-w-1')

  // 7. Combined with search query on worker ID
  const searchByWorkerId = workerTasks.filter((t) =>
    (t.workerId || '').toLowerCase().includes('beta')
  )
  assert.equal(searchByWorkerId.length, 1)
  assert.equal(searchByWorkerId[0].id, 'task-w-2')
})

// Requirement 3: Tasks view UI structure verification in OrchestrateView.tsx:
// - Absolutely NO separate worker overview boxes/ticker above task list (TasksDurableWorkersSection / Active Project Automations removed).
// - All tasks / Worker tasks filter controls present and wired.
// - Filter applies across all 5 layout variants (matrix, kanban, fleet, split, timeline).
// - Worker-generated task rows, cards, and details display exact worker tag and canonical link.
// - Workers tab and pending proposal review controls preserved.
test('OrchestrateView structural invariants: no worker overview panels, unified task list, exact tags and links', () => {
  const orchestrateSourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(orchestrateSourcePath, 'utf8')

  // Invariant A: Bad panel behavior removed
  assert.ok(!source.includes('TasksDurableWorkersSection'), 'TasksDurableWorkersSection must NOT be defined or rendered')
  assert.ok(!source.includes('<WorkerTaskActivity'), 'WorkerTaskActivity must NOT be rendered in Tasks view')
  assert.ok(!source.includes('Active Project Automations'), 'Legacy Active Project Automations ticker/cards must NOT be rendered')
  assert.ok(!source.includes('Manage Fleet in Workers Hub'), 'Manage Fleet in Workers Hub ticker link must NOT be rendered')

  // Invariant B: All tasks / Worker tasks filter buttons exist with testids
  const toolbar = fs.readFileSync(path.join(__dirname, 'task-list-toolbar.tsx'), 'utf8')
  assert.ok(toolbar.includes("'filter-all-tasks'"), 'Must provide All scope filter button')
  assert.ok(toolbar.includes("'filter-worker-tasks'"), 'Must provide Workers scope filter button')
  assert.ok(toolbar.includes("label: 'Workers'"), 'Scope labels omit counts; status chips own counts')
  assert.ok(source.includes('taskSourceFilter'), 'Must maintain taskSourceFilter state')

  // Invariant C: Filter predicate is based on durable worker_id, not generic workerName
  assert.ok(
    source.includes('Boolean(t.workerId?.trim() || t.worker_id?.trim())') ||
    source.includes('Boolean(t.workerId || t.worker_id)'),
    'Filter must check durable workerId / worker_id'
  )

  // Invariant D: Filter applies to all 5 layout variants
  // 1. Matrix renders filteredTasks
  assert.ok(source.includes('filteredTasks.map((t) => {'), 'Matrix must render filteredTasks')
  // 2. Kanban derives columns from filteredTasks
  assert.ok(source.includes('const colTasks = filteredTasks.filter('), 'Kanban colTasks must filter from filteredTasks')
  // 3. Fleet renders filteredTasks
  assert.ok(source.includes('Project Tasks ({filteredTasks.length})'), 'Fleet must show filteredTasks count')
  assert.ok(source.includes('filteredTasks.map((task) => ('), 'Fleet must render filteredTasks')
  // 4. Split renders filteredTasks
  assert.ok(source.includes('Tasks ({filteredTasks.length})'), 'Split must show filteredTasks count')
  assert.ok(source.includes('filteredTasks.map((t) => {'), 'Split must render filteredTasks')
  // 5. Timeline renders filteredTasks
  assert.ok(source.includes('Activity Stream ({filteredTasks.length})'), 'Timeline must show filteredTasks count')
  assert.ok(source.includes('filteredTasks.map((t) => ('), 'Timeline must render filteredTasks')

  // Invariant E: Exact worker tags and links displayed on task rows, cards, and details
  assert.ok(source.includes('data-testid="worker-tag"'), 'Must render worker-tag for worker-generated tasks')
  assert.ok(source.includes('data-testid="worker-link"'), 'Must render worker-link for worker-generated tasks')
  assert.ok(source.includes('data-testid="worker-task-spec"'), 'Must render worker-task-spec in task details')
  assert.ok(source.includes('swarmWorkerLink'), 'Must link to worker detail via canonical swarmWorkerLink')
  assert.ok(source.includes('swarmWorkerHref'), 'Must provide canonical worker href via swarmWorkerHref')

  // Invariant F: Preserves Workers tab and pending worker proposal banner
  assert.ok(source.includes('<WorkerHub workspaceSlug={workspaceSlug} initialWorkerId={workerDetailId || routeWorkerId}'), 'Workers tab Hub preserved')
  assert.ok(source.includes('Pending Worker Proposal for'), 'Pending worker proposal banner at top of project preserved')
})
