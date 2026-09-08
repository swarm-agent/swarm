import { memo, useEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import { fetchAttachmentPage, selectWorkingWorkspaces, SessionAttachmentInventory, type AttachmentRepository } from '../queries/session-attachments'
import { subscribeDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { repositoryEventInvalidates, scheduleRepositoryRefresh } from '../../state/session-repositories'

export interface SessionAttachmentsViewProps {
  items: AttachmentRepository[]
  workingSources?: string[]
  loading: boolean
  stale: boolean
  error: boolean
  more: boolean
  onRefresh: () => void
  onMore: () => void
}

export function SessionAttachmentsView({ items, workingSources = [], loading, stale, error, more, onRefresh, onMore }: SessionAttachmentsViewProps) {
  const dialog = useRef<HTMLDialogElement>(null)
  const workingWorkspaces = selectWorkingWorkspaces(items, workingSources)
  const workspaceNames = workingWorkspaces.map(item => item.workspace_name || item.workspace_id).join(', ')
  const workingLabel = workspaceNames || (error ? 'Workspaces unavailable' : loading ? 'Loading workspaces' : more ? 'Workspaces loading incomplete' : 'No working workspace reported')
  const stateLabel = error ? (items.length ? 'Workspace list stale' : 'Workspaces unavailable') : loading && !items.length ? 'Loading workspaces' : more ? `${items.length}+ workspaces` : `${items.length} workspaces`
  // Identity owns a stable header slot. Cache invalidation is not a user-facing
  // failure: keep refresh details in the dialog, and reserve an error indicator
  // rather than appending text that resizes the title on every repository event.
  return <>
    <button type="button" aria-haspopup="dialog" aria-label={`Workspaces: ${workingLabel}${error ? ' — unable to update' : ''}`} title={`${workingLabel}${error ? ' — unable to update; open workspaces to retry' : stale ? ' — workspace information may be stale' : ''}`}
      className="relative block h-5 min-w-0 w-full max-w-full rounded pr-3 text-left text-[10px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] sm:h-7 sm:w-40 sm:shrink-0 sm:pl-2 sm:pr-5 sm:text-xs sm:font-normal lg:w-56"
      onClick={() => dialog.current?.showModal()}>
      <span className="block truncate">{workspaceNames || 'Workspaces'}</span>
      {error ? <span aria-hidden="true" className="absolute right-1 top-1/2 -translate-y-1/2 text-[var(--app-danger)]">!</span> : null}
    </button>
    <dialog ref={dialog} aria-label="Session workspaces" className="fixed m-auto max-h-[80dvh] w-[min(36rem,calc(100vw-2rem))] overflow-y-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-sm text-[var(--app-text)] backdrop:bg-black/40">
      <div className="flex items-center justify-between gap-2"><h2 className="font-semibold">Session workspaces</h2><button type="button" onClick={() => dialog.current?.close()} aria-label="Close workspaces">Close</button></div>
      <ul className="my-2 grid gap-1" aria-label="Working workspace list">
        {workingWorkspaces.map(item => <li key={item.workspace_id}>{item.workspace_name || item.workspace_id}</li>)}
      </ul>
      <p className="my-2 text-xs">The session workspace and workspaces with running delegated work. Available workspaces below are access, not work in progress.</p>
      {loading ? <p role="status" className="my-2 text-xs">{items.length ? 'Updating workspace information…' : 'Loading workspaces…'}</p> : null}
      {stale && !loading ? <p role="status" className="my-2 text-xs">Workspace information may be stale; displayed entries may no longer be attached.</p> : null}
      {error ? <p role="alert" className="my-2 text-xs">Unable to update workspace information. Refresh to retry.</p> : null}
      <details><summary className="cursor-pointer">Available workspaces</summary>
      <p role="status" className="my-2">{stateLabel}{error ? ' — unable to update; retry below.' : ''}</p>
      {!loading && !error && !more && !items.length ? <p>No attached workspaces.</p> : null}
      <ul className="grid gap-2" aria-label="Attached workspace list">
        {items.map((item) => <li key={item.workspace_id} data-workspace-id={item.workspace_id} className="min-w-0 rounded border border-[var(--app-border)] p-2 [overflow-wrap:anywhere]">
          <div className="font-medium">{item.workspace_name || item.workspace_id}{item.default ? <span className="ml-2 text-xs">Default</span> : null}</div>
          <div className="text-xs text-[var(--app-text-muted)]">{item.workspace_id}</div>
          <div className="text-xs">Source: {item.source_path}</div>
          {item.availability !== 'available' ? <p className="text-xs">Repository unavailable{item.error ? `: ${item.error}` : ''}</p> : null}
        </li>)}
      </ul>
      {!items.some(item => item.default) && !loading ? <p className="mt-2 text-xs">No explicit default in the loaded attachments.</p> : null}
      {more ? <p>Partial inventory — continue scanning for attached workspaces. Each step reads at most 80 repository rows; only 64 attachment identities are retained.</p> : null}
      </details>
      <div className="mt-3 flex gap-3"><button type="button" disabled={loading} onClick={onRefresh}>Refresh</button>{more ? <button type="button" disabled={loading || stale} onClick={onMore}>Load more workspaces</button> : null}</div>
    </dialog>
  </>
}

export const SessionAttachments = memo(function SessionAttachments({ sessionId }: { sessionId: string }) {
  const inventory = useMemo(() => new SessionAttachmentInventory((cursor, signal) => fetchAttachmentPage(sessionId, cursor, signal)), [sessionId])
  const state = useSyncExternalStore(inventory.subscribe, inventory.snapshot, inventory.snapshot)
  // Canonical cache actions own refreshes, independently of header timer ticks
  // and token-only projection revisions.
  useEffect(() => {
    const scheduler = scheduleRepositoryRefresh(inventory, () => document.visibilityState !== 'hidden')
    const unsubscribe = subscribeDesktopV3Cache(mutation => {
      if (!mutation || repositoryEventInvalidates(mutation.action, new Set([sessionId, ...inventory.state.workingSessionIds]))) scheduler.invalidate()
    })
    document.addEventListener('visibilitychange', scheduler.visibilityChanged)
    if (document.visibilityState !== 'hidden') void inventory.refresh()
    return () => {
      unsubscribe(); scheduler.dispose()
      document.removeEventListener('visibilitychange', scheduler.visibilityChanged)
    }
  }, [inventory, sessionId])
  return <SessionAttachmentsView {...state} more={Boolean(state.nextCursor)}
    onRefresh={() => { void inventory.refresh() }} onMore={() => { void inventory.loadMore() }} />
})
