import assert from 'node:assert/strict'
import test from 'node:test'
import { videoSections } from './video-sections'
import type { MediaLibraryItem } from './types'

const video = (id: string, duration: number, parent?: string): MediaLibraryItem => ({
  id, kind: 'video', title: id, directUrl: `/media/${id}`, artifact: {},
  videoProvenance: {
    account_scope_id: 'fixture', provider: 'fixture', model: 'fixture', transport: 'fixture', created_at: 1,
    operation: parent ? 'extend' : 'create', observed_duration_ms: duration,
    is_combined_output: Boolean(parent), source_link: parent ? { deliverable_id: parent } : undefined,
  },
} as MediaLibraryItem)

// Purpose: videoSections is the narrow provenance boundary for seeking a combined
// result. Prevent duplicate footage and fabricated boundaries from list order or
// requested duration; assert exact ranges, drift, branching, and fail-closed cases.
test('combined extensions use observed cumulative endpoints, retaining branches', () => {
  const base = video('base', 10000)
  const next = video('next', 20010, base.id)
  const branch = video('branch', 19020, base.id)
  const items = [branch, next, base]
  assert.deepEqual(videoSections(next, items).map(s => [s.source.id, s.start, s.end]), [['base', 0, 10], ['next', 10, 20.01]])
  assert.deepEqual(videoSections(branch, items).map(s => [s.source.id, s.start, s.end]), [['base', 0, 10], ['branch', 10, 19.02]])
  assert.deepEqual(items.map(i => i.id), ['branch', 'next', 'base'])
})

// Purpose: unavailable/malformed provenance cannot authorize section seeking.
// Pure tests cover the smallest boundary without pretending to verify playback.
test('unverified, noncombined, missing-parent and cyclic versions have no sections', () => {
  const base = video('base', 10000)
  const next = video('next', 20010, base.id)
  assert.deepEqual(videoSections(next, []), [])
  assert.deepEqual(videoSections({ ...next, videoProvenance: { ...next.videoProvenance!, is_combined_output: false } }, [base]), [])
  assert.deepEqual(videoSections({ ...base, durationSeconds: 10, videoProvenance: undefined }, []), [])
  assert.deepEqual(videoSections(video('base', 10000, 'next'), [video('next', 20000, 'base')]), [])
  assert.deepEqual(videoSections(video('short', 9000, 'base'), [base]), [])
})
