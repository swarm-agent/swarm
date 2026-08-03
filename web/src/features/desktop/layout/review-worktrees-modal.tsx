import { useCallback, useEffect, useId, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Archive, Bot, CheckCircle2, ChevronDown, CircleHelp, Eye, EyeOff, GitBranch, GitCommitHorizontal, GitMerge, LoaderCircle, RefreshCcw, ShieldAlert, X } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { cn } from '../../../lib/cn'
import { commitWorkspaceChanges, fetchGitStatus } from '../git/api'
import type { GitFileStatus } from '../git/types'
import { archiveDesktopV3Sessions } from '../session-v3/plan-execution-api'
import { reviewDesktopV3Worktrees, unarchiveDesktopV3ReviewSessions, type ReviewWorktreeCandidate, type ReviewWorktreesResponse } from '../session-v3/review-worktrees-api'
import { resolveDesktopV3StartupAgent } from '../chat/services/desktop-startup-agent'
import type { AgentStateRecord } from '../chat/types/chat'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveReviewAutoArchiveMinutes } from '../settings/swarm/mutations/save-review-auto-archive-minutes'
import { normalizeReviewAutoArchiveMinutes, REVIEW_AUTO_ARCHIVE_MINUTES, type UISettingsWire } from '../settings/swarm/types/swarm-settings'

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

export function selectableReviewIDs(result: ReviewWorktreesResponse | null): string[] {
  return result?.retained.map((item) => item.session_id) ?? []
}

export function selectedArchiveCandidates(result: ReviewWorktreesResponse | null, selected: Set<string>): ReviewWorktreeCandidate[] {
  return result?.retained.filter((item) => selected.has(item.session_id)) ?? []
}

export function currentCheckoutCommitCandidate(result: ReviewWorktreesResponse | null): ReviewWorktreeCandidate | null {
  return result?.retained.find((item) => item.current_checkout && item.commit_eligible && Boolean(item.worktree_path)) ?? null
}

export function reviewCommitCandidates(result: ReviewWorktreesResponse | null): ReviewWorktreeCandidate[] {
  return result?.retained.filter((item) => item.commit_eligible && !item.current_checkout && Boolean(item.worktree_path)) ?? []
}

export function shouldShowReviewCommitAction(result: ReviewWorktreesResponse | null): boolean {
  return reviewCommitCandidates(result).length > 0
}

export interface ReviewWorktreeIntegrationFailure {
  candidate: ReviewWorktreeCandidate
  error: string
  operation?: 'integrate' | 'commit_and_integrate'
}

export function resolveReviewWorktreeRepairAgent(agentState?: AgentStateRecord | null): string {
  if (!agentState) return ''
  return resolveDesktopV3StartupAgent(agentState)
}

export function reviewWorktreeIntegrationFailureDisplay(error: string, showError: boolean, operation: ReviewWorktreeIntegrationFailure['operation'] = 'integrate'): { summary: string; fullError?: string } {
  return {
    summary: operation === 'commit_and_integrate'
      ? 'The worktree could not be committed and integrated in one pass. Swarm was given the error so it can finish the repair safely.'
      : 'The worktree could not be integrated. The target branch was left unchanged.',
    fullError: showError ? error : undefined,
  }
}

export function buildReviewWorktreeFixPrompt(failure: ReviewWorktreeIntegrationFailure, workspacePath?: string): string {
  const candidate = failure.candidate
  const instruction = failure.operation === 'commit_and_integrate'
    ? 'Fix this Review Worktrees commit-and-integration failure. The source commit may or may not have been created. Inspect the worktree, target branch, and complete error; create the intended commit if it is still missing, then safely cherry-pick/integrate it into the target branch. Preserve unrelated changes and do not duplicate a commit that already succeeded.'
    : 'Fix this Review Worktrees integration failure. Inspect the branches and error, resolve the cherry-pick/integration conflict safely, and complete the integration. Preserve current target-branch behavior and do not discard unrelated changes.'
  return [
    instruction,
    '',
    `Session: ${candidate.title || candidate.session_id} (${candidate.session_id})`,
    `Source branch: ${candidate.worktree_branch || 'unknown'}`,
    `Target branch: ${candidate.target_branch || 'unknown'}`,
    `Workspace: ${workspacePath?.trim() || candidate.worktree_path || 'unknown'}`,
    '',
    failure.operation === 'commit_and_integrate' ? 'Commit/integration error:' : 'Integration error:',
    failure.error,
  ].join('\n')
}

function gitFileStatusLabel(file: GitFileStatus): string {
  if (file.conflict) return 'conflict'
  if (file.untracked) return 'new'
  if (file.staged && file.modified) return 'staged + modified'
  if (file.staged) return 'staged'
  if (file.modified) return 'modified'
  return file.xy?.trim() || file.kind
}

