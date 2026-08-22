import assert from 'node:assert/strict'
import test from 'node:test'

import { VIDEO_TRANSITION_KINDS, buildVideoIterationTimeline, loadLatestVideoEditProposals, proposedVideoPlanClipDetails, rejectVideoEditProposal, renderedVideoArtifactUrl, selectedVideoProposalChangeIDs, transitionLabel, videoPlanPartMessageSelection, videoPlanTransitionMessageSelection, videoProposalFocusClipId, videoProposalProjectionSequence, type VideoEditProposalWire } from '../video-studio/video-studio-surface'

import {
  acceptedVideoPlan,
  applyPendingVideoProposal,
  createAdditionalVideoProject,
  defaultRenderedVideoExportPath,
  preferredVisibleVideoProject,
  createVideoThread,
  ensurePrimaryVideoProject,
  listVideoProjects,
  layoutTimelineSegments,
  projectTimelineToTimelineSegments,
  replaceCachedImageMedia,
  replaceCachedVideoMedia,
  resolveVideoStudioSessionRoute,
  serializeVideoClipForRequest,
  timelineSegmentsToProjectTimeline,
  videoPlanClipDetails,
  VIDEO_STUDIO_AGENT_NAME,
  videoChildSessionMetadata,
  videoStudioSessionMetadata,
  type VideoClip,
  type VideoProjectTimelineWire,
  type VideoTimelineClipWire,
} from './video-tool-page'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { WorkspaceOverviewSwarmTarget, WorkspaceOverviewTopologyRoute } from '../../../workspaces/launcher/types/workspace-overview'

function videoStudioWorkspace(route: WorkspaceOverviewTopologyRoute): WorkspaceEntry {
  return {
    path: '/workspace/video',
    workspaceId: 'workspace-1',
    localWorkspaceBindingId: route.workspaceBindingId,
    workspaceName: 'video',
    themeId: '',
    directories: ['/workspace/video'],
    isGitRepo: true,
    sortIndex: 0,
    addedAt: 1,
    updatedAt: 1,
    lastSelectedAt: 1,
    active: true,
    worktreeEnabled: false,
    topologyRoutes: [route],
  }
}

const videoStudioSwarmTarget: WorkspaceOverviewSwarmTarget = {
  swarmId: 'host-swarm',
  name: 'Local',
  role: 'host',
  relationship: 'self',
  kind: 'host',
  current: true,
}

const videoStudioSelfRoute: WorkspaceOverviewTopologyRoute = {
  routeId: 'swarm:host-swarm:binding:binding-video',
  routeSource: 'topology/workspace_binding',
  workspaceBindingId: 'binding-video',
  runtimeSwarmId: 'host-swarm',
  runtimeSwarmName: 'Local',
  runtimeKind: 'host',
  runtimeRelationship: 'self',
  authorityHostSwarmId: 'host-swarm',
  hostSwarmId: 'host-swarm',
  hostSwarmName: 'Local',
  hostWorkspacePath: '/workspace/video',
  hostWorkspaceName: 'video',
  runtimeWorkspacePath: '/workspace/video',
  writable: true,
  createdAt: 1,
  updatedAt: 1,
}

test('Video Studio builds the canonical rendered artifact URL', () => {
  assert.equal(renderedVideoArtifactUrl('session/a', { output_artifact: { collection_id: 'collection', variant_id: 'variant/b' } }), '/v3/sessions/session%2Fa/artifacts/variant%2Fb')
})

test('Video Studio provides a workspace-local default MP4 export path', () => {
  assert.equal(defaultRenderedVideoExportPath('/workspace/video/', 'Swarm Onboarding!', 11), '/workspace/video/exports/swarm-onboarding-r11.mp4')
})

test('Video Studio replaces cached image media when a stable clip source changes', () => {
  const first = { src: '', decoding: 'auto' } as unknown as HTMLImageElement
  const second = { src: '', decoding: 'auto' } as unknown as HTMLImageElement
  const cache = new Map()
  assert.equal(replaceCachedImageMedia(cache, 'part-2', '/old.png', () => first).replaced, false)
  const replacement = replaceCachedImageMedia(cache, 'part-2', '/new.png', () => second)
  assert.equal(replacement.replaced, true)
  assert.equal(replacement.entry.element, second)
  assert.equal(replacement.entry.src, '/new.png')
})

