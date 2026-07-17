import { describe, expect, it } from 'vitest'
import { currentCheckoutCommitCandidate, reviewWorktreeReasonLabel, selectableReviewIDs, selectedArchiveCandidates } from './review-worktrees-modal'
import type { ReviewWorktreeCandidate, ReviewWorktreesResponse } from '../session-v3/review-worktrees-api'

function candidate(overrides: Partial<ReviewWorktreeCandidate>): ReviewWorktreeCandidate {
  return { session_id: 'session-1', title: 'Session', updated_at: 1, classification: 'retained', reason: 'uncommitted_work', archive_ready: false, ...overrides }
}

describe('review worktrees modal helpers', () => {
  it('explains why dirty and unintegrated sessions are retained', () => {
    expect(reviewWorktreeReasonLabel(candidate({ dirty_count: 2 }))).toBe('2 uncommitted changes')
    expect(reviewWorktreeReasonLabel(candidate({ reason: 'current_checkout_uncommitted_work', dirty_count: 2, current_checkout: true, commit_eligible: true }))).toBe('2 uncommitted changes in the current checkout')
    expect(reviewWorktreeReasonLabel(candidate({ reason: 'commits_missing_from_target', missing_commit_count: 1, target_branch: 'dev', integrate_eligible: true }))).toBe('1 commit not in dev')
  })

  it('only exposes keep-in-review candidates for archive selection', () => {
    const result: ReviewWorktreesResponse = { ok: true, target_detection: '', comparison: '', retained: [candidate({ session_id: 'keep', worktree_branch: 'agent/keep', worktree_path: '/worktrees/keep' })], done: [candidate({ session_id: 'done', classification: 'done', reason: 'clean_and_integrated', worktree_branch: 'agent/done', worktree_path: '/worktrees/done', done_at: 10, archive_after: 3_600_010 })], archived_session_ids: [], recently_archived: [], grace_period_ms: 3_600_000, checkout_dirty: true, checkout_dirty_count: 2, blocked_by_checkout_count: 1, complete: true }
    expect(selectableReviewIDs(result)).toEqual(['keep'])
    expect(selectedArchiveCandidates(result, new Set(['keep', 'done']))).toEqual([expect.objectContaining({ session_id: 'keep', worktree_branch: 'agent/keep', worktree_path: '/worktrees/keep' })])
  })

  it('selects an eligible current-checkout session for the prominent commit action', () => {
    const checkout = candidate({ session_id: 'checkout', reason: 'current_checkout_uncommitted_work', current_checkout: true, commit_eligible: true, worktree_path: '/workspace' })
    const result: ReviewWorktreesResponse = { ok: true, target_detection: '', comparison: '', retained: [candidate({ session_id: 'managed', commit_eligible: true, worktree_path: '/worktrees/managed' }), checkout], done: [], archived_session_ids: [], recently_archived: [], grace_period_ms: 3_600_000, checkout_dirty: true, checkout_dirty_count: 2, blocked_by_checkout_count: 1, complete: true }
    expect(currentCheckoutCommitCandidate(result)).toEqual(checkout)
  })
})
