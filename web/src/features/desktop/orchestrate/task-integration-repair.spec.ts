import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { createTaskIntegrationBatchController, integrationSkipReason } from './task-integration-batch'
import { acquireIntegrationBatch, createTaskIntegrationController, taskIntegrationKey, taskIntegrationRequest, type TaskIntegrationResult } from './task-integration-operation'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

const project = { id: 'repair-project', name: 'Project' } as ProjectSummary
function task(id = 'task', state = 'candidate_work'): RunningTask {
  return {
    id, title: id, status: 'needs_review', agentType: 'coder', workspaceTarget: 'local', elapsed: '', subtasks: [],
    sessionId: `session-${id}`, activeAttemptId: 'initial', revision: 1,
    sourceWorkspacePath: '/fixture/repository', sourceWorkspaceId: 'workspace', sourceWorkspaceGeneration: 1,
    baseCommit: 'base', worktreeBranch: `agent/${id}`, baseBranch: 'dev', gitStatus: 'diverged',
    deliveryAssessment: {
      task_id: id, task_revision: 1, session_id: `session-${id}`, attempt_id: 'initial',
      workspace_id: 'workspace', workspace_generation: 1, base_oid: 'base', source_oid: `source-${id}`,
      target_oid: 'target', source_branch: `agent/${id}`, target_branch: 'dev', state,
      freshness: 'observed', candidate_commits: 1, reason: '', reason_code: '',
      allowed_actions: state === 'candidate_work' ? ['integrate'] : ['recover_integrate'],
    },
  }
}
const success = (row: RunningTask): TaskIntegrationResult => ({ status: 'integrated', task: { id: row.id, session_id: row.sessionId!, is_integrated: true } })

// Purpose: resolving the retained controller conflict must preserve taskDelivery
// authority over stale local/legacy success, rather than allowing the new batch
// gate to bypass it. These controller calls prove zero writes and no false result.
test('canonical stale assessment overrides legacy and local success', { timeout: 5000 }, async () => {
  const controller = createTaskIntegrationController(), row = task()
  await controller.run(project, row, async () => success(row), () => {})
  const stale = { ...row, revision: 2, isIntegrated: true, status: 'completed' }
  let writes = 0
  const result = await controller.run(project, stale, async () => { writes++; return success(stale) }, () => {})
  assert.equal(result.status, 'skipped')
  assert.match(integrationSkipReason(project.id, stale)!, /stale/i)
  assert.equal(writes, 0)
})

// Purpose: individual recover-integrate keeps the captured target's exact recovery
// proof, including restart reconciliation. Bulk writes must never implicitly
// request recovery. The shared controller is the narrowest authority seam.
test('recovery remains individual and requires an exact recovery receipt', { timeout: 5000 }, async () => {
  const row = task('recovery', 'history_rewritten')
  row.integration = { state: 'in_progress', recovery_base: 'base', session_id: row.sessionId, attempt_id: row.activeAttemptId }
  assert.match(integrationSkipReason(project.id, row)!, /individual Recover & integrate/)
  const controller = createTaskIntegrationController()
  let writes = 0
  const lease = acquireIntegrationBatch()!
  assert.ok(lease)
  try {
    assert.equal((await controller.run(project, row, async () => { writes++; return success(row) }, () => {}, lease.token)).status, 'skipped')
    assert.equal(writes, 0)
  } finally { lease.release() }
  const request = taskIntegrationRequest(project.id, row)
  assert.equal(request.url, '/v3/projects/repair-project/tasks/recovery/recover-integrate')
  assert.deepEqual(request.body, { session_id: row.sessionId, source_branch: row.worktreeBranch, target_branch: 'dev', revision: 1, attempt_id: 'initial', source_head: 'source-recovery', target_head: 'target' })
  const receipt = (status: 'recovered' | 'equivalent'): TaskIntegrationResult => ({ status, task: {
    id: row.id, session_id: row.sessionId!, active_attempt_id: row.activeAttemptId, is_integrated: false,
    integration: { state: status, source_head: 'source-recovery', recovery_base: 'base', recovered_head: 'recovered-tip', resulting_target_head: 'target-tip' },
  } })
  for (const invalid of [success(row), { ...receipt('recovered'), task: { ...receipt('recovered').task!, active_attempt_id: 'wrong-attempt' } }]) {
    let refreshes = 0
    assert.equal((await controller.run(project, row, async () => invalid, () => { refreshes++ })).status, 'failed')
    assert.equal(refreshes, 1)
    assert.equal(controller.get(taskIntegrationKey(project.id, row)).phase, 'error')
  }
  for (const status of ['recovered', 'equivalent'] as const) {
    const operation = createTaskIntegrationController()
    assert.equal((await operation.run(project, row, async () => receipt(status), () => {})).status, status)
    assert.equal(operation.get(taskIntegrationKey(project.id, row)).phase, 'success')
    assert.equal(row.isIntegrated, undefined, 'recovery never claims original commit ancestry')
  }
})

