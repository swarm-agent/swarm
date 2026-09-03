import assert from 'node:assert/strict'
import test from 'node:test'

import {
  desktopV3NativeArtifactCandidateSelectionEndpoint,
  desktopV3NativeArtifactIterationPrompt,
  desktopV3NativeArtifactPreviewEndpoint,
  normalizeDesktopV3NativeArtifactRevision,
  normalizeDesktopV3NativeArtifactSummary,
  selectDesktopV3NativeArtifactCandidate,
  type DesktopV3NativeArtifactStudio,
} from './artifact-v3-api'

const summaryWire = {
  artifact_id: 'artifact-1', artifact_ref: 'artifact-ref-1', owner_session_id: 'session-1', label: 'Site', status: 'ready', part_count: 96, turn_count: 2,
  head: { revision_ref: 'revision-ref-2', commit_oid: 'commit-2', tree_oid: 'tree-2', generation: 4, selected_event_seq: 12 },
}

test('native Artifact V3 summary and revision preserve exact Git and cross-part diff evidence', () => {
  const summary = normalizeDesktopV3NativeArtifactSummary(summaryWire)
  assert.ok(summary)
  assert.equal(summary.partCount, 96)
  assert.equal(summary.head?.commitOid, 'commit-2')
  assert.equal(normalizeDesktopV3NativeArtifactSummary({ ...summaryWire, artifact_ref: undefined })?.artifactRef, 'artifact-1')
  const nativeSummary = normalizeDesktopV3NativeArtifactSummary({
    id: 'artifact-2', owner_session_id: 'session-1', revision: 41, parts: [{ id: 'hero' }], turns: [{ turn_id: 'turn-1' }],
    current_revision: { revision_ref: 'revision-2', commit_oid: 'commit-2', tree_oid: 'tree-2', build: { status: 'succeeded' }, validation: { status: 'valid' } },
  })
  assert.equal(nativeSummary?.status, 'ready')
  assert.equal(nativeSummary?.head?.revisionRef, 'revision-2')
  assert.equal(nativeSummary?.partCount, 1)
  assert.equal(nativeSummary?.turnCount, 1)
  assert.equal(nativeSummary?.head?.selectedEventSeq, 41)

  const revision = normalizeDesktopV3NativeArtifactRevision({
    revision_ref: 'revision-ref-2', commit_oid: 'commit-2', tree_oid: 'tree-2', manifest_blob_oid: 'manifest-2', parent_commit_oids: ['commit-1'], status: 'ready',
    changed_files: [{ path: 'styles/theme.css', status: 'modified', additions: 8, deletions: 2, affected_part_ids: ['hero', 'pricing'], shared: true }],
    affected_part_ids: ['hero', 'pricing'], diagnostics: [{ code: 'locator-resolved', message: 'Pricing locator resolved', phase: 'preview', part_ids: ['pricing'] }],
  })
  assert.ok(revision)
  assert.deepEqual(revision.parentCommitOids, ['commit-1'])
  assert.equal(revision.changedFiles[0]?.shared, true)
  assert.deepEqual(revision.changedFiles[0]?.affectedPartIds, ['hero', 'pricing'])
  assert.equal(revision.diagnostics[0]?.code, 'locator-resolved')
})

test('native Artifact V3 detail shape keeps nested parts, turns, candidates, and repository evidence', () => {
  const revision = normalizeDesktopV3NativeArtifactRevision({
    revision_ref: 'revision-root', commit_oid: 'commit-root', tree_oid: 'tree-root', parents: ['commit-parent'],
    changed_files: ['index.html', 'styles/theme.css'], changed_parts: ['hero', 'pricing'],
    build: { id: 'build-root', status: 'succeeded' }, validation: { id: 'validation-root', status: 'valid' },
  })
  assert.ok(revision)
  assert.equal(revision.status, 'ready')
  assert.deepEqual(revision.parentCommitOids, ['commit-parent'])
  assert.deepEqual(revision.changedFiles.map((file) => file.path), ['index.html', 'styles/theme.css'])
  assert.deepEqual(revision.affectedPartIds, ['hero', 'pricing'])
  assert.equal(revision.buildId, 'build-root')
  assert.equal(revision.validationId, 'validation-root')
})

test('native Artifact V3 preview and candidate selection use separate exact routes and head CAS', async () => {
  assert.equal(desktopV3NativeArtifactPreviewEndpoint('session 1', 'artifact/1', 'revision ref'), '/v3/sessions/session%201/artifacts-v3/artifact%2F1/preview?revision=revision+ref')
  assert.equal(desktopV3NativeArtifactCandidateSelectionEndpoint('session 1', 'artifact/1', 'turn/2'), '/v3/sessions/session%201/artifacts-v3/artifact%2F1/turns/turn%2F2/select')

  const originalFetch = globalThis.fetch
  let requestURL = ''
  let requestBody: Record<string, unknown> = {}
  globalThis.fetch = (async (input, init) => {
    requestURL = String(input)
    requestBody = JSON.parse(String(init?.body)) as Record<string, unknown>
    return new Response(JSON.stringify({ ok: true, head: { revision_ref: 'revision-ref-3', commit_oid: 'commit-3', tree_oid: 'tree-3', generation: 5, selected_event_seq: 13 } }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    const head = await selectDesktopV3NativeArtifactCandidate({ sessionId: 'session-1', artifactId: 'artifact-1', turnId: 'turn-2', candidateId: 'candidate-3', expectedHead: { revisionRef: 'revision-ref-2', commitOid: 'commit-2', treeOid: 'tree-2', generation: 4, selectedEventSeq: 12 }, expectedTurnRevision: 7 })
    assert.equal(requestURL, '/v3/sessions/session-1/artifacts-v3/artifact-1/turns/turn-2/select')
    assert.equal(requestBody.candidate_id, 'candidate-3')
    assert.equal(requestBody.expected_head_ref, 'revision-ref-2')
    assert.equal(requestBody.expected_turn_revision, 7)
    assert.equal(head.commitOid, 'commit-3')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('native Artifact V3 iteration prompt treats parts as intent and permits required shared repairs', () => {
  const artifact = normalizeDesktopV3NativeArtifactSummary(summaryWire)!
  const studio: DesktopV3NativeArtifactStudio = {
    artifact,
    parts: [{ id: 'pricing', label: 'Pricing', description: '', locator: { kind: 'selector', path: 'index.html', value: '#pricing', paths: [] } }],
    turns: [], revisions: [],
  }
  const prompt = desktopV3NativeArtifactIterationPrompt(studio, ['pricing'])
  assert.match(prompt, /Target part IDs \(intent only\): pricing/)
  assert.match(prompt, /exact revision reference: revision-ref-2/)
  assert.match(prompt, /shared-file or cross-part changes/)
  assert.doesNotMatch(prompt, /preserve.*byte-for-byte/i)
})
