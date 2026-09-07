import { useEffect, useRef } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { fetchAttachmentPage, selectSessionAttachments, type AttachmentRepository } from '../queries/session-attachments'

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
  const stateLabel = error ? (items.length ? 'Workspace list stale' : 'Workspaces unavailable') : loading ? 'Loading workspaces' : stale ? 'Refreshing workspaces' : more ? `${items.length}+ workspaces` : `${items.length} workspaces`
  return <>
    <button type="button" aria-haspopup="dialog" aria-label={`Session workspaces: ${stateLabel}`} title={defaultWorkspace ? `Default: ${defaultWorkspace.workspace_name}` : 'No default workspace reported'}
      className="max-w-40 truncate rounded px-2 py-1 text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] sm:max-w-64"
      onClick={() => dialog.current?.showModal()}>{stateLabel}{defaultWorkspace ? ` · Default: ${defaultWorkspace.workspace_name || defaultWorkspace.workspace_id}` : ''}</button>
    <dialog ref={dialog} aria-label="Session workspaces" className="fixed m-auto max-h-[80dvh] w-[min(36rem,calc(100vw-2rem))] overflow-y-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-sm text-[var(--app-text)] backdrop:bg-black/40">
      <div className="flex items-center justify-between gap-2"><h2 className="font-semibold">Attached workspaces</h2><button type="button" onClick={() => dialog.current?.close()} aria-label="Close workspaces">Close</button></div>
      <p className="my-2 text-xs">Source attachments only. Runtime branch is shown separately in the header; retained worker repositories remain in Git.</p>
      <p role="status" className="my-2">{stateLabel}{stale || error ? ' — displayed entries may no longer be attached.' : ''}</p>
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
      <div className="mt-3 flex gap-3"><button type="button" disabled={loading || stale} onClick={onRefresh}>Refresh</button>{more ? <button type="button" disabled={loading || stale} onClick={onMore}>Load more workspaces</button> : null}</div>
    </dialog>
  </>
}

export function SessionAttachments({ sessionId, revision }: { sessionId: string; revision?: number }) {
  const query = useInfiniteQuery({
    queryKey: ['session-header-attachments', sessionId],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => fetchAttachmentPage(sessionId, pageParam, signal),
    getNextPageParam: (page, pages) => {
      const cursor = page.next_cursor
      if (!cursor || pages.length >= 16 || pages.slice(0, -1).some((previous) => previous.next_cursor === cursor)) return undefined
      return cursor
    },
    retry: false,
    staleTime: 15_000,
    refetchInterval: 30_000,
    refetchOnWindowFocus: 'always',
    refetchOnReconnect: 'always',
  })
  const { refetch } = query
  useEffect(() => { void refetch() }, [revision, refetch])
  const pages = query.data?.pages ?? []
  // Automatically read at most four sequential pages (80 rows, 64 attachments).
  // Further history traversal always requires a user gesture.
  useEffect(() => {
    if (pages.length > 0 && pages.length < 4 && query.hasNextPage && !query.isFetching && !query.isError) void query.fetchNextPage()
  }, [pages.length, query.hasNextPage, query.isFetching, query.isError, query.fetchNextPage])
  const items = selectSessionAttachments(pages)
  const repeatedCursor = Boolean(pages[pages.length - 1]?.next_cursor) && !query.hasNextPage
  return <SessionAttachmentsView items={items} loading={query.isPending || query.isFetchingNextPage}
    stale={query.isFetching && !query.isPending && !query.isFetchingNextPage} error={query.isError || repeatedCursor}
    more={Boolean(query.hasNextPage)} onRefresh={() => { void refetch() }} onMore={() => { void query.fetchNextPage() }} />
}
