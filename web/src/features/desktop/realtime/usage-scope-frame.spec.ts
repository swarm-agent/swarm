// Purpose: assertDesktopV3RealtimeFrame/applyRealtimeFrame admit the backend's
// transcript-free durable usage projection and preserve its opaque cursor.
// Threat: rejecting usage as unknown disconnects cards; private session payloads
// masquerade as scope frames. Wire+cache is the narrowest protocol boundary.
import test from 'node:test'
import assert from 'node:assert/strict'
import { assertDesktopV3RealtimeFrame } from '../state/desktop-v3-cache-wire'
import { createEmptyDesktopV3CacheState, applyRealtimeFrame } from '../state/desktop-v3-cache-reducer'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'
test('usage scope frame retains opaque cursor without installing a session', () => {
  const frame: RealtimeMessage = { protocol: 'v3.realtime', protocol_version: 1, kind: 'usage.scope.updated', endpoint_cursor: 'opaque-scope-cursor', event: { event_type: 'usage.scope.updated', payload: { scope_totals: [] } } as any }
  assert.doesNotThrow(() => assertDesktopV3RealtimeFrame(frame))
  const state = createEmptyDesktopV3CacheState()
  const next = applyRealtimeFrame(state, { frame })
  assert.equal(next.realtime.endpointCursor, frame.endpoint_cursor)
  assert.deepEqual(next.sessionsById, state.sessionsById)
  for (const invalid of [{ ...frame, endpoint_cursor: '' }, { ...frame, session_id: 'private' }, { ...frame, event: { ...frame.event, payload: { scope_totals: Array(321).fill({}) } } }]) assert.throws(() => assertDesktopV3RealtimeFrame(invalid as RealtimeMessage), /protocol invalid/)
})

// Purpose: buildDesktopV3InitialRealtimeResume/buildDesktopUsageWorksets must
// request the backend's registered projects resource under account/workspace
// selectors, even with no selected chat and an old hidden worker session.
// Threat: accepting frames in isolation hides a real subscription gap. Outgoing
// protocol construction is the narrowest Desktop proof; backend selection tests
// separately prove principal ownership and hidden-navigation matching.
test('outgoing usage demand scopes history by workspace and shared allowance by account', async () => {
  const { buildDesktopV3InitialRealtimeResume, buildDesktopUsageWorksets } = await import('./v3-realtime-controller')
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'sidebar' }
  state.syncScopesById.sidebar = { scopeId: 'sidebar', surface: 'desktop', streamKind: 'v3.sync.snapshot', selectorFilterHash: 'hash', resourceSet: 'metadata', selector: { kind: 'recent', global: true, recent: { limit: 50 } }, endpointCursor: 'opaque', replayPath: '/v3/sync/stream', replayTransport: 'http_post', needsBootstrap: false }
  const input = { accountScopeId: 'acct', scope: { kind: 'worker' as const, id: 'worker' } }
  state.usagePages.page = { input, generation: 0, loading: false, stale: false }
  state.workerPages.worker = { input: { kind: 'detail', accountScopeId: 'acct', workerId: 'worker' }, generation: 0, loading: false, stale: false, data: { worker: { id: 'worker', account_scope_id: 'acct', name: 'Fixture', instructions: '', lifecycle_state: 'active', revision: 1, created_at: 0, updated_at: 0, authorized_workspaces: { primary: { workspace_id: 'workspace', available: true, path: '/projects/fixture' }, unavailable: { workspace_id: 'no', available: false, path: '/projects/forbidden' } } } } }
  const initial = buildDesktopV3InitialRealtimeResume(state, 'client', null)
  assert.deepEqual(initial.subscriptions, [])
  assert.ok(initial.worksets[0].resources?.includes('projects'))
  const requests = buildDesktopUsageWorksets(state, 'client', 'acct')
  assert.equal(requests.length, 1)
  assert.deepEqual(requests[0].selector, { kind: 'workspace', workspace_paths: ['/projects/fixture'] })
  assert.deepEqual(requests[0].resources, ['projects'])
  assert.equal(requests[0].auto_subscribe_sessions, false)
  state.usagePages.budget = { input: { ...input, budget: true }, generation: 0, loading: false, stale: false }
  const allowance = buildDesktopUsageWorksets(state, 'client', 'acct').find(w => w.workset_id === 'usage:account-allowance')!
  assert.deepEqual(allowance.selector, { kind: 'global', global: true })
  assert.deepEqual(allowance.resources, ['projects'])
  assert.equal(allowance.auto_subscribe_sessions, false)
  delete state.usagePages.budget
  assert.deepEqual(buildDesktopUsageWorksets(state, 'client', 'foreign'), [])
  delete state.usagePages.page
  assert.deepEqual(buildDesktopUsageWorksets(state, 'client', 'acct'), [])
})
