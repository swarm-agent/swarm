import assert from 'node:assert/strict'
import test from 'node:test'

import { serializeVideoClipForRequest, type VideoClip } from './video-tool-page'

test('serializeVideoClipForRequest sends Go API wire fields for clip metadata', () => {
  const clip: VideoClip = {
    id: 'clip-1',
    name: 'launch.mp4',
    path: '/workspace/video/launch.mp4',
    extension: '.mp4',
    sizeBytes: 123456,
    modifiedAt: 1700000000000,
  }

  const payload = serializeVideoClipForRequest(clip)

  assert.deepEqual(payload, {
    id: 'clip-1',
    name: 'launch.mp4',
    path: '/workspace/video/launch.mp4',
    extension: '.mp4',
    size_bytes: 123456,
    modified_at: 1700000000000,
  })
  assert.equal('sizeBytes' in payload, false)
  assert.equal('modifiedAt' in payload, false)
})
