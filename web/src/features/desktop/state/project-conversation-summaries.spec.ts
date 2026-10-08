import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { SessionSnapshot } from './desktop-v3-cache-types'

// Purpose: collection summaries are display-only. Applying them must not turn
// partial sessions/history into hydrated cache entries or overwrite selected chat.
// The canonical cache reducer is the narrowest boundary proving that invariant.
test('project row summaries never hydrate or replace selected conversation state', () => {
  const state = createEmptyDesktopV3CacheState()
  const session: SessionSnapshot = { id: 's', workspace_path: '', workspace_name: '', title: 'Full', mode: 'auto', created_at: 1, updated_at: 2, message_count: 100, last_message_at: 2 }
  state.sessionsById.s = { kind: 'full', session, needsHydrate: false }
  state.messagesBySession.s = { items: [], byMessageId: {}, byGlobalSeq: {}, knownFull: false }
  const original = state.messagesBySession.s
  desktopV3CacheReducer(state, { type: 'projectConversations.applySummaries', projectId: 'p', sessions: [{ ...session, title: 'Summary' }, { ...session, id: 'unhydrated' }] })
  assert.equal(state.sessionsById.s.kind === 'full' && state.sessionsById.s.session.title, 'Full')
  assert.equal(state.sessionsById.unhydrated, undefined)
  assert.equal(state.messagesBySession.s, original)
  assert.equal(state.messagesBySession.s.knownFull, false)
  assert.equal(state.projectConversationSummaries?.p.length, 2)
})

// Purpose: archive collection rows must not overwrite canonical tombstones,
// resurrect deleted sessions, or mark partial selected history complete. The
// reducer proves display-only publication and replacement without hydration.
test('archive summaries stay display-only and replace stale membership', () => {
  const state = createEmptyDesktopV3CacheState()
  state.messagesBySession.s = { items: [], byMessageId: {}, byGlobalSeq: {}, knownFull: false }
  state.tombstonesBySession.s = { session_id: 's', deleted: true, updated_at: 20 }
  const original = state.messagesBySession.s
  desktopV3CacheReducer(state, { type: 'projectConversations.applyArchiveSummaries', projectId: 'p', tombstones: [{ session_id: 's', archived: true, updated_at: 10 }] })
  assert.equal(state.sessionsById.s, undefined)
  assert.equal(state.tombstonesBySession.s.deleted, true)
  assert.equal(state.messagesBySession.s, original)
  assert.equal(state.messagesBySession.s.knownFull, false)
  assert.equal(state.projectArchiveSummaries?.p.length, 1)
  desktopV3CacheReducer(state, { type: 'projectConversations.applyArchiveSummaries', projectId: 'p', tombstones: [] })
  assert.equal(state.projectArchiveSummaries?.p.length, 0)
})

function conversation(id: string): SessionSnapshot {
  return { id, workspace_path: '', workspace_name: '', title: id, mode: 'auto', created_at: 10, updated_at: 10, message_count: 0, last_message_at: 0,
    metadata: { agent_name: 'system-orchestrator', project_id: 'p' } }
}

function createReceipt(session = conversation('new')) {
  return { ok: true as const, session_id: session.id, session, projection: { session_id: session.id, last_seq: 1 }, mutation: {}, realtime_outbox: null } as import('./desktop-v3-cache-types').SessionCreateMutationResponse
}

// Purpose: the canonical creation reducer must publish project membership atomically
// and stale collection reads must not erase it. Reducer tests isolate the interleaving
// without transport timing, while proving replay, removal and selected-chat safety.
test('creation joins empty and populated summaries and survives an older read', () => {
  for (const initial of [[], [conversation('existing')]]) {
    const state = createEmptyDesktopV3CacheState()
    state.selectedSessionId = 'selected'
    state.projectConversationSummaries = { p: initial, other: [conversation('unrelated')] }
    const requestSessionIds = initial.map(session => session.id)
    const action = { type: 'mutation.sessionCreateResult' as const, raw: createReceipt(), sidebarScopeId: 'project:p' }
    desktopV3CacheReducer(state, action)
    desktopV3CacheReducer(state, action)
    assert.deepEqual(state.projectConversationSummaries.p.map(session => session.id), [...requestSessionIds, 'new'])
    desktopV3CacheReducer(state, { type: 'projectConversations.applySummaries', projectId: 'p', requestSessionIds, sessions: initial })
    assert.deepEqual(new Set(state.projectConversationSummaries.p.map(session => session.id)), new Set([...requestSessionIds, 'new']))
    desktopV3CacheReducer(state, { type: 'projectConversations.applySummaries', projectId: 'p', requestSessionIds, sessions: [...initial, conversation('new'), conversation('new')] })
    assert.equal(state.projectConversationSummaries.p.length, initial.length + 1)
    assert.equal(state.selectedSessionId, 'selected')
    assert.deepEqual(state.projectConversationSummaries.other.map(session => session.id), ['unrelated'])
    // A later authoritative read can remove membership; this is not an eternal union.
    desktopV3CacheReducer(state, { type: 'projectConversations.applySummaries', projectId: 'p', requestSessionIds: [...requestSessionIds, 'new'], sessions: initial })
    assert.deepEqual(state.projectConversationSummaries.p, initial)
  }
})

// Purpose: project creation admission must reject invalid ownership before mutating
// any cache, and idempotent receipts must never undo archive/delete. The reducer is
// the narrowest atomic state boundary for these negative/postcondition assertions.
test('failed or foreign creation cannot publish rows and replay cannot resurrect tombstones', () => {
  const own = conversation('new')
  for (const metadata of [
    { ...own.metadata, project_id: 'other' }, { ...own.metadata, worker_id: 'worker' },
    { ...own.metadata, task_id: 'task' }, { ...own.metadata, parent_session_id: 'parent' },
    { ...own.metadata, swarm_v3_project_id: 'other' }, { ...own.metadata, agent_name: 'system-coder' },
  ]) {
    const state = createEmptyDesktopV3CacheState()
    const before = structuredClone(state)
    assert.throws(() => desktopV3CacheReducer(state, { type: 'mutation.sessionCreateResult', raw: createReceipt({ ...own, metadata }), sidebarScopeId: 'project:p' }), /does not belong/)
    assert.deepEqual(state, before)
  }
  const state = createEmptyDesktopV3CacheState()
  const before = structuredClone(state)
  assert.throws(() => desktopV3CacheReducer(state, { type: 'mutation.sessionCreateResult', raw: { ...createReceipt(), session_id: 'wrong' }, sidebarScopeId: 'project:p' }), /identity/)
  desktopV3CacheReducer(state, { type: 'mutation.sessionCreateResult', raw: { ok: false, error: 'failed' }, sidebarScopeId: 'project:p' })
  assert.deepEqual(state, before)
  for (const tombstone of [{ session_id: 'new', archived: true, updated_at: 20 }, { session_id: 'new', deleted: true, updated_at: 20 }]) {
    state.tombstonesBySession.new = tombstone
    const archived = structuredClone(state)
    desktopV3CacheReducer(state, { type: 'mutation.sessionCreateResult', raw: createReceipt(), sidebarScopeId: 'project:p' })
    assert.deepEqual(state, archived)
  }
})
