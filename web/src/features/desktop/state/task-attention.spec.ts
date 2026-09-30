import assert from 'node:assert/strict'
import test from 'node:test'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheState, SessionSnapshot } from './desktop-v3-cache-types'
import type { DesktopPermissionRecord } from '../types/realtime'
import type { DesktopSessionPlanRecord } from '../chat/types/chat'
import type { TaskAttentionDecision } from './task-attention'
import { taskAttentionSessionIds, taskAttentionPermissions, taskAttentionLabel, taskAttentionContext, submitTaskAttentionDecision } from './task-attention'

// Purpose: taskAttentionSessionIds/Permissions must expose every authorized nested
// wait without leaking other tasks, principals or superseded generations. Pure
// selectors are the narrowest layer proving membership independently of selection.
function session(state: DesktopV3CacheState, id: string, parent = '', account = 'account', user = 'user') {
  state.sessionsById[id] = { kind: 'full', needsHydrate: false, session: {
    id, account_scope_id: account, user_id: user, metadata: { parent_session_id: parent },
  } as SessionSnapshot }
}
function permission(sessionId: string, id = sessionId, toolName = 'ask_user'): DesktopPermissionRecord {
  return { id, sessionId, toolName, toolArguments: JSON.stringify({ questions: [{ id: 'q', question: 'Which direction?', options: [{ label: 'One', value: 'one' }, { label: 'Two', value: 'two' }] }] }), runId: 'run', callId: 'call', status: 'pending', decision: '', reason: '', requirement: '', mode: 'auto', createdAt: 1, updatedAt: 1, resolvedAt: 0, permissionRequestedAt: 1 }
}
function fixture() {
  const state = createEmptyDesktopV3CacheState()
  session(state, 'root'); session(state, 'child', 'root'); session(state, 'grandchild', 'child')
  session(state, 'other'); session(state, 'foreign', 'root', 'foreign-account')
  session(state, 'foreign-user', 'root', 'account', 'foreign-user')
  session(state, 'old', 'root'); session(state, 'old-child', 'old')
  for (const id of Object.keys(state.sessionsById)) state.permissionsBySession[id] = [permission(id)]
  return state
}
test('root, child and grandchild waits coexist, with current program attempt and principal isolation', () => {
  const state = fixture()
  state.selectedSessionId = 'other'
  const task = { sessionId: 'root', taskProgramStatus: { jobs: [{ current_session_id: 'child', generation_history: [{ session_id: 'old' }] }] } }
  assert.deepEqual(taskAttentionSessionIds(state, task), ['child', 'grandchild', 'root'])
  assert.deepEqual(taskAttentionPermissions(state, taskAttentionSessionIds(state, task)).map(p => p.id), ['child', 'grandchild', 'root'])
  state.permissionsBySession.child.push(permission('child', 'second', 'bash'))
  assert.equal(taskAttentionPermissions(state, taskAttentionSessionIds(state, task)).length, 4)
  assert.equal(taskAttentionLabel(permission('root')), 'Needs your input')
  assert.match(taskAttentionContext(permission('root')), /Which direction/)
  assert.equal(taskAttentionLabel(permission('child', 'bash', 'bash')), 'Approval required')
  assert.match(taskAttentionContext({ ...permission('child', 'bash', 'bash'), toolArguments: '{"command":"git status"}' }), /git status/)
})

