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
