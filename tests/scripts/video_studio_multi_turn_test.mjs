#!/usr/bin/env node
import assert from 'node:assert/strict'
import { IdentityLedger, assertSyntheticDumpTopology, normalizeVideoSnapshot } from '../../scripts/runners/video-studio-multi-turn.mjs'

const ref = (suffix) => ({ session_id: 'session-test', collection_id: 'collection-test', variant_id: `variant-${suffix}`, event_seq: 1 })
const snapshot = ({ proposal = 'proposal-1', revision = 'revision-1', second = 'b1', includeThird = false } = {}) => ({
  project_id: 'project-1', base_revision_id: 'revision-base', working_revision_id: revision, proposal_id: proposal,
  clips: [
    { id: 'clip-a', part_id: 'part-a', media_type: 'text/html', start_ms: 0, end_ms: 10000, order: 0 },
    { id: 'clip-b', part_id: 'part-b', media_type: 'text/html', start_ms: 10000, end_ms: 20000, order: 1 },
    ...(includeThird ? [{ id: 'clip-c', part_id: 'part-c', media_type: 'text/html', start_ms: 20000, end_ms: 30000, order: 2 }] : []),
  ],
  parts: [
    { id: 'part-a', clip_id: 'clip-a', media_type: 'text/html', start_ms: 0, end_ms: 10000, order: 0, candidates: [{ id: 'a1', ...ref('a1') }, { id: 'a2', ...ref('a2') }], selected_source: ref('a1'), derivative: ref('a1-mp4') },
    { id: 'part-b', clip_id: 'clip-b', media_type: 'text/html', start_ms: 10000, end_ms: 20000, order: 1, candidates: [{ id: second, ...ref(second) }, { id: `${second}-alt`, ...ref(`${second}-alt`) }], selected_source: ref(second), derivative: ref(`${second}-mp4`) },
    ...(includeThird ? [{ id: 'part-c', clip_id: 'clip-c', media_type: 'text/html', start_ms: 20000, end_ms: 30000, order: 2, candidates: [{ id: 'c1', ...ref('c1') }, { id: 'c2', ...ref('c2') }], selected_source: ref('c1'), derivative: ref('c1-mp4') }] : []),
  ],
})

const dumped = assertSyntheticDumpTopology()
assert.equal(dumped.clips.length, 2)
assert.deepEqual(dumped.parts.map((part) => part.candidates.length), [2, 2])
assert.deepEqual(dumped.parts.map((part) => part.end_ms - part.start_ms), [10000, 10000])

const ledger = new IdentityLedger()
const first = ledger.record('first', snapshot())
const replaced = ledger.record('replace-b', snapshot({ proposal: 'proposal-2', revision: 'revision-2', second: 'b2' }))
ledger.assertNoDrift(first, replaced, { mutablePartIDs: ['part-b'], mutableClipIDs: ['clip-b'] })
const appended = ledger.record('append-c', snapshot({ proposal: 'proposal-3', revision: 'revision-3', second: 'b2', includeThird: true }))
ledger.assertNoDrift(replaced, appended, { allowAppendedParts: true, allowAppendedClips: true })
assert.throws(() => ledger.assertNoDrift(first, replaced), /non-target part part-b drifted/)

const nested = normalizeVideoSnapshot({ project: { id: 'p' }, working_revision: { id: 'work', timeline: { clips: [{ id: 'clip', media_type: 'video/mp4', timeline_start_ms: 0, timeline_end_ms: 1000 }] } }, proposal: { id: 'proposal', base_revision_id: 'base', working_revision_id: 'work', plan: { parts: [{ id: 'part', duration_ms: 1000, visual_media_type: 'image/png', animation_candidates: { status: 'awaiting_export', selected_candidate_id: 'candidate', selected_source: ref('candidate'), candidates: [{ id: 'candidate', source: ref('candidate') }, { id: 'candidate-alt', source: ref('candidate-alt') }] } }] } } })
assert.equal(nested.project_id, 'p')
assert.equal(nested.base_revision_id, 'base')
assert.equal(nested.clips[0].media_type, 'video/mp4')
assert.equal(nested.parts[0].media_type, 'image/png')
assert.equal(nested.parts[0].selected_candidate_id, 'candidate')
assert.equal(nested.parts[0].candidates.length, 2)

const revisionOverlay = normalizeVideoSnapshot({ project: { id: 'p-overlay' }, working_revision: { id: 'work-overlay', timeline: { clips: [{ id: 'part-a', media_type: 'video/mp4', timeline_start_ms: 0, timeline_end_ms: 1000 }, { id: 'part-b', media_type: 'video/mp4', timeline_start_ms: 1000, timeline_end_ms: 2000 }], metadata: { accepted_video_plan: { parts: [{ id: 'part-a', duration_ms: 1000, visual_media_type: 'video/mp4', animation_candidates: { status: 'ready', selected_candidate_id: 'a1', selected_source: ref('a1'), derivative: ref('a1-mp4'), candidates: [{ id: 'a1', source: ref('a1') }, { id: 'a2', source: ref('a2') }] } }, { id: 'part-b', duration_ms: 1000, visual_media_type: 'video/mp4', animation_candidates: { status: 'ready', selected_candidate_id: 'b1', selected_source: ref('b1'), derivative: ref('b1-mp4'), candidates: [{ id: 'b1', source: ref('b1') }, { id: 'b2', source: ref('b2') }] } }] } } } }, proposal: { id: 'proposal-overlay', base_revision_id: 'base-overlay', working_revision_id: 'work-overlay', plan: { kind: 'revision', parts: [{ id: 'part-b', duration_ms: 1000, visual_media_type: 'video/mp4', animation_candidates: { status: 'ready', selected_candidate_id: 'b3', selected_source: ref('b3'), derivative: ref('b3-mp4'), candidates: [{ id: 'b3', source: ref('b3') }, { id: 'b4', source: ref('b4') }] } }] } } })
assert.deepEqual(revisionOverlay.parts.map((part) => part.id), ['part-a', 'part-b'])
assert.equal(revisionOverlay.parts[0].selected_candidate_id, 'a1')
assert.equal(revisionOverlay.parts[1].selected_candidate_id, 'b3')
assert.equal(ledger.summary().length, 3)
process.stdout.write('video studio identity ledger contracts: PASS\n')
