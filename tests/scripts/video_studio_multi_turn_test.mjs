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

const nested = normalizeVideoSnapshot({ project: { id: 'p', base_revision: { id: 'base' }, working_revision: { id: 'work', timeline: { clips: [{ id: 'clip', media_type: 'video/mp4', start_ms: 0, end_ms: 1000 }] } }, proposal: { id: 'proposal', parts: [{ id: 'part', media_type: 'video/mp4', start_ms: 0, end_ms: 1000, candidates: [{ id: 'candidate' }] }] } } })
assert.equal(nested.project_id, 'p')
assert.equal(nested.clips[0].media_type, 'video/mp4')
assert.equal(ledger.summary().length, 3)
process.stdout.write('video studio identity ledger contracts: PASS\n')
