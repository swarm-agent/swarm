import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopV3ArtifactCatalogEntry, DesktopV3ArtifactChainReference } from './artifact-api'
import {
  desktopV3ArtifactStudioChangedPartIds,
  desktopV3ArtifactStudioEntries,
  desktopV3ArtifactStudioHead,
  desktopV3ArtifactStudioParent,
  desktopV3ArtifactStudioPartIterations,
  desktopV3ArtifactStudioRounds,
  desktopV3ArtifactStudioSamePartRevision,
  desktopV3ArtifactStudioSectionAlternatives,
} from './artifact-studio-model'

const ref = (id: string, eventSeq: number): DesktopV3ArtifactChainReference => ({ sessionId: 'session-1', collectionId: `collection-${eventSeq}`, variantId: id, eventSeq })

const digest = (value: string) => value.padEnd(64, value[0] ?? 'a').slice(0, 64)
const partRef = (partId: string, revision: string, owner = 'session-1') => ({ artifactChainId: 'chain-1', partId, partRevisionId: revision, ownerSessionId: owner, digestSha256: digest(revision.replace(/[^a-f0-9]/g, 'a')), size: revision.length + 10, mediaType: 'text/plain' })

function artifact(input: { id: string; eventSeq: number; step: string; revision: number; candidates: DesktopV3ArtifactChainReference[]; parent?: DesktopV3ArtifactChainReference; accepted?: DesktopV3ArtifactChainReference; head: DesktopV3ArtifactChainReference; section?: string }): DesktopV3ArtifactCatalogEntry {
  return {
    artifactId: input.id, collectionId: `collection-${input.eventSeq}`, sessionId: 'session-1', sessionTitle: 'Studio', workspacePath: '', workspaceName: '', planId: '', planTitle: '', checkpointId: '', checkpointTitle: '',
    label: input.id, description: '', collectionName: input.step, collectionDescription: '', filename: `${input.id}.html`, mediaType: 'text/html', kind: 'html', status: 'ready', previewable: true, category: 'visual', updatedAt: input.eventSeq, eventSeq: input.eventSeq,
    graphState: 'authoritative', artifactChainId: 'chain-1', artifactStepId: input.step, revisionNumber: input.revision, candidateIndex: input.candidates.findIndex((candidate) => candidate.variantId === input.id) + 1,
    chain: { id: 'chain-1', graphState: 'authoritative', name: 'Artifact', root: ref('base', 1), head: input.head, revisionCount: input.revision, lastRoundId: input.step },
    step: { id: input.step, graphState: 'authoritative', artifactChainId: 'chain-1', revisionNumber: input.revision, candidates: input.candidates, ...(input.parent ? { parent: input.parent } : {}), ...(input.accepted ? { accepted: input.accepted } : {}) },
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

test('artifact studio groups section-targeted revision turns by part', () => {
  const baseRef = ref('base', 1)
  const flowRoundTwoRefs = [ref('flow-a', 2), ref('flow-b', 3)]
  const flowRoundThreeRefs = [ref('flow-c', 4), ref('flow-d', 5)]
  const head = flowRoundThreeRefs[0]!
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head })
  const baseParts = [{ partId: 'flow', definitionOwnerSessionId: 'session-1', revision: partRef('flow', 'a1') }, { partId: 'footer', definitionOwnerSessionId: 'session-1', revision: partRef('footer', 'b1') }]
  Object.assign(base, { partGraphState: 'authoritative', composition: { id: 'composition-1', artifactChainId: 'chain-1', parts: baseParts }, partDefinitions: [{ id: 'flow', label: 'Flow', description: '', locator: { id: 'flow', label: 'Flow', kind: 'temporal', description: '', startMs: 0, endMs: 1000, x: 0, y: 0, width: 0, height: 0, page: 0, stateId: '', selector: '' } }, { id: 'footer', label: 'Footer', description: '', locator: null }], partRevisions: baseParts.map((part) => ({ reference: part.revision, parent: null, createdAt: 1, eventSeq: 1 })) })
  const roundTwo = flowRoundTwoRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: flowRoundTwoRefs, parent: baseRef, accepted: flowRoundTwoRefs[0], head, section: 'flow' }))
  roundTwo.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'authoritative', targetedPartId: 'flow', composition: { id: `composition-2-${index}`, artifactChainId: 'chain-1', parts: [{ ...baseParts[0]!, revision: partRef('flow', `a2${index}`) }, baseParts[1]!] }, partDefinitions: base.partDefinitions, partRevisions: [{ reference: partRef('flow', `a2${index}`), parent: baseParts[0]!.revision, createdAt: 2, eventSeq: 2 }, { reference: baseParts[1]!.revision, parent: null, createdAt: 1, eventSeq: 1 }] }))
  const roundThree = flowRoundThreeRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 4, step: 'step-3', revision: 3, candidates: flowRoundThreeRefs, parent: flowRoundTwoRefs[0], accepted: head, head, section: 'flow' }))
  roundThree.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'authoritative', targetedPartId: 'flow', composition: { id: `composition-3-${index}`, artifactChainId: 'chain-1', parts: [{ ...baseParts[0]!, revision: partRef('flow', `a3${index}`) }, baseParts[1]!] }, partDefinitions: base.partDefinitions, partRevisions: [{ reference: partRef('flow', `a3${index}`), parent: roundTwo[0]!.composition!.parts[0]!.revision, createdAt: 3, eventSeq: 4 }, { reference: baseParts[1]!.revision, parent: null, createdAt: 1, eventSeq: 1 }] }))

  const partIterations = desktopV3ArtifactStudioPartIterations([base, ...roundTwo, ...roundThree], base)
  assert.deepEqual(partIterations.map((part) => [part.id, part.label, part.turns.map((turn) => [turn.revisionNumber, turn.candidates.length])]), [
    ['flow', 'Flow', [[2, 2], [3, 2]]],
  ])
})

