import { describe, expect, it } from 'vitest'
import { buildReviewWorktreeFixPrompt, currentCheckoutCommitCandidate, resolveReviewWorktreeRepairAgent, reviewCommitCandidates, reviewWorktreeIntegrationFailureDisplay, reviewWorktreeReasonLabel, selectableReviewIDs, selectedArchiveCandidates, shouldShowReviewCommitAction } from './review-worktrees-modal'
import type { ReviewWorktreeCandidate, ReviewWorktreesResponse } from '../session-v3/review-worktrees-api'
import type { AgentProfileRecord, AgentStateRecord } from '../chat/types/chat'

function candidate(overrides: Partial<ReviewWorktreeCandidate>): ReviewWorktreeCandidate {
  return { session_id: 'session-1', title: 'Session', updated_at: 1, classification: 'retained', reason: 'uncommitted_work', archive_ready: false, ...overrides }
}

function primaryAgent(name: string, overrides: Partial<AgentProfileRecord> = {}): AgentProfileRecord {
  return {
    name,
    mode: 'primary',
    description: '',
    provider: 'codex',
    model: 'gpt-5.4',
    thinking: 'high',
    modelMode: 'single',
    planProvider: '', planModel: '', planThinking: '', planServiceTier: '',
    autoProvider: '', autoModel: '', autoThinking: '', autoServiceTier: '',
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null, toolContract: null,
    enabled: true, protected: false, updatedAt: 0,
    ...overrides,
  }
}

function agentState(profiles: AgentProfileRecord[], activePrimary: string): AgentStateRecord {
  return { profiles, activePrimary, activeSubagent: {}, version: 1, providerDefaultsPreview: null, toolInventory: null }
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

  it('shows the AI review commit action only for uncommitted managed worktrees', () => {
    const cleanResult: ReviewWorktreesResponse = { ok: true, target_detection: '', comparison: '', retained: [candidate({ session_id: 'clean', reason: 'commits_missing_from_target', missing_commit_count: 1, worktree_path: '/worktrees/clean' })], done: [], archived_session_ids: [], recently_archived: [], grace_period_ms: 3_600_000, checkout_dirty: false, checkout_dirty_count: 0, blocked_by_checkout_count: 0, complete: true }
    expect(reviewCommitCandidates(cleanResult)).toEqual([])
    expect(shouldShowReviewCommitAction(cleanResult)).toBe(false)

    const dirty = candidate({ session_id: 'dirty', dirty_count: 2, commit_eligible: true, worktree_path: '/worktrees/dirty' })
    const dirtyResult = { ...cleanResult, retained: [dirty] }
    expect(reviewCommitCandidates(dirtyResult)).toEqual([dirty])
    expect(shouldShowReviewCommitAction(dirtyResult)).toBe(true)
  })

  it('defaults repair to the enabled Swarm primary even when its model mode is single and runtime is readwrite', () => {
    expect(resolveReviewWorktreeRepairAgent(agentState([
      primaryAgent('legacy', { runtimeMode: 'plan_auto', exitPlanModeEnabled: true }),
      primaryAgent('swarm'),
    ], 'legacy'))).toBe('swarm')
  })

  it('does not expose repair when no enabled primary agent exists', () => {
    expect(resolveReviewWorktreeRepairAgent(agentState([
      primaryAgent('swarm', { enabled: false }),
    ], 'swarm'))).toBe('')
  })

  it('keeps integration details hidden until requested', () => {
    const error = 'CONFLICT (content): Merge conflict in web/src/app.tsx\nlong diagnostics'
    expect(reviewWorktreeIntegrationFailureDisplay(error, false)).toEqual({
      summary: 'The worktree could not be integrated. The target branch was left unchanged.',
      fullError: undefined,
    })
    expect(reviewWorktreeIntegrationFailureDisplay(error, true).fullError).toBe(error)
  })

  it('builds a single-candidate auto repair prompt with the complete error', () => {
    const item = candidate({ session_id: 'failed-session', title: 'Failed integration', worktree_branch: 'agent/fix', target_branch: 'dev', worktree_path: '/worktrees/fix' })
    const prompt = buildReviewWorktreeFixPrompt({ candidate: item, error: 'CONFLICT in app.tsx' }, '/workspace')
    expect(prompt).toContain('resolve the cherry-pick/integration conflict safely')
    expect(prompt).toContain('Session: Failed integration (failed-session)')
    expect(prompt).toContain('Source branch: agent/fix')
    expect(prompt).toContain('Target branch: dev')
    expect(prompt).toContain('Workspace: /workspace')
    expect(prompt).toContain('Worktree: /worktrees/fix')
    expect(prompt).toContain('CONFLICT in app.tsx')
  })

  it('tells Swarm to recover either side of a combined commit-and-integration failure', () => {
    const item = candidate({ session_id: 'failed-session', title: 'Failed commit', worktree_branch: 'agent/fix', target_branch: 'dev', worktree_path: '/worktrees/fix' })
    const failure = { candidate: item, error: 'cherry-pick conflict in app.tsx', operation: 'commit_and_integrate' as const }
    const prompt = buildReviewWorktreeFixPrompt(failure, '/workspace')
    expect(prompt).toContain('source commit may or may not have been created')
    expect(prompt).toContain('create the intended commit if it is still missing')
    expect(prompt).toContain('do not duplicate a commit that already succeeded')
    expect(prompt).toContain('Workspace: /workspace')
    expect(prompt).toContain('Worktree: /worktrees/fix')
    expect(prompt).toContain('cherry-pick conflict in app.tsx')
    expect(reviewWorktreeIntegrationFailureDisplay(failure.error, false, failure.operation).summary).toContain('Swarm was given the error')
  })
})
