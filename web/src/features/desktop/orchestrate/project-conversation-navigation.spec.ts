import test from 'node:test'
import assert from 'node:assert/strict'
import { cachedProjectConversation, firstProjectConversation } from './project-entry-policy'
import type { DesktopV3CacheState, SessionSnapshot } from '../state/desktop-v3-cache-types'
import type { ProjectSessionRow } from '../state/project-session-rows'

// Purpose: OrchestrateView may skip admission I/O only for canonical, visible
// project-owned conversations. This selector-level test is the narrowest proof
// that unknown, foreign, hidden, child and tombstoned records cannot use that
// fast path; rejected lookups must not mutate the cache.
test('cached conversation admission is scoped and read-only', () => {
  const session = { id: 'chat', metadata: { agent_name: 'system-orchestrator', project_id: 'project' } } as SessionSnapshot
  const state = { sessionsById: { chat: { kind: 'full', session } }, tombstonesBySession: {} } as unknown as DesktopV3CacheState
  assert.equal(cachedProjectConversation(state, 'project', 'chat'), session)
  assert.equal(cachedProjectConversation(state, 'other', 'chat'), undefined)
  assert.equal(cachedProjectConversation(state, 'project', 'unknown'), undefined)
  for (const changed of [
    { ...session, id: 'mismatch' },
    { ...session, navigation_hidden: true },
    { ...session, metadata: { ...session.metadata, parent_session_id: 'parent' } },
    { ...session, metadata: { ...session.metadata, task_id: 'task' } },
    { ...session, metadata: { ...session.metadata, swarm_v3_project_id: 'other' } },
  ]) {
    const candidate = { ...state, sessionsById: { chat: { kind: 'full', session: changed } } } as DesktopV3CacheState
    const before = JSON.stringify(candidate)
    assert.equal(cachedProjectConversation(candidate, 'project', 'chat'), undefined)
    assert.equal(JSON.stringify(candidate), before)
  }
  const archived = { ...state, tombstonesBySession: { chat: { archived: true } } } as DesktopV3CacheState
  assert.equal(cachedProjectConversation(archived, 'project', 'chat'), undefined)
  assert.equal(cachedProjectConversation({ ...state, sessionsById: {} }, 'project', 'chat'), undefined)
})

// Purpose: default project entry must use the same unarchived order as
// ProjectSessionList, never partial hydration order or a legacy primary pointer.
// Pure selector assertions cover empty/error/loading cases without timing mocks.
test('default conversation follows the completed sidebar order', () => {
  const rows = [
    { session: { id: 'archived' }, archivedVersion: 1 },
    { session: { id: 'running-first' } },
    { session: { id: 'newest-created' } },
  ] as ProjectSessionRow[]
  assert.equal(firstProjectConversation(rows, true, false, ''), 'running-first')
  assert.equal(firstProjectConversation(rows, false, false, ''), '')
  assert.equal(firstProjectConversation(rows, true, true, ''), '')
  assert.equal(firstProjectConversation(rows, true, false, 'failed'), '')
  assert.equal(firstProjectConversation([], true, false, ''), '')
  assert.equal(firstProjectConversation(rows.slice(0, 1), true, false, ''), '')
})
