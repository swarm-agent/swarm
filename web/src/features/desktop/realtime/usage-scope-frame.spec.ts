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
