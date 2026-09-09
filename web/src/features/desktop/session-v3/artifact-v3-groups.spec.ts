import assert from 'node:assert/strict'
import test from 'node:test'
import { defaultNativeGenerationGroup, groupNativeArtifactSidebar } from './artifact-v3-groups'
import type { DesktopV3NativeArtifactSummary } from './artifact-v3-api'

// Requirement: persisted generation membership, not labels or latest repair,
// groups sibling alternatives. Threat: flat duplicate rows, unrelated merges,
// and a one-option correction hiding the original family. The pure selector is
// the narrowest layer proving grouping; browser tests prove click behavior.
const artifact = (id: string, index: number, session = 'parent'): DesktopV3NativeArtifactSummary => ({
  artifactId: id, ownerSessionId: session, artifactRef: id, label: 'Same title', description: '',
  status: 'ready', head: null, partCount: 2, turnCount: 1, updatedAt: 1,
  generations: index ? [{ waveId: 'family', index, count: 5 }] : [],
})

test('five fresh siblings remain one family after individual corrections', () => {
  const items = [5, 3, 1, 4, 2].map((index) => artifact(`option-${index}`, index))
  items[1]!.generations!.push({ waveId: 'repair', index: 1, count: 1 })
  const before = JSON.stringify(items)
  const groups = groupNativeArtifactSidebar(items)
  assert.equal(groups.length, 1)
  assert.equal(groups[0]!.count, 5)
  assert.deepEqual(groups[0]!.artifacts.map((item) => item.artifactId), [1, 2, 3, 4, 5].map((i) => `option-${i}`))
  assert.equal(JSON.stringify(items), before)
})

test('titles and cross-session wave collisions never merge unrelated artifacts', () => {
  assert.equal(groupNativeArtifactSidebar([artifact('a', 1), artifact('b', 1, 'other'), artifact('c', 0), artifact('d', 0)]).length, 4)
})

test('partial and failed families retain declared count and identity', () => {
  const failed = { ...artifact('failed', 3), status: 'error' as const }
  const groups = groupNativeArtifactSidebar([failed])
  assert.equal(groups[0]!.count, 5)
  assert.equal(groups[0]!.artifacts[0]!.status, 'error')
  const original = { waveId: 'family', count: 5, members: [] }
  const repair = { waveId: 'repair', count: 1, members: [] }
  assert.equal(defaultNativeGenerationGroup([original, repair], ''), original)
  assert.equal(defaultNativeGenerationGroup([original, repair], 'repair'), repair)
  assert.equal(defaultNativeGenerationGroup([original, repair], 'stale'), original)
})
