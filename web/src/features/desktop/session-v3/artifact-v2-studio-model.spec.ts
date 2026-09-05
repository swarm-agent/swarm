import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopV3ArtifactV2CatalogItem, DesktopV3ArtifactV2Studio } from './artifact-v2-api'
import { desktopV3ArtifactV2ChangedParts, desktopV3ArtifactV2SidebarGroups, desktopV3ArtifactV2StudioModel } from './artifact-v2-studio-model'

const digest = (value: string) => value.repeat(64).slice(0, 64)

function catalog(state: DesktopV3ArtifactV2CatalogItem['working']['state']): DesktopV3ArtifactV2CatalogItem {
  return { schemaVersion: 1, working: { id: `art-${state}`, sessionId: 'session', kind: 'managed_creative', state, policyRevision: 'policy', capabilityClass: 'managed', intentReference: 'Launch visual', revision: 2, eventSeq: 4, latestBuildId: '', latestValidationId: '', activeIterationId: state === 'iterating' ? 'round-1' : '', createdAt: 1, updatedAt: 4 }, projection: { artifactId: `art-${state}`, sessionId: 'session', kind: 'managed_creative', state, revision: 2, eventSeq: 4, partCount: 2, latestBuildId: '', latestValidationId: '', activeIterationId: state === 'iterating' ? 'round-1' : '', updatedAt: 4 } }
}

const base = { id: 'base', artifactId: 'art', parentCompositionId: '', policyRevision: 'p', constructionVersion: 'c', digestSha256: digest('a'), revision: 1, eventSeq: 3, createdAt: 3, parts: [{ partId: 'hero', partRevisionId: 'hero-1', digestSha256: digest('b'), locked: false }, { partId: 'footer', partRevisionId: 'footer-1', digestSha256: digest('c'), locked: true }] }
const candidate = { ...base, id: 'candidate', parentCompositionId: 'base', eventSeq: 5, parts: [{ ...base.parts[0]!, partRevisionId: 'hero-2', digestSha256: digest('d') }, base.parts[1]!] }

test('Artifact V2 sidebar exposes durable lifecycle groups without V1 collection semantics', () => {
  const groups = desktopV3ArtifactV2SidebarGroups([catalog('allocated'), catalog('invalid'), catalog('validating'), catalog('ready'), catalog('iterating'), catalog('published_view')])
  assert.deepEqual(groups.map((group) => group.section).sort(), ['invalid', 'iterations', 'published', 'ready', 'working', 'working'])
  assert.ok(groups.every((group) => group.key === group.item.working.id))
  assert.ok(groups.every((group) => !group.key.includes('collection:') && !group.key.includes('variant')))
})

test('Artifact V2 targeted candidate comparison proves unrelated locked parts remain exact', () => {
  assert.deepEqual(desktopV3ArtifactV2ChangedParts(base, candidate), ['hero'])
  const studio = { schemaVersion: 1, working: { ...catalog('iterating').working, id: 'art', compositionHead: { compositionId: 'base', headRevision: 1, digestSha256: base.digestSha256, eventSeq: 3 } }, projection: { ...catalog('iterating').projection, artifactId: 'art' }, parts: [{ id: 'hero', artifactId: 'art', key: 'hero', label: 'Hero', role: '', mediaClass: 'text/html', locatorKind: '', locatorValue: '', order: 0, revision: 1, eventSeq: 1, createdAt: 1, updatedAt: 1 }, { id: 'footer', artifactId: 'art', key: 'footer', label: 'Footer', role: '', mediaClass: 'text/html', locatorKind: '', locatorValue: '', order: 1, revision: 1, eventSeq: 2, createdAt: 2, updatedAt: 2 }], partRevisions: [], compositions: [base, candidate], builds: [], validations: [], iterations: [{ id: 'round-1', artifactId: 'art', baseCompositionId: 'base', baseCompositionDigest: base.digestSha256, targetPartIds: ['hero'], requestedCandidates: 1, status: 'awaiting_selection', candidates: [{ slotId: 'candidate-1', compositionId: 'candidate', status: 'ready', failureCode: '', eventSeq: 5 }], selectedSlotId: '', revision: 2, eventSeq: 5, createdAt: 4, updatedAt: 5 }], publishedHeads: [] } satisfies DesktopV3ArtifactV2Studio
  const model = desktopV3ArtifactV2StudioModel(studio)
  assert.equal(model.candidates.length, 1)
  assert.deepEqual(model.candidates[0]?.changedPartIds, ['hero'])
  assert.equal(model.candidates[0]?.preservesUnrelatedParts, true)
  assert.equal(model.head?.parts.find((part) => part.partId === 'footer')?.locked, true)
})
