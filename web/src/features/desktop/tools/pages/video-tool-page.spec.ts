import assert from 'node:assert/strict'
import test from 'node:test'

import { VIDEO_TRANSITION_KINDS, transitionLabel } from '../video-studio/video-studio-surface'

import {
  projectTimelineToTimelineSegments,
  serializeVideoClipForRequest,
  timelineSegmentsToProjectTimeline,
  videoChildSessionMetadata,
  type VideoClip,
  type VideoProjectTimelineWire,
} from './video-tool-page'

test('serializeVideoClipForRequest sends Go API wire fields for clip metadata', () => {
  const clip: VideoClip = {
    id: 'clip-1',
    name: 'launch.mp4',
    sourceRef: 'videosrc_launch',
    extension: '.mp4',
    sizeBytes: 123456,
    modifiedAt: 1700000000000,
  }

  const payload = serializeVideoClipForRequest(clip)

  assert.deepEqual(payload, {
    id: 'clip-1',
    name: 'launch.mp4',
    source_ref: 'videosrc_launch',
    extension: '.mp4',
    size_bytes: 123456,
    modified_at: 1700000000000,
  })
  assert.equal('path' in payload, false)
  assert.equal('sizeBytes' in payload, false)
  assert.equal('modifiedAt' in payload, false)
})

test('timelineSegmentsToProjectTimeline builds structured V3 VideoProject timeline with ms timestamps', () => {
  const clips: VideoClip[] = [
    {
      id: 'clip-1',
      name: 'intro.mp4',
      sourceRef: 'videosrc_intro',
      extension: '.mp4',
      sizeBytes: 5000,
      modifiedAt: 1700000000000,
    },
    {
      id: 'clip-2',
      name: 'demo.mp4',
      sourceRef: 'videosrc_demo',
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
  assert.equal(timeline.width, 1920)
  assert.equal(timeline.height, 1080)
  assert.equal(timeline.fps, 30)
  assert.equal(timeline.total_duration_ms, 4000)
  assert.equal(timeline.clips.length, 2)

  assert.deepEqual(timeline.clips[0], {
    id: 'seg-1',
    name: 'intro.mp4',
    track: 0,
    sequence: 0,
    source_kind: 'source_video',
    source_ref: 'videosrc_intro',
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
    source_ref: 'videosrc_demo',
    source_start_ms: 0,
    source_end_ms: 6000,
    timeline_start_ms: 4000,
    timeline_end_ms: 4000,
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

test('projectTimelineToTimelineSegments resolves durable source refs back to VideoThread clip media', () => {
  const timeline: VideoProjectTimelineWire = {
    schema_version: 1,
    clips: [{
      id: 'segment-intro',
      source_kind: 'source_video',
      source_ref: 'videosrc_intro',
      duration_ms: 3000,
      visible: true,
    }],
  }
  const clips: VideoClip[] = [{
    id: 'clip-local-id',
    name: 'intro.mp4',
    sourceRef: 'videosrc_intro',
    extension: '.mp4',
    sizeBytes: 100,
    modifiedAt: 1,
  }]

  const [segment] = projectTimelineToTimelineSegments(timeline, {}, clips, 'session-1')

  assert.equal(segment.id, 'segment-intro')
  assert.equal(segment.clipId, 'clip-local-id')
  assert.equal(segment.src, '/v1/workspace/video/threads/session-1/clips/media?clip_id=clip-local-id')
})

test('Video Studio exposes the complete launch transition vocabulary', () => {
  assert.deepEqual(VIDEO_TRANSITION_KINDS, ['cut', 'fade_through_black', 'crossfade', 'fade_to_black', 'fade_from_black'])
  assert.equal(transitionLabel('fade_through_black'), 'Fade through black')
})

test('videoChildSessionMetadata carries canonical project and revision identity', () => {
  const metadata = videoChildSessionMetadata({
    thread: {
      id: 'session-parent', title: 'Launch video', workspacePath: '/workspace', workspaceName: 'workspace',
      videoFolders: ['/workspace/video'], videoClips: [], videoClipOrder: [], createdAt: 1, updatedAt: 1,
    },
    projectId: 'project-primary',
    revisionId: 'revision-current',
    folderPath: '/workspace/video',
    clips: [],
  })

  assert.equal(metadata.parent_session_id, 'session-parent')
  assert.equal(metadata.parent_video_project_id, 'project-primary')
  assert.equal(metadata.parent_video_revision_id, 'revision-current')
  assert.equal(metadata.lineage_kind, 'video_child')
})
