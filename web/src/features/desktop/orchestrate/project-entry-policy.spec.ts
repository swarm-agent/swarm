import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { admittedConversationId, completeAccountOnboarding, legacyHistorySessions, projectConversationBatches } from './project-entry-policy'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'

// Purpose: DesktopOnboardingGate must finish account setup with zero workspaces.
// completeAccountOnboarding is the narrow sequencing boundary: failed completion,
// auth refresh or navigation must never dismiss the vault's onboarding gate.
test('account onboarding opens projects only after authoritative completion and auth refresh', async () => {
  for (const failure of ['', 'finalize', 'auth', 'navigate', 'incomplete']) {
    const calls: string[] = []
    const run = completeAccountOnboarding({
      finalize: async () => { calls.push('finalize'); if (failure === 'finalize') throw Error('finalize'); return { needsOnboarding: failure === 'incomplete' } },
      refreshAuth: async () => { calls.push('auth'); if (failure === 'auth') throw Error('auth') },
      openProjects: async () => { calls.push('navigate'); if (failure === 'navigate') throw Error('navigate') },
      complete: () => { calls.push('complete') },
    })
    if (failure) { await assert.rejects(run); assert.ok(!calls.includes('complete')) }
    else { await run; assert.deepEqual(calls, ['finalize', 'auth', 'navigate', 'complete']) }
    if (failure === 'incomplete' || failure === 'auth' || failure === 'finalize') assert.ok(!calls.includes('navigate'))
  }
})

// Purpose: the runtime's session-view hydrates obey sessionsV3SyncHydrateOptions' eight-ID
// cap; this pure batch boundary proves complete, deduplicated coverage without 200 requests.
test('conversation hydration batches cover all IDs within the API cap', () => {
  const ids = Array.from({ length: 200 }, (_, i) => `session-${i}`)
  const batches = projectConversationBatches([...ids, ' ', ids[0]])
  assert.equal(batches.length, 25)
  assert.ok(batches.every(batch => batch.length <= 8))
  assert.deepEqual(batches.flat(), ids)
  assert.deepEqual(projectConversationBatches([]), [])
})

// Purpose: task-return and composer admission in OrchestrateView must reject unverified
// or stale route identities. Pure admission tests exercise both project and session changes.
test('task return cannot convert a rejected route into an admitted parent', () => {
  const admission = { projectId: 'a', sessionId: 'parent' }
  assert.equal(admittedConversationId(admission, 'a', 'parent'), 'parent')
  assert.equal(admittedConversationId(null, 'a', 'guessed'), '')
  assert.equal(admittedConversationId(admission, 'b', 'parent'), '')
  assert.equal(admittedConversationId(admission, 'a', 'child'), '')
})

// Purpose: ProjectEntryPage exposes bounded original history without fabricating ownership.
// The selector layer proves hidden/system/rootless rows are excluded and records unchanged.
test('legacy history is bounded and does not reparent sessions', () => {
  const rows = Array.from({ length: 60 }, (_, i) => ({ id: `old-${i}`, workspace_path: '/repo', updated_at: i, metadata: {} } as SessionSnapshot))
  const history = legacyHistorySessions([...rows,
    { ...rows[0], id: 'hidden', navigation_hidden: true },
    { ...rows[0], id: 'system', system_session: true },
    { ...rows[0], id: 'rootless', workspace_path: '' },
  ])
  assert.equal(history.length, 50)
  assert.equal(history[0].id, 'old-59')
  assert.ok(history.every(row => row.id.startsWith('old-')))
  assert.ok(rows.every(row => Object.keys(row.metadata!).length === 0))
})

// Purpose: supplemental wiring regression checks connect the tested boundaries to their
// UI callers. These checks are not browser or security execution evidence.
test('onboarding, history normalization and task return wire the project-first boundaries', () => {
  const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
  const gate = source('../onboarding/components/desktop-onboarding-gate.tsx')
  const continueHandler = gate.slice(gate.indexOf('const handleProviderContinue'), gate.indexOf('const handleIdentitySubmit'))
  assert.match(continueHandler, /completeAccountOnboarding/)
  assert.match(continueHandler, /to: '\/projects'/)
  assert.doesNotMatch(continueHandler, /openWorkspace|finishWithWorkspace|transitionToStep\('workspace'\)/)
  const entry = source('./project-entry-page.tsx')
  assert.match(entry, /recent: \{ limit: 50 \}/)
  assert.match(entry, /createProject: true/)
  assert.match(entry, /Existing session history/)
  const desktop = source('../layout/desktop-app-page.tsx')
  assert.match(desktop, /to: '\/history\/\$workspaceSlug\/\$sessionId',\s+params: \{ workspaceSlug: canonicalWorkspaceSlug/)
  const view = source('./OrchestrateView.tsx')
  const back = view.slice(view.indexOf('const handleBackToOrchestrator'), view.indexOf('const handleOrchestratorSessionReset'))
  assert.match(back, /setActiveSessionId\(admittedParentId\)/)
  assert.doesNotMatch(back, /setActiveSessionId\(routeConversationId\)/)
  assert.ok(view.includes('projectConversationLink(projectSegment || project.id, project.primarySessionId)'), 'navigation uses the resolved project segment')
  assert.ok(view.includes('createProjectConversation(projectId, conversationRequest.current)'), 'new conversation uses a stable request identity')
  // Task details must retain program-child and repair-session access, not just the primary task session.
  assert.match(view, /extractTaskSessionIds\(task\)\.includes\(activeSessionId\) \|\| taskOutcome\(task\)\.repairSessionId === activeSessionId/)
})
