import { useState } from 'react'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { useWorkerPage } from '../runtime/desktop-workers'
import { workerDetailHref, workerLifecycleLabel } from '../orchestrate/worker-presentation'

export function DurableWorkerSidebar({ workspaceSlug, onOpen }: {
  workspaceSlug?: string; onOpen?: () => void
}) {
  const [collapsed, setCollapsed] = useState(false)
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  if (!accountScopeId) return null
  return <WorkerSidebarAccount key={accountScopeId} accountScopeId={accountScopeId} workspaceSlug={workspaceSlug} collapsed={collapsed} onToggle={() => setCollapsed(!collapsed)} onOpen={onOpen} />
}
function WorkerSidebarAccount({ accountScopeId, workspaceSlug, collapsed, onToggle, onOpen }: {
  accountScopeId: string; workspaceSlug?: string; collapsed: boolean; onToggle: () => void; onOpen?: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  // Same first-page query as WorkerHub; shared demand survives navigation and refresh.
  const page = useWorkerPage({ kind: 'list', accountScopeId, limit: 100 })
  const workers = page?.data && 'workers' in page.data ? page.data.workers : []
  const more = page?.data && 'workers' in page.data ? page.data.next_cursor : undefined
  return <section className="grid min-w-0 gap-1.5" aria-label="Workers" data-testid="durable-worker-sidebar">
    <header className="flex items-center gap-2 px-1 text-[10px] font-semibold text-[var(--app-text-muted)]">
      <button aria-label={`${collapsed ? 'Expand' : 'Collapse'} Workers section`} aria-expanded={!collapsed} onClick={onToggle}>{collapsed ? '▸' : '▾'}</button>
      <span>Workers</span><span title="Account-wide durable workers; + indicates additional pages">{page?.data ? `${workers.length}${more ? '+' : ''}` : '…'}</span>
      {onOpen && <button className="ml-auto hover:text-[var(--app-text)]" onClick={onOpen}>View</button>}
    </header>
    {page?.error && <p role="alert" className="px-2 text-[10px] text-[var(--app-warning)]">Workers unavailable: {page.error}</p>}
    {page?.stale && page.data && <p className="px-2 text-[10px] text-[var(--app-text-muted)]">Refreshing · last-known list</p>}
    {!collapsed && <>
      {(expanded ? workers : workers.slice(0, 5)).map(worker => <a key={worker.id} href={workerDetailHref(worker.id, workspaceSlug)} className="grid min-w-0 gap-1 rounded-lg px-2 py-2 text-xs hover:bg-[var(--app-surface-hover)]"><span className="truncate font-medium text-[var(--app-text)]">{worker.name}</span><span className="text-[10px] text-[var(--app-text-muted)]">{workerLifecycleLabel(worker.lifecycle_state)}{worker.pending_review ? ' · Changes pending approval' : ''} · {worker.automations?.length || 0} jobs</span></a>)}
      {!page?.loading && !page?.error && !workers.length && <p className="px-2 text-[10px] text-[var(--app-text-muted)]">No workers yet</p>}
      {workers.length > 5 && <button className="text-[10px] text-[var(--app-text-muted)]" onClick={() => setExpanded(!expanded)}>{expanded ? 'Show fewer' : `Show ${workers.length - 5} more`}</button>}
      {more && onOpen && <button className="text-[10px] text-[var(--app-text-muted)]" onClick={onOpen}>Browse all workers</button>}
    </>}
  </section>
}

export function DurableWorkerCount({ accountScopeId }: { accountScopeId: string }) {
  const page = useWorkerPage({ kind: 'list', accountScopeId, limit: 100 })
  const data = page?.data && 'workers' in page.data ? page.data : undefined
  const pendingPage = useWorkerPage({ kind: 'list', accountScopeId, limit: 100 })
  const pending = pendingPage?.data && 'workers' in pendingPage.data ? pendingPage.data : undefined
  return <span title={page?.error ? `Workers unavailable: ${page.error}` : 'Account-wide workers; + indicates more pages'}>{data ? `${data.workers.length}${data.next_cursor ? '+' : ''}${page?.stale ? ' ·' : ''}` : '…'}{pending && pending.workers.filter(worker => worker.lifecycle_state === 'pending' || worker.pending_review).length > 0 ? ` · ${pending.workers.filter(worker => worker.lifecycle_state === 'pending' || worker.pending_review).length}${pending.next_cursor ? '+' : ''} pending approval` : ''}{pendingPage?.error ? ' · approvals unavailable' : ''}</span>
}
