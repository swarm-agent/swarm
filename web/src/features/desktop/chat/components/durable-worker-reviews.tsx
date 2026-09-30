import { useState } from 'react'
import { getDesktopSessionIdentitySnapshot } from '../../../../app/api'
import { useWorkerPage } from '../../runtime/desktop-workers'
import { PendingWorkerCard } from '../../orchestrate/pending-worker-card'

/** Canonical worker reviews in chat; no session plan or permission record is approval authority. */
export function DurableWorkerReviews() {
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  return accountScopeId ? <AccountReviews key={accountScopeId} accountScopeId={accountScopeId} /> : null
}
function AccountReviews({ accountScopeId }: { accountScopeId: string }) {
  const [cursor, setCursor] = useState<string | undefined>()
  const page = useWorkerPage({ kind: 'list', accountScopeId, limit: 25, cursor })
  const data = page?.data && 'workers' in page.data ? page.data : undefined
  const reviews = data?.workers.filter(worker => worker.account_scope_id === accountScopeId && (worker.lifecycle_state === 'pending' || worker.pending_review)) || []
  if (!reviews.length && !data?.next_cursor && !cursor && !page?.error) return null
  return <details className="max-h-80 overflow-y-auto rounded-lg border border-amber-500/30 p-2 text-xs" data-testid="chat-durable-worker-reviews"><summary>Worker reviews · {reviews.length} on this page · account-wide</summary>
    {page?.error && <p role="alert">Worker reviews unavailable: {page.error}</p>}
    {reviews.map(worker => <PendingWorkerCard key={`${accountScopeId}:${worker.id}`} worker={worker} accountScopeId={accountScopeId} stale={!!page?.stale || !!page?.error} mutationError={page?.mutationError} />)}
    {data?.next_cursor && <button type="button" onClick={() => setCursor(data.next_cursor)}>More workers to review</button>}{cursor && <button type="button" onClick={() => setCursor(undefined)}>Latest workers</button>}
  </details>
}
