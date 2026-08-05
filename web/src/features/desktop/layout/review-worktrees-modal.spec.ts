import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { buildReviewWorktreeFixPrompt, CollapsibleReviewSection, currentCheckoutCommitCandidate, resolveReviewWorktreeRepairAgent, reviewCommitCandidates, reviewWorktreeIntegrationFailureDisplay, reviewWorktreeReasonLabel, selectableReviewIDs, selectedArchiveCandidates, shouldShowReviewCommitAction } from './review-worktrees-modal'
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
  it('keeps completed sections collapsed until their headers are expanded', () => {
    const collapsed = renderToStaticMarkup(createElement(CollapsibleReviewSection, {
      title: 'Done',
      icon: createElement('span'),
      count: 1,
      expanded: false,
      onExpandedChange: vi.fn(),
      children: createElement('span', null, 'Completed session'),
    }))
    expect(collapsed).toContain('aria-expanded="false"')
    expect(collapsed).not.toContain('Completed session')

    const expanded = renderToStaticMarkup(createElement(CollapsibleReviewSection, {
      title: 'Archived',
      icon: createElement('span'),
      count: 1,
      expanded: true,
      onExpandedChange: vi.fn(),
      children: createElement('span', null, 'Archived session'),
    }))
    expect(expanded).toContain('aria-expanded="true"')
    expect(expanded).toContain('Archived session')
  })

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

  it('turns the original integration button into an anchored popout with integrate-and-archive stacked above it', async () => {
    const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./review-worktrees-modal.tsx', import.meta.url), 'utf8'))
    const commitPanelStart = source.indexOf('{commitCandidate ? (')
    const commitPanelEnd = source.indexOf('{reviewingSelection', commitPanelStart)
    const commitPanel = source.slice(commitPanelStart, commitPanelEnd)
    const integrationActionStart = source.indexOf('const renderIntegrationAction')
    const integrationActionEnd = source.indexOf('const archiveSelected', integrationActionStart)
    const integrationAction = source.slice(integrationActionStart, integrationActionEnd)

    expect(commitPanel).toContain('Archive session after integration')
    expect(commitPanel).toContain('disabled={committing || !commitIntegrateAfter}')
    expect(commitPanel).toContain('commitReviewChanges(!commitCandidate.current_checkout && commitIntegrateAfter, commitArchiveAfter)')
    expect(integrationAction).toContain('data-integration-popout-anchor')
    expect(integrationAction).toContain('createPortal(popout, document.body)')
    expect(integrationAction).toContain('className="fixed z-[90]')
    expect(source).toContain('window.innerWidth - width - viewportPadding')
    expect(source).toContain('window.innerHeight - anchorRect.bottom')
    expect(source).toContain("window.addEventListener('scroll', positionIntegrationPopout, true)")
    expect(integrationAction).toContain('aria-label="Integration options popout"')
    expect(integrationAction).toContain('Integrate and Archive')
    expect(integrationAction).toContain('Confirm integration?')
    expect(integrationAction.indexOf('Integrate and Archive')).toBeLessThan(integrationAction.indexOf('Confirm integration?'))
    expect(integrationAction).toContain('integrateWorktree(true)')
    expect(integrationAction).toContain("integrationFailure ? 'Try integration again'")
    expect(integrationAction).toContain("integrationSucceeded ? 'Try archive again'")
    expect(integrationAction).toContain("showIntegrationError ? 'Hide Error' : 'Show Error'")
    expect(integrationAction).toContain('aria-label="Close integration options"')
    expect(source).toContain("document.addEventListener('pointerdown', dismissOnOutsidePointer)")
    expect(source).toContain("event.key === 'Escape'")
    expect(integrationAction).toContain('Ask Swarm for Help')
    expect(integrationAction).toContain('void onAskSwarmFix(integrationFailure)')
    expect(source).not.toContain('aria-label="In-place integration confirmation"')
    expect(source).not.toContain('role="alertdialog"')
    expect(integrationAction).not.toContain('<Dialog')
    expect(source).toContain('archiveSessionIds: [candidate.session_id]')
    expect(source).toContain('archiveSessionIds: [integrateCandidate.session_id]')
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
    expect(prompt).toContain('CONFLICT in app.tsx')
  })

  it('tells Swarm to recover either side of a combined commit-and-integration failure', () => {
    const item = candidate({ session_id: 'failed-session', title: 'Failed commit', worktree_branch: 'agent/fix', target_branch: 'dev', worktree_path: '/worktrees/fix' })
    const failure = { candidate: item, error: 'cherry-pick conflict in app.tsx', operation: 'commit_and_integrate' as const }
    const prompt = buildReviewWorktreeFixPrompt(failure, '/workspace')
    expect(prompt).toContain('source commit may or may not have been created')
    expect(prompt).toContain('create the intended commit if it is still missing')
    expect(prompt).toContain('do not duplicate a commit that already succeeded')
    expect(prompt).toContain('cherry-pick conflict in app.tsx')
    expect(reviewWorktreeIntegrationFailureDisplay(failure.error, false, failure.operation).summary).toContain('Swarm was given the error')
  })
})
