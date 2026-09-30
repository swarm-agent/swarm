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
  const launch = source.slice(source.indexOf('const launchIntegrationRepair'), source.indexOf('// Integrate / Promote'))
  assert.match(launch, /repairFlights.current.has/)
  assert.match(launch, /repairFlights.current.add/)
  assert.match(launch, /finally[\s\S]*repairFlights.current.delete/)
  assert.match(launch, /repairOperations.current.get/)
  assert.match(launch, /startNewDesktopV3Session/)
  assert.match(launch, /agentName: 'swarm'/)
  assert.match(launch, /mode: 'on'/)
  assert.match(launch, /Repair launch failed/)
  assert.match(launch, /sessionId: result.sessionId/)
})
