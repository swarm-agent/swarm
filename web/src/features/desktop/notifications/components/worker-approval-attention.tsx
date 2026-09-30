import { getDesktopSessionIdentitySnapshot } from '../../../../app/api'
import { useWorkerPage } from '../../runtime/desktop-workers'

// Derived attention, not a notification record. Opening/clearing the inbox cannot approve a worker.
export function useWorkerApprovalAttention(accountScopeId: string) {
  const page = useWorkerPage({ kind: 'list', accountScopeId, limit: 100 })
  const data = page?.data && 'workers' in page.data ? page.data : undefined
  return { page, workers: (data?.workers || []).filter(worker => worker.account_scope_id === accountScopeId && (worker.lifecycle_state === 'pending' || worker.pending_review)), partial: !!data?.next_cursor }
}
export function WorkerApprovalAttention({ accountScopeId, onInspect, onBrowse }: {
  accountScopeId: string; onInspect: (id: string) => void; onBrowse: () => void
}) {
  const { page, workers, partial } = useWorkerApprovalAttention(accountScopeId)
  return <section aria-label="Worker approvals" className="space-y-2 p-4 text-sm">
    <h3 className="font-semibold">Worker approvals · {workers.length}{partial ? '+' : ''}</h3>
    {page?.error && <p role="alert">Approvals unavailable: {page.error}</p>}
    {page?.stale && <p role="status">Refreshing · last-known approvals</p>}
    {page?.loading && !page.data && <p role="status">Checking worker approvals…</p>}
    {workers.map(worker => <button type="button" key={worker.id} className="block w-full break-words rounded-lg border border-slate-700 p-2 text-left" onClick={() => onInspect(worker.id)}>{worker.name} · Pending approval</button>)}
    {partial && <button type="button" onClick={onBrowse}>Browse more approvals in Workers</button>}
  </section>
}

export function WorkerApprovalBadge({ accountScopeId }: { accountScopeId: string }) {
  const attention = useWorkerApprovalAttention(accountScopeId)
  if (attention.page?.error) return <span className="text-[9px]" title="Worker approvals unavailable">!</span>
  return attention.workers.length || attention.partial ? <span className="text-[9px] text-amber-300" aria-label={`${attention.workers.length}${attention.partial ? '+' : ''} workers pending approval`}>{attention.workers.length}{attention.partial ? '+' : ''}</span> : null
}

export function AccountWorkerApprovalBadge() {
  const account = getDesktopSessionIdentitySnapshot()?.accountScopeId
  return account ? <WorkerApprovalBadge key={account} accountScopeId={account} /> : null
}
