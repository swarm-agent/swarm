import assert from 'node:assert/strict'
import test from 'node:test'

import { VIDEO_TRANSITION_KINDS, buildVideoIterationTimeline, loadLatestVideoEditProposals, proposedVideoPlanClipDetails, rejectVideoEditProposal, renderedVideoArtifactUrl, selectedVideoProposalChangeIDs, transitionLabel, videoPlanPartMessageSelection, videoPlanTransitionMessageSelection, videoProposalFocusClipId, videoProposalProjectionSequence, videoProposalsForConversationTurn, type VideoEditProposalWire } from '../video-studio/video-studio-surface'

import {
  acceptedVideoPlan,
  applyPendingVideoProposal,
  createAdditionalVideoProject,
  defaultRenderedVideoExportPath,
  preferredVisibleVideoProject,
  proposalOwnsAnimationPart,
  createVideoThread,
  ensurePrimaryVideoProject,
  fetchWorkspaceVideoCatalog,
  filterWorkspaceVideoCatalog,
  forkWorkspaceVideoRevision,
  listVideoProjects,
  layoutTimelineSegments,
  projectTimelineToTimelineSegments,
  replaceCachedImageMedia,
  replaceCachedVideoMedia,
  replaceVideoEditProposal,
  resolveVideoStudioSessionRoute,
  scanVideoFolder,
  serializeVideoClipForRequest,
  soundtrackTimelineClip,
  timelineSegmentsToProjectTimeline,
  videoPlanClipDetails,
  videoPlanForPlayback,
  videoActivePreviewCandidate,
  videoActivePreviewIdentity,
  videoAnimationPartAtClip,
  videoClipReviewState,
  VIDEO_STUDIO_AGENT_NAME,
  videoChildSessionMetadata,
  videoStudioSessionMetadata,
  selectVideoAnimationCandidateLocally,
  unresolvedVideoIterationLockPartIDs,
  selectWorkspaceVideoRevision,
  workspaceVideoContextMetadata,
  workspaceVideosForSession,
  type VideoClip,
  type WorkspaceVideoCatalogItemWire,
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

test('Video Studio scans authenticated audio references without accepting host paths', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.equal(String(input), '/v1/workspace/video/scan')
    assert.deepEqual(JSON.parse(String(init?.body)), { workspace_path: '/workspace/video', root_path: '/workspace/video/media' })
    return new Response(JSON.stringify({ root_path: '/workspace/video/media', clips: [], audio_clips: [{ ref: 'audiosrc_exact', name: 'theme.wav', extension: '.wav', mime_type: 'audio/wav', size_bytes: 42, modified_at: 1, source_fingerprint: 'fingerprint', fingerprint_version: 'v1', path: '/forbidden.wav' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    const result = await scanVideoFolder('/workspace/video', '/workspace/video/media')
    assert.deepEqual(result.audioClips, [{ ref: 'audiosrc_exact', name: 'theme.wav', extension: '.wav', mime_type: 'audio/wav', size_bytes: 42, modified_at: 1, source_fingerprint: 'fingerprint', fingerprint_version: 'v1' }])
    assert.equal('path' in result.audioClips[0], false)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('Video Studio builds a bounded durable soundtrack clip for a pending proposal', () => {
  const clip = soundtrackTimelineClip({ audio: { ref: 'audiosrc_exact', name: 'theme.wav', extension: '.wav', mime_type: 'audio/wav', size_bytes: 42, modified_at: 1, source_fingerprint: 'fingerprint', fingerprint_version: 'v1' }, durationMs: 5500 })
  assert.equal(clip.source_kind, 'source_audio')
  assert.equal(clip.visible, false)
  assert.equal(clip.audio_source?.ref, 'audiosrc_exact')
  assert.deepEqual([clip.source_start_ms, clip.source_end_ms, clip.timeline_start_ms, clip.timeline_end_ms], [0, 5500, 0, 5500])
  assert.equal('path' in (clip.audio_source ?? {}), false)
})

test('Video Studio maps accepted and pending source audio into its dedicated preview lane', () => {
  const timeline: VideoProjectTimelineWire = { schema_version: 1, clips: [{ id: 'visual', source_kind: 'color', duration_ms: 5000, timeline_start_ms: 0, timeline_end_ms: 5000, visible: true }, { id: 'soundtrack', name: 'theme.wav', source_kind: 'source_audio', audio_source: { ref: 'audiosrc_exact', name: 'theme.wav', mime_type: 'audio/wav', size_bytes: 42, source_fingerprint: 'fingerprint', fingerprint_version: 'v1' }, duration_ms: 3000, source_start_ms: 500, source_end_ms: 3500, timeline_start_ms: 1000, timeline_end_ms: 4000, volume: 0.65, muted: false }] }
  const segments = projectTimelineToTimelineSegments(timeline, {}, [], 'session-1')
  assert.equal(segments[1].type, 'audio')
  assert.equal(segments[1].src, '/v3/sessions/session-1/video/sources/media?source_ref=audiosrc_exact')
  assert.deepEqual([segments[1].start, segments[1].sourceStart, segments[1].duration, segments[1].volume, segments[1].muted], [1, 0.5, 3, 0.65, false])
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

test('Video Studio preserves the accepted live animation while previewing a soundtrack-only proposal', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'slide-1', event_seq: 9 }
  const candidate = { id: 'scan', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'scan', event_seq: 12 } }
  const timeline: VideoProjectTimelineWire = {
    clips: [],
    metadata: { accepted_video_plan: { kind: 'initial', parts: [{ id: 'intro', title: 'Intro', duration_ms: 8000, visual, animation_candidates: { candidates: [candidate], selected_candidate_id: 'scan', selected_source: candidate.source, status: 'awaiting_export' } }] } },
  }
  const soundtrackProposal: VideoEditProposalWire = {
    id: 'soundtrack', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: [{ id: 'add-audio', type: 'add_clip', clip: { id: 'audio', source_kind: 'source_audio' } }], created_at: 1, updated_at: 1,
  }

  assert.equal(videoPlanForPlayback(soundtrackProposal, timeline)?.parts[0].animation_candidates?.selected_candidate_id, 'scan')
  assert.equal(proposalOwnsAnimationPart(soundtrackProposal, 'intro'), false)
})

test('Video Studio does not project an older pending proposal into a newer conversation turn', () => {
  const oldProposal = {
    id: 'old', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending' as const, operations: [], created_at: 100, updated_at: 100,
  }
  const currentProposal = { ...oldProposal, id: 'current', created_at: 300, updated_at: 300 }

  assert.deepEqual(videoProposalsForConversationTurn([oldProposal, currentProposal], 200).map((proposal) => proposal.id), ['current'])
  assert.deepEqual(videoProposalsForConversationTurn([oldProposal], 200), [])
  assert.deepEqual(videoProposalsForConversationTurn([oldProposal], 0), [oldProposal])
})

test('Video Studio keeps only the selected animation playing after its iteration turn is over', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'slide-1', event_seq: 9 }
  const orbit = { id: 'orbit', source: { ...visual, variant_id: 'orbit' } }
  const scan = { id: 'scan', source: { ...visual, variant_id: 'scan' } }
  const timeline: VideoProjectTimelineWire = {
    clips: [],
    metadata: { accepted_video_plan: { kind: 'initial', parts: [{ id: 'intro', title: 'Intro', duration_ms: 8000, visual, animation_candidates: { candidates: [orbit, scan], selected_candidate_id: 'scan', selected_source: scan.source, status: 'awaiting_export' } }] } },
  }

  const playback = videoPlanForPlayback(null, timeline, false)?.parts[0].animation_candidates
  assert.deepEqual(playback?.candidates.map((candidate) => candidate.id), ['scan'])
  assert.equal(playback?.selected_candidate_id, 'scan')
  assert.deepEqual(videoPlanForPlayback(null, timeline)?.parts[0].animation_candidates?.candidates.map((candidate) => candidate.id), ['orbit', 'scan'])
})

test('Video Studio falls back to the still when an older animation turn has no selected candidate', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'slide-1', event_seq: 9 }
  const timeline: VideoProjectTimelineWire = {
    clips: [],
    metadata: { accepted_video_plan: { kind: 'initial', parts: [{ id: 'intro', title: 'Intro', duration_ms: 8000, visual, animation_candidates: { candidates: [{ id: 'scan', source: visual }], status: 'awaiting_selection' } }] } },
  }

  assert.equal(videoPlanForPlayback(null, timeline, false)?.parts[0].animation_candidates, undefined)
})

test('Video Studio updates only the selected stable part and rejects cross-part candidate combinations', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'fallback', event_seq: 9 }
  const introA = { id: 'intro-a', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'intro-a', event_seq: 10 } }
  const introB = { id: 'intro-b', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'intro-b', event_seq: 11 } }
  const outroA = { id: 'outro-a', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'outro-a', event_seq: 12 } }
  const proposal: VideoEditProposalWire = {
    id: 'multi-part', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: [], created_at: 1, updated_at: 1,
    plan: { kind: 'revision', parts: [
      { id: 'intro', title: 'Intro', duration_ms: 5000, visual, animation_candidates: { candidates: [introA, introB], status: 'awaiting_selection' } },
      { id: 'outro', title: 'Outro', duration_ms: 4000, visual, animation_candidates: { candidates: [outroA], status: 'awaiting_selection' } },
    ] },
  }

  const selected = selectVideoAnimationCandidateLocally(proposal, 'intro', introB)
  assert.equal(selected?.plan?.parts[0].animation_candidates?.selected_candidate_id, 'intro-b')
  assert.equal(selected?.plan?.parts[0].animation_candidates?.status, 'awaiting_export')
  assert.equal(selected?.plan?.parts[1].animation_candidates?.selected_candidate_id, undefined)
  assert.equal(selectVideoAnimationCandidateLocally(proposal, 'intro', outroA), null)
  assert.equal(selectVideoAnimationCandidateLocally(proposal, 'outro', { ...outroA, source: introA.source }), null)
  assert.equal(replaceVideoEditProposal([proposal], selected ?? proposal)[0], selected)
  assert.deepEqual(replaceVideoEditProposal([], selected ?? proposal), [selected])
})

