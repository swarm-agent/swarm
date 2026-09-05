import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import type { DesktopV3ArtifactCatalogEntry } from '../../session-v3/artifact-api'
import { desktopV3ArtifactSidebarGroups, desktopV3ArtifactSidebarIterationGroup, desktopV3ArtifactSidebarPartChatSelection, desktopV3ArtifactsForSession, desktopV3HasPendingVisualSwarm, desktopV3MobileVisualSwarmArtifactToOpen, desktopV3NextSessionSidebarView } from './desktop-v3-artifact-sidebar'

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

test('sidebar collapses ten initial iterations into one clickable group entry', () => {
  const entries = Array.from({ length: 10 }, (_, index) => ({
    ...artifact(`child-${index + 1}`, `variant-${index + 1}`, 'session-a'),
    collectionId: 'collection-10',
    collectionName: 'Landing page directions',
    status: index < 8 ? 'ready' as const : 'staging' as const,
    progress: { total: 10, staging: 2, ready: 8, failed: 0, unavailable: 0 },
    lineage: {
      ...artifact(`child-${index + 1}`, `variant-${index + 1}`, 'session-a').lineage,
      iterationGroupId: 'group-10',
      iterationGroup: 'Landing page directions',
      iterationIndex: index + 1,
    },
  }))

  const groups = desktopV3ArtifactSidebarGroups(entries)
  const iterationGroup = desktopV3ArtifactSidebarIterationGroup(entries, groups[0]!)
  assert.equal(groups.length, 1)
  assert.equal(iterationGroup?.iterationCount, 10)
  assert.equal(iterationGroup?.collectionId, 'collection-10')
  assert.equal(iterationGroup?.partId, '')
  assert.equal(iterationGroup?.target.artifactId, 'variant-1')
})

