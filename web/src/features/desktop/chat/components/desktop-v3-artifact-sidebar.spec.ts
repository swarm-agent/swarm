import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import type { DesktopV3ArtifactCatalogEntry } from '../../session-v3/artifact-api'
import { desktopV3ArtifactSidebarGroups, desktopV3ArtifactsForSession, desktopV3HasPendingVisualSwarm, desktopV3MobileVisualSwarmArtifactToOpen, desktopV3NextSessionSidebarView } from './desktop-v3-artifact-sidebar'

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
    outputRequirements: {
      presetId: 'twitter_header',
      width: 1500,
      height: 500,
      aspectRatio: '3:1',
      orientation: 'landscape',
      resolutionSource: 'preset',
      registryVersion: '2026-08-01',
    },
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

test('sidebar labels ready images as exact chat remix inputs', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /Attach \$\{artifact\.label\} for chat changes/)
  assert.match(source, /Attach to chat for remixing/)
  assert.match(source, /onAddToChat\(\[artifact\]\)/)
})

test('visible sidebar artifact thumbnails keep live animation enabled', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /<DesktopV3ArtifactThumbnail artifact=\{representative\} live \/>/)
  assert.match(source, /<DesktopV3ArtifactThumbnail artifact=\{artifact\} live \/>/)
  assert.doesNotMatch(source, /requestLivePreview/)
  assert.match(source, /<video src=\{previewURL\} autoPlay loop muted playsInline/)
  assert.match(source, /useDesktopV3ArtifactPreviewVisibility<HTMLSpanElement>\(previewEnabled\)/)
})

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
  assert.deepEqual(groups[0]?.entries.map((entry) => entry.outputRequirements?.presetId), ['twitter_header', 'twitter_header', 'twitter_header'])
})

test('delegated collection variants group under their parent session', () => {
  const first = { ...artifact('child-a', 'variant-1', 'session-a'), collectionId: 'collection-1' }
  const second = { ...artifact('child-b', 'variant-2', 'session-a'), collectionId: 'collection-1' }

  const groups = desktopV3ArtifactSidebarGroups([first, second])
  assert.equal(groups.length, 1)
  assert.deepEqual(groups[0]?.entries.map((entry) => entry.artifactId), ['variant-1', 'variant-2'])
})

test('ordinary artifacts remain separate sidebar entries', () => {
  assert.deepEqual(desktopV3ArtifactSidebarGroups([artifact('session-a', 'one'), artifact('session-a', 'two')]).map((group) => group.entries.length), [1, 1])
})

test('pending visual swarms are identified from durable staging lineage', () => {
  const pending = { ...artifact('session-a', 'variant-1'), status: 'staging' as const, lineage: { ...artifact('session-a', 'variant-1').lineage, iterationGroupId: 'group-1' } }
  const ready = { ...pending, status: 'ready' as const }
  const ordinaryStaging = { ...pending, lineage: { ...pending.lineage, iterationGroupId: '' } }

  assert.equal(desktopV3HasPendingVisualSwarm([pending]), true)
  assert.equal(desktopV3HasPendingVisualSwarm([ready]), false)
  assert.equal(desktopV3HasPendingVisualSwarm([ordinaryStaging]), false)
})

test('mobile visual swarm staging opens the newest unseen artifact group only off the desktop sidebar viewport', () => {
  const first = { ...artifact('session-a', 'variant-1'), status: 'staging' as const, lineage: { ...artifact('session-a', 'variant-1').lineage, iterationGroupId: 'group-1' } }
  const latest = { ...artifact('child-a', 'variant-2', 'session-a'), status: 'staging' as const, lineage: { ...artifact('child-a', 'variant-2', 'session-a').lineage, iterationGroupId: 'group-2' } }

  assert.equal(desktopV3MobileVisualSwarmArtifactToOpen({ artifacts: [first, latest], sessionId: 'session-a', sidebarViewport: false, openedGroupKeys: new Set() })?.artifactId, 'variant-2')
  assert.equal(desktopV3MobileVisualSwarmArtifactToOpen({ artifacts: [first, latest], sessionId: 'session-a', sidebarViewport: false, openedGroupKeys: new Set(['session-a:group-2']) })?.artifactId, 'variant-1')
  assert.equal(desktopV3MobileVisualSwarmArtifactToOpen({ artifacts: [first, latest], sessionId: 'session-a', sidebarViewport: true, openedGroupKeys: new Set() }), undefined)
  assert.equal(desktopV3MobileVisualSwarmArtifactToOpen({ artifacts: [first, latest], sessionId: 'session-b', sidebarViewport: false, openedGroupKeys: new Set() }), undefined)
})

test('pending visual swarm overrides plan sidebar prioritization', () => {
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: true, prioritizePlan: true, hasPendingVisualSwarm: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: false }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true, prioritizePlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 2, artifactCount: 0, hasPlan: true, hasPendingVisualSwarm: true }), 'plan')
})
