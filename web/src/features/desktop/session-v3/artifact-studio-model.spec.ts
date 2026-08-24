import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopV3ArtifactCatalogEntry, DesktopV3ArtifactChainReference } from './artifact-api'
import {
  desktopV3ArtifactStudioChangedPartIds,
  desktopV3ArtifactStudioEntries,
  desktopV3ArtifactStudioHead,
  desktopV3ArtifactStudioParent,
  desktopV3ArtifactStudioPartIterations,
  desktopV3ArtifactStudioPresentationGroupKey,
  desktopV3ArtifactStudioRounds,
  desktopV3ArtifactStudioSamePartRevision,
  desktopV3ArtifactStudioSectionAlternatives,
  desktopV3ArtifactStudioTurns,
} from './artifact-studio-model'

const ref = (id: string, eventSeq: number): DesktopV3ArtifactChainReference => ({ sessionId: 'session-1', collectionId: `collection-${eventSeq}`, variantId: id, eventSeq })

const digest = (value: string) => value.padEnd(64, value[0] ?? 'a').slice(0, 64)
const partRef = (partId: string, revision: string, owner = 'session-1') => ({ artifactChainId: 'chain-1', partId, partRevisionId: revision, ownerSessionId: owner, digestSha256: digest(revision.replace(/[^a-f0-9]/g, 'a')), size: revision.length + 10, mediaType: 'text/plain' })

function artifact(input: { id: string; eventSeq: number; step: string; revision: number; candidates: DesktopV3ArtifactChainReference[]; parent?: DesktopV3ArtifactChainReference; accepted?: DesktopV3ArtifactChainReference; head: DesktopV3ArtifactChainReference; section?: string }): DesktopV3ArtifactCatalogEntry {
  return {
    artifactId: input.id, collectionId: `collection-${input.eventSeq}`, sessionId: 'session-1', sessionTitle: 'Studio', workspacePath: '', workspaceName: '', planId: '', planTitle: '', checkpointId: '', checkpointTitle: '',
    label: input.id, description: '', collectionName: input.step, collectionDescription: '', filename: `${input.id}.html`, mediaType: 'text/html', kind: 'html', status: 'ready', previewable: true, category: 'visual', updatedAt: input.eventSeq, eventSeq: input.eventSeq,
    graphState: 'git_projection', artifactChainId: 'chain-1', artifactStepId: input.step, revisionNumber: input.revision, candidateIndex: input.candidates.findIndex((candidate) => candidate.variantId === input.id) + 1,
    chain: { id: 'chain-1', graphState: 'git_projection', name: 'Artifact', root: ref('base', 1), head: input.head, revisionCount: input.revision, lastRoundId: input.step },
    step: { id: input.step, graphState: 'git_projection', artifactChainId: 'chain-1', revisionNumber: input.revision, candidates: input.candidates, ...(input.parent ? { parent: input.parent } : {}), ...(input.accepted ? { accepted: input.accepted } : {}) },
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

test('artifact studio keeps source-free root iterations together until one candidate gains a later turn', () => {
  const firstRef = ref('first', 1)
  const secondRef = ref('second', 2)
  const first = artifact({ id: 'first', eventSeq: 1, step: 'root-first', revision: 1, candidates: [firstRef], head: firstRef })
  const second = artifact({ id: 'second', eventSeq: 2, step: 'root-second', revision: 1, candidates: [secondRef], head: secondRef })
  const firstGroupRef = { ...firstRef, collectionId: 'overall-wave' }
  const secondGroupRef = { ...secondRef, collectionId: 'overall-wave' }
  first.collectionId = 'overall-wave'
  second.collectionId = 'overall-wave'
  first.artifactChainId = 'chain-first'
  first.chain = { ...first.chain!, id: 'chain-first', root: firstGroupRef, head: firstGroupRef, revisionCount: 1 }
  first.step = { ...first.step!, artifactChainId: 'chain-first', candidates: [firstGroupRef] }
  second.artifactChainId = 'chain-second'
  second.chain = { ...second.chain!, id: 'chain-second', root: secondGroupRef, head: secondGroupRef, revisionCount: 1 }
  second.step = { ...second.step!, artifactChainId: 'chain-second', candidates: [secondGroupRef] }

  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second], first), 'collection:session-1:overall-wave')
  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second], second), 'collection:session-1:overall-wave')

  const nextRef = { ...ref('first-v2', 3), collectionId: 'first-v2-collection' }
  const next = artifact({ id: 'first-v2', eventSeq: 3, step: 'first-turn-2', revision: 2, candidates: [nextRef], parent: firstGroupRef, head: nextRef })
  next.artifactChainId = 'chain-first'
  next.chain = { ...next.chain!, id: 'chain-first', root: firstGroupRef, revisionCount: 2 }
  next.step = { ...next.step!, artifactChainId: 'chain-first' }
  first.chain = { ...first.chain!, revisionCount: 2 }

  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second, next], first), 'chain:chain-first')
  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second, next], next), 'chain:chain-first')
  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second, next], second), 'collection:session-1:overall-wave')
})

