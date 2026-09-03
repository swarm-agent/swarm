import assert from 'node:assert/strict'
import test from 'node:test'

import { desktopV3ArtifactV2IterationPrompt, normalizeDesktopV3ArtifactV2CatalogItem, normalizeDesktopV3ArtifactV2Studio } from './artifact-v2-api'

const working = { id: 'artv2-1', session_id: 'session-1', kind: 'managed_creative', state: 'invalid', policy_revision: 'policy-1', capability_class: 'managed', intent_reference: 'Hero concept', revision: 8, event_seq: 20, composition_head: { composition_id: 'composition-1', head_revision: 2, digest_sha256: 'a'.repeat(64), event_seq: 17 }, published_head: { published_head_id: 'published-1', composition_id: 'composition-1', digest_sha256: 'a'.repeat(64), event_seq: 19 }, latest_build_id: 'build-1', latest_validation_id: 'validation-1', active_iteration_id: '', latest_diagnostic: { code: 'viewport_overflow', phase: 'validation', severity: 'error', part_id: 'hero', retry_class: 'repairable', safe_message: 'Hero overflows the viewport.' }, created_at: 1, updated_at: 20 }
const projection = { artifact_id: 'artv2-1', session_id: 'session-1', kind: 'managed_creative', state: 'invalid', revision: 8, event_seq: 20, part_count: 2, composition_head: working.composition_head, latest_build_id: 'build-1', latest_validation_id: 'validation-1', active_iteration_id: '', latest_diagnostic: working.latest_diagnostic, updated_at: 20 }

test('Artifact V2 catalog preserves working invalid state and bounded diagnostic without V1 fields', () => {
  const item = normalizeDesktopV3ArtifactV2CatalogItem({ schema_version: 1, working, projection })
  assert.equal(item?.working.state, 'invalid')
  assert.equal(item?.projection.latestDiagnostic?.partId, 'hero')
  assert.equal(item?.working.compositionHead?.compositionId, 'composition-1')
  assert.equal((item as unknown as Record<string, unknown>)?.collectionId, undefined)
})

test('Artifact V2 Studio normalization rejects mismatched catalog authority and builds exact iteration prompt', () => {
  assert.equal(normalizeDesktopV3ArtifactV2CatalogItem({ schema_version: 1, working, projection: { ...projection, artifact_id: 'foreign' } }), null)
  const studio = normalizeDesktopV3ArtifactV2Studio({ schema_version: 1, working: { ...working, state: 'ready', latest_diagnostic: undefined }, projection: { ...projection, state: 'ready', latest_diagnostic: undefined }, parts: [{ id: 'hero', artifact_id: 'artv2-1', key: 'hero', label: 'Hero', media_class: 'text/html', order: 0 }, { id: 'footer', artifact_id: 'artv2-1', key: 'footer', label: 'Footer', media_class: 'text/html', order: 1 }], part_revisions: [], compositions: [], builds: [], validations: [], iterations: [], published_heads: [] })
  assert.ok(studio)
  const prompt = desktopV3ArtifactV2IterationPrompt(studio!, ['hero'], 3)
  assert.match(prompt, /artifact_v2_source=\{"artifact_id":"artv2-1","published_head_id":"published-1","composition_id":"composition-1"/)
  assert.match(prompt, /artifact_v2_target_part_ids=hero/)
  assert.match(prompt, /Never call or fall back to manage_artifact/)
  assert.throws(() => desktopV3ArtifactV2IterationPrompt(studio!, ['missing'], 3), /exact part/)
})
