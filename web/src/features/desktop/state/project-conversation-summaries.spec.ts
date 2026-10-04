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