test('artifact studio groups one generating turn across collections by durable iteration group', () => {
  const first = artifact({ id: 'first', eventSeq: 1, step: 'first', revision: 1, candidates: [ref('first', 1)], head: ref('first', 1) })
  const second = artifact({ id: 'second', eventSeq: 2, step: 'second', revision: 1, candidates: [ref('second', 2)], head: ref('second', 2) })
  first.collectionId = 'collection-a'
  second.collectionId = 'collection-b'
  first.lineage.iterationGroupId = 'task-turn-9'
  second.lineage.iterationGroupId = 'task-turn-9'

  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second], first), 'turn:session-1:task-turn-9')
  assert.equal(desktopV3ArtifactStudioPresentationGroupKey([first, second], second), 'turn:session-1:task-turn-9')
})

test('artifact studio groups section-targeted revision turns by part', () => {
  const baseRef = ref('base', 1)
  const flowRoundTwoRefs = [ref('flow-a', 2), ref('flow-b', 3)]
  const flowRoundThreeRefs = [ref('flow-c', 4), ref('flow-d', 5)]
  const head = flowRoundThreeRefs[0]!
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head })
  const baseParts = [{ partId: 'flow', definitionOwnerSessionId: 'session-1', revision: partRef('flow', 'a1') }, { partId: 'footer', definitionOwnerSessionId: 'session-1', revision: partRef('footer', 'b1') }]
  Object.assign(base, { partGraphState: 'git_projection', composition: { id: 'composition-1', artifactChainId: 'chain-1', parts: baseParts }, partDefinitions: [{ id: 'flow', label: 'Flow', description: '', locator: { id: 'flow', label: 'Flow', kind: 'temporal', description: '', startMs: 0, endMs: 1000, x: 0, y: 0, width: 0, height: 0, page: 0, stateId: '', selector: '' } }, { id: 'footer', label: 'Footer', description: '', locator: null }], partRevisions: baseParts.map((part) => ({ reference: part.revision, parent: null, createdAt: 1, eventSeq: 1 })) })
  const roundTwo = flowRoundTwoRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: flowRoundTwoRefs, parent: baseRef, accepted: flowRoundTwoRefs[0], head, section: 'flow' }))
  roundTwo.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'git_projection', targetedPartId: 'flow', composition: { id: `composition-2-${index}`, artifactChainId: 'chain-1', parts: [{ ...baseParts[0]!, revision: partRef('flow', `a2${index}`) }, baseParts[1]!] }, partDefinitions: base.partDefinitions, partRevisions: [{ reference: partRef('flow', `a2${index}`), parent: baseParts[0]!.revision, createdAt: 2, eventSeq: 2 }, { reference: baseParts[1]!.revision, parent: null, createdAt: 1, eventSeq: 1 }] }))
  const roundThree = flowRoundThreeRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 4, step: 'step-3', revision: 3, candidates: flowRoundThreeRefs, parent: flowRoundTwoRefs[0], accepted: head, head, section: 'flow' }))
  roundThree.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'git_projection', targetedPartId: 'flow', composition: { id: `composition-3-${index}`, artifactChainId: 'chain-1', parts: [{ ...baseParts[0]!, revision: partRef('flow', `a3${index}`) }, baseParts[1]!] }, partDefinitions: base.partDefinitions, partRevisions: [{ reference: partRef('flow', `a3${index}`), parent: roundTwo[0]!.composition!.parts[0]!.revision, createdAt: 3, eventSeq: 4 }, { reference: baseParts[1]!.revision, parent: null, createdAt: 1, eventSeq: 1 }] }))

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
  Object.assign(base, { partGraphState: 'git_projection', composition: { id: 'composition-1', artifactChainId: 'chain-1', parts: baseParts }, acceptedPartHeads: baseParts, partDefinitions: [{ id: 'hero', label: 'Hero', description: '', locator: null }, { id: 'footer', label: 'Footer', description: '', locator: null }] })
  const candidates = candidateRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: candidateRefs, parent: baseRef, accepted: head, head }))
  candidates.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'git_projection', composition: { id: `composition-2-${index}`, artifactChainId: 'chain-1', iterationTurnId: 'turn-2', iterationGroupId: 'group-2', parts: [{ ...baseParts[0]!, revision: partRef('hero', `a2${index}`) }, { ...baseParts[1]!, revision: partRef('footer', `b2${index}`), locked: true }] }, partDefinitions: base.partDefinitions }))

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