// Purpose: the repaired queue rechecks canonical source OIDs and uses the latest
// revision only on the same captured lane. It must skip recovery/unavailable
// assessments and never retarget a changed source while earlier writes finish.
test('batch respects delivery eligibility, source fence and refreshed revision', { timeout: 5000 }, async () => {
  const rows = [task('a'), task('b'), task('recover', 'history_rewritten'), { ...task('stale'), revision: 2 }]
  const latest = { ...rows[0], revision: 2, deliveryAssessment: { ...rows[0].deliveryAssessment!, task_revision: 2 } }
  const changed = { ...rows[1], deliveryAssessment: { ...rows[1].deliveryAssessment!, source_oid: 'changed-source' } }
  const batch = createTaskIntegrationBatchController(), operations = createTaskIntegrationController()
  const requests: ReturnType<typeof taskIntegrationRequest>[] = []
  let refreshes = 0
  await batch.run(project, rows, id => id === 'a' ? latest : id === 'b' ? changed : rows.find(row => row.id === id),
    (row, token) => operations.run(project, row, async () => { requests.push(taskIntegrationRequest(project.id, row)); return success(row) }, () => {}, token),
    () => { refreshes++ })
  assert.equal(requests.length, 1)
  assert.equal(requests[0].url, '/v3/projects/repair-project/tasks/a/integrate')
  assert.equal(requests[0].body.revision, 2)
  assert.equal(requests[0].body.source_head, 'source-a')
  assert.equal(requests[0].body.target_branch, 'dev')
  assert.deepEqual(batch.getSnapshot().get(project.id)?.entries.map(entry => entry.status), ['integrated', 'not_attempted', 'skipped', 'skipped'])
  assert.equal(refreshes, 1)
})

// Purpose: exercise OrchestrateView's actual repaired batch callback rather than
// a source-string assertion. Transport is captured in memory: no live Git. This
// proves canonical request construction/CAS fields and the navigation fence at
// the production caller boundary; browser specs separately exercise the toolbar.
test('view batch callback uses canonical request and refuses navigation retargeting', { timeout: 5000 }, async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const closure = source.slice(source.indexOf('const handleIntegrateSelected ='), source.indexOf('// Integrate / Promote task commits'))
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${closure}; return handleIntegrateSelected`, { compilerOptions: { target: ts.default.ScriptTarget.ES2020 } }).outputText
  const rows = [task('a'), task('b')]
  const navigation = { current: { generation: 1 } }
  const requests: { url: string; body: unknown }[] = [], refreshes: string[] = []
  const scope = {
    selectedProject: project, managementBusy: false, batchNavigation: navigation,
    batchSourceRef: { current: { projectId: project.id, tasks: rows } },
    taskIntegrationBatches: createTaskIntegrationBatchController(), taskIntegrationOperations: createTaskIntegrationController(), taskIntegrationRequest,
    requestJson: async (url: string, options: { body: string }) => {
      requests.push({ url, body: JSON.parse(options.body) })
      navigation.current.generation++
      return success(rows[0])
    },
    desktopProjects: { invalidate: (id: string) => { refreshes.push(id) } },
  }
  const submit = new Function(...Object.keys(scope), compiled)(...Object.values(scope)) as (rows: RunningTask[]) => Promise<void>
  await submit(rows)
  assert.deepEqual(requests, [{ url: taskIntegrationRequest(project.id, rows[0]).url, body: taskIntegrationRequest(project.id, rows[0]).body }])
  assert.deepEqual(refreshes, [project.id])
  assert.deepEqual(scope.taskIntegrationBatches.getSnapshot().get(project.id)?.entries.map(entry => entry.status), ['integrated', 'not_attempted'])
})