test('sidebar groups a focused iteration turn under its exact artifact part', () => {
  const rootReference = { sessionId: 'session-a', collectionId: 'base', variantId: 'base', eventSeq: 1 }
  const candidateReferences = Array.from({ length: 10 }, (_, index) => ({ sessionId: 'session-a', collectionId: 'hero-round', variantId: `hero-${index + 1}`, eventSeq: index + 2 }))
  const chain = { id: 'chain-hero', graphState: 'git_projection' as const, name: 'Landing page', root: rootReference, head: candidateReferences[0]!, revisionCount: 2, lastRoundId: 'hero-round' }
  const base = { ...artifact('session-a', 'base'), collectionId: 'base', eventSeq: 1, graphState: 'git_projection' as const, artifactChainId: 'chain-hero', artifactStepId: 'root', chain, step: { id: 'root', graphState: 'git_projection' as const, artifactChainId: 'chain-hero', revisionNumber: 1, candidates: [rootReference] }, partGraphState: 'git_projection' as const, partDefinitions: [{ id: 'hero', label: 'Hero', description: '', locator: null }], composition: { id: 'base-composition', artifactChainId: 'chain-hero', parts: [{ partId: 'hero', definitionOwnerSessionId: 'session-a', revision: { artifactChainId: 'chain-hero', partId: 'hero', partRevisionId: 'hero-base', ownerSessionId: 'session-a', digestSha256: 'a'.repeat(64), size: 10, mediaType: 'text/html' } }] } } as DesktopV3ArtifactCatalogEntry
  const candidates = candidateReferences.map((reference, index) => ({ ...base, artifactId: reference.variantId, collectionId: reference.collectionId, eventSeq: reference.eventSeq, artifactStepId: 'hero-round', candidateIndex: index + 1, step: { id: 'hero-round', graphState: 'git_projection' as const, artifactChainId: 'chain-hero', revisionNumber: 2, parent: rootReference, candidates: candidateReferences }, lineage: { ...base.lineage, iterationGroupId: 'hero-group', partId: 'hero', partLabel: 'Hero' }, composition: { id: `hero-composition-${index}`, artifactChainId: 'chain-hero', iterationGroupId: 'hero-group', iterationTurnId: 'hero-round', parts: [{ partId: 'hero', definitionOwnerSessionId: 'session-a', revision: { artifactChainId: 'chain-hero', partId: 'hero', partRevisionId: `hero-${index + 1}`, ownerSessionId: 'session-a', digestSha256: String(index + 1).padStart(64, 'b').slice(-64), size: 10, mediaType: 'text/html' } }] }, partRevisions: [{ reference: { artifactChainId: 'chain-hero', partId: 'hero', partRevisionId: `hero-${index + 1}`, ownerSessionId: 'session-a', digestSha256: String(index + 1).padStart(64, 'b').slice(-64), size: 10, mediaType: 'text/html' }, iterationTurnId: 'hero-round', iterationGroupId: 'hero-group', eventSeq: reference.eventSeq }] })) as DesktopV3ArtifactCatalogEntry[]

  const entries = [base, ...candidates]
  base.chain = { ...chain, head: candidateReferences[0]! }
  const group = desktopV3ArtifactSidebarGroups(entries).find((candidate) => candidate.key === 'turn:session-a:hero-group')!
  const iterationGroup = desktopV3ArtifactSidebarIterationGroup(entries, group)
  assert.equal(iterationGroup?.iterationCount, 10)
  assert.equal(iterationGroup?.partId, 'hero')
  assert.equal(iterationGroup?.partLabel, 'Hero')
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
  const rootReference = { sessionId: 'session-a', collectionId: 'collection-overall', variantId: 'variant-root', eventSeq: 1 }
  const nextReference = { sessionId: 'session-a', collectionId: 'collection-turn-2', variantId: 'variant-turn-2', eventSeq: 2 }
  const root = {
    ...artifact('session-a', 'variant-root'),
    collectionId: 'collection-overall',
    collectionName: 'Overall concepts',
    eventSeq: 1,
    graphState: 'git_projection' as const,
    artifactChainId: 'chain-selected',
    artifactStepId: 'step-root',
    candidateIndex: 1,
    chain: { id: 'chain-selected', graphState: 'git_projection', name: 'Selected concept', root: rootReference, head: nextReference, revisionCount: 2, lastRoundId: 'step-turn-2' },
    step: { id: 'step-root', graphState: 'git_projection', artifactChainId: 'chain-selected', revisionNumber: 1, candidates: [rootReference] },
  } as DesktopV3ArtifactCatalogEntry
  const nextTurn = {
    ...root,
    artifactId: 'variant-turn-2',
    collectionId: 'collection-turn-2',
    eventSeq: 2,
    artifactStepId: 'step-turn-2',
    step: { id: 'step-turn-2', graphState: 'git_projection', artifactChainId: 'chain-selected', revisionNumber: 2, parent: rootReference, candidates: [nextReference] },
  } as DesktopV3ArtifactCatalogEntry
  const otherReference = { sessionId: 'session-a', collectionId: 'collection-overall', variantId: 'variant-other', eventSeq: 3 }
  const unselectedRoot = {
    ...root,
    artifactId: 'variant-other',
    eventSeq: 3,
    artifactChainId: 'chain-other',
    artifactStepId: 'step-overall',
    candidateIndex: 2,
    chain: { id: 'chain-other', graphState: 'git_projection', name: 'Other concept', root: otherReference, head: otherReference, revisionCount: 1, lastRoundId: 'step-overall' },
    step: { id: 'step-overall', graphState: 'git_projection', artifactChainId: 'chain-other', revisionNumber: 1, candidates: [otherReference] },
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

test('sidebar presents one storyboard proposal with its ordered parts and exact viewer navigation', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /desktopV3ArtifactStudioStoryboard\(artifacts, representative\)/)
  assert.match(source, /One initial proposal/)
  assert.match(source, /data-artifact-sidebar-storyboard-part/)
  assert.match(source, /storyboard\.parts\.map/)
  assert.match(source, /onOpenArtifact\(target, part\.id\)/)
  assert.match(source, /const initialStoryboardPart = storyboard\?\.parts\[0\]/)
  assert.match(source, /const openTarget = iterationGroup\?\.target \?\? initialStoryboardPart\?\.still \?\? storyboard\?\.source \?\? representative/)
  assert.match(source, /const openPartId = iterationGroup\?\.partId \?\? initialStoryboardPart\?\.id \?\? ''/)
  assert.match(source, /href=\{artifactHref\(openTarget\)\}/)
  assert.match(source, /onOpenArtifact\(openTarget, openPartId, group\.key\)/)
})

test('sidebar turn copy keeps multi-part rounds at the complete-iteration level', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /Current authored turn/)
  assert.match(source, /Authored turn history/)
  assert.match(source, /Authoritative head/)
  assert.match(source, /Decision recorded/)
  assert.match(source, /turn\.parts\.length === 1/)
  assert.match(source, /parts changed together/)
  assert.match(source, />Open iteration</)
})