test('artifact studio keeps review-part lineage attached to whole-artifact turns', () => {
  const baseRef = ref('base', 1)
  const candidateRefs = [ref('bloom-a', 2), ref('bloom-b', 3)]
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head: baseRef })
  base.parts = [{ id: 'ignite', label: '01 — Ignite', kind: 'temporal', startMs: 0, endMs: 4000 }, { id: 'bloom', label: '03 — Bloom', kind: 'temporal', startMs: 8000, endMs: 12000 }]
  const candidates = candidateRefs.map((reference, index) => artifact({ id: reference.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: candidateRefs, parent: baseRef, head: baseRef, section: 'bloom' }))
  candidates.forEach((candidate) => {
    candidate.parts = base.parts
    candidate.lineage.iterationSectionLabel = '03 — Bloom'
    candidate.lineage.iterationSectionStartMs = 8000
    candidate.lineage.iterationSectionEndMs = 12000
  })

  const turns = desktopV3ArtifactStudioTurns([base, ...candidates], candidates[0]!)
  assert.deepEqual(turns[1]?.changedPartIds, [])
  assert.deepEqual(turns[1]?.relatedTargets, [{ partId: 'bloom', label: '03 — Bloom', kind: 'temporal', startMs: 8000, endMs: 12000 }])
})