test('artifact studio groups atomic multi-part candidates into every affected part lineage', () => {
  const baseRef = ref('base', 1)
  const candidateRefs = [ref('multi-a', 2), ref('multi-b', 3)]
  const head = candidateRefs[0]!
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head })
  const baseParts = [{ partId: 'hero', definitionOwnerSessionId: 'session-1', revision: partRef('hero', 'a1') }, { partId: 'footer', definitionOwnerSessionId: 'session-1', revision: partRef('footer', 'b1') }]
  Object.assign(base, { partGraphState: 'authoritative', composition: { id: 'composition-1', artifactChainId: 'chain-1', parts: baseParts }, acceptedPartHeads: baseParts, partDefinitions: [{ id: 'hero', label: 'Hero', description: '', locator: null }, { id: 'footer', label: 'Footer', description: '', locator: null }] })
  const candidates = candidateRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: candidateRefs, parent: baseRef, accepted: head, head }))
  candidates.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'authoritative', composition: { id: `composition-2-${index}`, artifactChainId: 'chain-1', iterationTurnId: 'turn-2', iterationGroupId: 'group-2', parts: [{ ...baseParts[0]!, revision: partRef('hero', `a2${index}`) }, { ...baseParts[1]!, revision: partRef('footer', `b2${index}`), locked: true }] }, partDefinitions: base.partDefinitions }))

  const entries = [base, ...candidates]
  assert.deepEqual(desktopV3ArtifactStudioChangedPartIds(entries, candidates[0]!), ['hero', 'footer'])
  assert.equal(desktopV3ArtifactStudioSamePartRevision({ ...baseParts[0]!, locked: true }, baseParts[0]!), true, 'lock metadata must not fabricate a byte revision change')
  const history = desktopV3ArtifactStudioPartIterations(entries, base)
  assert.deepEqual(history.map((part) => part.accepted?.revision.partRevisionId), ['a1', 'b1'])
  assert.deepEqual(history.map((part) => [part.id, part.turns[0]?.groupId, part.turns[0]?.changedPartIds, part.turns[0]?.candidates.length]), [
    ['hero', 'group-2', ['hero', 'footer'], 2],
    ['footer', 'group-2', ['hero', 'footer'], 2],
  ])
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
