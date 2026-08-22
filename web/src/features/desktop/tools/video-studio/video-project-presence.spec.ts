import assert from 'node:assert/strict'
import test from 'node:test'

import { sessionVideoProjectPresenceKey, videoProposalProjectionSequence } from './video-project-presence'

test('video project presence sequence advances on canonical project events', () => {
  assert.equal(videoProposalProjectionSequence({ eventsBySession: {
    chat: [
      { id: 'message', session_id: 'chat', seq: 3, event_type: 'message.appended', payload: {}, ts_unix_ms: 1 },
      { id: 'project', session_id: 'chat', seq: 4, event_type: 'session.video_project.created', payload: {}, ts_unix_ms: 2 },
      { id: 'revision', session_id: 'chat', seq: 7, event_type: 'session.video_project.revision.created', payload: {}, ts_unix_ms: 3 },
    ],
  } }, 'chat'), 7)
  assert.equal(videoProposalProjectionSequence({ eventsBySession: {} }, 'chat'), 0)
})

test('video project presence query key changes when realtime project state advances', () => {
  assert.deepEqual(sessionVideoProjectPresenceKey(' chat ', 7.9), ['session-video-project-presence', 'chat', 7])
})
