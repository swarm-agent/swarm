import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Archive, CheckCircle2, GitBranch, GitCommitHorizontal, GitMerge, LoaderCircle, RefreshCcw, ShieldAlert, X } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { cn } from '../../../lib/cn'
import { commitWorkspaceChanges, fetchGitStatus } from '../git/api'
import type { GitFileStatus } from '../git/types'
import { reviewDesktopV3Worktrees, unarchiveDesktopV3ReviewSessions, type ReviewWorktreeCandidate, type ReviewWorktreesResponse } from '../session-v3/review-worktrees-api'

export const NEEDS_REVIEW_AUTO_CLEANUP_KEY = 'swarm.desktop.needs-review.auto-cleanup'
export const NEEDS_REVIEW_AUTO_CLEANUP_EVENT = 'swarm:needs-review-auto-cleanup-changed'

export function reviewWorktreeReasonLabel(item: ReviewWorktreeCandidate): string {
  switch (item.reason) {
    case 'uncommitted_work': return `${item.dirty_count ?? 0} uncommitted change${item.dirty_count === 1 ? '' : 's'}`
    case 'current_checkout_uncommitted_work': return `${item.dirty_count ?? 0} uncommitted change${item.dirty_count === 1 ? '' : 's'} in the current checkout`
    case 'current_checkout_clean': return 'Current checkout is clean'
    case 'commits_missing_from_target': return `${item.missing_commit_count ?? 0} commit${item.missing_commit_count === 1 ? '' : 's'} not in ${item.target_branch || 'target'}`
    case 'clean_and_integrated': return `Integrated into ${item.target_branch || 'target'}`
    case 'managed_worktree_metadata_missing': return 'Managed worktree metadata is incomplete'
    case 'target_branch_unavailable': return `Could not inspect ${item.target_branch || 'target branch'}`
    case 'done_timestamp_missing': return 'Done timing is unavailable; recheck before automatic archive'
    default: return 'Worktree unavailable'
  }
}

export function selectableDoneIDs(result: ReviewWorktreesResponse | null): string[] {
  return result?.done.map((item) => item.session_id) ?? []
}

export function selectedArchiveCandidates(result: ReviewWorktreesResponse | null, selected: Set<string>): ReviewWorktreeCandidate[] {
  return result?.done.filter((item) => selected.has(item.session_id)) ?? []
}

export function currentCheckoutCommitCandidate(result: ReviewWorktreesResponse | null): ReviewWorktreeCandidate | null {
  return result?.retained.find((item) => item.current_checkout && item.commit_eligible && Boolean(item.worktree_path)) ?? null
}

function gitFileStatusLabel(file: GitFileStatus): string {
  if (file.conflict) return 'conflict'
  if (file.untracked) return 'new'
  if (file.staged && file.modified) return 'staged + modified'
  if (file.staged) return 'staged'
  if (file.modified) return 'modified'
  return file.xy?.trim() || file.kind
}

