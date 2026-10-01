import { useState } from 'react'
import { Button } from '../../../../components/ui/button'
import { useDesktopV3CacheSelector, getDesktopV3CacheSnapshot } from '../../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'
import type { AutomationV2Mutation, AutomationV2Response } from '../../state/desktop-automation-v2-api'
import { selectPendingWorkerSidebarReviews } from '../../state/desktop-automation-v2-state'
import { automationV2Review } from '../../state/desktop-automation-v2-api'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import { normalizeStructuredPlanDocument, StructuredPlanReviewView } from '../../chat/components/structured-plan-document'
import { scheduleLabel } from './automation-v2-schedule'

// The mutation accepts only the exact review and its server-authoritative scope.
// A newer revision must be inspected before an old button can be used.
export async function decidePendingWorkerReview(
  state: DesktopV3CacheState,
  id: string,
  shownRevision: number,
  shownDigest: string,
  action: 'accept_automation' | 'decline_automation',
  mutate: (input: AutomationV2Mutation) => Promise<AutomationV2Response>,
) {
  const current = selectPendingWorkerSidebarReviews(state).find(({ permission }) => permission.id === id)
  if (!current || current.proposal.revision !== shownRevision || current.proposal.digest !== shownDigest) {
    throw new Error('Worker review changed. Inspect the latest revision before deciding.')
  }
  const { proposal } = current
  const response = await mutate({ action, workspace_id: proposal.workspace_id, session_id: proposal.session_id, review: automationV2Review(proposal) })
  if (action === 'accept_automation' && (!response.record || response.record.digest !== proposal.digest || response.record.session_id !== proposal.session_id || !response.record.automation_id)) {
    throw new Error('Worker acceptance result unavailable. Refresh before retrying.')
  }
  return response
}

/** The sidebar reads the canonical pending permission; it never promotes a chat into a worker. */
export function PendingWorkerSidebarReviews({ onOpenChat }: { onOpenChat: (sessionId: string) => void }) {
  const reviews = useDesktopV3CacheSelector(selectPendingWorkerSidebarReviews, (a, b) =>
    a.length === b.length && a.every((item, index) =>
      item.permission.id === b[index].permission.id && item.permission.toolArguments === b[index].permission.toolArguments),
  )
  const [expanded, setExpanded] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<{ id: string; message: string } | null>(null)

  async function decide(id: string, shownRevision: number, shownDigest: string, action: 'accept_automation' | 'decline_automation') {
    if (busy) return
    setBusy(id)
    setError(null)
    try {
      // Re-read the permission immediately before acting. A stale card is not authority.
      await decidePendingWorkerReview(getDesktopV3CacheSnapshot(), id, shownRevision, shownDigest, action, input => desktopAutomationV2.mutate(input))
      // V3 reconciliation removes the resolved permission; accepted workers are listed by record.
    } catch (cause) {
      setError({ id, message: cause instanceof Error ? cause.message : 'Worker decision failed. Refresh before retrying.' })
    } finally {
      setBusy(null)
    }
  }

  if (!reviews.length) return null
  return <section aria-label="Pending worker reviews" className="ml-5 grid min-w-0 gap-1 border-l border-[var(--app-border)] pl-2" data-testid="sidebar-pending-workers">
    {reviews.map(({ permission, proposal }) => {
      const reviewKey = `${permission.id}:${proposal.revision}:${proposal.digest}`
      const open = expanded === reviewKey
      const document = normalizeStructuredPlanDocument(proposal.document)
      return <article key={permission.id} className="min-w-0 rounded-lg bg-[var(--app-surface)] p-2 text-xs" data-testid="sidebar-pending-worker">
        <div className="flex min-w-0 items-center justify-between gap-2">
          <span className="min-w-0 truncate font-semibold text-[var(--app-text)]" title={proposal.document.title}>{proposal.document.title}</span>
          <button type="button" className="shrink-0 rounded border border-[var(--app-warning)] px-1.5 py-0.5 font-semibold text-[var(--app-warning)]" aria-expanded={open} aria-controls={`worker-review-${permission.id}`} onClick={() => setExpanded(open ? null : reviewKey)}>Pending</button>
        </div>
        {open ? <p className="mt-1 text-[var(--app-text-muted)]">Revision {proposal.revision} · {scheduleLabel(proposal.document.automation_v2.schedule)}</p> : null}
        {open ? <div id={`worker-review-${permission.id}`} className="mt-2 max-h-64 overflow-y-auto rounded border border-[var(--app-border)] p-2" data-testid="sidebar-worker-review-details">
          <p className="mb-2 break-words">{proposal.document.info.goal}</p>
          {document ? <StructuredPlanReviewView document={document} /> : <p role="alert">Worker plan unavailable. Refresh before accepting.</p>}
          <p className="mt-2">Expiration: {proposal.document.automation_v2.expiration?.kind === 'at' ? new Date(proposal.document.automation_v2.expiration.expires_at!).toLocaleString() : 'Until stopped'}</p>
        </div> : null}
        {error?.id === permission.id ? <p role="alert" className="mt-2 break-words text-[var(--app-danger)]">{error.message}</p> : null}
        {open ? <div className="mt-2 flex flex-wrap gap-1">
          <Button size="sm" variant="outline" disabled={!!busy} onClick={() => onOpenChat(proposal.session_id)}>Open chat</Button>
          <Button size="sm" variant="outline" disabled={!!busy || !open || !document} onClick={() => void decide(permission.id, proposal.revision, proposal.digest, 'decline_automation')}>Decline</Button>
          <Button size="sm" disabled={!!busy || !open || !document} onClick={() => void decide(permission.id, proposal.revision, proposal.digest, 'accept_automation')}>Accept</Button>
        </div> : null}
      </article>
    })}
  </section>
}
