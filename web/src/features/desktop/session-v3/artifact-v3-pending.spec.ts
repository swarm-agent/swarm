import assert from 'node:assert/strict'
import test from 'node:test'
import { firstDesktopV3NativeArtifactPendingCandidate, normalizeDesktopV3NativeArtifactSummary, pendingDesktopV3NativeArtifactTurns, type DesktopV3NativeArtifactTurn } from './artifact-v3-api'

// Requirement: pending navigation is derived from turn status and ready exact
// revisions, never from latest history or unchosen siblings of selected turns.
// Authority: shared catalog/Studio projection; pure tests cover ordering/failure.
test('pending artifact entry prefers newest reviewable turn and preserves candidate order', () => {
  const turn = (turnId: string, createdAt: number, status = 'awaiting_selection', candidates: unknown[] = []) => ({ turn_id: turnId, created_at: createdAt, status, candidates })
  const candidate = (id: string, status = 'ready') => ({ candidate_id: id, status, revision: status === 'ready' ? { revision_ref: `revision-${id}`, commit_oid: id, status: 'ready' } : undefined })
  const summary = normalizeDesktopV3NativeArtifactSummary({ id: 'artifact', title: 'Authored title', turns: [
    turn('old', 1, 'awaiting_selection', [candidate('old')]),
    turn('latest', 3, 'awaiting_selection', [candidate('failed', 'failed'), candidate('first'), candidate('second')]),
    turn('building', 4, 'building', [candidate('building', 'building')]),
    turn('selected', 5, 'selected', [candidate('unused-sibling')]),
    turn('cancelled', 6, 'cancelled', [candidate('cancelled')]),
  ] }, 'session')!
  assert.equal(summary.label, 'Authored title')
  assert.equal(summary.turnCount, 5)
  assert.deepEqual(summary.pendingTurns?.map((turn) => turn.turnId), ['building', 'latest', 'old'])
  const selected = firstDesktopV3NativeArtifactPendingCandidate(summary.pendingTurns!)!
  assert.equal(selected.turn.turnId, 'latest')
  assert.equal(selected.candidate.candidateId, 'first')
  assert.equal(firstDesktopV3NativeArtifactPendingCandidate([summary.pendingTurns![0]!]), null)
  const contradictory = { ...selected.turn, candidates: selected.turn.candidates.map((candidate) => ({ ...candidate, selected: true })) } as DesktopV3NativeArtifactTurn
  assert.deepEqual(pendingDesktopV3NativeArtifactTurns([contradictory]), [])
  assert.equal(normalizeDesktopV3NativeArtifactSummary({ id: 'untitled' }, 'session')?.label, 'Untitled artifact')
})
