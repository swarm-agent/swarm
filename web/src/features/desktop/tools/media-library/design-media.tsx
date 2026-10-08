import { useEffect, useMemo, useSyncExternalStore, type ReactNode } from 'react'
import { desktopDesigns } from '../../runtime/desktop-design-runtime'
import type { DesignResource } from '../../state/desktop-design-state'
import { designThreads, designReadyItems } from '../../orchestrate/design-media-task'
import { MediaTaskCard } from '../../orchestrate/media-task-card'
import type { MediaLibraryItem } from './types'

export function useDesignResource<T>(resource: DesignResource<T>) {
  const snapshot = useSyncExternalStore(resource.subscribe, resource.getSnapshot, resource.getSnapshot)
  useEffect(() => { void resource.refresh() }, [resource])
  return snapshot
}
export function useProjectDesigns(project: string, view: 'active' | 'archived' = 'active') {
  const resource = useMemo(() => desktopDesigns.project(project, view), [project, view])
  const snapshot = useSyncExternalStore(resource.subscribe, resource.getSnapshot, resource.getSnapshot)
  useEffect(() => { if (project) void resource.refresh() }, [project, resource])
  const items = useMemo(() => designReadyItems(snapshot.data?.designs ?? []), [snapshot.data])
  return { ...snapshot, items, refresh: resource.refresh }
}
export function DesignMediaTasks({ projectId, onPreview, column, archived = false, selectionControl, archiveDisabled }: { archived?: boolean; projectId: string; onPreview: (item: MediaLibraryItem) => void; column?: string; selectionControl?: (id: string) => ReactNode; archiveDisabled?: boolean }) {
  const { data, loading, error, refresh } = useProjectDesigns(projectId, archived ? 'archived' : 'active')
  return <>
    {loading && !data && <p role="status">Loading design tasks…</p>}
    {error && <p role="alert">{error} <button onClick={() => void refresh()}>Retry designs</button></p>}
    {designThreads((data?.designs ?? []).filter(row => row.request.candidates.some(candidate => Boolean(candidate.archived) === archived))).map(thread => {
      const current = thread.turns.find(row => ['queued', 'accepted', 'running', 'pending'].includes(row.request.state)) ?? thread.turns[thread.turns.length - 1]!
      const status = ['succeeded', 'partial_success'].includes(current.request.state) ? 'completed' : ['failed', 'cancelled', 'interrupted'].includes(current.request.state) ? 'failed' : ['queued', 'accepted'].includes(current.request.state) ? 'queued' : 'running'
      if (column && status !== column) return null
      return <MediaTaskCard key={thread.id} threadId={thread.id} source="independent-design" archived={archived} design={thread.turns[0]} designs={thread.turns} onDesignPreview={onPreview} selectionControl={selectionControl?.(thread.id)} archiveDisabled={archiveDisabled} />
    })}
    {(!column || column === 'queued') && data?.next_cursor && <button disabled={loading} onClick={() => void desktopDesigns.moreProject(projectId, archived ? 'archived' : 'active')}>{archived ? 'Load more archived items' : 'Load more'}</button>}
  </>
}
