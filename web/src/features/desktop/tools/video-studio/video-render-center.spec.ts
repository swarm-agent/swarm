import assert from 'node:assert/strict'
import test from 'node:test'

import {
  VIDEO_RENDER_PRESETS,
  formatVideoRenderDuration,
  videoRenderElapsedMs,
  videoRenderJobActive,
  videoRenderRemainingMs,
  type VideoRenderJobSnapshotWire,
} from './video-render-center'

function renderJob(overrides: Partial<VideoRenderJobSnapshotWire> = {}): VideoRenderJobSnapshotWire {
  return {
    id: 'render-1',
    project_id: 'project-1',
    revision_id: 'revision-1',
    revision_number: 1,
    session_id: 'session-1',
    status: 'rendering',
    progress: 0.25,
    created_at: 1_000_000_000_000,
    started_at: 1_000_000_000_000,
    updated_at: 1_000_000_001_000,
    ...overrides,
  }
}

test('Video Studio exposes only allowlisted quality and FPS presets', () => {
  assert.deepEqual(VIDEO_RENDER_PRESETS.map(({ id, quality, fps }) => ({ id, quality, fps })), [
    { id: 'draft', quality: 'draft', fps: 30 },
    { id: 'high', quality: 'high', fps: 30 },
    { id: 'maximum', quality: 'high', fps: 60 },
  ])
})

test('render jobs distinguish safe cancellation states', () => {
  assert.equal(videoRenderJobActive(renderJob({ status: 'queued' })), true)
  assert.equal(videoRenderJobActive(renderJob({ status: 'rendering' })), true)
  for (const status of ['ready', 'failed', 'cancelled', 'stale'] as const) {
    assert.equal(videoRenderJobActive(renderJob({ status })), false)
  }
})

test('render ETA prefers the durable server estimate and otherwise derives from progress', () => {
  const now = 1_000_000_010_000
  assert.equal(videoRenderRemainingMs(renderJob({ estimated_remaining_ms: 9_000 }), now), 9_000)
  assert.equal(videoRenderRemainingMs(renderJob({ estimated_remaining_seconds: 12 }), now), 12_000)
  assert.equal(videoRenderElapsedMs(renderJob(), now), 10_000)
  assert.equal(videoRenderRemainingMs(renderJob(), now), 30_000)
  assert.equal(videoRenderRemainingMs(renderJob({ progress: 0 }), now), null)
  assert.equal(videoRenderRemainingMs(renderJob({ status: 'ready' }), now), null)
})

test('render durations are concise for queue display', () => {
  assert.equal(formatVideoRenderDuration(null), 'Calculating…')
  assert.equal(formatVideoRenderDuration(9_000), '9s')
  assert.equal(formatVideoRenderDuration(125_000), '2m 5s')
  assert.equal(formatVideoRenderDuration(3_900_000), '1h 5m')
})
