import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { buildReviewWorktreeFixPrompt, CollapsibleReviewSection, currentCheckoutCommitCandidate, reviewCommitCandidates, reviewWorktreeIntegrationFailureDisplay, reviewWorktreeReasonLabel, selectableReviewIDs, selectedArchiveCandidates, shouldShowReviewCommitAction } from './review-worktrees-modal'
import type { ReviewWorktreeCandidate, ReviewWorktreesResponse } from '../session-v3/review-worktrees-api'

function candidate(overrides: Partial<ReviewWorktreeCandidate>): ReviewWorktreeCandidate {
  return { session_id: 'session-1', title: 'Session', updated_at: 1, classification: 'retained', reason: 'uncommitted_work', archive_ready: false, ...overrides }
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

  it('routes Review Worktrees failures through the compiled Swarm workspace repair launcher', async () => {
    const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8'))
    const handlerStart = source.indexOf('const handleAskSwarmToFixReviewIntegration')
    const handlerEnd = source.indexOf('useEffect(() => {', handlerStart)
    const handler = source.slice(handlerStart, handlerEnd)

    expect(source).toContain("const DESKTOP_REPAIR_AGENT_NAME = 'swarm'")
    expect(source).toMatch(/const launchDesktopRepairSession = useCallback[\s\S]*agentName: DESKTOP_REPAIR_AGENT_NAME[\s\S]*worktree: \{ mode: 'off' \}/)
    expect(source).toMatch(/sourceBindingId = sessionWorkspaceBindingId\(sourceSession\?\.metadata\)[\s\S]*workspacePathByBindingId\.get\(sourceBindingId\)[\s\S]*swarm_v3_runtime_swarm_id/)
    expect(handler).toContain('launchDesktopRepairSession({')
    expect(handler).toContain('owningWorkspacePath: topWorkspacePath')
    expect(handler).toContain('sourceSessionId: failure.candidate.session_id')
    expect(handler).toContain("source: 'desktop-v3-review-worktrees-recovery'")
    expect(handler).toContain('integration_error: failure.error')
    expect(handler).not.toContain('resolveReviewWorktreeRepairAgent')
  })

  it('opens a clear anchored confirmation with optional archive-after-integration', async () => {
    const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./review-worktrees-modal.tsx', import.meta.url), 'utf8'))
    const confirmation = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./integration-confirmation.tsx', import.meta.url), 'utf8'))
    const commitPanelStart = source.indexOf('{commitCandidate ? (')
    const commitPanelEnd = source.indexOf('{reviewingSelection', commitPanelStart)
    const commitPanel = source.slice(commitPanelStart, commitPanelEnd)
    const integrationActionStart = source.indexOf('const renderIntegrationAction')
    const integrationActionEnd = source.indexOf('const archiveSelected', integrationActionStart)
    const integrationAction = source.slice(integrationActionStart, integrationActionEnd)

    expect(commitPanel).toContain('Archive session after integration')
    expect(commitPanel).toContain('disabled={committing || !commitIntegrateAfter}')
    expect(commitPanel).toContain('commitReviewChanges(!commitCandidate.current_checkout && commitIntegrateAfter, commitArchiveAfter)')
    expect(source).toContain('promoteSessionIds: [candidate.session_id]')
    expect(source).toContain('sourceHeadBySessionId: { [candidate.session_id]: sourceHead }')
    expect(source).toContain('targetBranch')
    expect(source).toContain('targetHead')
    expect(source).not.toContain('integrateSessionIds:')
    expect(integrationAction).toContain('data-integration-popout-anchor')
    expect(integrationAction).toContain('createPortal(popout, document.body)')
    expect(integrationAction).toContain('className="fixed z-[90]')
    expect(source).toContain('window.innerWidth - width - viewportPadding')
    expect(source).toContain('window.innerHeight - anchorRect.bottom')
    expect(source).toContain("window.addEventListener('scroll', positionIntegrationPopout, true)")
    expect(integrationAction).toContain('aria-label="Confirm worktree integration"')
    expect(integrationAction).toContain('<IntegrationConfirmation')
    expect(integrationAction).toContain('archiveAfter={integrationArchiveAfter}')
    expect(integrationAction).toContain('onArchiveAfterChange={setIntegrationArchiveAfter}')
    expect(integrationAction).toContain('onConfirm={() => void integrateWorktree()}')
    expect(integrationAction).toContain("`Confirm integration into ${item.target_branch || 'target'}`")
    expect(confirmation).toContain('Confirm integration into ${target}')
    expect(confirmation).toContain('Archive session after integration')
    expect(confirmation).toContain('Optional. The session is archived only if integration succeeds.')
    expect(confirmation).toContain('onClick={onConfirm}')
    expect(confirmation).toContain('onClick={onCancel}')
    expect(integrationAction).toContain("showIntegrationError ? 'Hide Error' : 'Show Error'")
    expect(integrationAction).toContain('aria-label="Close integration options"')
    expect(source).toContain("document.addEventListener('pointerdown', dismissOnOutsidePointer)")
    expect(source).toContain("event.key === 'Escape'")
    expect(integrationAction).toContain('Ask Swarm for Help')
    expect(integrationAction).toContain('void onAskSwarmFix(integrationFailure)')
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