test('Video Studio disposes cached video media when a stable clip source changes', () => {
  const calls: string[] = []
  const first = { pause: () => calls.push('pause'), removeAttribute: (name: string) => calls.push(`remove:${name}`), load: () => calls.push('load') } as unknown as HTMLVideoElement
  const second = {} as HTMLVideoElement
  const cache = new Map()
  replaceCachedVideoMedia(cache, 'part-2', '/old.mp4', () => first)
  const replacement = replaceCachedVideoMedia(cache, 'part-2', '/new.mp4', () => second)
  assert.equal(replacement.replaced, true)
  assert.equal(replacement.entry.element, second)
  assert.deepEqual(calls, ['pause', 'remove:src', 'load'])
})

test('Video Studio preserves a timeline clip exact artifact reference for composer attachment fallback', () => {
  const artifactRef: NonNullable<VideoTimelineClipWire['artifact_ref']> = {
    session_id: 'session-1', collection_id: 'slides', variant_id: 'slide-1', event_seq: 9, media_type: 'image/png',
  }
  const [segment] = projectTimelineToTimelineSegments({ clips: [{
    id: 'part-1', source_kind: 'managed_artifact', artifact_ref: artifactRef, duration_ms: 2500,
  }] }, {}, [], 'session-1')

  assert.deepEqual(segment.artifactRef, artifactRef)
})

test('Video Studio reads an accepted visual plan from canonical revision metadata', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'slide-1', event_seq: 9 }
  assert.deepEqual(acceptedVideoPlan({
    clips: [],
    metadata: { accepted_video_plan: { kind: 'initial', summary: 'Visual launch video', parts: [{ id: 'part-1', title: 'Hook', duration_ms: 5000, visual }] } },
  }), {
    kind: 'initial',
    summary: 'Visual launch video',
    parts: [{ id: 'part-1', title: 'Hook', duration_ms: 5000, visual }],
  })
})

test('Video Creator selects an accepted reviewable plan ahead of an empty primary project', () => {
  const selected = preferredVisibleVideoProject([
    { id: 'primary', session_id: 'session-1', title: 'Empty', project_kind: 'video_tool', created_at: 1, updated_at: 1 },
    { id: 'plan', session_id: 'session-1', title: 'Plan', current_revision_id: 'revision-1', metadata: { reviewable_plan: true }, created_at: 2, updated_at: 2 },
  ])

  assert.equal(selected?.id, 'plan')
})

test('Video Creator exposes every still-plan field from text clips', () => {
  assert.deepEqual(videoPlanClipDetails({
    id: 'section-1',
    sequence: 0,
    source_kind: 'text',
    name: '00:00–00:05 — One idea | Narration: Every story begins with one simple idea. | Planned still: A blank notebook in morning light.',
    captions: [{ id: 'caption-1', text: 'One idea.' }],
  }), {
    timing: '00:00–00:05',
    title: 'One idea',
    narration: 'Every story begins with one simple idea.',
    still: 'A blank notebook in morning light.',
    onScreenText: 'One idea.',
  })
})

test('Video Studio rejects a plan with durable editable feedback instead of sending a replacement automatically', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.equal(String(input), '/v3/sessions/session-1/video/projects/project-1/edit-proposals/proposal-1/reject')
    assert.equal(init?.method, 'POST')
    assert.equal(new Headers(init?.headers).get('Content-Type'), 'application/json')
    assert.deepEqual(JSON.parse(String(init?.body)), { feedback: 'Please make part two more visual.' })
    return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    await rejectVideoEditProposal('session-1', 'project-1', 'proposal-1', 'Please make part two more visual.')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Video Studio observes only authoritative video-project events for live pending proposals', () => {
  assert.equal(videoProposalProjectionSequence({
    eventsBySession: {
      'session-1': [
        { id: 'message', session_id: 'session-1', seq: 41, event_type: 'session.message.created', payload: {}, ts_unix_ms: 1 },
        { id: 'proposal', session_id: 'session-1', seq: 42, event_type: 'session.video_project.edit_proposal.created', payload: {}, ts_unix_ms: 2 },
        { id: 'run', session_id: 'session-1', seq: 43, event_type: 'session.run.completed', payload: {}, ts_unix_ms: 3 },
      ],
    },
  }, 'session-1'), 42)
  assert.equal(videoProposalProjectionSequence({ eventsBySession: {} }, 'session-1'), 0)
})