test('Video Studio blocks rendering only when a selected clip has multiple unlocked iterations', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'fallback', event_seq: 9 }
  const proposal: VideoEditProposalWire = {
    id: 'render-locks', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: [], created_at: 1, updated_at: 1,
    plan: { kind: 'revision', parts: [
      { id: 'plain', title: 'Plain', duration_ms: 3000, visual },
      { id: 'single', title: 'Single', duration_ms: 3000, visual, animation_candidates: { candidates: [{ id: 'only', source: { ...visual, variant_id: 'only' } }], status: 'awaiting_selection' } },
      { id: 'multi', title: 'Multi', duration_ms: 3000, visual, animation_candidates: { candidates: [{ id: 'one', source: { ...visual, variant_id: 'one' } }, { id: 'two', source: { ...visual, variant_id: 'two' } }], status: 'awaiting_selection' } },
    ] },
  }

  assert.deepEqual(unresolvedVideoIterationLockPartIDs(proposal, ['plain', 'single']), [])
  assert.deepEqual(unresolvedVideoIterationLockPartIDs(proposal, ['multi']), ['multi'])
  const locked = selectVideoAnimationCandidateLocally(proposal, 'multi', proposal.plan!.parts[2].animation_candidates!.candidates[1])
  assert.deepEqual(unresolvedVideoIterationLockPartIDs(locked, ['multi']), [])
  assert.deepEqual(unresolvedVideoIterationLockPartIDs({ ...proposal, plan: { ...proposal.plan!, kind: 'initial' } }), ['multi'])
  assert.deepEqual(unresolvedVideoIterationLockPartIDs({ ...proposal, plan: undefined }, ['multi']), [])
})

