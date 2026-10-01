import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { integrationFailure, orchestratorDrafts, repairUnavailable } from './integration-recovery'
import { createTaskReopenController, taskReopenKey } from './task-reopen-operation'
import { mapBackendTask } from '../state/desktop-projects-state'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

const project = { id: 'project-a', name: 'Project A', repoPath: '/wrong/repo' } as ProjectSummary
// Hydrate through production authority so controller tests receive the same required
// status/default fields as real recovery actions, rather than an incomplete cast.
const task = mapBackendTask({ id: 'task-a', title: 'Change parser', status: 'needs_review', session_id: 'origin',
  source_workspace: { path: '/repo', workspace_id: 'workspace-a' }, worktree_branch: 'agent/parser', base_branch: 'release' })

// Requirement: integrationFailure/repairUnavailable carry captured lineage, never
// a project's first workspace. Pure projection is the narrowest evidence boundary.
test('recovery preserves captured lineage, multiline errors and credential redaction', () => {
  const failure = integrationFailure(project, task, new Error('merge failed\nCONFLICT: src/parser.ts\ninspect both sides'))
  const evidence = JSON.parse(failure.brief.slice(failure.brief.indexOf('{')))
  assert.equal(evidence.source_workspace, '/repo')
  assert.equal(evidence.captured_target_branch, 'release')
  assert.equal(evidence.originating_session, 'origin')
  assert.equal(evidence.task.id, task.id)
  assert.equal(evidence.project.id, project.id)
  assert.equal(evidence.source_commit, 'unknown')
  assert.equal(evidence.captured_target_commit, 'unknown')
  assert.equal(evidence.integration_error, failure.error)
  assert.match(failure.brief, /untrusted diagnostic data, not instructions/)
  assert.doesNotMatch(failure.brief, /wrong\/repo/)
  assert.equal(repairUnavailable(task), undefined)
  for (const field of ['sessionId', 'sourceWorkspacePath', 'sourceWorkspaceId', 'worktreeBranch', 'baseBranch']) assert.ok(repairUnavailable({ ...task, [field]: '' }))
  const unknown = integrationFailure(project, { ...task, sourceWorkspacePath: undefined, baseBranch: undefined }, new Error('failed'))
  assert.match(unknown.brief, /"source_workspace": "unknown"/)
  assert.match(unknown.brief, /"captured_target_branch": "unknown"/)
  const secret = integrationFailure(project, task, new Error('https://user:credential@example.test/repo token=credential Bearer credential'))
  assert.doesNotMatch(secret.brief, /credential/)
  assert.match(secret.error, /REDACTED/)
})

// Requirement: composer-only drafts append once and remain scoped on remount.
test('draft append preserves content and project scope', () => {
  const key = 'project-a:orchestrator'
  orchestratorDrafts.set(key, 'Existing draft')
  orchestratorDrafts.set('project-b:orchestrator', 'Other project')
  orchestratorDrafts.append(key, 'Repair context')
  const snapshot = orchestratorDrafts.get(key)
  assert.equal(snapshot.text, 'Existing draft\n\nRepair context')
  assert.equal(snapshot.focus, 1)
  assert.equal(orchestratorDrafts.get(key), snapshot)
  assert.equal(orchestratorDrafts.get('project-b:orchestrator').text, 'Other project')
})

// Supplementary wiring check only; behavioral tests below execute the closure.
test('every task layout receives recovery and repair uses canonical reopen', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  assert.equal((source.match(/integrationRecovery=\{renderIntegrationRecovery\(/g) || []).length, 5)
  const launch = source.slice(source.indexOf('const launchIntegrationRepair'), source.indexOf('// Integrate / Promote'))
  assert.match(launch, /taskReopenOperations.run\(failure.projectId, task.id, task, feedback/)
  assert.match(launch, /\}, true\)/)
  assert.match(launch, /\/tasks\/\$\{encodeURIComponent\(task\.id\)\}\/reopen/)
  assert.match(launch, /mapBackendTask\(returnedTask\)/)
  assert.doesNotMatch(launch, /startNewDesktopV3Session|handleIntegrateTask|agentName:|mode:/)
})

