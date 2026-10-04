import { useState, type ReactNode } from 'react'
import { DesignMediaTasks } from '../tools/media-library/design-media'
import type { MediaLibraryItem } from '../tools/media-library/types'
import type { RunningTask } from './orchestrate-types'
import { isCreativeMediaTask, MediaTaskThreads, type MediaTaskActions } from './media-task-card'

// Both panes stay mounted so switching tabs does not reset a conversation draft,
// selected creative turn, or a ready preview. Data remains owned by project runtimes.
export function ProjectRightSidebar({ tab, onTab, media, children }: {
  tab: 'chat' | 'media'; onTab: (tab: 'chat' | 'media') => void; media: ReactNode; children: ReactNode
}) {
  return <section className="swarm-conversation-panel swarm-project-right-sidebar" aria-label="Project sidebar">
    <div role="tablist" aria-label="Project sidebar views" className="swarm-sidebar-tabs">
      {(['chat', 'media'] as const).map(value => <button key={value} id={`project-sidebar-${value}-tab`} type="button" role="tab"
        aria-selected={tab === value} aria-controls={`project-sidebar-${value}`} tabIndex={tab === value ? 0 : -1}
        onClick={() => onTab(value)} onKeyDown={event => {
          if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) {
            event.preventDefault()
            const next = event.key === 'Home' ? 'chat' : event.key === 'End' ? 'media' : value === 'chat' ? 'media' : 'chat'
            onTab(next)
            document.getElementById(`project-sidebar-${next}-tab`)?.focus()
          }
        }}>{value === 'chat' ? 'Chat' : 'Media'}</button>)}
    </div>
    <div id="project-sidebar-chat" role="tabpanel" aria-labelledby="project-sidebar-chat-tab" hidden={tab !== 'chat'} className="swarm-sidebar-pane">{children}</div>
    <div id="project-sidebar-media" role="tabpanel" aria-labelledby="project-sidebar-media-tab" hidden={tab !== 'media'} className="swarm-sidebar-pane swarm-sidebar-media">{media}</div>
  </section>
}

export function ProjectMediaSidebar({ projectId, tasks, actions, onPreviewDesign, onLibrary, loading, error, onRetry,
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
