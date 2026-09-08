import { requestJson } from '../../../app/api'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { outboxRecordToCacheEvent } from '../state/desktop-v3-cache-wire'
import { buildDesktopV3ChildCardHydrateInput, postDesktopV3SyncHydrate } from '../state/desktop-v3-sync-api'
import type { V3RealtimeOutboxRecord } from '../state/desktop-v3-cache-types'

export type ReviewWorktreeReason =
  | 'inspection_pending'
  | 'uncommitted_work'
  | 'current_checkout_uncommitted_work'
  | 'current_checkout_clean'
  | 'commits_missing_from_target'
  | 'clean_and_integrated'
  | 'managed_worktree_metadata_missing'
  | 'worktree_unavailable'
  | 'target_branch_unavailable'
  | 'done_timestamp_missing'

export interface ReviewWorktreeCandidate {
  session_id: string
  title: string
  updated_at: number
  worktree_branch?: string
  worktree_path?: string
  source_head?: string
  target_branch?: string
  classification: 'retained' | 'done'
  reason: ReviewWorktreeReason
  dirty_count?: number
  missing_commit_count?: number
  equivalent_commit_count?: number
  done_at?: number
  archive_after?: number
  archive_ready: boolean
  current_checkout?: boolean
  commit_eligible?: boolean
  integrate_eligible?: boolean
  commit_job?: {
    batch_id: string
    status: 'pending' | 'running' | 'completed' | 'failed'
    run_session_id?: string
    commit_hash?: string
    error?: string
    updated_at: number
  }
}

export interface RecentlyArchivedReviewSession {
  session_id: string
  title: string
  updated_at: number
  worktree_branch?: string
  worktree_path?: string
  target_branch?: string
}

export interface ReviewWorktreesResponse {
  ok: boolean
  inspection_pending?: boolean
  target_detection: string
  current_target_branch?: string
  current_target_head?: string
  comparison: string
  retained: ReviewWorktreeCandidate[]
  done: ReviewWorktreeCandidate[]
  archived_session_ids: string[]
  commit_batch_id?: string
  recently_archived: RecentlyArchivedReviewSession[]
  grace_period_ms: number
  checkout_dirty: boolean | null
  checkout_dirty_count: number | null
  blocked_by_checkout_count: number | null
  complete: boolean
}

export interface UnarchiveReviewSessionsResponse {
  ok: boolean
  unarchived_session_ids: string[]
  reactivated?: Record<string, V3RealtimeOutboxRecord>
}

export async function unarchiveDesktopV3ReviewSessions(versions: Record<string, number>): Promise<UnarchiveReviewSessionsResponse> {
  const sessionIds = Object.keys(versions)
  if (sessionIds.length === 0) throw new Error('Select at least one archived session')
  const response = await requestJson<UnarchiveReviewSessionsResponse>('/v3/sessions:unarchive', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_ids: sessionIds, expected_updated_at_by_id: versions }),
  })

  const restoredIds = response.unarchived_session_ids ?? sessionIds
  for (const sessionId of restoredIds) {
    const outbox = response.reactivated?.[sessionId]
    if (!outbox?.event || !outbox.projection) continue
    dispatchDesktopV3Cache({ type: 'realtime.applyEvent', event: outboxRecordToCacheEvent(outbox) })
  }
  if (restoredIds.length === 0) return response

  // Hydrate by canonical ids even after applying the committed reactivation
  // events. This repairs any cache resources and subscription membership that
  // were removed when the archive tombstones were observed.
  try {
    const hydrate = await postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput(restoredIds, { activePlan: true, permissionSummary: true }))
    dispatchDesktopV3Cache({ type: 'hydrate.apply', source: 'hydrate', scopeId: hydrate.scope_id, requestedSessionIds: restoredIds, snapshot: hydrate })
  } catch (error) {
    // The durable mutation already succeeded and its committed event was applied
    // above. Do not report the unarchive itself as failed if supplemental cache
    // hydration is temporarily unavailable; realtime can still converge it.
    console.error('[desktop-v3] unarchive hydrate failed', error)
  }
  return response
}

export async function reviewDesktopV3Worktrees(input: {
  workspacePath?: string
  catalogOnly?: boolean
  signal?: AbortSignal
  sessionIds?: string[]
  archiveSessionIds?: string[]
  archiveAll?: boolean
  promoteSessionIds?: string[]
  sourceHeadBySessionId?: Record<string, string>
  targetBranch?: string
  targetHead?: string
  commitSessionIds?: string[]
  automatic?: boolean
  graceHours?: number
} = {}): Promise<ReviewWorktreesResponse> {
  const actionIds = [...new Set([...(input.promoteSessionIds ?? []), ...(input.archiveSessionIds ?? [])])]
  const sessionIds = input.sessionIds ?? (!input.automatic && !input.archiveAll && !input.commitSessionIds?.length && actionIds.length > 0 ? actionIds : undefined)
  const response = await requestJson<ReviewWorktreesResponse>('/v3/sessions:review-worktrees', {
    method: 'POST',
    signal: input.signal,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      workspace_path: input.workspacePath?.trim() || undefined,
      catalog_only: input.catalogOnly || undefined,
      // Exact actions should not inspect every unrelated review lane afterward.
      session_ids: sessionIds,
      archive_session_ids: input.archiveSessionIds,
      archive_all: input.archiveAll,
      promote_session_ids: input.promoteSessionIds,
      source_head_by_session_id: input.sourceHeadBySessionId,
      target_branch: input.targetBranch?.trim() || undefined,
      target_head: input.targetHead?.trim() || undefined,
      commit_session_ids: input.commitSessionIds,
      automatic: input.automatic,
      grace_hours: input.graceHours ? String(input.graceHours) : undefined,
    }),
  })
  for (const sessionId of response.archived_session_ids ?? []) {
    dispatchDesktopV3Cache({
      type: 'mutation.sessionArchiveResult',
      raw: { ok: true, session_id: sessionId, archived: true, tombstone: { session_id: sessionId, kind: 'archived', archived: true } },
    })
  }
  return response
}
