import { useState } from 'react'
import { DesignMediaTasks } from '../tools/media-library/design-media'
import type { MediaLibraryItem } from '../tools/media-library/types'
import type { RunningTask } from './orchestrate-types'
import { isCreativeMediaTask, MediaTaskThreads, type MediaTaskActions } from './media-task-card'

// Project runtimes retain ownership of the existing generation records.
export function ProjectMediaTasks({ projectId, tasks, actions, onPreviewDesign, onLibrary, loading, error, onRetry,
  archivedTasks, archivedLoading, archivedError, onLoadArchived,
}: {
  projectId: string; tasks: readonly RunningTask[]; actions: (task: RunningTask) => MediaTaskActions
  onPreviewDesign: (item: MediaLibraryItem) => void; onLibrary: () => void
  loading?: boolean; error?: string; onRetry: () => void
  archivedTasks: readonly RunningTask[]; archivedLoading: boolean; archivedError: string; onLoadArchived: () => void
}) {
  const [archived, setArchived] = useState(false)
  const media = tasks.filter(isCreativeMediaTask)
  const archivedMedia = archivedTasks.filter(isCreativeMediaTask)
  return <>
    <header className="flex items-center justify-between gap-2"><h2>Project media</h2><button type="button" onClick={onLibrary}>Historical library</button></header>
    {loading && !media.length && <p role="status">Loading media…</p>}
    {error && <p role="alert">{error} <button type="button" onClick={onRetry}>Retry media</button></p>}
    <DesignMediaTasks projectId={projectId} onPreview={onPreviewDesign} />
    <MediaTaskThreads tasks={media} visibleTaskIds={new Set(media.map(task => task.id))} actions={actions} />
    {!loading && !error && !media.length && <p className="text-xs text-slate-400">No image, video or audio generations yet.</p>}
    <button type="button" aria-expanded={archived} onClick={() => { if (!archived) onLoadArchived(); setArchived(!archived) }}>Archived media</button>
    {archived && <section aria-label="Archived media" className="space-y-3">
      <DesignMediaTasks projectId={projectId} archived onPreview={onPreviewDesign} />
      {archivedLoading && <p role="status">Loading archived media…</p>}
      {archivedError && <p role="alert">{archivedError} <button type="button" onClick={onLoadArchived}>Retry archived media</button></p>}
      <MediaTaskThreads tasks={archivedMedia} visibleTaskIds={new Set(archivedMedia.map(task => task.id))} actions={task => ({ onPreview: actions(task).onPreview })} />
    </section>}
  </>
}