test('sidebar groups documents and render-only helpers by durable media role without inventing In Progress', () => {
  const original = { ...artifact('session-a', 'atlas-original'), collectionId: 'atlas-original', eventSeq: 3231, updatedAt: 3231, graphState: 'git_projection' as const, artifactChainId: 'atlas-chain', artifactStepId: 'atlas-step-1', chain: { id: 'atlas-chain', graphState: 'git_projection', name: 'Atlas gyroscope', root: { sessionId: 'session-a', collectionId: 'atlas-original', variantId: 'atlas-original', eventSeq: 3231 }, head: { sessionId: 'session-a', collectionId: 'atlas-derived', variantId: 'atlas-derived', eventSeq: 3443 }, revisionCount: 2, lastRoundId: 'atlas-step-2' }, step: { id: 'atlas-step-1', graphState: 'git_projection', artifactChainId: 'atlas-chain', revisionNumber: 1, candidates: [{ sessionId: 'session-a', collectionId: 'atlas-original', variantId: 'atlas-original', eventSeq: 3231 }] } } as DesktopV3ArtifactCatalogEntry
  const derived = { ...original, artifactId: 'atlas-derived', collectionId: 'atlas-derived', eventSeq: 3443, updatedAt: 3443, artifactStepId: 'atlas-step-2', step: { id: 'atlas-step-2', graphState: 'git_projection', artifactChainId: 'atlas-chain', revisionNumber: 2, parent: original.step!.candidates[0], candidates: [{ sessionId: 'session-a', collectionId: 'atlas-derived', variantId: 'atlas-derived', eventSeq: 3443 }] } } as DesktopV3ArtifactCatalogEntry
  const markdown = (id: string) => ({ ...artifact('session-a', id), filename: `${id}.md`, mediaType: 'text/markdown', kind: 'text', category: 'document' as const })
  const image = { ...artifact('session-a', 'visual'), mediaType: 'image/png', kind: 'image' }
  const fallback = { ...image, artifactId: 'atlas-fallback', role: 'render_only' as const, collectionId: 'fallback', eventSeq: 3468, updatedAt: 3468, lineage: { ...image.lineage, sourceSessionId: 'session-a', sourceCollectionId: 'atlas-derived', sourceVariantId: 'atlas-derived', sourceEventSeq: 3443 } }

  const groups = desktopV3ArtifactSidebarGroups([markdown('notes-1'), fallback, image, derived, markdown('notes-2'), original])
  assert.deepEqual(groups.map((group) => group.section), ['motion', 'visual', 'documents', 'supporting'])
  assert.deepEqual(groups[0]?.entries.map((entry) => entry.artifactId), ['atlas-original', 'atlas-derived'])
  assert.deepEqual(groups.find((group) => group.section === 'documents')?.entries.map((entry) => entry.filename), ['notes-1.md', 'notes-2.md'])
  assert.deepEqual(groups.find((group) => group.section === 'supporting')?.entries.map((entry) => entry.artifactId), ['atlas-fallback'])
})