test('artifact studio exposes every authoritative step as a turn, including roots and single fine-tunes', () => {
  const baseRef = ref('base', 1)
  const fineTuneRef = ref('fine-tune', 2)
  const noOpRef = ref('metadata-only', 3)
  const head = noOpRef
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head })
  const baseParts = [{ partId: 'hero', definitionOwnerSessionId: 'session-1', revision: partRef('hero', 'a1') }, { partId: 'footer', definitionOwnerSessionId: 'session-1', revision: partRef('footer', 'b1') }]
  Object.assign(base, { partGraphState: 'git_projection', composition: { id: 'composition-1', artifactChainId: 'chain-1', iterationTurnId: 'turn-1', iterationGroupId: 'group-1', parts: baseParts }, partDefinitions: [{ id: 'hero', label: 'Hero', description: '', locator: null }, { id: 'footer', label: 'Footer', description: '', locator: null }] })
  const fineTune = artifact({ id: 'fine-tune', eventSeq: 2, step: 'step-2', revision: 2, candidates: [fineTuneRef], parent: baseRef, accepted: fineTuneRef, head })
  Object.assign(fineTune, { partGraphState: 'git_projection', composition: { id: 'composition-2', artifactChainId: 'chain-1', iterationTurnId: 'turn-2', iterationGroupId: 'fine-tune-group', parts: [{ ...baseParts[0]!, revision: partRef('hero', 'a2') }, baseParts[1]!] }, partDefinitions: base.partDefinitions })
  const noOp = artifact({ id: 'metadata-only', eventSeq: 3, step: 'step-3', revision: 3, candidates: [noOpRef], parent: fineTuneRef, accepted: noOpRef, head })
  Object.assign(noOp, { partGraphState: 'git_projection', composition: { id: 'composition-3', artifactChainId: 'chain-1', iterationTurnId: 'turn-3', iterationGroupId: 'metadata-group', parts: fineTune.composition!.parts }, partDefinitions: base.partDefinitions })

  const turns = desktopV3ArtifactStudioTurns([noOp, fineTune, base], noOp)
  assert.deepEqual(turns.map((turn) => [turn.id, turn.changedPartIds, turn.candidates.length, turn.accepted?.entry?.artifactId]), [
    ['step-1', ['hero', 'footer'], 1, 'base'],
    ['step-2', ['hero'], 1, 'fine-tune'],
    ['step-3', [], 1, 'metadata-only'],
  ])
  assert.equal(turns[1]?.groupId, 'fine-tune-group')
  assert.equal(turns[1]?.parts[0]?.candidates[0]?.part?.revision.partRevisionId, 'a2')
  assert.equal(turns[1]?.parts[0]?.accepted?.entry?.artifactId, 'fine-tune')
})

test('artifact studio detects changed parts by durable identity when composition order changes', () => {
  const baseRef = ref('base', 1)
  const candidateRef = ref('reordered', 2)
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head: candidateRef })
  const hero = { partId: 'hero', definitionOwnerSessionId: 'session-1', revision: partRef('hero', 'a1') }
  const footer = { partId: 'footer', definitionOwnerSessionId: 'session-1', revision: partRef('footer', 'b1') }
  Object.assign(base, { partGraphState: 'git_projection', composition: { id: 'composition-1', artifactChainId: 'chain-1', iterationTurnId: 'turn-1', iterationGroupId: 'group-1', parts: [hero, footer] }, partDefinitions: [{ id: 'hero', label: 'Hero', description: '', locator: null }, { id: 'footer', label: 'Footer', description: '', locator: null }] })
  const candidate = artifact({ id: 'reordered', eventSeq: 2, step: 'step-2', revision: 2, candidates: [candidateRef], parent: baseRef, accepted: candidateRef, head: candidateRef })
  Object.assign(candidate, { partGraphState: 'git_projection', composition: { id: 'composition-2', artifactChainId: 'chain-1', iterationTurnId: 'turn-2', iterationGroupId: 'group-2', parts: [footer, { ...hero, revision: partRef('hero', 'a2') }] }, partDefinitions: base.partDefinitions })

  assert.deepEqual(desktopV3ArtifactStudioChangedPartIds([base, candidate], candidate), ['hero'])
  assert.deepEqual(desktopV3ArtifactStudioTurns([base, candidate], candidate)[1]?.changedPartIds, ['hero'])
})