test('Video Studio prefers animation candidates owned by the pending visual proposal', () => {
  const visual = { session_id: 'session-1', collection_id: 'slides', variant_id: 'slide-1', event_seq: 9 }
  const proposal: VideoEditProposalWire = {
    id: 'visual-change', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', operations: [], created_at: 1, updated_at: 1,
    plan: { kind: 'revision', parts: [{ id: 'intro', title: 'Intro', duration_ms: 8000, visual, animation_candidates: { candidates: [{ id: 'pulse', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'pulse', event_seq: 13 } }], status: 'awaiting_selection' } }] },
  }

  assert.equal(videoPlanForPlayback(proposal, { clips: [] }), proposal.plan)
  assert.equal(proposalOwnsAnimationPart(proposal, 'intro'), true)
})

test('Video Studio binds active HTML preview to exact project, proposal, revision, clip, part, candidate, and source lineage', () => {
  const visual = { session_id: 'session-1', collection_id: 'fallbacks', variant_id: 'still', event_seq: 9 }
  const candidate = { id: 'orbit', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'orbit-v2', event_seq: 12 } }
  const part = { id: 'intro', title: 'Intro', duration_ms: 8000, visual, animation_candidates: { candidates: [candidate], selected_candidate_id: 'orbit', selected_source: candidate.source, status: 'awaiting_export' as const } }
  const proposal: VideoEditProposalWire = {
    id: 'proposal-2', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    working_revision_id: 'revision-2', status: 'pending', operations: [], created_at: 1, updated_at: 1,
    plan: { kind: 'revision', parts: [part] },
  }
  const identity = videoActivePreviewIdentity({ projectId: 'project-1', proposal, revisionId: 'revision-2', timelineClipId: 'intro', part, candidate })

  assert.equal(videoActivePreviewCandidate({ identity, projectId: 'project-1', proposal, revisionId: 'revision-2', timelineClipId: 'intro', part })?.id, 'orbit')
  assert.equal(videoActivePreviewCandidate({ identity, projectId: 'project-1', proposal, revisionId: 'revision-2', timelineClipId: 'outro', part }), null)
  assert.equal(videoActivePreviewCandidate({ identity, projectId: 'project-1', proposal: { ...proposal, id: 'proposal-3' }, revisionId: 'revision-2', timelineClipId: 'intro', part }), null)
  assert.equal(videoActivePreviewCandidate({ identity, projectId: 'project-1', proposal: { ...proposal, working_revision_id: 'revision-3' }, revisionId: 'revision-3', timelineClipId: 'intro', part }), null)
  assert.equal(videoActivePreviewCandidate({ identity, projectId: 'project-1', proposal, revisionId: 'revision-2', timelineClipId: 'intro', part: { ...part, animation_candidates: { ...part.animation_candidates, candidates: [{ ...candidate, source: { ...candidate.source, event_seq: 13 } }] } } }), null)
})

