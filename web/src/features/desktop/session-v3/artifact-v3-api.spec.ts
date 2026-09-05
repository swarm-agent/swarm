import assert from 'node:assert/strict'
import test from 'node:test'

import {
  fetchDesktopV3NativeArtifactStudio,
  desktopV3NativeArtifactCandidateSelectionEndpoint,
  desktopV3NativeArtifactIterationPrompt,
  desktopV3NativeArtifactPreviewEndpoint,
  normalizeDesktopV3NativeArtifactRevision,
  preflightDesktopV3NativeArtifactPreview,
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
    if (requestURL.endsWith('/preview/access')) {
      return new Response(JSON.stringify({ ok: true, preview_url: '/v3/sessions/session-1/artifacts-v3/artifact-1/preview/access/token?revision=revision-ref-2' }), { headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ ok: true, head: { revision_ref: 'revision-ref-3', commit_oid: 'commit-3', tree_oid: 'tree-3', generation: 5, selected_event_seq: 13 } }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    const previewURL = await preflightDesktopV3NativeArtifactPreview('session-1', 'artifact-1', 'revision-ref-2')
    assert.equal(requestURL, '/v3/sessions/session-1/artifacts-v3/artifact-1/preview/access')
    assert.equal(requestBody.revision_ref, 'revision-ref-2')
    assert.match(previewURL, /\/preview\/access\/token/)

    const head = await selectDesktopV3NativeArtifactCandidate({ sessionId: 'session-1', artifactId: 'artifact-1', turnId: 'turn-2', candidateId: 'candidate-3', expectedHead: { revisionRef: 'revision-ref-2', commitOid: 'commit-2', treeOid: 'tree-2', generation: 4, selectedEventSeq: 12 }, expectedTurnRevision: 7 })
    assert.equal(requestURL, '/v3/sessions/session-1/artifacts-v3/artifact-1/turns/turn-2/select')
    assert.equal(requestBody.candidate_id, 'candidate-3')
    assert.equal(requestBody.expected_head_ref, 'revision-ref-2')
    assert.equal(requestBody.expected_turn_revision, 7)
    assert.match(String(requestBody.client_request_id), /^desktop-artifact-v3-select-[0-9a-f-]+$/)
    assert.doesNotMatch(String(requestBody.client_request_id), /:/)
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

// Requirement: new turns and candidate-specific Parts remain reviewable without
// selecting head. Threat: history pagination and head-only Parts hide candidate
// content. Exercise the production API adapter with bounded wire responses.
test('Studio retains candidate manifests outside the history page and orders turns oldest first', async () => {
  const originalFetch = globalThis.fetch
  const part = (id: string) => ({ id, label: id, locator: { kind: 'selector', path: 'index.html', value: `#${id}` } })
  const revision = (id: string, partId: string) => ({ revision_ref: `revision-${id}`, commit_oid: id, manifest: { parts: [part(partId)] }, build: { status: 'succeeded' }, validation: { status: 'valid' } })
  const head = revision('head', 'original')
  globalThis.fetch = (async (input, init) => {
    assert.equal(init?.method, 'GET', 'loading candidates must never select head')
    const payload = String(input).includes('/revisions?')
      ? { ok: true, revisions: [head], next_cursor: 'opaque-more' }
      : { ok: true, artifact: { ...summaryWire, head, parts: [part('original')], turns: [
        { turn_id: 'new', created_at: 20, target_part_ids: ['orbit', 'legend'], candidates: [{ candidate_id: 'new-option', status: 'ready', revision: revision('candidate', 'new-part') }] },
        { turn_id: 'old', created_at: 10, candidates: [{ candidate_id: 'failed', status: 'failed', diagnostics: [{ code: 'failed', message: 'No revision' }] }] },
      ] } }
    return new Response(JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    const studio = await fetchDesktopV3NativeArtifactStudio('session-1', 'artifact-1')
    assert.equal(studio.artifact.head?.commitOid, 'head')
    assert.deepEqual(studio.turns.map((turn) => turn.turnId), ['old', 'new'])
    assert.deepEqual(studio.turns[1]?.targetPartIds, ['orbit', 'legend'])
    assert.equal(studio.turns[0]?.candidates[0]?.revision, null)
    assert.deepEqual(studio.revisions.find((item) => item.commitOid === 'candidate')?.parts?.map((item) => item.id), ['new-part'])
    assert.deepEqual(studio.parts.map((item) => item.id), ['original'])
  } finally { globalThis.fetch = originalFetch }
})
