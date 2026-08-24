import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import type { DesktopV3ArtifactCatalogEntry } from '../../session-v3/artifact-api'
import { desktopV3ArtifactSidebarGroups, desktopV3ArtifactSidebarPartChatSelection, desktopV3ArtifactsForSession, desktopV3HasPendingVisualSwarm, desktopV3MobileVisualSwarmArtifactToOpen, desktopV3NextSessionSidebarView } from './desktop-v3-artifact-sidebar'

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
  assert.match(source, /desktopV3ArtifactMessageSelection\(artifact, 'select'\)/)
})

test('sidebar animates every visible governed preview while isolating arbitrary HTML', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /sidebarArtifactNeedsExclusiveLivePreview/)
  assert.match(source, /artifact\.mediaType === 'text\/html' && !artifact\.animationProfile/)
  assert.match(source, /const livePreviewKey = \(requestedArtifact\?\.status === 'ready'/)
  assert.match(source, /\|\| selectedLivePreviewKey/)
  assert.match(source, /requestLivePreview/)
  assert.match(source, /sidebarArtifactPreviewKey\(artifact\) === livePreviewKey/)
  assert.match(source, /!exclusive \|\| live/)
  assert.match(source, /!sidebarArtifactNeedsMotionPermission\(artifact\) \|\| previewMotionAllowed/)
  assert.match(source, /fetchDesktopV3ArtifactPreviewAccess\(artifact\.sessionId, artifact\.artifactId/)
  assert.match(source, /desktopV3ArtifactDirectContentURL\(artifact\)/)
  assert.match(source, /formatDesktopV3ArtifactAnimationProfile\(representative\.animationProfile\)/)
  assert.match(source, /<video src=\{previewURL\} muted playsInline preload="metadata"/)
  assert.match(source, /useDesktopV3ArtifactPreviewVisibility<HTMLSpanElement>/)
  assert.match(source, /const DesktopV3ArtifactThumbnail = memo\(/)
  assert.match(source, /sidebarArtifactThumbnailEqual/)
  assert.match(source, /sidebarArtifactAnimationProfileKey\(left\) === sidebarArtifactAnimationProfileKey\(right\)/)
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

test('source-free overall iteration roots stay grouped before artifact turns begin', () => {
  const projectedRoot = (artifactId: string, chainId: string, candidateIndex: number) => ({
    ...artifact('session-a', artifactId),
    collectionId: 'collection-overall',
    collectionName: 'Overall concepts',
    graphState: 'git_projection' as const,
    artifactChainId: chainId,
    artifactStepId: 'step-overall',
    candidateIndex,
    chain: { name: `Candidate ${candidateIndex}`, revisionCount: 1 },
    step: { id: 'step-overall', revisionNumber: 1 },
  }) as DesktopV3ArtifactCatalogEntry

  const groups = desktopV3ArtifactSidebarGroups([
    projectedRoot('variant-3', 'chain-3', 3),
    projectedRoot('variant-1', 'chain-1', 1),
    projectedRoot('variant-2', 'chain-2', 2),
  ])

  assert.equal(groups.length, 1)
  assert.equal(groups[0]?.label, 'Overall concepts')
  assert.deepEqual(groups[0]?.entries.map((entry) => entry.artifactId), ['variant-1', 'variant-2', 'variant-3'])
})

test('a candidate becomes a turn-based artifact only after its chain gains another revision', () => {
  const root = {
    ...artifact('session-a', 'variant-root'),
    collectionId: 'collection-overall',
    collectionName: 'Overall concepts',
    graphState: 'git_projection' as const,
    artifactChainId: 'chain-selected',
    artifactStepId: 'step-root',
    candidateIndex: 1,
    chain: { name: 'Selected concept', revisionCount: 2 },
    step: { id: 'step-root', revisionNumber: 1 },
  } as DesktopV3ArtifactCatalogEntry
  const nextTurn = {
    ...root,
    artifactId: 'variant-turn-2',
    collectionId: 'collection-turn-2',
    artifactStepId: 'step-turn-2',
    step: { id: 'step-turn-2', revisionNumber: 2 },
  } as DesktopV3ArtifactCatalogEntry
  const unselectedRoot = {
    ...root,
    artifactId: 'variant-other',
    artifactChainId: 'chain-other',
    artifactStepId: 'step-overall',
    candidateIndex: 2,
    chain: { name: 'Other concept', revisionCount: 1 },
    step: { id: 'step-overall', revisionNumber: 1 },
  } as DesktopV3ArtifactCatalogEntry

  const groups = desktopV3ArtifactSidebarGroups([unselectedRoot, nextTurn, root])

  assert.equal(groups.length, 2)
  assert.deepEqual(groups.find((group) => group.key === 'chain:chain-selected')?.entries.map((entry) => entry.artifactId), ['variant-root', 'variant-turn-2'])
  assert.deepEqual(groups.find((group) => group.key === 'collection:session-a:collection-overall')?.entries.map((entry) => entry.artifactId), ['variant-other'])
})

test('sidebar part attachment links the exact authoritative part back to chat', () => {
  const entry = {
    ...artifact('session-a', 'variant-1'),
    collectionId: 'collection-1',
    eventSeq: 42,
    partGraphState: 'git_projection' as const,
    artifactChainId: 'chain-1',
    partDefinitions: [{ id: 'signal', label: 'Signal', description: 'Signal animation.', locator: { id: 'signal', label: 'Signal', description: 'Signal animation.', kind: 'temporal' as const, startMs: 0, endMs: 4000, x: 0, y: 0, width: 0, height: 0, page: 0, stateId: '', selector: '' } }],
    composition: { id: 'composition-1', artifactChainId: 'chain-1', parent: null, iterationTurnId: '', iterationGroupId: '', construction: null, parts: [{ partId: 'signal', definitionOwnerSessionId: 'session-a', revision: { artifactChainId: 'chain-1', partId: 'signal', partRevisionId: 'signal-r1', ownerSessionId: 'session-a', digestSha256: 'a'.repeat(64), size: 10, mediaType: 'text/html' } }] },
  }

  const selection = desktopV3ArtifactSidebarPartChatSelection(entry, 'signal')
  assert.equal(selection.part_id, 'signal')
  assert.equal(selection.action, 'use')
  assert.match(selection.label, /Signal/)
  assert.match(selection.description ?? '', /temporal/)
})

test('sidebar distinguishes accepted byte heads from pending locked candidates', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /desktopV3ArtifactStudioSamePartRevision\(currentSlot, acceptedSlot\)/)
  assert.match(source, /acceptedCurrent \? 'Accepted head' : currentSlot\?\.locked \? 'Locked pending' : 'History'/)
})

test('sidebar turn copy keeps multi-part rounds at the complete-iteration level', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /Latest turn/)
  assert.match(source, /Turn history/)
  assert.match(source, /Composition head/)
  assert.match(source, /Decision recorded/)
  assert.match(source, /turn\.parts\.length === 1/)
  assert.match(source, /parts changed together/)
  assert.match(source, />Open iteration</)
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

test('pending visual swarm initially opens artifacts without overriding later user navigation', () => {
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: true, prioritizePlan: true, hasPendingVisualSwarm: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: false }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true, prioritizePlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 2, artifactCount: 0, hasPlan: true, hasPendingVisualSwarm: true }), 'plan')

})
