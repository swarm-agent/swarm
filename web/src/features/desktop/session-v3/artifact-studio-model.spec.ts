import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopV3ArtifactCatalogEntry, DesktopV3ArtifactChainReference } from './artifact-api'
import {
  desktopV3ArtifactStudioEntries,
  desktopV3ArtifactStudioHead,
  desktopV3ArtifactStudioParent,
  desktopV3ArtifactStudioRounds,
  desktopV3ArtifactStudioSectionAlternatives,
} from './artifact-studio-model'

const ref = (id: string, eventSeq: number): DesktopV3ArtifactChainReference => ({ sessionId: 'session-1', collectionId: `collection-${eventSeq}`, variantId: id, eventSeq })

function artifact(input: { id: string; eventSeq: number; step: string; revision: number; candidates: DesktopV3ArtifactChainReference[]; parent?: DesktopV3ArtifactChainReference; accepted?: DesktopV3ArtifactChainReference; head: DesktopV3ArtifactChainReference; section?: string }): DesktopV3ArtifactCatalogEntry {
  return {
    artifactId: input.id, collectionId: `collection-${input.eventSeq}`, sessionId: 'session-1', sessionTitle: 'Studio', workspacePath: '', workspaceName: '', planId: '', planTitle: '', checkpointId: '', checkpointTitle: '',
    label: input.id, description: '', collectionName: input.step, collectionDescription: '', filename: `${input.id}.html`, mediaType: 'text/html', kind: 'html', status: 'ready', previewable: true, category: 'visual', updatedAt: input.eventSeq, eventSeq: input.eventSeq,
    graphState: 'authoritative', artifactChainId: 'chain-1', artifactStepId: input.step, revisionNumber: input.revision, candidateIndex: input.candidates.findIndex((candidate) => candidate.variantId === input.id) + 1,
    chain: { id: 'chain-1', name: 'Artifact', root: ref('base', 1), head: input.head, revisionCount: input.revision, lastRoundId: input.step },
    step: { id: input.step, artifactChainId: 'chain-1', revisionNumber: input.revision, candidates: input.candidates, ...(input.parent ? { parent: input.parent } : {}), ...(input.accepted ? { accepted: input.accepted } : {}) },
    lineage: { parentSessionId: 'session-1', sourceSessionId: '', sourceCollectionId: '', sourceVariantId: '', taskCallId: '', programId: '', programJobId: '', childSessionId: '', iterationGroupId: '', iterationGroup: '', iterationId: input.id, iterationIndex: 1, iterationLabel: input.id, iterationTheme: '', iterationSectionId: input.section ?? '', iterationSectionLabel: input.section ?? '', iterationSectionStartMs: 0, iterationSectionEndMs: 1000, runId: '', planId: '', checkpointId: '', attemptId: '' },
  }
}

test('artifact studio renders authoritative ordered steps with ten candidates and explicit accepted head', () => {
  const baseRef = ref('base', 1)
  const candidateRefs = Array.from({ length: 10 }, (_, index) => ref(`candidate-${index + 1}`, index + 2))
  const accepted = candidateRefs[6]!
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head: accepted })
  const candidates = candidateRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: candidateRefs, parent: baseRef, accepted, head: accepted, section: 'hero' }))
  const entries = [candidates[9]!, base, ...candidates.slice(0, 9)]

  assert.equal(desktopV3ArtifactStudioEntries(entries, candidates[0]!).length, 11)
  assert.equal(desktopV3ArtifactStudioHead(entries, candidates[0]!)?.artifactId, 'candidate-7')
  assert.deepEqual(desktopV3ArtifactStudioRounds(entries, candidates[0]!).map((step) => [step.id, step.candidates.length, step.accepted?.artifactId]), [['step-1', 1, 'base'], ['step-2', 10, 'candidate-7']])
  assert.equal(desktopV3ArtifactStudioParent(entries, candidates[0]!)?.artifactId, 'base')
  assert.equal(desktopV3ArtifactStudioSectionAlternatives(entries, candidates[0]!, 'hero').length, 10)
})

test('artifact studio never infers lineage or a head for legacy unstructured entries', () => {
  const legacy = artifact({ id: 'legacy', eventSeq: 20, step: 'legacy-step', revision: 3, candidates: [ref('legacy', 20)], head: ref('legacy', 20) })
  legacy.graphState = 'legacy_unproven'
  legacy.step = undefined
  legacy.artifactStepId = undefined
  legacy.selected = true
  assert.deepEqual(desktopV3ArtifactStudioEntries([legacy], legacy), [legacy])
  assert.equal(desktopV3ArtifactStudioHead([legacy], legacy), undefined)
  assert.equal(desktopV3ArtifactStudioParent([legacy], legacy), undefined)
  assert.deepEqual(desktopV3ArtifactStudioRounds([legacy], legacy), [])
  assert.deepEqual(desktopV3ArtifactStudioSectionAlternatives([legacy], legacy, 'hero'), [])
})