test('artifact studio uses authoritative part revision turn metadata when the parent candidate is not hydrated', () => {
  const parentRef = ref('missing-parent', 1)
  const candidateRef = ref('candidate', 2)
  const candidate = artifact({ id: 'candidate', eventSeq: 2, step: 'step-2', revision: 2, candidates: [candidateRef], parent: parentRef, accepted: candidateRef, head: candidateRef })
  const heroRevision = partRef('hero', 'a2')
  Object.assign(candidate, {
    partGraphState: 'git_projection',
    composition: { id: 'composition-2', artifactChainId: 'chain-1', iterationTurnId: 'turn-2', iterationGroupId: 'group-2', parts: [{ partId: 'hero', definitionOwnerSessionId: 'session-1', revision: heroRevision }] },
    partDefinitions: [{ id: 'hero', label: 'Hero', description: '', locator: null }],
    partRevisions: [{ reference: heroRevision, parent: partRef('hero', 'a1'), iterationTurnId: 'turn-2', iterationGroupId: 'group-2', createdAt: 2, eventSeq: 2 }],
  })

  const turns = desktopV3ArtifactStudioTurns([candidate], candidate)
  assert.deepEqual(turns.map((turn) => [turn.id, turn.changedPartIds, turn.parts[0]?.candidates.length]), [['step-2', ['hero'], 1]])
})

test('artifact studio resolves staging and failed placeholders even before their Git projection is complete', () => {
  const baseRef = ref('base', 1)
  const stagingRef = ref('body-staging', 2)
  const failedRef = ref('body-failed', 3)
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef, stagingRef, failedRef], accepted: baseRef, head: baseRef })
  const staging = artifact({ id: 'body-staging', eventSeq: 2, step: 'placeholder', revision: 2, candidates: [stagingRef], head: baseRef })
  staging.graphState = undefined
  staging.step = undefined
  staging.artifactStepId = undefined
  staging.status = 'staging'
  staging.eventSeq = 20
  const failed = artifact({ id: 'body-failed', eventSeq: 3, step: 'placeholder', revision: 2, candidates: [failedRef], head: baseRef })
  failed.graphState = undefined
  failed.step = undefined
  failed.artifactStepId = undefined
  failed.status = 'failed'
  failed.eventSeq = 30

  const turn = desktopV3ArtifactStudioTurns([base, staging, failed], base)[0]
  assert.deepEqual(turn?.candidates.map((candidate) => [candidate.reference.variantId, candidate.entry?.status]), [
    ['base', 'ready'], ['body-staging', 'staging'], ['body-failed', 'failed'],
  ])
})

test('artifact studio preserves authoritative unresolved candidates without fabricating part state', () => {
  const baseRef = ref('base', 1)
  const missingRef = ref('still-staging', 2)
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef, missingRef], accepted: baseRef, head: baseRef })

  const turns = desktopV3ArtifactStudioTurns([base], base)
  assert.equal(turns.length, 1)
  assert.deepEqual(turns[0]?.candidates.map((candidate) => [candidate.reference.variantId, candidate.entry?.artifactId]), [['base', 'base'], ['still-staging', undefined]])
  assert.deepEqual(turns[0]?.changedPartIds, [])
})

test('artifact studio orders an unresolved latest turn after the accepted composition head', () => {
  const baseRef = ref('base', 1)
  const optionRefs = [ref('signal-a', 2), ref('signal-b', 3), ref('signal-c', 4)]
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head: baseRef })
  const options = optionRefs.map((candidate, index) => artifact({ id: candidate.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: optionRefs, parent: baseRef, head: baseRef }))
  assert.deepEqual(desktopV3ArtifactStudioTurns([base, ...options], base).map((turn) => [turn.id, turn.revisionNumber, turn.accepted?.entry?.artifactId]), [
    ['step-1', 1, 'base'],
    ['step-2', 2, undefined],
  ])
})