test('Video Studio reports one concise human review state for each clip media state', () => {
  const source = { session_id: 'session-1', collection_id: 'animations', variant_id: 'orbit', event_seq: 1 }
  const part = (status: 'awaiting_selection' | 'awaiting_export' | 'ready' | 'failed', selected = false) => ({
    id: 'intro', title: 'Intro', duration_ms: 1000,
    animation_candidates: { candidates: [{ id: 'orbit', source }], ...(selected ? { selected_candidate_id: 'orbit', selected_source: source } : {}), status },
  })
  assert.deepEqual(videoClipReviewState(part('awaiting_selection'), 'image/png', 'image'), { mediaKind: 'Live HTML', state: 'Choose motion' })
  assert.deepEqual(videoClipReviewState(part('awaiting_export', true), 'image/png', 'image'), { mediaKind: 'Live HTML', state: 'Motion selected · converting' })
  assert.deepEqual(videoClipReviewState(part('ready', true), 'video/mp4', 'video'), { mediaKind: 'Motion', state: 'Motion ready' })
  assert.deepEqual(videoClipReviewState(undefined, 'video/mp4', 'video'), { mediaKind: 'Video', state: 'Video' })
  assert.deepEqual(videoClipReviewState(undefined, 'image/png', 'image'), { mediaKind: 'Still', state: 'Still' })
})