export function ReviewWorktreesModal({ workspacePath, onClose, onAskSwarmFix, repairFixAvailable = false }: { workspacePath?: string; onClose: () => void; onAskSwarmFix?: (failure: ReviewWorktreeIntegrationFailure) => void | Promise<void>; repairFixAvailable?: boolean }) {
  const [result, setResult] = useState<ReviewWorktreesResponse | null>(null)
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [reviewingSelection, setReviewingSelection] = useState(false)
  const [loading, setLoading] = useState(false)
  const [archiving, setArchiving] = useState(false)
  const [commitCandidate, setCommitCandidate] = useState<ReviewWorktreeCandidate | null>(null)
  const [commitFiles, setCommitFiles] = useState<GitFileStatus[]>([])
  const [commitMessage, setCommitMessage] = useState('')
  const [commitIntegrateAfter, setCommitIntegrateAfter] = useState(false)
  const [commitArchiveAfter, setCommitArchiveAfter] = useState(false)
  const [openingCommit, setOpeningCommit] = useState(false)
  const [committing, setCommitting] = useState(false)
  const [batchCommitting, setBatchCommitting] = useState(false)
  const [activeCommitBatchID, setActiveCommitBatchID] = useState('')
  const [releasedAfterCommit, setReleasedAfterCommit] = useState<ReviewWorktreeCandidate[]>([])
  const [integrateCandidate, setIntegrateCandidate] = useState<ReviewWorktreeCandidate | null>(null)
  const [integrating, setIntegrating] = useState(false)
  const [integrationSucceeded, setIntegrationSucceeded] = useState(false)
  const [integrationArchiveAfter, setIntegrationArchiveAfter] = useState(false)
  const [integrationArchiveError, setIntegrationArchiveError] = useState('')
  const [integrationFailure, setIntegrationFailure] = useState<ReviewWorktreeIntegrationFailure | null>(null)
  const [showIntegrationError, setShowIntegrationError] = useState(false)
  const [error, setError] = useState('')
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [autoArchiveMinutes, setAutoArchiveMinutes] = useState(0)
  const [savingAutoArchive, setSavingAutoArchive] = useState(false)
  const [runningAutoArchive, setRunningAutoArchive] = useState(false)
  const [autoArchiveStatus, setAutoArchiveStatus] = useState('')
  const [doneExpanded, setDoneExpanded] = useState(false)
  const [archivedExpanded, setArchivedExpanded] = useState(false)
  const refresh = useCallback(async (automatic = false): Promise<ReviewWorktreesResponse | null> => {
    setLoading(true)
    setError('')
    try {
      const next = await reviewDesktopV3Worktrees({ workspacePath, automatic, graceHours: 1 })
      setResult(next)
      if (next.commit_batch_id) setActiveCommitBatchID(next.commit_batch_id)
      setSelected((current) => new Set([...current].filter((id) => next.retained.some((item) => item.session_id === id))))
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
        setSelected((current) => new Set([...current].filter((id) => next.retained.some((item) => item.session_id === id))))
      })
      .catch((cause) => { if (!cancelled) setError(cause instanceof Error ? cause.message : 'Could not inspect review worktrees.') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [workspacePath])
  useEffect(() => {
    let cancelled = false
    void getUISettings().then((settings) => {
      if (cancelled) return
      setUISettings(settings)
      setAutoArchiveMinutes(normalizeReviewAutoArchiveMinutes(settings.chat?.review_auto_archive_minutes))
    }).catch((cause) => {
      if (!cancelled) setError(cause instanceof Error ? cause.message : 'Could not load auto-archive setting.')
    })
    return () => { cancelled = true }
  }, [])
  const setAutoArchive = async (minutes: number) => {
    if (!uiSettings || savingAutoArchive) return
    setSavingAutoArchive(true)
    setAutoArchiveStatus('')
    setError('')
    try {
      const saved = await saveReviewAutoArchiveMinutes({ current: uiSettings, minutes })
      setUISettings(saved)
      setAutoArchiveMinutes(normalizeReviewAutoArchiveMinutes(saved.chat?.review_auto_archive_minutes))
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not save auto-archive setting.')
    } finally {
      setSavingAutoArchive(false)
    }
  }
  const runAutoArchiveNow = async () => {
    if (autoArchiveMinutes <= 0 || runningAutoArchive) return
    setRunningAutoArchive(true)
    setAutoArchiveStatus('')
    const next = await refresh(true)
    if (next) {
      const archivedCount = next.archived_session_ids.length
      if (archivedCount > 0) await refresh(false)
      setAutoArchiveStatus(archivedCount > 0
        ? `Archived ${archivedCount} eligible session${archivedCount === 1 ? '' : 's'}.`
        : 'Auto-archive check complete. No sessions are ready yet.')
    }
    setRunningAutoArchive(false)
  }
  const reviewIDs = useMemo(() => selectableReviewIDs(result), [result])
  const archiveCandidates = useMemo(() => selectedArchiveCandidates(result, selected), [result, selected])
  const checkoutCommitCandidate = useMemo(() => currentCheckoutCommitCandidate(result), [result])
  const reviewCommitWorktrees = useMemo(() => reviewCommitCandidates(result), [result])
  const showReviewCommitAction = useMemo(() => shouldShowReviewCommitAction(result), [result])
  const activeReviewCommitJobs = useMemo(() => result?.retained.filter((item) => item.commit_job?.status === 'pending' || item.commit_job?.status === 'running') ?? [], [result])
  const completedReviewCommitJobs = useMemo(() => [...(result?.retained ?? []), ...(result?.done ?? [])].filter((item) => item.commit_job?.status === 'completed') ?? [], [result])
  const failedReviewCommitJobs = useMemo(() => result?.retained.filter((item) => item.commit_job?.status === 'failed') ?? [], [result])
  const toggleSelected = (id: string) => {
    if (!reviewingSelection || !reviewIDs.includes(id)) return
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
      setCommitIntegrateAfter(false)
      setCommitArchiveAfter(false)
      setCommitCandidate(candidate)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load the changed-file list.')
    } finally {
      setOpeningCommit(false)
    }
  }
  const commitReviewChanges = async (integrateAfterCommit: boolean, archiveAfterIntegration = false) => {
    const candidate = commitCandidate
    const worktreePath = candidate?.worktree_path
    if (!candidate?.commit_eligible || !worktreePath || !commitMessage.trim()) return
    const commitAndIntegrate = integrateAfterCommit && !candidate.current_checkout
    const waitingIDs = candidate.current_checkout
      ? new Set(result?.retained.filter((item) => item.current_checkout && item.reason === 'current_checkout_uncommitted_work').map((item) => item.session_id) ?? [])
      : new Set<string>()
    setCommitting(true)
    setError('')
    let commitSucceeded = false
    let integrateSucceeded = false
    try {
      await commitWorkspaceChanges({ workspacePath: worktreePath, sessionId: candidate.session_id, message: commitMessage, all: true })
      commitSucceeded = true
      if (commitAndIntegrate) {
        await reviewDesktopV3Worktrees({ workspacePath, integrateSessionIds: [candidate.session_id], graceHours: 1 })
        integrateSucceeded = true
        if (archiveAfterIntegration) {
          await reviewDesktopV3Worktrees({ workspacePath, archiveSessionIds: [candidate.session_id], graceHours: 1 })
        }
      }
      setCommitCandidate(null)
      setCommitFiles([])
      setCommitMessage('')
      setCommitIntegrateAfter(false)
      setCommitArchiveAfter(false)
      const next = await refresh(false)
      if (next && waitingIDs.size > 0) setReleasedAfterCommit(next.done.filter((item) => waitingIDs.has(item.session_id)))
    } catch (cause) {
      const failureMessage = cause instanceof Error
        ? cause.message
        : commitAndIntegrate
          ? 'Could not commit and integrate the worktree.'
          : candidate.current_checkout
            ? 'Could not commit the current checkout.'
            : 'Could not commit the worktree.'
      if (commitSucceeded && commitAndIntegrate) {
        setCommitCandidate(null)
        setCommitFiles([])
        setCommitMessage('')
        setCommitIntegrateAfter(false)
        setCommitArchiveAfter(false)
        setIntegrateCandidate(candidate)
        setIntegrationArchiveAfter(archiveAfterIntegration)
        setIntegrationSucceeded(integrateSucceeded)
        if (integrateSucceeded) {
          setIntegrationArchiveError(failureMessage)
          setIntegrationFailure(null)
        } else {
          setIntegrationArchiveError('')
          setIntegrationFailure({ candidate, error: failureMessage, operation: 'commit_and_integrate' })
        }
      } else {
        setError(failureMessage)
      }
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
    if (reviewCommitWorktrees.length === 0 || batchCommitting) return
    setBatchCommitting(true)
    setError('')
    try {
      const next = await reviewDesktopV3Worktrees({ workspacePath, commitSessionIds: reviewCommitWorktrees.map((item) => item.session_id), graceHours: 1 })
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
    setIntegrationFailure(null)
    setIntegrationArchiveError('')
    setShowIntegrationError(false)
    let integrated = integrationSucceeded
    try {
      if (!integrated) {
        await reviewDesktopV3Worktrees({ workspacePath, integrateSessionIds: [integrateCandidate.session_id], graceHours: 1 })
        integrated = true
        setIntegrationSucceeded(true)
      }
      if (integrationArchiveAfter) {
        await reviewDesktopV3Worktrees({ workspacePath, archiveSessionIds: [integrateCandidate.session_id], graceHours: 1 })
      }
      await refresh(false)
    } catch (cause) {
      const failureMessage = cause instanceof Error ? cause.message : integrated ? 'Could not archive the integrated session.' : 'Could not integrate the worktree.'
      if (integrated) {
        setIntegrationArchiveError(failureMessage)
      } else {
        setIntegrationFailure({ candidate: integrateCandidate, error: failureMessage, operation: 'integrate' })
      }
    } finally {
      setIntegrating(false)
    }
  }
  const closeIntegrateReview = () => {
    if (integrating) return
    setIntegrateCandidate(null)
    setIntegrationSucceeded(false)
    setIntegrationFailure(null)
    setIntegrationArchiveAfter(false)
    setIntegrationArchiveError('')
    setShowIntegrationError(false)
  }
  const openIntegrateReview = (candidate: ReviewWorktreeCandidate) => {
    setIntegrationSucceeded(false)
    setIntegrationFailure(null)
    setIntegrationArchiveAfter(false)
    setIntegrationArchiveError('')
    setShowIntegrationError(false)
    setIntegrateCandidate(candidate)
  }
  const askSwarmToFixIntegration = () => {
    if (!integrationFailure || !onAskSwarmFix || !repairFixAvailable) return
    void onAskSwarmFix(integrationFailure)
  }
  const archiveSelected = async () => {
    if (archiveCandidates.length === 0 || !reviewingSelection) return
    setArchiving(true)
    setError('')
    try {
      await archiveDesktopV3Sessions(archiveCandidates.map((item) => item.session_id))
      setSelected(new Set())
      setReviewingSelection(false)
      await refresh(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not archive selected sessions.')
    } finally {
      setArchiving(false)
    }
  }
  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Review worktrees" className="z-[80] max-sm:block max-sm:p-0" data-mobile-review-worktrees>
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="w-[min(1120px,calc(100vw-32px))] p-0 shadow-2xl max-sm:h-[100dvh] max-sm:max-h-none max-sm:w-full max-sm:rounded-none max-sm:border-0">
        <header className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 max-sm:pt-[calc(var(--app-safe-area-top)+1rem)] max-sm:pr-[calc(var(--app-safe-area-right)+1rem)] max-sm:pl-[calc(var(--app-safe-area-left)+1rem)]">
          <div><h2 className="text-base font-semibold text-[var(--app-text)]">Review worktrees</h2><p className="mt-0.5 text-xs text-[var(--app-text-subtle)]">Compare each session worktree with {result?.current_target_branch ? <strong>{result.current_target_branch}</strong> : 'its managed target branch'} before archiving.</p></div>
          <button type="button" className="-mr-2 flex min-h-11 min-w-11 touch-manipulation items-center justify-center rounded-full text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)]" aria-label="Close review cleanup" onClick={onClose}><X size={20} /></button>
        </header>
        <div className="shrink-0 border-b border-[var(--app-border)] px-5 py-3 max-sm:px-[calc(var(--app-safe-area-right)+1rem)] max-sm:pl-[calc(var(--app-safe-area-left)+1rem)]">
          <div className="flex flex-wrap items-center gap-2 text-xs max-sm:grid max-sm:grid-cols-2">
            <button type="button" className="inline-flex min-h-11 touch-manipulation items-center justify-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1.5" disabled={loading} onClick={() => void refresh(false)}>{loading ? <LoaderCircle size={13} className="animate-spin" /> : <RefreshCcw size={13} />} Recheck worktrees</button>
            <button type="button" className="min-h-11 touch-manipulation rounded-md border border-[var(--app-border)] px-2.5 py-1.5 disabled:opacity-50" disabled={reviewIDs.length === 0} onClick={() => { setReviewingSelection((current) => !current); setSelected(new Set()) }}>{reviewingSelection ? 'Cancel selection' : 'Select to archive'}</button>
            <button type="button" className="min-h-11 touch-manipulation rounded-md border border-[var(--app-border)] px-2.5 py-1.5 disabled:opacity-50" disabled={reviewIDs.length === 0} onClick={() => { setReviewingSelection(true); setSelected(new Set(reviewIDs)) }}>Archive all</button>
            <div className="ml-auto flex min-h-11 items-center gap-2 max-sm:col-span-2 max-sm:ml-0 max-sm:flex-wrap">
              <span className="inline-flex items-center gap-1">Auto-archive<span className="group relative inline-flex"><button type="button" className="inline-flex h-7 w-7 items-center justify-center rounded-full text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" aria-label="How auto-archive works" title="How auto-archive works"><CircleHelp size={14} /></button><span className="pointer-events-none absolute top-full right-0 z-30 mt-2 hidden w-72 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-left text-xs font-normal leading-5 text-[var(--app-text)] shadow-xl group-hover:block group-focus-within:block">Auto-archive checks whether a session’s commit has been integrated into its configured main/target branch. Once integrated, it waits the selected time after that session’s latest activity, then archives it. Run now performs the same safe check immediately.</span></span></span>
              <select className="min-h-11 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1" aria-label="Auto-archive delay" value={autoArchiveMinutes} disabled={!uiSettings || savingAutoArchive || runningAutoArchive} onChange={(event) => void setAutoArchive(Number(event.target.value))}><option value={0}>Off</option>{REVIEW_AUTO_ARCHIVE_MINUTES.map((minutes) => <option key={minutes} value={minutes}>After {minutes === 60 ? '1 hour' : `${minutes} minutes`}</option>)}</select>
              <button type="button" className="inline-flex min-h-11 touch-manipulation items-center justify-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1.5 disabled:opacity-50" disabled={!uiSettings || autoArchiveMinutes <= 0 || savingAutoArchive || runningAutoArchive} onClick={() => void runAutoArchiveNow()}>{runningAutoArchive ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}Run now</button>
            </div>
          </div>
          {autoArchiveStatus ? <p className="mt-2 text-xs leading-5 text-[var(--app-success)]" role="status">{autoArchiveStatus}</p> : null}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-5 max-sm:px-[calc(var(--app-safe-area-right)+1rem)] max-sm:pb-[calc(var(--app-safe-area-bottom)+1rem)] max-sm:pl-[calc(var(--app-safe-area-left)+1rem)] [-webkit-overflow-scrolling:touch]">
          {error ? <p className="mt-3 rounded-md bg-[var(--app-danger-bg)] p-2.5 text-xs text-[var(--app-danger)]">{error}</p> : null}
          {result?.checkout_dirty ? <section className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-[var(--app-warning)] bg-[var(--app-surface-subtle)] p-4" aria-label="Dirty main checkout summary"><div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-[var(--app-text)]">{result.current_target_branch || 'Current branch'} has {result.checkout_dirty_count} uncommitted change{result.checkout_dirty_count === 1 ? '' : 's'}</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">{result.blocked_by_checkout_count} chat{result.blocked_by_checkout_count === 1 ? ' is' : 's are'} waiting for these commits before they can get archived. Commit, then Swarm will recheck and show the exact chats released into Done.</p></div>{checkoutCommitCandidate ? <button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-warning)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--app-warning)] disabled:opacity-50" disabled={openingCommit} onClick={() => void openCommitReview(checkoutCommitCandidate)}>{openingCommit ? <LoaderCircle size={13} className="animate-spin" /> : <GitCommitHorizontal size={13} />}Commit {result.current_target_branch || 'branch'}…</button> : null}</section> : null}
          {!result?.checkout_dirty && releasedAfterCommit.length > 0 ? <section className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-[var(--app-success)] bg-[var(--app-surface-subtle)] p-4" aria-label="Released chats ready to archive"><div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-[var(--app-text)]">{releasedAfterCommit.length} chat{releasedAfterCommit.length === 1 ? '' : 's'} released into Done</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">The commit succeeded and these exact sessions passed the backend safety recheck. Archive them now or leave them in Done for the configured auto-archive delay.</p></div><button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--app-primary)] disabled:opacity-50" disabled={archiving} onClick={() => void archiveReleased()}>{archiving ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}Archive {releasedAfterCommit.length}</button></section> : null}
          {showReviewCommitAction ? <section className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-[var(--app-primary)] bg-[var(--app-surface-subtle)] p-4" aria-label="AI review commit batch"><div className="min-w-0 flex-1"><h3 className="text-sm font-semibold text-[var(--app-text)]">Prepare review commits with AI</h3><p className="mt-1 text-xs text-[var(--app-text-subtle)]">The AI utility model receives each isolated worktree’s bounded change set, chooses a message, and creates one commit per worktree in parallel. Sessions stay in review.</p><p className="mt-2 text-xs text-[var(--app-text-muted)]">{activeReviewCommitJobs.length > 0 ? `${activeReviewCommitJobs.length} pending or running` : completedReviewCommitJobs.length > 0 ? `${completedReviewCommitJobs.length} committed and ready to review` : `${reviewCommitWorktrees.length} ready to commit`}{failedReviewCommitJobs.length > 0 ? ` · ${failedReviewCommitJobs.length} failed` : ''}</p>{failedReviewCommitJobs.map((item) => <p key={item.session_id} className="mt-1 truncate text-xs text-[var(--app-danger)]" title={item.commit_job?.error}>{item.title || item.session_id}: {item.commit_job?.error}</p>)}</div><button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-2 text-xs font-semibold text-[var(--app-primary)] disabled:opacity-50" disabled={batchCommitting || activeReviewCommitJobs.length > 0 || reviewCommitWorktrees.length === 0} onClick={() => void commitReviewWorktrees()}>{batchCommitting || activeReviewCommitJobs.length > 0 ? <LoaderCircle size={13} className="animate-spin" /> : <GitCommitHorizontal size={13} />}{activeReviewCommitJobs.length > 0 ? 'Committing…' : `Commit ${reviewCommitWorktrees.length} with AI`}</button></section> : null}
          <CandidatePile title="Keep in review" icon={<ShieldAlert size={14} />} items={result?.retained ?? []} selected={selected} onToggle={toggleSelected} selectable={reviewingSelection} onCommit={(item) => void openCommitReview(item)} onIntegrate={openIntegrateReview} />
          <CandidatePile title={autoArchiveMinutes > 0 ? `Done · archives after ${autoArchiveMinutes === 60 ? '1h' : `${autoArchiveMinutes}m`}` : 'Done · auto-archive off'} icon={<CheckCircle2 size={14} />} items={result?.done ?? []} selected={selected} onToggle={() => undefined} selectable={false} expanded={doneExpanded} onExpandedChange={setDoneExpanded} />
          {commitCandidate ? (
            <section className="mt-5 rounded-xl border border-[var(--app-warning)] bg-[var(--app-surface-subtle)] p-4" aria-label={commitCandidate.current_checkout ? 'Commit current checkout review' : 'Commit worktree review'}>
              <h3 className="text-sm font-semibold text-[var(--app-text)]">{commitCandidate.current_checkout ? 'Commit current checkout before archiving' : `Commit changes in ${commitCandidate.worktree_branch || 'the worktree branch'}`}</h3>
              <p className="mt-1 text-xs text-[var(--app-text-subtle)]">{commitCandidate.current_checkout ? <>This stages all {commitFiles.length} shown changes and creates one commit in <strong>{commitCandidate.worktree_branch || 'the current branch'}</strong>. It does not archive anything until the commit succeeds and Swarm rechecks the waiting sessions.</> : <>This stages all {commitFiles.length} shown changes and creates one commit in <strong>{commitCandidate.worktree_branch || 'the worktree branch'}</strong>. Choose <strong>Commit only</strong> to leave it there for review, or <strong>Commit and integrate</strong> to safely cherry-pick it into <strong>{commitCandidate.target_branch || 'the target branch'}</strong> in the same action. If either step of the combined action fails, Swarm starts a repair session with the complete error.</>}</p>
              <div className="mt-3 max-h-48 overflow-y-auto rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] font-mono text-xs" aria-label="Changed files">{commitFiles.map((file) => <div key={`${file.kind}:${file.path}`} className="flex min-w-0 gap-2 border-b border-[var(--app-border)] px-2.5 py-1.5 last:border-0"><span className="shrink-0 text-[var(--app-text-subtle)]">{gitFileStatusLabel(file)}</span><span className="truncate" title={file.path}>{file.path}</span></div>)}</div>
              <label className="mt-3 block text-xs font-medium text-[var(--app-text)]">Commit message<input className="mt-1 w-full rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-2 font-normal" value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} placeholder="Describe these changes" autoFocus /></label>
              {!commitCandidate.current_checkout ? <div className="mt-3 grid gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs"><label className="flex items-start gap-2 text-[var(--app-text)]"><input className="mt-0.5" type="checkbox" checked={commitIntegrateAfter} disabled={committing} onChange={(event) => { const checked = event.target.checked; setCommitIntegrateAfter(checked); if (!checked) setCommitArchiveAfter(false) }} /><span><strong>Integrate into {commitCandidate.target_branch || 'target'}</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">Safely integrate the worktree after the commit succeeds.</span></span></label><label className={cn('flex items-start gap-2', commitIntegrateAfter ? 'text-[var(--app-text)]' : 'text-[var(--app-text-subtle)]')}><input className="mt-0.5" type="checkbox" checked={commitArchiveAfter} disabled={committing || !commitIntegrateAfter} onChange={(event) => setCommitArchiveAfter(event.target.checked)} /><span><strong>Archive session after integration</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">Only archives after integration is verified.</span></span></label></div> : null}
              <div className="mt-3 flex flex-wrap justify-end gap-2"><button type="button" className="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs" disabled={committing} onClick={() => { setCommitCandidate(null); setCommitFiles([]); setCommitMessage(''); setCommitIntegrateAfter(false); setCommitArchiveAfter(false) }}>Cancel</button><button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-semibold text-[var(--app-primary)] disabled:opacity-50" disabled={committing || commitFiles.length === 0 || !commitMessage.trim()} onClick={() => void commitReviewChanges(!commitCandidate.current_checkout && commitIntegrateAfter, commitArchiveAfter)}>{committing ? <LoaderCircle size={13} className="animate-spin" /> : <GitCommitHorizontal size={13} />}{commitCandidate.current_checkout ? 'Commit changes' : commitIntegrateAfter ? 'Commit and integrate' : 'Commit only'}</button></div>
            </section>
          ) : null}
          {reviewingSelection && archiveCandidates.length > 0 ? (
            <section className="mt-5 rounded-xl border border-[var(--app-primary)] bg-[var(--app-surface-subtle)] p-4" aria-label="Archive selection review">
              <h3 className="text-sm font-semibold text-[var(--app-text)]">This will archive {archiveCandidates.length} session{archiveCandidates.length === 1 ? '' : 's'}</h3>
              <p className="mt-1 text-xs text-[var(--app-text-subtle)]">Review the exact session and worktree set before confirming.</p>
              <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">{archiveCandidates.map((item) => <WorktreeCard key={item.session_id} item={item} selected={selected} onToggle={toggleSelected} selectable />)}</div>
              <div className="mt-3 flex justify-end gap-2"><button type="button" className="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs" disabled={archiving} onClick={() => { setReviewingSelection(false); setSelected(new Set()) }}>Cancel</button><button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs text-[var(--app-text)] disabled:opacity-50" disabled={archiving} onClick={() => void archiveSelected()}>{archiving ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}Confirm archive ({archiveCandidates.length})</button></div>
            </section>
          ) : null}
          {(result?.recently_archived.length ?? 0) > 0 ? <CollapsibleReviewSection title="Archived" icon={<Archive size={14} />} count={result?.recently_archived.length ?? 0} expanded={archivedExpanded} onExpandedChange={setArchivedExpanded}><div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">{result?.recently_archived.map((item) => <div key={item.session_id} className="rounded-lg border border-[var(--app-border)] p-3 text-xs"><span className="block truncate font-medium">{item.title || item.worktree_branch || item.session_id}</span><span className="mt-1 block truncate text-[var(--app-text-muted)]">{item.worktree_branch || 'No worktree branch'}{item.target_branch ? ` → ${item.target_branch}` : ''}</span><button type="button" className="mt-2 rounded border border-[var(--app-border)] px-2 py-1" disabled={archiving} onClick={() => void restoreSession(item.session_id, item.updated_at)}>Restore</button></div>)}</div></CollapsibleReviewSection> : null}
        </div>
        {integrateCandidate ? (
          <div className="absolute inset-0 z-20 flex items-center justify-center p-4 max-sm:items-end max-sm:p-0" role="alertdialog" aria-modal="true" aria-labelledby="integrate-worktree-title">
            <button type="button" className="absolute inset-0 bg-[var(--app-backdrop)]" aria-label="Close integration confirmation" disabled={integrating} onClick={closeIntegrateReview} />
            <section className="relative z-10 w-[min(520px,100%)] rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5 shadow-2xl max-sm:max-h-[90dvh] max-sm:overflow-y-auto max-sm:rounded-b-none max-sm:pb-[calc(var(--app-safe-area-bottom)+1.25rem)]">
              {integrationSucceeded && !integrationArchiveError ? (
                <>
                  <div className="flex items-start gap-3">
                    <CheckCircle2 size={20} className="mt-0.5 shrink-0 text-[var(--app-success)]" />
                    <div>
                      <h3 id="integrate-worktree-title" className="text-sm font-semibold text-[var(--app-text)]">Integrated into {integrateCandidate.target_branch || 'target'}</h3>
                      <p className="mt-1 text-xs text-[var(--app-text-subtle)]"><strong>{integrateCandidate.worktree_branch || integrateCandidate.title || 'The worktree'}</strong> was integrated successfully. {integrationArchiveAfter ? 'The session was archived after the safety recheck.' : 'The chat is now in Done and was not archived.'}</p>
                    </div>
                  </div>
                  <div className="mt-4 flex justify-end"><button type="button" className="rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-semibold text-[var(--app-primary)]" onClick={closeIntegrateReview} autoFocus>Done</button></div>
                </>
              ) : (
                <>
                  <h3 id="integrate-worktree-title" className="text-sm font-semibold text-[var(--app-text)]">{integrationSucceeded ? `Archive integrated session` : `Integrate ${integrateCandidate.worktree_branch} into ${integrateCandidate.target_branch}`}</h3>
                  <p className="mt-1 text-xs text-[var(--app-text-subtle)]">{integrationSucceeded ? 'The worktree is integrated. Retry only the remaining archive step.' : 'Swarm preflights the full commit stack and leaves the target unchanged on conflict.'}</p>
                  <label className="mt-3 flex items-start gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-xs text-[var(--app-text)]"><input className="mt-0.5" type="checkbox" checked={integrationArchiveAfter} disabled={integrating} onChange={(event) => setIntegrationArchiveAfter(event.target.checked)} /><span><strong>Archive session after integration</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">Only archives after integration is verified.</span></span></label>
                  {integrationArchiveError ? <div className="mt-3 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-3 text-xs text-[var(--app-danger)]" role="alert">{integrationArchiveError}</div> : null}
                  {integrationFailure ? (() => { const display = reviewWorktreeIntegrationFailureDisplay(integrationFailure.error, showIntegrationError, integrationFailure.operation); return <div className="mt-3 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-3" role="alert"><p className="text-xs font-medium text-[var(--app-danger)]">Integration failed</p><p className="mt-1 text-xs text-[var(--app-text-subtle)]">{display.summary}</p>{display.fullError ? <pre className="mt-3 max-h-52 overflow-auto whitespace-pre-wrap rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] p-2.5 text-xs text-[var(--app-text)]">{display.fullError}</pre> : null}<div className="mt-3 flex flex-wrap gap-2"><button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2.5 py-1.5 text-xs text-[var(--app-text)]" onClick={() => setShowIntegrationError((current) => !current)}>{showIntegrationError ? <EyeOff size={13} /> : <Eye size={13} />}{showIntegrationError ? 'Hide Error' : 'Show Error'}</button>{repairFixAvailable && onAskSwarmFix ? <button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-primary)] bg-transparent px-2.5 py-1.5 text-xs font-semibold text-[var(--app-primary)]" onClick={askSwarmToFixIntegration}><Bot size={13} />Ask Swarm to fix this</button> : null}</div></div> })() : null}
                  <div className="mt-4 flex justify-end gap-2"><button type="button" className="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs" disabled={integrating} onClick={closeIntegrateReview} autoFocus>Cancel</button><button type="button" className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-semibold text-[var(--app-primary)] disabled:opacity-50" disabled={integrating} onClick={() => void integrateWorktree()}>{integrating ? <LoaderCircle size={13} className="animate-spin" /> : <GitMerge size={13} />}{integrationSucceeded ? 'Try archive again' : integrationFailure ? 'Try integration again' : 'Confirm integration'}</button></div>
                </>
              )}
            </section>
          </div>
        ) : null}
      </DialogPanel>
    </Dialog>
  )
}

export function CollapsibleReviewSection({ title, icon, count, expanded, onExpandedChange, children }: { title: string; icon: ReactNode; count: number; expanded: boolean; onExpandedChange: (expanded: boolean) => void; children: ReactNode }) {
  const contentID = useId()
  return <section className="mt-5"><h3><button type="button" className="mb-2 flex min-h-11 w-full touch-manipulation items-center gap-1.5 rounded-md text-left text-xs font-semibold uppercase tracking-wide text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-subtle)]" aria-expanded={expanded} aria-controls={contentID} onClick={() => onExpandedChange(!expanded)}>{icon}{title}<span className="ml-auto">{count}</span><ChevronDown size={14} className={cn('transition-transform', expanded && 'rotate-180')} /></button></h3>{expanded ? <div id={contentID}>{children}</div> : null}</section>
}

function CandidatePile({ title, icon, items, selected, onToggle, selectable, onCommit, onIntegrate, expanded, onExpandedChange }: { title: string; icon: ReactNode; items: ReviewWorktreeCandidate[]; selected: Set<string>; onToggle: (id: string) => void; selectable: boolean; onCommit?: (item: ReviewWorktreeCandidate) => void; onIntegrate?: (item: ReviewWorktreeCandidate) => void; expanded?: boolean; onExpandedChange?: (expanded: boolean) => void }) {
  const contents = <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">{items.length === 0 ? <p className="col-span-full rounded-lg bg-[var(--app-surface-subtle)] p-3 text-xs text-[var(--app-text-muted)]">None</p> : items.map((item) => <WorktreeCard key={item.session_id} item={item} selected={selected} onToggle={onToggle} selectable={selectable} onCommit={onCommit} onIntegrate={onIntegrate} />)}</div>
  if (expanded !== undefined && onExpandedChange) return <CollapsibleReviewSection title={title} icon={icon} count={items.length} expanded={expanded} onExpandedChange={onExpandedChange}>{contents}</CollapsibleReviewSection>
  return <section className="mt-5"><h3 className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">{icon}{title} <span className="ml-auto">{items.length}</span></h3>{contents}</section>
}

function WorktreeCard({ item, selected, onToggle, selectable, onCommit, onIntegrate }: { item: ReviewWorktreeCandidate; selected: Set<string>; onToggle: (id: string) => void; selectable: boolean; onCommit?: (item: ReviewWorktreeCandidate) => void; onIntegrate?: (item: ReviewWorktreeCandidate) => void }) {
  return <div className={cn('grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-2 rounded-lg border p-3 text-xs', selected.has(item.session_id) ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)]' : 'border-[var(--app-border)]')}><label className={selectable ? 'cursor-pointer' : ''}>{selectable ? <input className="mt-0.5" type="checkbox" checked={selected.has(item.session_id)} onChange={() => onToggle(item.session_id)} aria-label={`Select ${item.title || item.session_id}`} /> : <span className="mt-1.5 block h-2 w-2 rounded-full bg-[var(--app-warning)]" />}</label><span className="min-w-0"><span className="block truncate font-semibold text-[var(--app-text)]" title={item.title || item.session_id}>{item.title || item.session_id}</span><span className="mt-1 flex min-w-0 items-center gap-1 text-[var(--app-text-muted)]"><GitBranch size={12} className="shrink-0" /><span className="truncate" title={item.worktree_branch}>{item.worktree_branch || 'Unknown worktree branch'}</span></span><span className="mt-0.5 block truncate text-[var(--app-text-subtle)]" title={item.worktree_path}>{item.worktree_path || 'Worktree path unavailable'}</span><span className="mt-2 block text-[var(--app-text-muted)]">{reviewWorktreeReasonLabel(item)}</span>{item.commit_job ? <span className={cn('mt-1 block', item.commit_job.status === 'failed' ? 'text-[var(--app-danger)]' : item.commit_job.status === 'completed' ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]')}>{item.commit_job.status === 'completed' ? `AI committed ${item.commit_job.commit_hash?.slice(0, 8) || ''}` : item.commit_job.status === 'failed' ? 'AI commit failed' : `AI commit ${item.commit_job.status}`}</span> : null}{item.commit_eligible && onCommit && item.commit_job?.status !== 'pending' && item.commit_job?.status !== 'running' ? <button type="button" className="mt-2 inline-flex min-h-11 touch-manipulation items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2 py-1 text-[var(--app-text)] max-sm:w-full max-sm:justify-center" onClick={() => onCommit(item)}><GitCommitHorizontal size={12} />{item.current_checkout ? 'Review commit' : 'Review, commit + integrate'}</button> : null}{item.integrate_eligible && onIntegrate ? <button type="button" className="ml-2 mt-2 inline-flex min-h-11 touch-manipulation items-center gap-1.5 rounded-md border border-[var(--app-border)] px-2 py-1 text-[var(--app-text)] max-sm:ml-0 max-sm:w-full max-sm:justify-center" onClick={() => onIntegrate(item)}><GitMerge size={12} />Integrate into {item.target_branch || 'target'}</button> : null}{item.classification === 'done' && !item.archive_ready ? <span className="mt-1 block text-[var(--app-text-subtle)]">Grace period active</span> : null}</span></div>
}
