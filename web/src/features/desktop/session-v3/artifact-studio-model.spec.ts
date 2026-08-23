import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopV3ArtifactCatalogEntry } from './artifact-api'
import {
  desktopV3ArtifactStudioBranchDepth,
  desktopV3ArtifactStudioEntries,
  desktopV3ArtifactStudioHead,
  desktopV3ArtifactStudioRounds,
  desktopV3ArtifactStudioParent,
  desktopV3ArtifactStudioRoot,
  desktopV3ArtifactStudioSectionAlternatives,
  desktopV3ArtifactStudioSectionLineage,
} from './artifact-studio-model'

function artifact(input: {
  id: string
  collection: string
  eventSeq: number
  source?: DesktopV3ArtifactCatalogEntry
  section?: string
  selected?: boolean
  updatedAt?: number
  chain?: string
  revision?: number
  round?: string
  candidate?: number
}): DesktopV3ArtifactCatalogEntry {
  return {
    artifactId: input.id,
    collectionId: input.collection,
    sessionId: 'session-1',
    sessionTitle: 'Studio', workspacePath: '', workspaceName: '', planId: '', planTitle: '', checkpointId: '', checkpointTitle: '',
    label: input.id, description: '', collectionName: input.collection, collectionDescription: '', filename: `${input.id}.html`, mediaType: 'text/html', kind: 'html',
    status: 'ready', previewable: true, selected: input.selected, category: 'visual', updatedAt: input.updatedAt ?? input.eventSeq, eventSeq: input.eventSeq,
    artifactChainId: input.chain, revisionNumber: input.revision, revisionRoundId: input.round, candidateIndex: input.candidate,
    lineage: input.source ? {
      parentSessionId: 'session-1', sourceSessionId: input.source.sessionId, sourceCollectionId: input.source.collectionId!, sourceVariantId: input.source.artifactId, sourceEventSeq: input.source.eventSeq,
      taskCallId: '', programId: '', programJobId: '', childSessionId: '', iterationGroupId: '', iterationGroup: '', iterationId: input.id, iterationIndex: 1, iterationLabel: input.id, iterationTheme: '',
      iterationSectionId: input.section ?? '', iterationSectionLabel: input.section ?? '', iterationSectionStartMs: 0, iterationSectionEndMs: 1_000,
      runId: '', planId: '', checkpointId: '', attemptId: '',
    } : null,
  }
}

test('artifact studio uses server-projected canonical chains, ordered rounds, and the active head', () => {
  const base = artifact({ id: 'base', collection: 'round-1', eventSeq: 1, chain: 'chain-1', revision: 1, round: 'round-1', candidate: 1 })
  const candidateA = artifact({ id: 'candidate-a', collection: 'round-2', eventSeq: 2, chain: 'chain-1', revision: 2, round: 'round-2', candidate: 1 })
  const candidateB = artifact({ id: 'candidate-b', collection: 'round-2', eventSeq: 3, chain: 'chain-1', revision: 2, round: 'round-2', candidate: 2, selected: true })
  const unrelated = artifact({ id: 'unrelated', collection: 'other', eventSeq: 4, chain: 'chain-2', revision: 1 })
  const entries = [candidateB, unrelated, base, candidateA]

  assert.deepEqual(desktopV3ArtifactStudioEntries(entries, candidateA).map((entry) => entry.artifactId), ['base', 'candidate-a', 'candidate-b'])
  assert.equal(desktopV3ArtifactStudioHead(entries, candidateA).artifactId, 'candidate-b')
  assert.deepEqual(desktopV3ArtifactStudioRounds(entries, candidateA).map((round) => [round.id, round.revisionNumber, round.candidates.length]), [['round-1', 1, 1], ['round-2', 2, 2]])
})

test('artifact studio follows recursive exact source lineage into one branch tree', () => {
  const root = artifact({ id: 'root', collection: 'root-collection', eventSeq: 1 })
  const first = artifact({ id: 'first', collection: 'iteration-a', eventSeq: 2, source: root, section: 'opening' })
  const second = artifact({ id: 'second', collection: 'iteration-b', eventSeq: 3, source: first, section: 'opening' })
  const entries = [root, first, second]

  assert.equal(desktopV3ArtifactStudioParent(entries, second), first)
  assert.equal(desktopV3ArtifactStudioRoot(entries, second), root)
  assert.equal(desktopV3ArtifactStudioBranchDepth(entries, root), 0)
  assert.equal(desktopV3ArtifactStudioBranchDepth(entries, first), 1)
  assert.equal(desktopV3ArtifactStudioBranchDepth(entries, second), 2)
  assert.deepEqual(desktopV3ArtifactStudioSectionAlternatives(entries, second, 'opening'), [first, second])
  assert.equal(desktopV3ArtifactStudioSectionLineage(entries, second)?.iterationSectionId, 'opening')
})

test('artifact studio recovers section metadata through focused correction descendants', () => {
  const root = artifact({ id: 'root', collection: 'root-collection', eventSeq: 1 })
  const iteration = artifact({ id: 'iteration', collection: 'iteration-a', eventSeq: 2, source: root, section: 'opening' })
  const correction = artifact({ id: 'correction', collection: 'correction-a', eventSeq: 3, source: iteration })
  correction.lineage!.iterationSectionId = ''
  correction.lineage!.iterationSectionLabel = ''
  correction.lineage!.iterationSectionStartMs = -1
  correction.lineage!.iterationSectionEndMs = -1

  const entries = [root, iteration, correction]
  assert.equal(desktopV3ArtifactStudioSectionLineage(entries, correction)?.iterationSectionId, 'opening')
  assert.deepEqual(desktopV3ArtifactStudioSectionAlternatives(entries, correction, 'opening'), [iteration, correction])
})

test('artifact studio groups section alternatives when the exact external source is not in the local catalog', () => {
  const externalRoot = artifact({ id: 'external-root', collection: 'external-collection', eventSeq: 1 })
  const first = artifact({ id: 'first', collection: 'iteration-a', eventSeq: 2, source: externalRoot, section: 'opening' })
  const second = artifact({ id: 'second', collection: 'iteration-b', eventSeq: 3, source: externalRoot, section: 'opening' })

  assert.deepEqual(desktopV3ArtifactStudioSectionAlternatives([first, second], first, 'opening'), [first, second])
})

test('artifact studio keeps unrelated roots and sections out of a section branch', () => {
  const root = artifact({ id: 'root', collection: 'root-collection', eventSeq: 1 })
  const opening = artifact({ id: 'opening', collection: 'iteration-a', eventSeq: 2, source: root, section: 'opening' })
  const payoff = artifact({ id: 'payoff', collection: 'iteration-b', eventSeq: 3, source: root, section: 'payoff' })
  const otherRoot = artifact({ id: 'other-root', collection: 'other-root', eventSeq: 4 })
  const unrelated = artifact({ id: 'unrelated', collection: 'iteration-c', eventSeq: 5, source: otherRoot, section: 'opening' })

  assert.deepEqual(desktopV3ArtifactStudioSectionAlternatives([root, opening, payoff, otherRoot, unrelated], opening, 'opening'), [opening])
})