test('Video Studio shows live iterations only while their clip owns the playhead', () => {
  const plan = {
    kind: 'initial' as const,
    parts: [
      { id: 'intro-main', title: 'Intro', duration_ms: 8000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'intro', event_seq: 1 }, animation_candidates: { candidates: [{ id: 'orbit', source: { session_id: 'session-1', collection_id: 'animations', variant_id: 'orbit', event_seq: 2 } }], status: 'awaiting_selection' as const } },
      { id: 'parallel-build', title: 'Build', duration_ms: 7000, visual: { session_id: 'session-1', collection_id: 'slides', variant_id: 'build', event_seq: 3 } },
    ],
  }

  assert.equal(videoAnimationPartAtClip(plan, 'intro-main')?.id, 'intro-main')
  assert.equal(videoAnimationPartAtClip(plan, 'parallel-build'), null)
  assert.equal(videoAnimationPartAtClip(plan, null), null)
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

test('Video Studio filters standalone videos across title and related session provenance', () => {
  const items = [{
    project: { id: 'project-1', session_id: 'source-1', title: 'Launch film', current_revision_id: 'revision-2', created_at: 1, updated_at: 2 },
    revisions: [
      { id: 'revision-1', project_id: 'project-1', session_id: 'source-1', revision_number: 1, timeline: { clips: [] }, created_at: 1 },
      { id: 'revision-2', project_id: 'project-1', session_id: 'source-1', revision_number: 2, timeline: { clips: [] }, created_at: 2 },
    ],
    source_archived: true,
    source_session_id: 'source-1',
    source_session_title: 'Original campaign',
    related_sessions: [{ session_id: 'related-1', title: 'Polish pass' }],
  }] satisfies WorkspaceVideoCatalogItemWire[]
  assert.equal(filterWorkspaceVideoCatalog(items, 'launch').length, 1)
  assert.equal(filterWorkspaceVideoCatalog(items, 'polish').length, 1)
  assert.equal(filterWorkspaceVideoCatalog(items, 'missing').length, 0)
  assert.equal(selectWorkspaceVideoRevision(items[0])?.id, 'revision-2')
  assert.equal(selectWorkspaceVideoRevision(items[0], 'revision-1')?.revision_number, 1)
})

test('Video Studio loads standalone videos without a destination session and forks exact selected context', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push(url)
    if (url === '/v1/workspace/video/projects?workspace_path=%2Fworkspace%2Fvideo&limit=200') return new Response(JSON.stringify({ videos: [{ project: { id: 'project-1', session_id: 'source-1', title: 'Launch', created_at: 1, updated_at: 1 }, revisions: null, related_sessions: null, source_session_id: 'source-1' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (url === '/v1/workspace/video/projects/fork') {
      assert.deepEqual(JSON.parse(String(init?.body)), { workspace_path: '/workspace/video', source_session_id: 'source-1', source_project_id: 'project-1', source_revision_id: 'revision-7', destination_session_id: 'destination-1', attach_to_session: false })
      return new Response(JSON.stringify({ project: { id: 'forked', session_id: 'destination-1', title: 'Launch', created_at: 2, updated_at: 2 }, revision: { id: 'forked-revision', project_id: 'forked', session_id: 'destination-1', revision_number: 1, timeline: { clips: [] }, created_at: 2 } }), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch
  try {
    const videos = await fetchWorkspaceVideoCatalog('/workspace/video')
    assert.deepEqual(videos[0].revisions, [])
    assert.deepEqual(videos[0].related_sessions, [])
    const fork = await forkWorkspaceVideoRevision({ workspacePath: '/workspace/video', sourceSessionId: 'source-1', sourceProjectId: 'project-1', sourceRevisionId: 'revision-7', destinationSessionId: 'destination-1' })
    assert.equal(fork.current_revision?.id, 'forked-revision')
    assert.deepEqual(calls, ['/v1/workspace/video/projects?workspace_path=%2Fworkspace%2Fvideo&limit=200', '/v1/workspace/video/projects/fork'])
  } finally { globalThis.fetch = originalFetch }
})

test('Video Studio session metadata attaches exact standalone video identity for AI context', () => {
  const item = { project: { id: 'project-1', session_id: 'source-1', title: 'Launch', created_at: 1, updated_at: 1 }, revisions: [], source_session_id: 'source-1', related_sessions: [] } satisfies WorkspaceVideoCatalogItemWire
  assert.deepEqual(workspaceVideoContextMetadata(item, 'revision-7'), {
    experience: 'video_studio', launch_source: 'video_library', lineage_kind: 'video_project', creative_mode: 'video',
    source_session_id: 'source-1', source_video_project_id: 'project-1', source_video_revision_id: 'revision-7',
    video_context: { source_session_id: 'source-1', source_project_id: 'project-1', source_revision_id: 'revision-7', title: 'Launch' },
  })
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

test('pending visual plan consumes typed MP4 ranges, captions, and transitions without synthesizing presentation', () => {
  const shadow = applyPendingVideoProposal({ schema_version: 1, clips: [], transitions: [] }, {
    id: 'proposal-motion', project_id: 'project-1', base_revision_id: 'revision-1', base_revision_number: 1,
    status: 'pending', created_at: 1, updated_at: 1, operations: [],
    plan: {
      kind: 'initial',
      parts: [
        { id: 'motion-1', title: 'Motion', duration_ms: 2000, on_screen_text: 'descriptive only', transition_in: 'descriptive only', visual_media_type: 'video/mp4', source_start_ms: 500, source_end_ms: 2500, visual: { session_id: 'session-1', collection_id: 'motion', variant_id: 'clip-1', event_seq: 7 } },
        { id: 'still-2', title: 'Still', duration_ms: 1000, caption: { id: 'caption-2', text: 'Explicit', position: 'bottom', start_ms: 100, end_ms: 900 }, transition: { id: 'cut-1-2', kind: 'cut', from_clip_id: 'motion-1', to_clip_id: 'still-2' }, visual: { session_id: 'session-1', collection_id: 'stills', variant_id: 'still-2', event_seq: 8 } },
      ],
    },
  })

  assert.deepEqual([shadow.clips[0].source_start_ms, shadow.clips[0].source_end_ms, shadow.clips[0].captions], [500, 2500, []])
  assert.deepEqual(shadow.clips[1].captions, [{ id: 'caption-2', text: 'Explicit', position: 'bottom', start_ms: 2100, end_ms: 2900 }])
  assert.deepEqual(shadow.transitions, [{ id: 'cut-1-2', kind: 'cut', from_clip_id: 'motion-1', to_clip_id: 'still-2' }])
  const segments = projectTimelineToTimelineSegments(shadow, {}, [], 'session-1')
  assert.deepEqual([segments[0].type, segments[0].sourceStart, segments[0].duration], ['video', 0.5, 2])
  assert.equal(segments[0].src, '/v3/sessions/session-1/artifacts/clip-1')
  assert.deepEqual(segments[1].captions, shadow.clips[1].captions)
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

test('standalone video library filters videos and related sessions', () => {
  const items: WorkspaceVideoCatalogItemWire[] = [{
    project: { id: 'project-1', session_id: 'session-source', title: 'Launch film', description: 'Product reveal', current_revision_id: 'revision-2', current_revision_number: 2, revision_count: 2, created_at: 1, updated_at: 2 },
    revisions: [
      { id: 'revision-1', project_id: 'project-1', session_id: 'session-source', revision_number: 1, timeline: { schema_version: 1, clips: [] }, created_at: 1 },
      { id: 'revision-2', project_id: 'project-1', session_id: 'session-source', revision_number: 2, timeline: { schema_version: 1, clips: [] }, created_at: 2 },
    ],
    source_archived: true,
    source_session_id: 'session-source',
    source_session_title: 'Original session',
    related_sessions: [{ session_id: 'session-related', title: 'Follow-up edit' }],
  }]

  assert.equal(filterWorkspaceVideoCatalog(items, 'follow-up').length, 1)
  assert.equal(filterWorkspaceVideoCatalog(items, 'missing').length, 0)
  assert.equal(selectWorkspaceVideoRevision(items[0])?.id, 'revision-2')
  assert.equal(selectWorkspaceVideoRevision(items[0], 'revision-1')?.id, 'revision-1')
})

test('workspace video session selection resolves one or multiple associated videos', () => {
  const first = { project: { id: 'video-a', session_id: 'source-a', title: 'A', created_at: 1, updated_at: 1 }, revisions: [], source_session_id: 'source-a', related_sessions: [{ session_id: 'shared', title: 'Shared' }] } satisfies WorkspaceVideoCatalogItemWire
  const second = { project: { id: 'video-b', session_id: 'source-b', title: 'B', created_at: 1, updated_at: 1 }, revisions: [], source_session_id: 'source-b', related_sessions: [{ session_id: 'shared', title: 'Shared' }] } satisfies WorkspaceVideoCatalogItemWire
  assert.deepEqual(workspaceVideosForSession([first, second], 'source-a').map((item) => item.project.id), ['video-a'])
  assert.deepEqual(workspaceVideosForSession([first, second], 'shared').map((item) => item.project.id), ['video-a', 'video-b'])
})

test('workspaceVideoContextMetadata carries exact standalone video revision identity', () => {
  const item: WorkspaceVideoCatalogItemWire = {
    project: { id: 'project-1', session_id: 'session-source', title: 'Launch film', current_revision_id: 'revision-2', current_revision_number: 2, revision_count: 2, created_at: 1, updated_at: 2 },
    revisions: [],
    source_session_id: 'session-source',
    related_sessions: [],
  }

  const metadata = workspaceVideoContextMetadata(item, 'revision-1')
  assert.equal(metadata.launch_source, 'video_library')
  assert.equal(metadata.source_session_id, 'session-source')
  assert.equal(metadata.source_video_project_id, 'project-1')
  assert.equal(metadata.source_video_revision_id, 'revision-1')
  assert.deepEqual(metadata.video_context, {
    source_session_id: 'session-source',
    source_project_id: 'project-1',
    source_revision_id: 'revision-1',
    title: 'Launch film',
  })
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