test('sidebar pins the first focused chain and keeps later iteration groups visible', () => {
  const projectedChain = (chainId: string, eventSeq: number) => {
    const rootRef = { sessionId: 'session-a', collectionId: `${chainId}-root`, variantId: `${chainId}-root`, eventSeq: eventSeq - 1 }
    const headRef = { sessionId: 'session-a', collectionId: `${chainId}-head`, variantId: `${chainId}-head`, eventSeq }
    const chain = { id: chainId, graphState: 'git_projection' as const, name: chainId, root: rootRef, head: headRef, revisionCount: 2, lastRoundId: `${chainId}-step-2` }
    const root = { ...artifact('session-a', rootRef.variantId), collectionId: rootRef.collectionId, eventSeq: rootRef.eventSeq, updatedAt: rootRef.eventSeq, graphState: 'git_projection' as const, artifactChainId: chainId, artifactStepId: `${chainId}-step-1`, chain, step: { id: `${chainId}-step-1`, graphState: 'git_projection', artifactChainId: chainId, revisionNumber: 1, candidates: [rootRef] } } as DesktopV3ArtifactCatalogEntry
    const head = { ...root, artifactId: headRef.variantId, collectionId: headRef.collectionId, eventSeq, updatedAt: eventSeq, artifactStepId: `${chainId}-step-2`, step: { id: `${chainId}-step-2`, graphState: 'git_projection', artifactChainId: chainId, revisionNumber: 2, parent: rootRef, candidates: [headRef] } } as DesktopV3ArtifactCatalogEntry
    return [root, head]
  }

  const [olderRoot, olderHead] = projectedChain('older-chain', 20)
  const [newerRoot, newerHead] = projectedChain('newer-chain', 40)
  olderHead!.targetedPartId = 'part-2'
  olderHead!.lineage = { ...olderHead!.lineage!, iterationGroupId: 'older-focused', iterationGroup: 'Part 2 directions', partId: 'part-2', partLabel: 'Part 2' }
  newerHead!.targetedPartIds = ['part-2', 'part-3']
  newerHead!.lineage = { ...newerHead!.lineage!, iterationGroupId: 'newer-focused', iterationGroup: 'Part 2 + Part 3 directions' }

  const groups = desktopV3ArtifactSidebarGroups([newerHead!, newerRoot!, olderHead!, olderRoot!])
  assert.equal(groups.filter((group) => group.section === 'active').length, 1)
  assert.equal(groups.find((group) => group.section === 'active')?.key, 'chain:older-chain')
  assert.deepEqual(groups.filter((group) => group.key.startsWith('turn:')).map((group) => group.label), ['Part 2 directions', 'Part 2 + Part 3 directions'])
})

test('sidebar exact group navigation uses a durable presentation key while In Progress opens its head', async () => {
  const sidebar = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  const gallery = await readFile(new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url), 'utf8')
  const pane = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  assert.match(sidebar, /onOpenArtifact\(openTarget, openPartId, group\.key\)/)
  assert.match(gallery, /initialGroupKey\?/)
  assert.match(gallery, /groups\.find\(\(group\) => group\.key === initialGroupKey\)/)
  assert.match(pane, /setArtifactGalleryInitialGroupKey\(groupKey\)/)
  assert.match(pane, /groupKey\.startsWith\("turn:"\)/)
})

test('sidebar renders compact filename-first document rows and collapsed supporting assets', async () => {
  const source = await readFile(new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url), 'utf8')
  assert.match(source, /data-artifact-sidebar-document-row/)
  assert.match(source, /artifact\.filename \|\| artifact\.label/)
  assert.match(source, /open=\{group\.section === 'documents'\}/)
  assert.match(source, /Show supporting render assets/)
  assert.match(source, /Current authored turn/)
  assert.match(source, /Authoritative head/)
  assert.match(source, /onOpenArtifact\(target, turnPart\.partId\)/)
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
  assert.equal(desktopV3NextSessionSidebarView({ current: 'plan', previousArtifactCount: 0, artifactCount: 1, hasPlan: true, prioritizePlan: true, prioritizeArtifact: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true }), 'artifacts')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 1, artifactCount: 2, hasPlan: true, prioritizePlan: true }), 'plan')
  assert.equal(desktopV3NextSessionSidebarView({ current: 'artifacts', previousArtifactCount: 2, artifactCount: 0, hasPlan: true, hasPendingVisualSwarm: true }), 'plan')

})
