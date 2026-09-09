import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeDesktopV3NativeArtifactSummary, normalizeDesktopV3NativeArtifactRevision } from './artifact-v3-api'
import { readNativeArtifactNavigation } from './artifact-v3-navigation'

// Requirement: API normalization preserves canonical membership and scene identity.
// Threat: title-based grouping, duplicate slots, malformed times, or URL state
// becoming an authoritative selection. This pure adapter is the narrowest layer.
test('native membership is explicit and malformed groups fail closed', () => {
  const member = { wave_id: 'wave', index: 1, count: 2, artifact_id: 'one', turn_id: 'turn', candidate_id: 'candidate', status: 'failed' }
  const normalize = (extra: object) => normalizeDesktopV3NativeArtifactSummary({ id: 'one', owner_session_id: 'parent', ...extra })!
  assert.deepEqual(normalize({ label: 'Same title' }).generationGroups, [])
  assert.deepEqual(normalize({ generations: [member] }).generations, [{ waveId: 'wave', index: 1, count: 2 }])
  assert.equal(normalize({ generation_groups: [{ wave_id: 'wave', count: 2, members: [member] }] }).generationGroups?.[0]?.members[0]?.status, 'failed')
  for (const members of [[member, member], [{ ...member, wave_id: 'foreign' }], [{ ...member, commit_oid: 'invalid' }]]) {
    assert.deepEqual(normalize({ generation_groups: [{ wave_id: 'wave', count: 2, members }] }).generationGroups, [])
  }
})
test('temporal Parts retain ordered bounds and reject invalid scene descriptors', () => {
  const part = { id: 'opening', label: 'Opening', locator: { kind: 'selector', path: 'index.html', value: '#canvas' }, temporal: { scene_id: 'opening', start_ms: 0, end_ms: 1000 } }
  const revision = (parts: unknown[]) => normalizeDesktopV3NativeArtifactRevision({ revision_ref: `revision-${'a'.repeat(40)}`, commit_oid: 'a'.repeat(40), manifest: { parts } })!
  assert.deepEqual(revision([part]).parts?.[0]?.temporal, { sceneId: 'opening', startMs: 0, endMs: 1000 })
  for (const temporal of [{ ...part.temporal, scene_id: 'foreign' }, { ...part.temporal, start_ms: -1 }, { ...part.temporal, end_ms: 0 }]) assert.deepEqual(revision([{ ...part, temporal }]).parts, [])
  assert.deepEqual(readNativeArtifactNavigation('?native_artifact=one&native_wave=wave&native_revision=revision-a'), { artifactId: 'one', waveId: 'wave', revisionRef: 'revision-a' })
})