test('Video Studio normalizes nullable proposal arrays from durable wire state', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async () => new Response(JSON.stringify({ proposals: [{
    id: 'proposal-null', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: null, plan: { kind: 'initial', parts: null }, created_at: 1, updated_at: 1,
  }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })) as typeof fetch
  try {
    const proposals = await import('../video-studio/video-studio-surface').then(({ listVideoEditProposals }) => listVideoEditProposals('session-1', 'project-1'))
    assert.deepEqual(proposals[0].operations, [])
    assert.equal(proposals[0].plan, undefined)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Video Studio ignores a stale proposal reload that finishes after the realtime reload', async () => {
  const accepted: VideoEditProposalWire = {
    id: 'accepted', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'accepted', operations: [], created_at: 1, updated_at: 1,
  }
  const pending: VideoEditProposalWire = {
    id: 'pending-live', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: [], created_at: 2, updated_at: 2,
  }
  const resolvers: Array<(proposals: VideoEditProposalWire[]) => void> = []
  const loader = () => new Promise<VideoEditProposalWire[]>((resolve) => resolvers.push(resolve))
  const requestSequence = { current: 0 }
  let rendered: VideoEditProposalWire[] = []
  const load = () => loadLatestVideoEditProposals({
    sessionId: 'session-1',
    projectId: 'project-1',
    requestSequence,
    loader,
    onLoaded: (proposals) => { rendered = proposals },
    onError: (error) => assert.equal(error, null),
  })

  const initialLoad = load()
  const realtimeLoad = load()
  resolvers[1]([pending])
  await realtimeLoad
  assert.deepEqual(rendered.map((proposal) => proposal.id), ['pending-live'])

  resolvers[0]([accepted])
  await initialLoad
  assert.deepEqual(rendered.map((proposal) => proposal.id), ['pending-live'])
})

test('Video Studio attaches the selected visual as message metadata', () => {
  const proposal = {
    id: 'proposal-1', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending' as const, created_at: 1, updated_at: 1,
    operations: [
      { id: 'change-1', type: 'update_clip' as const, clip: { id: 'step-1' } },
      { id: 'change-2', type: 'add_transition' as const, transition: { id: 'transition-1', kind: 'crossfade' as const, from_clip_id: 'step-1', to_clip_id: 'step-2' } },
    ],
  }
  assert.equal(videoProposalFocusClipId(proposal), 'step-2')
  const part = {
    id: 'step-2', title: 'Install and launch', duration_ms: 2500,
    visual: { session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 7 },
  }
  assert.deepEqual(videoPlanPartMessageSelection(part), {
    session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 7,
    label: 'Install and launch', description: undefined, action: 'select',
  })
  assert.deepEqual(videoPlanTransitionMessageSelection(part, {
    id: 'transition-1', kind: 'crossfade', from_clip_id: 'step-1', to_clip_id: 'step-2', duration_ms: 350,
  }), {
    session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 7,
    label: 'Transition · Install and launch',
    description: 'Stable part step-2. Current transition: crossfade; 350ms; step-1 → step-2.',
    action: 'select',
  })
})

test('Video Creator classifies proposed opening and transition stills before acceptance', () => {
  assert.deepEqual(proposedVideoPlanClipDetails({
    id: 'title-card',
    source_kind: 'text',
    name: '00:00–00:03 — Opening title card | Narration: Here’s how to make dubstep. | Planned still: Bold title on a dark studio background.',
    captions: [{ id: 'title', text: 'How to make dubstep in 3 steps' }],
  }), {
    timing: '00:00–00:03',
    title: 'Opening title card',
    narration: 'Here’s how to make dubstep.',
    still: 'Bold title on a dark studio background.',
    onScreenText: 'How to make dubstep in 3 steps',
    kind: 'title',
  })

  assert.equal(proposedVideoPlanClipDetails({
    name: '00:09–00:11 — Transition still | Planned still: A section divider between content clips.',
  }).kind, 'transition')
})

test('Video Studio resolves V3 create authority from workspace overview', () => {
  const route = resolveVideoStudioSessionRoute(videoStudioWorkspace(videoStudioSelfRoute), videoStudioSwarmTarget)

  assert.ok(route)
  assert.equal(route.swarmId, 'host-swarm')
  assert.equal(route.workspaceBindingId, 'binding-video')
  assert.equal(route.hostWorkspacePath, '/workspace/video')
  assert.deepEqual(videoStudioSessionMetadata(), {
    experience: 'video_studio',
    launch_source: 'video_tool',
    lineage_kind: 'video_project',
  })
})

test('Start new video session completes through the canonical V3 session API with required agent_name', async () => {
  const route = resolveVideoStudioSessionRoute(videoStudioWorkspace(videoStudioSelfRoute), videoStudioSwarmTarget)
  assert.ok(route)

  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/model') {
      return new Response(JSON.stringify({
        preference: { provider: 'codex', model: 'gpt-5.6-sol', thinking: 'high' },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v3/sessions') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      assert.equal(body.agent_name, VIDEO_STUDIO_AGENT_NAME)
      assert.equal(body.swarm_id, 'host-swarm')
      assert.equal(body.workspace_binding_id, 'binding-video')
      assert.deepEqual(body.metadata, videoStudioSessionMetadata())
      assert.equal(String(init?.method), 'POST')
      return new Response(JSON.stringify({
        ok: true,
        session: {
          id: 'video-session-1',
          title: String(body.title ?? ''),
          workspace_path: '/workspace/video',
          workspace_name: 'video',
          mode: 'auto',
          metadata: { agent_name: VIDEO_STUDIO_AGENT_NAME, ...videoStudioSessionMetadata() },
          session_api: 'v3',
          last_event_seq: 1,
          projection_high_watermark_seq: 1,
          created_at: 1,
          updated_at: 1,
        },
        projection: { session_id: 'video-session-1', last_event_seq: 1, projection_high_watermark_seq: 1, updated_at: 1 },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v1/workspace/video/threads') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      assert.equal(body.session_id, 'video-session-1')
      assert.equal(body.workspace_path, '/workspace/video')
      return new Response(JSON.stringify({
        ok: true,
        thread: {
          id: body.session_id,
          title: body.title,
          workspace_path: body.workspace_path,
          workspace_name: body.workspace_name,
          video_folders: [],
          video_clips: [],
          video_clip_order: [],
          created_at: 1,
          updated_at: 1,
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const thread = await createVideoThread({
      title: 'Launch video',
      workspacePath: '/workspace/video',
      workspaceName: 'video',
      route,
      clips: [],
    })
    assert.equal(thread.id, 'video-session-1')
    assert.equal(thread.title, 'Launch video')
    assert.deepEqual(calls.map((call) => String(call.input)), [
      '/v1/model',
      '/v3/sessions',
      '/v1/workspace/video/threads',
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Video Studio initializes the primary project with an empty durable base revision', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    assert.equal(url, '/v3/sessions/session-1/video/projects/primary')
    assert.equal(init?.method, 'POST')
    const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
    const timeline = body.initial_timeline as Record<string, unknown>
    assert.equal(timeline.schema_version, 1)
    assert.equal(timeline.output_preset, 'landscape_1080p')
    assert.deepEqual(timeline.clips, [])
    assert.deepEqual(timeline.transitions, [])
    return new Response(JSON.stringify({
      project: { id: 'project-1', session_id: 'session-1', title: 'Video', current_revision_id: 'revision-1', current_revision_number: 1, project_kind: 'video_tool', created_at: 1, updated_at: 1 },
      revision: { id: 'revision-1', project_id: 'project-1', session_id: 'session-1', revision_number: 1, timeline, created_at: 1 },
    }), { status: 201, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  try {
    const detail = await ensurePrimaryVideoProject('session-1', 'Video')
    assert.equal(detail.project.current_revision_id, 'revision-1')
    assert.equal(detail.current_revision?.timeline.clips.length, 0)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Video Studio creates and lists multiple durable projects in one session', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push(url)
    if (url === '/v3/sessions/session-1/video/projects' && init?.method === 'POST') {
      const body = JSON.parse(String(init.body ?? '{}')) as Record<string, unknown>
      assert.equal(body.title, 'Video 2')
      assert.equal((body.initial_timeline as { clips?: unknown[] }).clips?.length, 0)
      return new Response(JSON.stringify({
        project: { id: 'project-2', session_id: 'session-1', title: 'Video 2', current_revision_id: 'revision-1', current_revision_number: 1, created_at: 1, updated_at: 1 },
        revision: { id: 'revision-1', project_id: 'project-2', session_id: 'session-1', revision_number: 1, timeline: body.initial_timeline, created_at: 1 },
      }), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v3/sessions/session-1/video/projects?limit=32') {
      return new Response(JSON.stringify({ projects: [
        { id: 'project-1', session_id: 'session-1', title: 'Video 1', created_at: 1, updated_at: 1 },
        { id: 'project-2', session_id: 'session-1', title: 'Video 2', created_at: 2, updated_at: 2 },
      ] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const created = await createAdditionalVideoProject('session-1', 'Video 2')
    assert.equal(created.project.id, 'project-2')
    assert.equal(created.current_revision?.id, 'revision-1')
    assert.equal((await listVideoProjects('session-1')).length, 2)
    assert.deepEqual(calls, [
      '/v3/sessions/session-1/video/projects',
      '/v3/sessions/session-1/video/projects?limit=32',
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Video Studio reports route unavailable only without a primary self V3 authority', () => {
  assert.equal(resolveVideoStudioSessionRoute(videoStudioWorkspace(videoStudioSelfRoute), null), null)
  assert.equal(resolveVideoStudioSessionRoute(videoStudioWorkspace({
    ...videoStudioSelfRoute,
    routeId: 'swarm:managed-swarm:binding:binding-managed',
    workspaceBindingId: 'binding-managed',
    runtimeSwarmId: 'managed-swarm',
    runtimeRelationship: 'managed',
  }), videoStudioSwarmTarget), null)
})

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

test('pending visual plan tolerates an empty legacy base revision', () => {
  const shadow = applyPendingVideoProposal({ schema_version: 1, clips: null as unknown as [], transitions: null as unknown as [] }, {
    id: 'proposal-empty-base', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', created_at: 1, updated_at: 1, operations: [],
    plan: { kind: 'initial', parts: [{ id: 'step-1', title: 'Launch', duration_ms: 4000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'still-1', event_seq: 1 } }] },
  })
  assert.equal(shadow.clips.length, 1)
  assert.equal(shadow.clips[0].artifact_ref?.variant_id, 'still-1')
})

test('pending selective still revision preserves accepted auxiliary footage in the live shadow cut', () => {
  const accepted: VideoProjectTimelineWire = {
    schema_version: 1,
    total_duration_ms: 20000,
    clips: [
      { id: 'step-1', track: 0, sequence: 0, source_kind: 'managed_artifact', duration_ms: 6000, timeline_start_ms: 0, timeline_end_ms: 6000, visible: true },
      { id: 'step-2', track: 0, sequence: 1, source_kind: 'managed_artifact', duration_ms: 7000, timeline_start_ms: 6000, timeline_end_ms: 13000, visible: true },
      { id: 'step-3', track: 0, sequence: 2, source_kind: 'managed_artifact', duration_ms: 7000, timeline_start_ms: 13000, timeline_end_ms: 20000, visible: true },
      { id: 'step-1-footage', track: 1, sequence: 0, layer: 1, source_kind: 'source_video', source_ref: 'videosrc_step_1', duration_ms: 1000, timeline_start_ms: 0, timeline_end_ms: 1000, visible: true },
    ],
    transitions: [],
    metadata: {
      accepted_video_plan: {
        kind: 'initial',
        parts: [
          { id: 'step-1', title: 'Step 1', duration_ms: 6000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'old-1', event_seq: 1 } },
          { id: 'step-2', title: 'Step 2', duration_ms: 7000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'old-2', event_seq: 2 } },
          { id: 'step-3', title: 'Step 3', duration_ms: 7000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'old-3', event_seq: 3 } },
        ],
      },
    },
  }
  const shadow = applyPendingVideoProposal(accepted, {
    id: 'proposal-visual', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', created_at: 1, updated_at: 1, operations: [],
    plan: {
      kind: 'revision',
      parts: [{ id: 'step-2', title: 'Step 2', duration_ms: 7000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'new-2', event_seq: 4 } }],
    },
  })

  assert.equal(shadow.clips.find((clip) => clip.id === 'step-2')?.artifact_ref?.variant_id, 'new-2')
  assert.deepEqual(shadow.clips.find((clip) => clip.id === 'step-1-footage'), accepted.clips[3])
  assert.equal(shadow.total_duration_ms, 20000)
})

test('pending overlay footage stays synchronized with step 1 without mutating the accepted timeline', () => {
  const accepted: VideoProjectTimelineWire = {
    schema_version: 1,
    total_duration_ms: 20000,
    clips: [
      { id: 'step-1', track: 0, sequence: 0, source_kind: 'managed_artifact', duration_ms: 6000, timeline_start_ms: 0, timeline_end_ms: 6000, visible: true },
      { id: 'step-2', track: 0, sequence: 1, source_kind: 'managed_artifact', duration_ms: 7000, timeline_start_ms: 6000, timeline_end_ms: 13000, visible: true },
      { id: 'step-3', track: 0, sequence: 2, source_kind: 'managed_artifact', duration_ms: 7000, timeline_start_ms: 13000, timeline_end_ms: 20000, visible: true },
    ],
    transitions: [],
  }
  const shadow = applyPendingVideoProposal(accepted, {
    id: 'proposal-1', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', created_at: 1, updated_at: 1,
    affected_ranges: [{ start_ms: 0, end_ms: 1000 }],
    operations: [{
      id: 'add-step-1-footage',
      type: 'add_clip',
      clip: {
        id: 'step-1-footage',
        track: 1,
        sequence: 0,
        layer: 1,
        source_kind: 'source_video',
        source_ref: 'videosrc_step_1',
        source_start_ms: 0,
        source_end_ms: 1000,
        duration_ms: 1000,
        timeline_start_ms: 0,
        timeline_end_ms: 1000,
        visible: true,
      },
    }],
  })

  assert.equal(accepted.clips.length, 3)
  assert.deepEqual(accepted.clips.map((clip) => clip.id), ['step-1', 'step-2', 'step-3'])
  assert.equal(accepted.total_duration_ms, 20000)
  assert.equal(shadow.clips.length, 4)
  assert.equal(shadow.total_duration_ms, 20000)
  assert.equal(shadow.metadata?.shadow_proposal_id, 'proposal-1')

  const segments = projectTimelineToTimelineSegments(shadow, {})
  assert.deepEqual(segments.map((segment) => segment.id), ['step-1', 'step-1-footage', 'step-2', 'step-3'])
  const layout = layoutTimelineSegments(segments)
  const step1 = layout.find((segment) => segment.id === 'step-1')
  const footage = layout.find((segment) => segment.id === 'step-1-footage')
  const step3 = layout.find((segment) => segment.id === 'step-3')
  assert.deepEqual([step1?.timelineStart, step1?.timelineEnd], [0, 6])
  assert.deepEqual([footage?.timelineStart, footage?.timelineEnd], [0, 1])
  assert.deepEqual([footage?.track, footage?.layer], [1, 1])
  assert.deepEqual([step3?.timelineStart, step3?.timelineEnd], [13, 20])
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

test('projectTimelineToTimelineSegments uses the canonical variant artifact endpoint for managed stills', () => {
  const timeline: VideoProjectTimelineWire = {
    schema_version: 1,
    clips: [{
      id: 'slide-1',
      source_kind: 'managed_artifact',
      artifact_ref: {
        session_id: 'session-1',
        collection_id: 'collection-1',
        variant_id: 'variant-1',
        event_seq: 42,
      },
      duration_ms: 3000,
      visible: true,
    }],
  }

  const [segment] = projectTimelineToTimelineSegments(timeline, {}, [], 'session-fallback')

  assert.equal(segment.type, 'image')
  assert.equal(segment.src, '/v3/sessions/session-1/artifacts/variant-1')
})

test('project timeline transitions round-trip and normalize preview duration to render overlap', () => {
  const timeline: VideoProjectTimelineWire = {
    schema_version: 1,
    clips: [
      { id: 'clip-a', source_kind: 'source_video', source_ref: 'source-a', duration_ms: 4000, visible: true },
      { id: 'clip-b', source_kind: 'source_video', source_ref: 'source-b', duration_ms: 6000, visible: true },
    ],
    transitions: [{ id: 'fade-a-b', kind: 'crossfade', from_clip_id: 'clip-a', to_clip_id: 'clip-b', duration_ms: 500 }],
  }

  const segments = projectTimelineToTimelineSegments(timeline, {})
  assert.equal(segments[1].transitionIn?.id, 'fade-a-b')
  assert.equal(segments[1].start, 3.5)

  const roundTripped = timelineSegmentsToProjectTimeline(segments, [])
  assert.equal(roundTripped.total_duration_ms, 9500)
  assert.deepEqual(roundTripped.transitions, timeline.transitions)
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

test('Video Studio nests stable changed parts beneath their parent and candidate iteration identity', () => {
  const proposal = {
    id: 'change-1', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    accepted_revision_id: 'revision-2', status: 'accepted', operations: [], created_at: 20, updated_at: 21,
    title: 'Tighten the launch story',
    plan: {
      kind: 'revision',
      parts: [
        { id: 'hook', title: 'Hook', duration_ms: 1000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'hook-2', event_seq: 8 } },
        { id: 'close', title: 'Close', duration_ms: 2000 },
      ],
    },
  } satisfies VideoEditProposalWire

  const [iteration] = buildVideoIterationTimeline([proposal], [{
    id: 'revision-2', revision_number: 2, parent_revision_id: 'revision-1', origin_proposal_id: 'change-1', change_summary: 'Accepted selected launch changes', created_at: 22,
  }])

  assert.equal(iteration.id, 'proposal:change-1')
  assert.equal(iteration.parentRevisionId, 'revision-1')
  assert.equal(iteration.candidateRevisionId, 'revision-2')
  assert.equal(iteration.candidateRevisionNumber, 2)
  assert.deepEqual(iteration.changes.map((change) => ({ id: change.id, clipId: change.clipId, range: [change.startMs, change.endMs] })), [
    { id: 'hook', clipId: 'hook', range: [0, 1000] },
    { id: 'close', clipId: 'close', range: [1000, 3000] },
  ])
  assert.equal(iteration.changes[0].artifact?.variant_id, 'hook-2')
})

test('Video Studio shows only selectively accepted changes inside accepted iterations', () => {
  const proposal = {
    id: 'change-1', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    accepted_revision_id: 'revision-2', accepted_operation_ids: ['hook'], status: 'accepted', operations: [], created_at: 20, updated_at: 21,
    plan: { kind: 'revision', parts: [{ id: 'hook', title: 'Hook', duration_ms: 1000 }, { id: 'close', title: 'Close', duration_ms: 2000 }] },
  } satisfies VideoEditProposalWire

  const [iteration] = buildVideoIterationTimeline([proposal], [{
    id: 'revision-2', revision_number: 2, parent_revision_id: 'revision-1', origin_proposal_id: 'change-1', created_at: 22,
  }])

  assert.deepEqual(iteration.changes.map((change) => change.id), ['hook'])
})

test('Video Studio keeps all changed sections enabled by default and omits disabled changes from selective confirmation', () => {
  const proposal = {
    id: 'change-1', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: [], created_at: 1, updated_at: 1,
    plan: { kind: 'revision', parts: [{ id: 'hook', title: 'Hook', duration_ms: 1000 }, { id: 'close', title: 'Close', duration_ms: 1000 }] },
  } satisfies VideoEditProposalWire

  assert.deepEqual(selectedVideoProposalChangeIDs(proposal, {}), ['hook', 'close'])
  assert.deepEqual(selectedVideoProposalChangeIDs(proposal, { 'change-1:part:close': false }), ['hook'])
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