export function NeedsReviewCleanupPanel({ workspacePath, onClose }: { workspacePath?: string; onClose: () => void }) {
  const [result, setResult] = useState<ReviewWorktreesResponse | null>(null)
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [reviewingSelection, setReviewingSelection] = useState(false)
  const [loading, setLoading] = useState(false)
  const [archiving, setArchiving] = useState(false)
  const [commitCandidate, setCommitCandidate] = useState<ReviewWorktreeCandidate | null>(null)
  const [commitFiles, setCommitFiles] = useState<GitFileStatus[]>([])
  const [commitMessage, setCommitMessage] = useState('')
  const [openingCommit, setOpeningCommit] = useState(false)
  const [committing, setCommitting] = useState(false)
  const [batchCommitting, setBatchCommitting] = useState(false)
  const [activeCommitBatchID, setActiveCommitBatchID] = useState('')
  const [releasedAfterCommit, setReleasedAfterCommit] = useState<ReviewWorktreeCandidate[]>([])
  const [integrateCandidate, setIntegrateCandidate] = useState<ReviewWorktreeCandidate | null>(null)
  const [integrating, setIntegrating] = useState(false)
  const [archiveAllReview, setArchiveAllReview] = useState(false)
  const [error, setError] = useState('')
  const [autoCleanup, setAutoCleanup] = useState(() => window.localStorage.getItem(NEEDS_REVIEW_AUTO_CLEANUP_KEY) === '1')
  const refresh = useCallback(async (automatic = false): Promise<ReviewWorktreesResponse | null> => {
    setLoading(true)
    setError('')
    try {
      const next = await reviewDesktopV3Worktrees({ workspacePath, automatic, graceHours: 1 })
      setResult(next)
      if (next.commit_batch_id) setActiveCommitBatchID(next.commit_batch_id)
      setSelected((current) => new Set([...current].filter((id) => next.done.some((item) => item.session_id === id))))
      setReviewingSelection(false)
      setReleasedAfterCommit((current) => current.map((item) => next.done.find((candidate) => candidate.session_id === item.session_id)).filter((item): item is ReviewWorktreeCandidate => Boolean(item)))
      return next
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not inspect review worktrees.')
      return null
    } finally {
      setLoading(false)
    }
  }, [workspacePath])
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    void reviewDesktopV3Worktrees({ workspacePath, graceHours: 1 })
      .then((next) => {
        if (cancelled) return
        setResult(next)
        setSelected((current) => new Set([...current].filter((id) => next.done.some((item) => item.session_id === id))))
      })
      .catch((cause) => { if (!cancelled) setError(cause instanceof Error ? cause.message : 'Could not inspect review worktrees.') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [workspacePath])
  useEffect(() => {
    window.localStorage.setItem(NEEDS_REVIEW_AUTO_CLEANUP_KEY, autoCleanup ? '1' : '0')
    window.dispatchEvent(new CustomEvent(NEEDS_REVIEW_AUTO_CLEANUP_EVENT, { detail: autoCleanup }))
  }, [autoCleanup])
  const doneIDs = useMemo(() => selectableDoneIDs(result), [result])
  const archiveCandidates = useMemo(() => selectedArchiveCandidates(result, selected), [result, selected])
  const checkoutCommitCandidate = useMemo(() => currentCheckoutCommitCandidate(result), [result])
  const reviewCommitCandidates = useMemo(() => result?.retained.filter((item) => item.commit_eligible && !item.current_checkout && Boolean(item.worktree_path)) ?? [], [result])
  const activeReviewCommitJobs = useMemo(() => result?.retained.filter((item) => item.commit_job?.status === 'pending' || item.commit_job?.status === 'running') ?? [], [result])
  const completedReviewCommitJobs = useMemo(() => [...(result?.retained ?? []), ...(result?.done ?? [])].filter((item) => item.commit_job?.status === 'completed') ?? [], [result])
  const failedReviewCommitJobs = useMemo(() => result?.retained.filter((item) => item.commit_job?.status === 'failed') ?? [], [result])
  const toggleSelected = (id: string) => {
    setReviewingSelection(false)
    setSelected((current) => { const next = new Set(current); next.has(id) ? next.delete(id) : next.add(id); return next })
  }
  const restoreSession = async (sessionId: string, updatedAt: number) => {
    setArchiving(true)
    setError('')
    try {
      await unarchiveDesktopV3ReviewSessions({ [sessionId]: updatedAt })
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not restore archived session.')
    } finally {
      setArchiving(false)
    }
  }
  const openCommitReview = async (candidate: ReviewWorktreeCandidate) => {
    if (!candidate.commit_eligible || !candidate.worktree_path || openingCommit) return
    setOpeningCommit(true)
    setError('')
    try {
      const response = await fetchGitStatus(candidate.worktree_path, 0, candidate.session_id)
      if (!response.status.has_git || response.status.files.length === 0) throw new Error('No committable changes were found. Recheck worktrees and try again.')
      setCommitFiles(response.status.files)
      setCommitMessage('')
      setCommitCandidate(candidate)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load the changed-file list.')
    } finally {
      setOpeningCommit(false)
    }
  }
  const commitCurrentCheckout = async () => {
    if (!commitCandidate?.commit_eligible || !commitCandidate.worktree_path || !commitMessage.trim()) return
    const waitingIDs = commitCandidate.current_checkout
      ? new Set(result?.retained.filter((item) => item.current_checkout && item.reason === 'current_checkout_uncommitted_work').map((item) => item.session_id) ?? [])
      : new Set<string>()
    setCommitting(true)
    setError('')
    try {
      await commitWorkspaceChanges({ workspacePath: commitCandidate.worktree_path, sessionId: commitCandidate.session_id, message: commitMessage, all: true })
      setCommitCandidate(null)
      setCommitFiles([])
      setCommitMessage('')
      const next = await refresh(false)
      if (next && waitingIDs.size > 0) setReleasedAfterCommit(next.done.filter((item) => waitingIDs.has(item.session_id)))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not commit the current checkout.')
    } finally {
      setCommitting(false)
    }
  }
  useEffect(() => {
    if (!activeCommitBatchID && activeReviewCommitJobs.length === 0) return
    const timer = window.setInterval(() => { void refresh(false) }, 1500)
    return () => window.clearInterval(timer)
  }, [activeCommitBatchID, activeReviewCommitJobs.length, refresh])
  useEffect(() => {
    if (!activeCommitBatchID || !result) return
    const batchItems = [...result.retained, ...result.done].filter((item) => item.commit_job?.batch_id === activeCommitBatchID)
    if (batchItems.length > 0 && batchItems.every((item) => item.commit_job?.status === 'completed' || item.commit_job?.status === 'failed')) setActiveCommitBatchID('')
  }, [activeCommitBatchID, result])
  const commitReviewWorktrees = async () => {
    if (reviewCommitCandidates.length === 0 || batchCommitting) return
    setBatchCommitting(true)
    setError('')
    try {
      const next = await reviewDesktopV3Worktrees({ workspacePath, commitSessionIds: reviewCommitCandidates.map((item) => item.session_id), graceHours: 1 })
      setResult(next)
      if (next.commit_batch_id) setActiveCommitBatchID(next.commit_batch_id)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not start review commits.')
    } finally {
      setBatchCommitting(false)
    }
  }
  const archiveReleased = async () => {
    const ids = releasedAfterCommit.map((item) => item.session_id)
    if (ids.length === 0 || archiving) return
    setArchiving(true)
    setError('')
    try {
      await reviewDesktopV3Worktrees({ workspacePath, archiveSessionIds: ids, graceHours: 1 })
      setReleasedAfterCommit([])
      setSelected(new Set())
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not archive the chats released by this commit.')
    } finally {
      setArchiving(false)
    }
  }
  const integrateWorktree = async () => {
    if (!integrateCandidate?.integrate_eligible) return
    setIntegrating(true)
    setError('')
    try {
      await reviewDesktopV3Worktrees({ workspacePath, integrateSessionIds: [integrateCandidate.session_id], graceHours: 1 })
      setIntegrateCandidate(null)
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not integrate the worktree.')
    } finally {
      setIntegrating(false)
    }
  }
  const archiveAll = async () => {
    if (!archiveAllReview || (result?.done.length ?? 0) === 0) return
    setArchiving(true)
    setError('')
    try {
      await reviewDesktopV3Worktrees({ workspacePath, archiveAll: true, graceHours: 1 })
      setArchiveAllReview(false)
      setSelected(new Set())
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not archive all eligible sessions.')
    } finally {
      setArchiving(false)
    }
  }
  const archiveSelected = async () => {
    if (archiveCandidates.length === 0 || !reviewingSelection) return
    setArchiving(true)
    setError('')
    try {
      await reviewDesktopV3Worktrees({ workspacePath, archiveSessionIds: archiveCandidates.map((item) => item.session_id), graceHours: 1 })
      setSelected(new Set())
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not archive selected sessions.')
    } finally {
      setArchiving(false)
    }
  }
  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Needs Review cleanup" className="z-[80]">
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="w-[min(1120px,calc(100vw-32px))] p-0 shadow-2xl">
        <header className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4">
          <div><h2 className="text-base font-semibold text-[var(--app-text)]">Review worktrees</h2><p className="mt-0.5 text-xs text-[var(--app-text-subtle)]">Compare each session worktree with {result?.current_target_branch ? <strong>{result.current_target_branch}</strong> : 'its managed target branch'} before archiving.</p></div>
          <button type="button" className="rounded-md p-1 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)]" aria-label="Close review cleanup" onClick={onClose}><X size={16} /></button>
        </header>
        <div className="flex flex-wrap items-center gap-2 border-b border-[var(--app-border)] px-5 py-3 text-xs">
          <button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1.5" disabled={loading} onClick={() => void refresh(false)}>{loading ? <LoaderCircle size={13} className="animate-spin" /> : <RefreshCcw size={13} />} Recheck worktrees</button>
          <label className="ml-auto inline-flex items-center gap-2"><input type="checkbox" checked={autoCleanup} onChange={(event) => setAutoCleanup(event.target.checked)} />Auto-archive after 1h grace</label>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-5">
          {error ? <p className="mt-3 rounded-md bg-[var(--app-danger-bg)] p-2.5 text-xs text-[var(--app-danger)]">{error}</p> : null}
          {result?.checkout_dirty ? <section className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-[var(--app-warning)] bg-[var(--app-surface-subtle)] p-4" aria-label="Dirty main checkout summary"><div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-[var(--app-text)]">{result.current_target_branch || 'Current branch'} has {result.checkout_dirty_count} uncommitted change{result.checkout_dirty_count === 1 ? '' : 's'}</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">{result.blocked_by_checkout_count} chat{result.blocked_by_checkout_count === 1 ? ' is' : 's are'} waiting for these commits before they can get archived. Commit, then Swarm will recheck and show the exact chats released into Done.</p></div>{checkoutCommitCandidate ? <button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-2 text-xs font-medium text-[var(--app-primary-text)] disabled:opacity-50" disabled={openingCommit} onClick={() => void openCommitReview(checkoutCommitCandidate)}>{openingCommit ? <LoaderCircle size={13} className="animate-spin" /> : <GitCommitHorizontal size={13} />}Commit {result.current_target_branch || 'branch'}…</button> : null}</section> : null}
          {!result?.checkout_dirty && releasedAfterCommit.length > 0 ? <section className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-[var(--app-success)] bg-[var(--app-surface-subtle)] p-4" aria-label="Released chats ready to archive"><div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-[var(--app-text)]">{releasedAfterCommit.length} chat{releasedAfterCommit.length === 1 ? '' : 's'} released into Done</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">The commit succeeded and these exact sessions passed the backend safety recheck. Archive them now or leave them in Done for the one-hour grace period.</p></div><button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-2 text-xs font-medium text-[var(--app-primary-text)] disabled:opacity-50" disabled={archiving} onClick={() => void archiveReleased()}>{archiving ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}Archive {releasedAfterCommit.length}</button></section> : null}
          {reviewCommitCandidates.length > 0 || activeReviewCommitJobs.length > 0 || completedReviewCommitJobs.length > 0 || failedReviewCommitJobs.length > 0 ? <section className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-[var(--app-primary)] bg-[var(--app-surface-subtle)] p-4" aria-label="AI review commit batch"><div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-[var(--app-text)]">Prepare review commits with AI</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">The auto model inspects each isolated worktree with only read and dedicated Git tools, chooses a message, and creates one commit per worktree in parallel. Sessions stay in review.</p><p className="mt-2 text-xs text-[var(--app-text-muted)]">{activeReviewCommitJobs.length > 0 ? `${activeReviewCommitJobs.length} pending or running` : completedReviewCommitJobs.length > 0 ? `${completedReviewCommitJobs.length} committed and ready to review` : `${reviewCommitCandidates.length} ready to commit`}{failedReviewCommitJobs.length > 0 ? ` · ${failedReviewCommitJobs.length} failed` : ''}</p>{failedReviewCommitJobs.map((item) => <p key={item.session_id} className="mt-1 truncate text-xs text-[var(--app-danger)]" title={item.commit_job?.error}>{item.title || item.session_id}: {item.commit_job?.error}</p>)}</div><button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-2 text-xs font-medium text-[var(--app-primary-text)] disabled:opacity-50" disabled={batchCommitting || activeReviewCommitJobs.length > 0 || reviewCommitCandidates.length === 0} onClick={() => void commitReviewWorktrees()}>{batchCommitting || activeReviewCommitJobs.length > 0 ? <LoaderCircle size={13} className="animate-spin" /> : <GitCommitHorizontal size={13} />}{activeReviewCommitJobs.length > 0 ? 'Committing…' : `Commit ${reviewCommitCandidates.length} with AI`}</button></section> : null}
          <CandidatePile title="Keep in review" icon={<ShieldAlert size={14} />} items={result?.retained ?? []} selected={selected} onToggle={() => undefined} selectable={false} onCommit={(item) => void openCommitReview(item)} onIntegrate={setIntegrateCandidate} />
          <CandidatePile title="Done · archives after 1h" icon={<CheckCircle2 size={14} />} items={result?.done ?? []} selected={selected} onToggle={toggleSelected} selectable />
          {commitCandidate ? (
            <section className="mt-5 rounded-xl border border-[var(--app-warning)] bg-[var(--app-surface-subtle)] p-4" aria-label="Commit current checkout review">
              <h3 className="text-sm font-semibold text-[var(--app-text)]">Commit current checkout before archiving</h3>
              <p className="mt-1 text-xs text-[var(--app-text-subtle)]">This stages all {commitFiles.length} shown changes and creates one commit in <strong>{commitCandidate.worktree_branch || 'the current branch'}</strong>. It does not archive anything until the commit succeeds and Swarm rechecks the waiting sessions.</p>
              <div className="mt-3 max-h-48 overflow-y-auto rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] font-mono text-xs" aria-label="Changed files">{commitFiles.map((file) => <div key={`${file.kind}:${file.path}`} className="flex min-w-0 gap-2 border-b border-[var(--app-border)] px-2.5 py-1.5 last:border-0"><span className="shrink-0 text-[var(--app-text-subtle)]">{gitFileStatusLabel(file)}</span><span className="truncate" title={file.path}>{file.path}</span></div>)}</div>
              <label className="mt-3 block text-xs font-medium text-[var(--app-text)]">Commit message<input className="mt-1 w-full rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-2 font-normal" value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} placeholder="Describe these changes" autoFocus /></label>
              <div className="mt-3 flex justify-end gap-2"><button type="button" className="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs" disabled={committing} onClick={() => { setCommitCandidate(null); setCommitFiles([]); setCommitMessage('') }}>Cancel</button><button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-1.5 text-xs text-[var(--app-primary-text)] disabled:opacity-50" disabled={committing || commitFiles.length === 0 || !commitMessage.trim()} onClick={() => void commitCurrentCheckout()}>{committing ? <LoaderCircle size={13} className="animate-spin" /> : <GitCommitHorizontal size={13} />}Commit changes</button></div>
            </section>
          ) : null}
          {integrateCandidate ? <section className="mt-5 rounded-xl border border-[var(--app-primary)] bg-[var(--app-surface-subtle)] p-4" aria-label="Integrate worktree review"><h3 className="text-sm font-semibold text-[var(--app-text)]">Integrate {integrateCandidate.worktree_branch} into {integrateCandidate.target_branch}</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">Swarm preflights the full commit stack and leaves the target unchanged on conflict. Success moves this chat into Done for one hour; it is not archived immediately.</p><div className="mt-3 flex justify-end gap-2"><button type="button" className="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs" disabled={integrating} onClick={() => setIntegrateCandidate(null)}>Cancel</button><button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-1.5 text-xs text-[var(--app-primary-text)]" disabled={integrating} onClick={() => void integrateWorktree()}>{integrating ? <LoaderCircle size={13} className="animate-spin" /> : <GitMerge size={13} />}Confirm integration</button></div></section> : null}
          {archiveAllReview && (result?.done.length ?? 0) > 0 ? <section className="mt-5 rounded-xl border border-[var(--app-warning)] bg-[var(--app-surface-subtle)] p-4" aria-label="Archive all review"><h3 className="text-sm font-semibold text-[var(--app-text)]">Archive all {result?.done.length} Done chat{result?.done.length === 1 ? '' : 's'}?</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">Only the exact backend-classified Done set below is included. Retained, conflicted, unrelated, and unavailable sessions remain untouched.</p><div className="mt-3 flex justify-end gap-2"><button type="button" className="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs" disabled={archiving} onClick={() => setArchiveAllReview(false)}>Cancel</button><button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-1.5 text-xs text-[var(--app-primary-text)]" disabled={archiving} onClick={() => void archiveAll()}>{archiving ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}Confirm archive all</button></div></section> : null}
          {reviewingSelection && archiveCandidates.length > 0 ? (
            <section className="mt-5 rounded-xl border border-[var(--app-primary)] bg-[var(--app-surface-subtle)] p-4" aria-label="Archive selection review">
              <h3 className="text-sm font-semibold text-[var(--app-text)]">This will archive {archiveCandidates.length} session{archiveCandidates.length === 1 ? '' : 's'}</h3>
              <p className="mt-1 text-xs text-[var(--app-text-subtle)]">Review the exact session and worktree set before confirming.</p>
              <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">{archiveCandidates.map((item) => <WorktreeCard key={item.session_id} item={item} selected={selected} onToggle={toggleSelected} selectable />)}</div>
            </section>
          ) : null}
          {(result?.recently_archived.length ?? 0) > 0 ? <section className="mt-5"><h3 className="mb-2 flex items-center text-xs font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">Recently archived <span className="ml-auto">{result?.recently_archived.length}</span></h3><div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">{result?.recently_archived.map((item) => <div key={item.session_id} className="rounded-lg border border-[var(--app-border)] p-3 text-xs"><span className="block truncate font-medium">{item.title || item.worktree_branch || item.session_id}</span><span className="mt-1 block truncate text-[var(--app-text-muted)]">{item.worktree_branch || 'No worktree branch'}{item.target_branch ? ` → ${item.target_branch}` : ''}</span><button type="button" className="mt-2 rounded border border-[var(--app-border)] px-2 py-1" disabled={archiving} onClick={() => void restoreSession(item.session_id, item.updated_at)}>Restore</button></div>)}</div></section> : null}
        </div>
        <footer className="flex flex-wrap items-center gap-3 border-t border-[var(--app-border)] px-5 py-4 text-xs">
          {doneIDs.length > 0 ? <><button type="button" onClick={() => { setReviewingSelection(false); setSelected(new Set(selected.size === doneIDs.length ? [] : doneIDs)) }}>{selected.size === doneIDs.length ? 'Clear all' : 'Select all Done'}</button><button type="button" className="rounded-md border border-[var(--app-border)] px-2.5 py-1.5" onClick={() => { setArchiveAllReview(true); setReviewingSelection(false) }}>Archive all Done…</button></> : <span className="text-[var(--app-text-muted)]">No archive candidates</span>}
          <span className="ml-auto text-[var(--app-text-muted)]">{selected.size} selected</span>
          {!reviewingSelection ? <button type="button" className="rounded-md bg-[var(--app-primary)] px-3 py-1.5 text-[var(--app-primary-text)] disabled:opacity-50" disabled={selected.size === 0} onClick={() => setReviewingSelection(true)}>Review archive set</button> : <button type="button" className="inline-flex items-center gap-1.5 rounded-md bg-[var(--app-primary)] px-3 py-1.5 text-[var(--app-primary-text)] disabled:opacity-50" disabled={archiveCandidates.length === 0 || archiving} onClick={() => void archiveSelected()}>{archiving ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}Confirm archive ({archiveCandidates.length})</button>}
        </footer>
      </DialogPanel>
    </Dialog>
  )
}