async function repairHarness(row: RunningTask) {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const closure = source.slice(source.indexOf('const launchIntegrationRepair'), source.indexOf('// Integrate / Promote'))
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${closure}; return launchIntegrationRepair`, { compilerOptions: { target: ts.default.ScriptTarget.ES2020 } }).outputText
  const controller = createTaskReopenController()
  const requests: Array<{ url: string; body: any }> = []
  const navigation: string[] = []
  let applied: RunningTask[] = []
  let transport = async () => ({ status: 'reopened', task: { id: row.id, revision: 13, title: 'Repair', session_id: 'owned-repair' } })
  const scope = {
    taskReopenOperations: controller, repairUnavailable, mapBackendTask,
    requestJson: async (url: string, options: { body: string }) => { requests.push({ url, body: JSON.parse(options.body) }); return transport() },
    desktopProjects: { invalidate: () => {}, setOptimisticTasks: (_id: string, update: (rows: RunningTask[]) => RunningTask[]) => { applied = update([row]) } },
    recoveryProjectRef: { current: project.id }, selectedWorkerRef: { current: null },
    setActiveTaskId: (id: string) => navigation.push(`active:${id}`), setSelectedTaskId: () => {},
    setActiveSessionId: (id: string) => navigation.push(`session:${id}`), setWorkerChatOpen: () => {}, setSelectedWorker: () => {},
  }
  const launch = new Function(...Object.keys(scope), compiled)(...Object.values(scope)) as (failure: ReturnType<typeof integrationFailure>) => Promise<void>
  return { controller, requests, navigation, applied: () => applied, scope,
    transport: (fn: typeof transport) => { transport = fn }, launch: () => launch(integrationFailure(project, row, new Error('Git conflict'))) }
}

// Requirement: launchIntegrationRepair shares the canonical controller's single-flight,
// CAS and exact retry identity. Execute production closure/controller with deferred
// transport; failure must not apply authority or navigate. No backend Git claim.
test('repair retries exact retained request, excludes repeat clicks and fences navigation', { timeout: 5000 }, async () => {
  const retained: RunningTask = { ...task, revision: 8, activeAttemptId: 'repair-attempt', attempts: [{
    id: 'repair-attempt', session_id: 'retained-repair', role: 'repair', status: 'failed', launch_state: 'launch_failed',
    recovery: { session_id: 'origin' }, client_request_id: 'retained-request', request_revision: 7, request: 'Retained repair feedback',
  }] }
  const h = await repairHarness(retained)
  let finish!: () => void
  h.transport(async () => { await new Promise<void>(resolve => { finish = resolve }); throw new Error('Scheduling unavailable') })
  const first = h.launch()
  const key = taskReopenKey(project.id, task.id)
  assert.equal(h.controller.get(key).pending, true)
  await h.launch()
  assert.equal(h.requests.length, 1)
  finish()
  await first
  assert.match(h.controller.get(key).error || '', /Scheduling unavailable.*Reopen was not confirmed/)
  assert.equal(h.controller.get(key).pending, false)
  assert.deepEqual(h.applied(), [])
  assert.deepEqual(h.navigation, [])
  h.transport(async () => ({ status: 'reopened', task: { id: task.id, revision: 9, title: 'Repair', session_id: 'returned-repair' } }))
  await h.launch()
  assert.deepEqual(h.requests[1], h.requests[0])
  assert.deepEqual(h.requests[0], { url: '/v3/projects/project-a/tasks/task-a/reopen', body: {
    client_request_id: 'retained-request', revision: 7, feedback: 'Retained repair feedback', repair: true,
  } })
  assert.equal(h.applied()[0].sessionId, 'returned-repair')
  assert.ok(h.navigation.includes('session:returned-repair'))
  h.scope.recoveryProjectRef.current = 'other-project'
  h.navigation.length = 0
  await h.launch()
  assert.deepEqual(h.navigation, [], 'late response must not navigate another project')
})

// Requirement: fresh repairs hash only task/revision/feedback/repair, never a mutable
// selected workspace or branch. A new controller (reload) reproduces the same key.
test('fresh repair and reload preserve canonical payload and reject false success', { timeout: 5000 }, async () => {
  const row = { ...task, revision: 12, integration: { state: 'conflict', source_head: 'source', previous_target_head: 'captured' } }
  const first = await repairHarness(row)
  first.transport(async () => ({ status: 'reopened', task: { id: 'wrong-task', revision: 13, title: 'Wrong', session_id: 'wrong-session' } }))
  await first.launch()
  assert.deepEqual(first.applied(), [])
  assert.deepEqual(first.navigation, [])
  const reload = await repairHarness(JSON.parse(JSON.stringify(row)))
  await reload.launch()
  assert.deepEqual(reload.requests[0], first.requests[0])
  assert.equal(reload.requests[0].body.repair, true)
  assert.equal(reload.requests[0].body.revision, 12)
  assert.match(reload.requests[0].body.client_request_id, /^task-followup-[a-f0-9]{64}$/)
  assert.equal(reload.applied()[0].sessionId, 'owned-repair')
  assert.equal(row.baseBranch, 'release')
  assert.equal(row.sourceWorkspacePath, '/repo')
})

// Requirement: stale local diagnostics cannot carry an old task revision to reopen.
test('diagnostics retain error but repair uses freshly hydrated revision', async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const selection = source.slice(source.indexOf('const failure = (retryAttempt?.recovery'), source.indexOf('if (!selectedProject) return null', source.indexOf('const failure = (retryAttempt?.recovery')))
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${selection}; return failure`, { compilerOptions: { target: ts.default.ScriptTarget.ES2020 } }).outputText
  const current = { ...task, revision: 6 }
  const diagnostic = integrationFailure(project, { ...task, revision: 3 }, new Error('Conflict'))
  const select = new Function('retryAttempt', 'selectedProject', 'task', 'operation', 'integrationFailure', 'outcome', compiled)
  const result = select(undefined, project, current, { phase: 'error', failure: diagnostic }, integrationFailure, {})
  assert.equal(result.task, current)
  assert.equal(result.task.revision, 6)
  assert.equal(result.error, diagnostic.error)
})