// Purpose: canonical permission.resolveResult freshness must clear all terminal
// states and reject stale resurrection; card selectors consume only this reducer.
test('resolved/denied/cancelled/expired requests stay cleared after stale decisions and detail hydration', () => {
  for (const status of ['approved', 'denied', 'cancelled', 'expired']) {
    let state = fixture()
    const pending = permission('root')
    state = desktopV3CacheReducer(state, { type: 'permission.resolveResult', sessionId: 'root', permissionId: 'root', permission: { ...pending, status, updatedAt: 5, resolvedAt: 5 } })
    assert.equal(taskAttentionPermissions(state, ['root']).length, 0)
    state = desktopV3CacheReducer(state, { type: 'permission.resolveResult', sessionId: 'root', permissionId: 'root', permission: pending })
    assert.equal(taskAttentionPermissions(state, ['root']).length, 0)
    state = desktopV3CacheReducer(state, {
      type: 'hydrate.apply', source: 'hydrate', scopeId: 'attention', requestedSessionIds: ['root'],
      snapshot: {
        sessions_by_id: { root: (state.sessionsById.root as { session: SessionSnapshot }).session },
        session_views_by_id: { root: { pending_permissions: [{ id: 'root', session_id: 'root', tool_name: 'ask_user', status: 'pending', created_at: 1, updated_at: 1 }] } },
        sync_scope: { scope_id: 'attention', resource_set: 'session_view' },
      } as never,
    })
    assert.equal(taskAttentionPermissions(state, ['root']).length, 0, 'delayed hydration cannot resurrect terminal permission')
    // A newly bootstrapped cache containing canonical unresolved details restores attention.
    const reloaded = createEmptyDesktopV3CacheState()
    session(reloaded, 'root'); reloaded.permissionsBySession.root = [pending]
    assert.equal(taskAttentionPermissions(reloaded, ['root']).length, 1)
  }
})

// Purpose: submitTaskAttentionDecision must route the exact origin and preserve
// structured answers/edited arguments. Failure must leave pending state retryable;
// shared single flight and fresh-state checks prevent duplicate/stale submissions.
test('answer, allow and deny route to originating session; failed and duplicate submissions do not clear waits', async () => {
  const state = fixture()
  const pending = permission('grandchild')
  const calls: unknown[][] = []
  const commits: unknown[] = []
  const deps = {
    getState: () => state,
    resolve: async (...args: [string, string, TaskAttentionDecision, string, Record<string, unknown>?]) => { calls.push(args); return { ...pending, status: 'approved', resolvedAt: 2 } },
    commit: (result: DesktopPermissionRecord | null) => { commits.push(result) },
  }
  for (const action of ['approve', 'deny'] as const) {
    await submitTaskAttentionDecision(pending, action, 'structured answer', { answers: [{ question_id: 'q', value: 'custom', text: 'My response' }] }, deps)
    assert.deepEqual(calls.at(-1)?.slice(0, 4), ['grandchild', 'grandchild', action, 'structured answer'])
    assert.deepEqual(calls.at(-1)?.[4], { answers: [{ question_id: 'q', value: 'custom', text: 'My response' }] })
  }
  const before = commits.length
  await assert.rejects(submitTaskAttentionDecision(pending, 'approve', '', undefined, { ...deps, resolve: async () => { throw new Error('offline') } }), /offline/)
  assert.equal(commits.length, before)
  assert.equal(taskAttentionPermissions(state, ['grandchild']).length, 1)
  let finish!: (result: DesktopPermissionRecord) => void
  const inFlight = submitTaskAttentionDecision(pending, 'approve', '', undefined, { ...deps, resolve: () => new Promise(resolve => { finish = resolve }) })
  await assert.rejects(submitTaskAttentionDecision(pending, 'deny', '', undefined, deps), /already being submitted/)
  finish({ ...pending, status: 'approved', resolvedAt: 2 }); await inFlight
  state.permissionsBySession.grandchild = [{ ...pending, status: 'denied', resolvedAt: 3 }]
  const count = calls.length
  await assert.rejects(submitTaskAttentionDecision(pending, 'approve', '', undefined, deps), /resolved/)
  assert.equal(calls.length, count)
})

// Purpose: plan execution uses the current durable attempt, not historical
// checkpoint sessions. Selector-layer evidence prevents old-attempt waits leaking.
test('plan current execution wins over old checkpoint attempts', () => {
  const state = fixture()
  state.plansBySession.root = { document: {
    executionState: { currentSessionId: 'child' },
    checkpoints: [{ attempts: [{ sessionId: 'old' }, { sessionId: 'child' }] }],
  } } as DesktopSessionPlanRecord
  assert.deepEqual(taskAttentionSessionIds(state, { sessionId: 'root' }), ['child', 'grandchild', 'root'])
})
