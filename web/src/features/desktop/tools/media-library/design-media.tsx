import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { desktopDesigns } from '../../runtime/desktop-design-runtime'
import type { DesignResource } from '../../state/desktop-design-state'
import { designRequestId, designReadyItems } from '../../orchestrate/design-media-task'
import { MediaTaskCard } from '../../orchestrate/media-task-card'
import type { MediaLibraryItem } from './types'

export function useDesignResource<T>(resource: DesignResource<T>) {
  const snapshot = useSyncExternalStore(resource.subscribe, resource.getSnapshot, resource.getSnapshot)
  useEffect(() => { void resource.refresh() }, [resource])
  return snapshot
}
export function useProjectDesigns(project: string) {
  const resource = useMemo(() => desktopDesigns.project(project), [project])
  const snapshot = useSyncExternalStore(resource.subscribe, resource.getSnapshot, resource.getSnapshot)
  useEffect(() => { if (project) void resource.refresh() }, [project, resource])
  const items = useMemo(() => designReadyItems(snapshot.data?.designs ?? []), [snapshot.data])
  return { ...snapshot, items, refresh: resource.refresh }
}
export function DesignMediaTasks({ projectId, onPreview, column }: { projectId: string; onPreview: (item: MediaLibraryItem) => void; column?: string }) {
  const { data, loading, error, refresh } = useProjectDesigns(projectId)
  return <>
    {loading && !data && <p role="status">Loading design tasks…</p>}
    {error && <p role="alert">{error} <button onClick={() => void refresh()}>Retry designs</button></p>}
    {data?.designs.filter(row => !column || (['succeeded', 'partial_success'].includes(row.request.state) ? 'completed' : ['failed', 'cancelled', 'interrupted'].includes(row.request.state) ? 'failed' : ['queued', 'accepted'].includes(row.request.state) ? 'queued' : 'running') === column).map(row => <MediaTaskCard key={designRequestId(row)} source="independent-design" design={row} onDesignPreview={onPreview} />)}
    {(!column || column === 'queued') && data?.next_cursor && <button disabled={loading} onClick={() => void desktopDesigns.moreProject(projectId)}>More design requests</button>}
  </>
}
