import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { integrationFailure, orchestratorDrafts, repairUnavailable } from './integration-recovery'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

// Requirement: integration recovery carries only captured lineage and diagnostic data,
// never substitutes a project's first workspace or target. Boundary: integrationFailure
// and repairUnavailable; pure tests are the narrowest proof of evidence construction.
const project = { id: 'project-a', name: 'Project A', repoPath: '/wrong/repo' } as ProjectSummary
const task = { id: 'task-a', title: 'Change parser', sessionId: 'origin', sourceWorkspacePath: '/repo', sourceWorkspaceId: 'workspace-a', worktreeBranch: 'agent/parser', baseBranch: 'release' } as RunningTask

test('recovery brief preserves exact lineage, complete multiline error and unknown commits', () => {
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
})

test('missing lineage disables launch without inventing target or source', () => {
  for (const field of ['sessionId', 'sourceWorkspacePath', 'sourceWorkspaceId', 'worktreeBranch', 'baseBranch']) {
    assert.ok(repairUnavailable({ ...task, [field]: '' }))
  }
  const failure = integrationFailure(project, { ...task, sourceWorkspacePath: undefined, baseBranch: undefined }, new Error('failed'))
  assert.match(failure.brief, /"source_workspace": "unknown"/)
  assert.match(failure.brief, /"captured_target_branch": "unknown"/)
})

test('diagnostic credential forms are not forwarded', () => {
  const failure = integrationFailure(project, task, new Error('https://user:credential@example.test/repo token=credential Bearer credential'))
  assert.doesNotMatch(failure.brief, /credential/)
  assert.match(failure.error, /REDACTED/)
})

