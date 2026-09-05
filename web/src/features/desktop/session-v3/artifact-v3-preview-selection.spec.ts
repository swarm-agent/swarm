import assert from 'node:assert/strict'
import test from 'node:test'
import { nativeArtifactPreviewSelectionEvent, toggleNativeArtifactPart } from './artifact-v3-preview-selection'
import type { DesktopV3NativeArtifactPart } from './artifact-v3-api'

// Requirement: only the current opaque-origin iframe can send declared selector
// intent. Threat: same-ID foreign windows, navigation, malformed payloads or file
// Parts gaining click authority. The production parser is the narrowest boundary;
// the sibling browser test separately proves the component listener's wiring.
test('native preview message boundary fails closed without changing selection', () => {
  const source = {} as Window
  const revision = `revision-${'a'.repeat(40)}`
  const parts = [
    { id: 'narration', locator: { kind: 'selector' } },
    { id: 'file', locator: { kind: 'file' } },
  ] as DesktopV3NativeArtifactPart[]
  const data = { protocol: 'swarm.artifact/v3', type: 'toggle-part', revision_ref: revision, part_id: 'narration' }
  const valid = { source, origin: 'null', data } as unknown as MessageEvent
  assert.deepEqual(nativeArtifactPreviewSelectionEvent(valid, source, revision, parts), { type: 'toggle-part', partId: 'narration' })
  const bad = [
    { ...valid, source: {} }, { ...valid, source: null }, { ...valid, origin: 'https://artifact.test' },
    ...[null, [], 'toggle-part', { ...data, protocol: 'swarm.artifact/v2' }, { ...data, revision_ref: 'old' },
      { ...data, part_id: 'unknown' }, { ...data, part_id: 'file' }, { ...data, part_id: [] },
      { ...data, type: 'submit-edit' }].map((payload) => ({ ...valid, data: payload })),
  ]
  for (const event of bad) {
    let selection = ['existing']
    const message = nativeArtifactPreviewSelectionEvent(event as MessageEvent, source, revision, parts)
    if (message?.type === 'toggle-part') selection = toggleNativeArtifactPart(selection, message.partId)
    assert.equal(message, null)
    assert.deepEqual(selection, ['existing'])
  }
  assert.equal(nativeArtifactPreviewSelectionEvent(valid, null, revision, parts), null)
  assert.deepEqual(toggleNativeArtifactPart(['first'], 'second'), ['first', 'second'])
  assert.deepEqual(toggleNativeArtifactPart(['first', 'second'], 'first'), ['second'])
})
