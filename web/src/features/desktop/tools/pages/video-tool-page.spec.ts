import assert from 'node:assert/strict'
import test from 'node:test'

import {
  projectTimelineToTimelineSegments,
  serializeVideoClipForRequest,
  timelineSegmentsToProjectTimeline,
  type VideoClip,
  type VideoProjectTimelineWire,
} from './video-tool-page'

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

test('timelineSegmentsToProjectTimeline builds structured V3 VideoProject timeline with ms timestamps', () => {
  const clips: VideoClip[] = [
    {
      id: 'clip-1',
      name: 'intro.mp4',
      path: '/workspace/intro.mp4',
      extension: '.mp4',
      sizeBytes: 5000,
      modifiedAt: 1700000000000,
    },
    {
      id: 'clip-2',
      name: 'demo.mp4',
      path: '/workspace/demo.mp4',
      extension: '.mp4',
      sizeBytes: 8000,
      modifiedAt: 1700000000000,
    },
  ]
  const segments = [
    {
      id: 'seg-1',
      type: 'video' as const,
      clipId: 'clip-1',
      src: '/media/clip-1',
      start: 0,
      sourceStart: 1.5,
      duration: 4.0,
      visible: true,
    },
    {
      id: 'seg-2',
      type: 'video' as const,
      clipId: 'clip-2',
      src: '/media/clip-2',
      start: 0,
      sourceStart: 0,
      duration: 6.0,
      visible: false,
    },
  ]

  const timeline = timelineSegmentsToProjectTimeline(segments, clips, 'landscape_1080p')

  assert.equal(timeline.schema_version, 1)
  assert.equal(timeline.output_preset, 'landscape_1080p')
  assert.equal(timeline.clips.length, 2)

  assert.deepEqual(timeline.clips[0], {
    id: 'seg-1',
    name: 'intro.mp4',
    track: 0,
    sequence: 0,
    source_kind: 'source_video',
    source_ref: '/workspace/intro.mp4',
    source_start_ms: 1500,
    source_end_ms: 5500,
    timeline_start_ms: 0,
    timeline_end_ms: 4000,
    duration_ms: 4000,
    visible: true,
    volume: 1.0,
  })

  assert.deepEqual(timeline.clips[1], {
    id: 'seg-2',
    name: 'demo.mp4',
    track: 0,
    sequence: 1,
    source_kind: 'source_video',
    source_ref: '/workspace/demo.mp4',
    source_start_ms: 0,
    source_end_ms: 6000,
    timeline_start_ms: 0,
    timeline_end_ms: 0,
    duration_ms: 6000,
    visible: false,
    volume: 1.0,
  })
})

test('projectTimelineToTimelineSegments reconstructs timeline segments from V3 project timeline', () => {
  const timeline: VideoProjectTimelineWire = {
    schema_version: 1,
    output_preset: 'landscape_1080p',
    clips: [
      {
        id: 'clip-1',
        name: 'intro.mp4',
        track: 0,
        sequence: 0,
        source_kind: 'source_video',
        source_start_ms: 2000,
        source_end_ms: 7000,
        timeline_start_ms: 0,
        timeline_end_ms: 5000,
        duration_ms: 5000,
        visible: true,
      },
    ],
  }

  const segments = projectTimelineToTimelineSegments(timeline, { 'clip-1': 10 })

  assert.equal(segments.length, 1)
  assert.equal(segments[0].id, 'clip-1')
  assert.equal(segments[0].sourceStart, 2.0)
  assert.equal(segments[0].duration, 5.0)
  assert.equal(segments[0].visible, true)
})
