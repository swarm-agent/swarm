import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopV3ArtifactCatalogEntry } from '../../session-v3/artifact-api'
import { desktopV3ArtifactSidebarGroups, desktopV3ArtifactsForSession, desktopV3NextSessionSidebarView } from './desktop-v3-artifact-sidebar'

function artifact(sessionId: string, artifactId: string, parentSessionId = ''): DesktopV3ArtifactCatalogEntry {
  return {
    artifactId,
    sessionId,
    sessionTitle: '',
    workspacePath: '',
    workspaceName: '',
    planId: '',
    planTitle: '',
    checkpointId: '',
    checkpointTitle: '',
    label: artifactId,
    description: '',
    collectionName: '',
    collectionDescription: '',
    filename: `${artifactId}.html`,
    mediaType: 'text/html',
    kind: 'html',
    status: 'ready',
    previewable: true,
    category: 'visual',
    updatedAt: 0,
    lineage: {
      parentSessionId,
      sourceSessionId: sessionId,
      sourceCollectionId: '',
      sourceVariantId: artifactId,
      taskCallId: '',
      programId: '',
      programJobId: '',
      childSessionId: '',
      iterationGroupId: '',
      iterationGroup: '',
      iterationId: '',
      iterationIndex: 0,
      iterationLabel: '',
      iterationTheme: '',
      runId: '',
      planId: '',
      checkpointId: '',
      attemptId: '',
    },
  }
}

test('session artifact sidebar includes native and delegated artifacts only for the active session', () => {
  const catalog = [
    artifact('session-a', 'native'),
    artifact('child-a', 'delegated', 'session-a'),
    artifact('session-b', 'other'),
    artifact('child-b', 'other-delegated', 'session-b'),
  ]

  assert.deepEqual(
    desktopV3ArtifactsForSession(catalog, 'session-a').map((entry) => entry.artifactId),
    ['native', 'delegated'],
  )
})

test('sidebar groups staged Iteration Swarm variants and preserves canonical progress', () => {
  const first = { ...artifact('session-a', 'variant-1'), collectionId: 'collection-1', status: 'staging' as const, progress: { total: 3, staging: 2, ready: 1, failed: 0, unavailable: 0 }, lineage: { ...artifact('session-a', 'variant-1').lineage, iterationGroupId: 'group-1', iterationGroup: 'Homepage remixes', iterationIndex: 1 } }
  const second = { ...artifact('session-a', 'variant-2'), collectionId: 'collection-1', status: 'ready' as const, progress: first.progress, lineage: { ...first.lineage, iterationIndex: 2 } }
  const third = { ...artifact('session-a', 'variant-3'), collectionId: 'collection-1', status: 'staging' as const, progress: first.progress, lineage: { ...first.lineage, iterationIndex: 3 } }

  const groups = desktopV3ArtifactSidebarGroups([third, first, second])
  assert.equal(groups.length, 1)
  assert.equal(groups[0]?.label, 'Homepage remixes')
  assert.deepEqual(groups[0]?.entries.map((entry) => entry.artifactId), ['variant-1', 'variant-2', 'variant-3'])
  assert.deepEqual(groups[0]?.progress, { total: 3, staging: 2, ready: 1, failed: 0, unavailable: 0 })
})

test('ordinary artifacts remain separate sidebar entries', () => {
  assert.deepEqual(desktopV3ArtifactSidebarGroups([artifact('session-a', 'one'), artifact('session-a', 'two')]).map((group) => group.entries.length), [1, 1])
})

test('first artifact opens artifact sidebar only when no plan exists', () => {
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: false }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true, prioritizePlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 2, artifactCount: 0, hasPlan: true }), 'plan')
})
