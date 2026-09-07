import { useEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import { fetchAttachmentPage, SessionAttachmentInventory, type AttachmentRepository } from '../queries/session-attachments'
import { subscribeDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { repositoryEventInvalidates, scheduleRepositoryRefresh } from '../../state/session-repositories'

export interface SessionAttachmentsViewProps {
  items: AttachmentRepository[]
  loading: boolean
  stale: boolean
  error: boolean
  more: boolean
  onRefresh: () => void
  onMore: () => void
}

export function SessionAttachmentsView({ items, loading, stale, error, more, onRefresh, onMore }: SessionAttachmentsViewProps) {
  const dialog = useRef<HTMLDialogElement>(null)
  const defaultWorkspace = items.find((item) => item.default)
  const stateLabel = error ? (items.length ? 'Workspace list stale' : 'Workspaces unavailable') : loading && !items.length ? 'Loading workspaces' : more ? `${items.length}+ workspaces` : `${items.length} workspaces`
  return <>
    <button type="button" aria-haspopup="dialog" aria-label={`Session workspaces: ${stateLabel}`} title={defaultWorkspace ? `Default: ${defaultWorkspace.workspace_name}` : 'No default workspace reported'}
      className="max-w-40 truncate rounded px-2 py-1 text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] sm:max-w-64"
      onClick={() => dialog.current?.showModal()}>{stateLabel}{defaultWorkspace ? ` · Default: ${defaultWorkspace.workspace_name || defaultWorkspace.workspace_id}` : ''}</button>
    <dialog ref={dialog} aria-label="Session workspaces" className="fixed m-auto max-h-[80dvh] w-[min(36rem,calc(100vw-2rem))] overflow-y-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-sm text-[var(--app-text)] backdrop:bg-black/40">
      <div className="flex items-center justify-between gap-2"><h2 className="font-semibold">Attached workspaces</h2><button type="button" onClick={() => dialog.current?.close()} aria-label="Close workspaces">Close</button></div>
      <p className="my-2 text-xs">Source attachments only. Runtime branch is shown separately in the header; retained worker repositories remain in Git.</p>
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
      {!defaultWorkspace && !loading ? <p className="mt-2 text-xs">No explicit default in the loaded attachments.</p> : null}
      {more ? <p>Partial inventory — continue scanning for attached workspaces. Each step reads at most 80 repository rows; only 64 attachment identities are retained.</p> : null}
      <div className="mt-3 flex gap-3"><button type="button" disabled={loading} onClick={onRefresh}>Refresh</button>{more ? <button type="button" disabled={loading || stale} onClick={onMore}>Load more workspaces</button> : null}</div>
    </dialog>
  </>
}

export function SessionAttachments({ sessionId, revision }: { sessionId: string; revision?: number }) {
  const inventory = useMemo(() => new SessionAttachmentInventory((cursor, signal) => fetchAttachmentPage(sessionId, cursor, signal)), [sessionId])
  const state = useSyncExternalStore(inventory.subscribe, inventory.snapshot, inventory.snapshot)
  // revision includes token-only changes. Canonical cache actions below carry
  // relevant invalidations instead of cancel/refetch on every projection revision.
  void revision
  useEffect(() => {
    const scheduler = scheduleRepositoryRefresh(inventory, () => document.visibilityState !== 'hidden')
    const unsubscribe = subscribeDesktopV3Cache(mutation => {
      if (!mutation || repositoryEventInvalidates(mutation.action, new Set([sessionId]), true)) scheduler.invalidate()
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
}