// Requirement: copy is a reviewable append, isolated by project/session and stable on
// rerender/remount. Boundary: composer-only draft store; no network or session writes.
test('draft append preserves content, does not repeat on read, and stays scoped', () => {
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

// Wiring checks supplement behavior tests; they do not prove Git execution or launch.
test('all task cards receive recovery, only integration catches create it, launch is guarded', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  assert.equal((source.match(/integrationRecovery=\{renderIntegrationRecovery\(/g) || []).length, 5)
  const controller = readFileSync(new URL('./task-integration-operation.ts', import.meta.url), 'utf8')
  assert.equal((controller.match(/integrationFailure\(project, capturedTask, error\)/g) || []).length, 1)
  assert.doesNotMatch(source, /setIntegrationFailures/)
  // Legacy receipt recovery is an explicit integration retry, not a second
  // reopen that invents missing provenance; joined API/Git tests prove admission.
  assert.match(source, /onClick=\{\(\) => void handleIntegrateTask\(task\.id\)\}>Retry integration to refresh verified receipt/)
  const launch = source.slice(source.indexOf('const launchIntegrationRepair'), source.indexOf('// Integrate / Promote'))
  assert.match(launch, /repairFlights.current.has/)
  assert.match(launch, /repairFlights.current.add/)
  assert.match(launch, /finally[\s\S]*repairFlights.current.delete/)
  // Task-linked follow-up owns request identity/history, not generic session launch.
  assert.match(launch, /task\.attempts\?\.find[\s\S]*task\.activeAttemptId[\s\S]*launch_state !== 'launched'/)
  assert.match(launch, /client_request_id: active\.client_request_id, revision: active\.request_revision, feedback: active\.request, repair: true/)
  assert.match(launch, /projectTaskFollowupPayload\(failure\.projectId, task\.id, task\.revision \?\? 0/)
  assert.match(launch, /\/tasks\/\$\{encodeURIComponent\(task\.id\)\}\/reopen/)
  assert.match(launch, /method: 'POST'[\s\S]*JSON\.stringify\(body\)/)
  assert.match(launch, /Missing task-linked repair session/)
  assert.match(launch, /desktopProjects\.invalidate\(failure\.projectId\)/)
  assert.match(launch, /Repair launch failed[\s\S]*Retry reuses the same session request/)
  assert.match(launch, /sessionId: result\.task\.session_id/)
  assert.match(launch, /setActiveSessionId\(result\.task\.session_id\)/)
  assert.match(launch, /recoveryProjectRef\.current !== failure\.projectId/)
  assert.doesNotMatch(launch, /startNewDesktopV3Session|repairOperations|agentName:|mode:/)
})

// Requirement: launchIntegrationRepair must use retained task history/request identity,
// reject duplicate flights, surface failure, and navigate only to the returned linked
// session. Execute the actual closure with injected transport/state: the narrowest
// layer proving UI ordering, not durable backend reservation or Git execution.
test('task-linked repair retries retained identity and isolates duplicate flights and navigation', { timeout: 5000 }, async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const closure = source.slice(source.indexOf('const launchIntegrationRepair'), source.indexOf('// Integrate / Promote'))
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${closure}; return launchIntegrationRepair`, {
    compilerOptions: { target: ts.default.ScriptTarget.ES2020 },
  }).outputText
  const repairTask = { ...task, revision: 8, activeAttemptId: 'repair-attempt', attempts: [{
    id: 'repair-attempt', session_id: 'retained-repair-session', role: 'repair', status: 'failed',
    launch_state: 'launch_failed', recovery: { session_id: 'retained-repair-session' },
    client_request_id: 'retained-request', request_revision: 7, request: 'Retained repair feedback',
  }] } as RunningTask
  const failure = integrationFailure(project, repairTask, new Error('Git conflict'))
  let state: Record<string, any> = {}
  const flights = { current: new Set<string>() }
  const requests: Array<{ url: string; body: any }> = []
  const navigation: string[] = []
  let reject = true
  let finish: (() => void) | undefined
  const scope = {
    repairFlights: flights, repairUnavailable, repairStates: state,
    setRepairStates: (update: (previous: typeof state) => typeof state) => { state = update(state) },
    projectTaskFollowupPayload: () => { assert.fail('retained recovery must not allocate a new request') },
    requestJson: async (url: string, options: { body: string }) => {
      requests.push({ url, body: JSON.parse(options.body) })
      await new Promise<void>(resolve => { finish = resolve })
      if (reject) throw new Error('Scheduling unavailable')
      return { task: { session_id: 'returned-repair-session' } }
    },
    desktopProjects: { invalidate: (id: string) => navigation.push(`invalidate:${id}`) },
    recoveryProjectRef: { current: project.id }, selectedWorkerRef: { current: null },
    setActiveTaskId: (id: string) => navigation.push(`active:${id}`),
    setSelectedTaskId: (id: string) => navigation.push(`selected:${id}`),
    setActiveSessionId: (id: string) => navigation.push(`session:${id}`),
    setWorkerChatOpen: () => {}, setSelectedWorker: () => {},
    redactIntegrationDiagnostic: (message: string) => message,
  }
  const launch = new Function(...Object.keys(scope), compiled)(...Object.values(scope)) as (failure: ReturnType<typeof integrationFailure>) => Promise<void>
  const first = launch(failure)
  assert.equal(state[task.id].loading, true)
  await launch(failure)
  assert.equal(requests.length, 1, 'duplicate flight issues no request')
  finish!()
  await first
  assert.match(state[task.id].error, /Repair launch failed: Scheduling unavailable.*Retry reuses the same session request/)
  assert.equal(flights.current.size, 0)
  assert.deepEqual(navigation, [], 'failed launch never navigates')
  reject = false
  const retry = launch(failure)
  finish!()
  await retry
  assert.deepEqual(requests[1], requests[0], 'retry preserves exact request and revision')
  assert.deepEqual(requests[0], { url: '/v3/projects/project-a/tasks/task-a/reopen', body: {
    client_request_id: 'retained-request', revision: 7, feedback: 'Retained repair feedback', repair: true,
  } })
  assert.equal(state[task.id].sessionId, 'returned-repair-session')
  assert.ok(navigation.includes('session:returned-repair-session'))
  assert.ok(navigation.includes('active:task-a'))
  assert.equal(flights.current.size, 0)
})

// Requirement: patch-equivalent but unmerged integration failures launch through
// the actual UI closure and canonical payload function, never integration retry or
// generic session creation. This runtime test proves transport ordering/identity;
// the joined real-Git API test proves receipt admission and exact-source allocation.
test('unmerged equivalent-patch failure launches repair with canonical fresh payload', { timeout: 5000 }, async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const closure = source.slice(source.indexOf('const launchIntegrationRepair'), source.indexOf('// Integrate / Promote'))
  const payloadSource = readFileSync(new URL('../runtime/project-task-followup.ts', import.meta.url), 'utf8')
  const payloadFunction = payloadSource.slice(payloadSource.indexOf('export async function projectTaskFollowupPayload'), payloadSource.indexOf('export async function reopenProjectTask')).replace('export ', '')
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${payloadFunction}\n${closure}; return launchIntegrationRepair`, {
    compilerOptions: { target: ts.default.ScriptTarget.ES2020 },
  }).outputText
  const freshTask = { ...task, revision: 12, isIntegrated: false, unintegratedCommits: 1, integration: {
    state: 'conflict', source_head: 'original-head', previous_target_head: 'captured-head',
  } } as RunningTask
  const failure = integrationFailure(project, freshTask, new Error('Integration requires original commit ancestry: equivalent patches exist on the captured target, but the source history is unmerged. Launch repair.'))
  const requests: Array<{ url: string; body: any }> = []
  const navigation: string[] = []
  let state: Record<string, any> = {}
  const scope = {
    repairFlights: { current: new Set<string>() }, repairUnavailable, repairStates: state,
    setRepairStates: (update: (previous: typeof state) => typeof state) => { state = update(state) },
    requestJson: async (url: string, options: { body: string }) => {
      requests.push({ url, body: JSON.parse(options.body) })
      return { task: { session_id: 'owned-repair' } }
    },
    desktopProjects: { invalidate: (id: string) => navigation.push(`invalidate:${id}`) },
    recoveryProjectRef: { current: project.id }, selectedWorkerRef: { current: null },
    setActiveTaskId: () => {}, setSelectedTaskId: () => {},
    setActiveSessionId: (id: string) => navigation.push(id),
    setWorkerChatOpen: () => {}, setSelectedWorker: () => {},
    redactIntegrationDiagnostic: (message: string) => message,
  }
  const launch = new Function(...Object.keys(scope), compiled)(...Object.values(scope)) as (failure: ReturnType<typeof integrationFailure>) => Promise<void>
  await launch(failure)
  assert.equal(requests.length, 1)
  assert.equal(requests[0].url, '/v3/projects/project-a/tasks/task-a/reopen')
  assert.equal(requests[0].body.repair, true)
  assert.equal(requests[0].body.revision, 12)
  assert.match(requests[0].body.client_request_id, /^task-followup-[a-f0-9]{64}$/)
  assert.equal(state[task.id].sessionId, 'owned-repair')
  assert.ok(navigation.includes('owned-repair'))
  await launch(failure)
  assert.deepEqual(requests[1], requests[0], 'same selected revision retains deterministic request identity')
})

// Requirement: a failed integration increments the durable task revision; repair
// must select the freshly hydrated row rather than the pre-mutation diagnostic
// snapshot. Execute the actual render-time selection expression without JSX.
test('integration diagnostics retain error but repair uses current task revision', async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const selection = source.slice(source.indexOf('const failure = (retryAttempt?.recovery'), source.indexOf('if (!selectedProject) return null', source.indexOf('const failure = (retryAttempt?.recovery')))
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${selection}; return failure`, {
    compilerOptions: { target: ts.default.ScriptTarget.ES2020 },
  }).outputText
  const staleTask = { ...task, revision: 3 } as RunningTask
  const currentTask = { ...task, revision: 6, integration: { state: 'conflict', source_head: 'original-head', previous_target_head: 'captured-head' } } as RunningTask
  const diagnostic = integrationFailure(project, staleTask, new Error('Integration requires original commit ancestry'))
  const select = new Function('retryAttempt', 'selectedProject', 'task', 'operation', 'integrationFailure', compiled)
  const selected = select(undefined, project, currentTask, { phase: 'error', failure: diagnostic }, integrationFailure)
  assert.equal(selected.task, currentTask)
  assert.equal(selected.task.revision, 6)
  assert.equal(selected.error, diagnostic.error)
  assert.equal(selected.projectId, diagnostic.projectId)
})