function CandidatePile({ title, icon, items, selected, onToggle, selectable, onCommit, onIntegrate }: { title: string; icon: ReactNode; items: ReviewWorktreeCandidate[]; selected: Set<string>; onToggle: (id: string) => void; selectable: boolean; onCommit?: (item: ReviewWorktreeCandidate) => void; onIntegrate?: (item: ReviewWorktreeCandidate) => void }) {
  return <section className="mt-5"><h3 className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">{icon}{title} <span className="ml-auto">{items.length}</span></h3><div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">{items.length === 0 ? <p className="col-span-full rounded-lg bg-[var(--app-surface-subtle)] p-3 text-xs text-[var(--app-text-muted)]">None</p> : items.map((item) => <WorktreeCard key={item.session_id} item={item} selected={selected} onToggle={onToggle} selectable={selectable} onCommit={onCommit} onIntegrate={onIntegrate} />)}</div></section>
}

function WorktreeCard({ item, selected, onToggle, selectable, onCommit, onIntegrate }: { item: ReviewWorktreeCandidate; selected: Set<string>; onToggle: (id: string) => void; selectable: boolean; onCommit?: (item: ReviewWorktreeCandidate) => void; onIntegrate?: (item: ReviewWorktreeCandidate) => void }) {
  return <div className={cn('grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-2 rounded-lg border p-3 text-xs', selected.has(item.session_id) ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)]' : 'border-[var(--app-border)]')}><label className={selectable ? 'cursor-pointer' : ''}>{selectable ? <input className="mt-0.5" type="checkbox" checked={selected.has(item.session_id)} onChange={() => onToggle(item.session_id)} aria-label={`Select ${item.title || item.session_id}`} /> : <span className="mt-1.5 block h-2 w-2 rounded-full bg-[var(--app-warning)]" />}</label><span className="min-w-0"><span className="block truncate font-semibold text-[var(--app-text)]" title={item.title || item.session_id}>{item.title || item.session_id}</span><span className="mt-1 flex min-w-0 items-center gap-1 text-[var(--app-text-muted)]"><GitBranch size={12} className="shrink-0" /><span className="truncate" title={item.worktree_branch}>{item.worktree_branch || 'Unknown worktree branch'}</span></span><span className="mt-0.5 block truncate text-[var(--app-text-subtle)]" title={item.worktree_path}>{item.worktree_path || 'Worktree path unavailable'}</span><span className="mt-2 block text-[var(--app-text-muted)]">{reviewWorktreeReasonLabel(item)}</span>{item.commit_job ? <span className={cn('mt-1 block', item.commit_job.status === 'failed' ? 'text-[var(--app-danger)]' : item.commit_job.status === 'completed' ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]')}>{item.commit_job.status === 'completed' ? `AI committed ${item.commit_job.commit_hash?.slice(0, 8) || ''}` : item.commit_job.status === 'failed' ? 'AI commit failed' : `AI commit ${item.commit_job.status}`}</span> : null}{item.commit_eligible && onCommit && item.commit_job?.status !== 'pending' && item.commit_job?.status !== 'running' ? <button type="button" className="mt-2 inline-flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2 py-1 text-[var(--app-text)]" onClick={() => onCommit(item)}><GitCommitHorizontal size={12} />Review commit</button> : null}{item.integrate_eligible && onIntegrate ? <button type="button" className="ml-2 mt-2 inline-flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2 py-1 text-[var(--app-text)]" onClick={() => onIntegrate(item)}><GitMerge size={12} />Integrate into {item.target_branch || 'target'}</button> : null}{item.classification === 'done' && !item.archive_ready ? <span className="mt-1 block text-[var(--app-text-subtle)]">Grace period active</span> : null}</span></div>
}