test('artifact studio preserves three Body candidates, a locked official merge, and a second iteration', () => {
  const baseRef = ref('base', 1)
  const bodyRefs = [ref('body-a', 2), ref('body-b', 3), ref('body-c', 4)]
  const mergedRef = ref('merged-body-b', 5)
  const secondRefs = [ref('body-d', 6), ref('body-e', 7)]
  const base = artifact({ id: 'base', eventSeq: 1, step: 'step-1', revision: 1, candidates: [baseRef], accepted: baseRef, head: mergedRef })
  const baseParts = ['header', 'body', 'footer'].map((part, index) => ({ partId: part, definitionOwnerSessionId: 'session-1', revision: partRef(part, `${index + 1}1`) }))
  Object.assign(base, { partGraphState: 'git_projection', composition: { id: 'composition-1', artifactChainId: 'chain-1', parts: baseParts }, partDefinitions: ['Header', 'Body', 'Footer'].map((label) => ({ id: label.toLowerCase(), label, description: '', locator: null })) })
  const bodyCandidates = bodyRefs.map((reference, index) => artifact({ id: reference.variantId, eventSeq: index + 2, step: 'step-2', revision: 2, candidates: bodyRefs, parent: baseRef, accepted: bodyRefs[1], head: mergedRef }))
  bodyCandidates.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'git_projection', composition: { id: `composition-2-${index}`, artifactChainId: 'chain-1', iterationTurnId: 'turn-2', iterationGroupId: 'body-round-1', parts: [baseParts[0]!, { ...baseParts[1]!, revision: partRef('body', `b2${index}`) }, baseParts[2]!] }, partDefinitions: base.partDefinitions }))
  const merged = artifact({ id: 'merged-body-b', eventSeq: 5, step: 'step-3', revision: 3, candidates: [mergedRef], parent: bodyRefs[1], accepted: mergedRef, head: mergedRef })
  Object.assign(merged, { partGraphState: 'git_projection', composition: { id: 'composition-3', artifactChainId: 'chain-1', iterationTurnId: 'turn-3', iterationGroupId: 'body-lock', parts: [baseParts[0]!, { ...bodyCandidates[1]!.composition!.parts[1]!, locked: true }, baseParts[2]!] }, partDefinitions: base.partDefinitions, acceptedPartHeads: [baseParts[0]!, { ...bodyCandidates[1]!.composition!.parts[1]!, locked: true }, baseParts[2]!] })
  const second = secondRefs.map((reference, index) => artifact({ id: reference.variantId, eventSeq: index + 6, step: 'step-4', revision: 4, candidates: secondRefs, parent: mergedRef, head: mergedRef }))
  second.forEach((candidate, index) => Object.assign(candidate, { partGraphState: 'git_projection', composition: { id: `composition-4-${index}`, artifactChainId: 'chain-1', iterationTurnId: 'turn-4', iterationGroupId: 'body-round-2', parts: [baseParts[0]!, { ...merged.composition!.parts[1]!, revision: partRef('body', `b4${index}`), locked: false }, baseParts[2]!] }, partDefinitions: base.partDefinitions }))

  const entries = [base, ...bodyCandidates, merged, ...second]
  const turns = desktopV3ArtifactStudioTurns(entries, base)
  assert.deepEqual(turns.map((turn) => [turn.revisionNumber, turn.changedPartIds, turn.candidates.length, turn.accepted?.entry?.artifactId]), [
    [1, ['header', 'body', 'footer'], 1, 'base'],
    [2, ['body'], 3, 'body-b'],
    [3, [], 1, 'merged-body-b'],
    [4, ['body'], 2, undefined],
  ])
  assert.equal(desktopV3ArtifactStudioHead(entries, bodyCandidates[0]!)?.artifactId, 'merged-body-b')
  assert.equal(desktopV3ArtifactStudioEntries(entries, bodyCandidates[0]!).some((entry) => entry.artifactId === 'merged-body-b'), true)
  assert.equal(turns[1]?.parts[0]?.accepted?.part?.revision.partRevisionId, 'b21')
  assert.equal(merged.composition?.parts[1]?.locked, true)
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
  assert.deepEqual(desktopV3ArtifactStudioTurns([legacy], legacy), [])
  assert.deepEqual(desktopV3ArtifactStudioSectionAlternatives([legacy], legacy, 'hero'), [])
})
