import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeDesktopV3NativeArtifactRevision, normalizeDesktopV3NativeArtifactSummary } from './artifact-v3-api'

// Requirement: inherited render provenance uses the canonical nested owner DTO,
// without replacing destination identity or inventing evidence for ordinary heads.
// Threat: a flattened normalizer silently drops provenance in gallery consumers.
// Pure API normalization is the narrowest layer; no browser rendering is claimed.
test('retained native provenance preserves source owner and independent destination', () => {
  const inherited = { owner: { account_scope_id: 'account', user_id: 'user', session_id: 'source' }, artifact_id: 'original', commit_oid: 'a'.repeat(40), tree_oid: 'b'.repeat(40) }
  const lineage = { source_session_id: 'source', source_artifact_id: 'original', source_commit_oid: 'a'.repeat(40) }
  const head = { revision_ref: 'revision-' + 'c'.repeat(40), commit_oid: 'c'.repeat(40), tree_oid: 'b'.repeat(40), lineage, build: { status: 'succeeded', inherited_from: inherited }, validation: { status: 'valid', inherited_from: inherited } }
  const summary = normalizeDesktopV3NativeArtifactSummary({ id: 'copy', owner_session_id: 'destination', lineage, head })
  const revision = normalizeDesktopV3NativeArtifactRevision(head)
  assert.equal(summary?.ownerSessionId, 'destination')
  assert.equal(summary?.artifactId, 'copy')
  assert.equal(summary?.head?.commitOid, 'c'.repeat(40))
  assert.equal(summary?.lineage?.sourceArtifactId, 'original')
  assert.equal(summary?.inheritedFrom?.sessionId, 'source')
  assert.deepEqual(summary?.inheritedFrom, revision?.inheritedFrom)
  assert.equal(revision?.inheritedFrom?.commitOid, 'a'.repeat(40))
  assert.equal(normalizeDesktopV3NativeArtifactRevision({ ...head, build: {}, validation: {} })?.inheritedFrom, null)
  assert.equal(normalizeDesktopV3NativeArtifactRevision({ ...head, build: {}, validation: { inherited_from: { ...inherited, owner: {} } } })?.inheritedFrom, null)
})
