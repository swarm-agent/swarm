import { useMemo, useState } from 'react'
import { useDesktopProject } from '../../runtime/desktop-projects'
import { MediaTaskCard } from '../../orchestrate/media-task-card'
import { deliverableToMediaItem } from '../../orchestrate/deliverable-media-item'
import type { MediaDeliverable, RunningTask } from '../../orchestrate/orchestrate-types'
import { MediaViewerModal } from '../../tools/media-library/media-viewer-modal'
import { manageMediaJobState, type ManageMediaJobRef } from '../services/manage-media-job'

const STATE_LABEL = { generating: 'Generating', ready: 'Ready', failed: 'Failed' } as const

/**
 * The card under a chat tool call that started a media job: the project's
 * media task as the Orchestrate studio draws it (thumbnails, poster, player
 * row, failure reason), with its state. Clicking a file opens the media viewer.
 */
export function ManageMediaJobCardView({ task, onPreview }: { task?: RunningTask | null; onPreview?: (output: MediaDeliverable) => void }) {
  const state = manageMediaJobState(task)
  return (
    <div className="w-full min-w-0 py-1.5" data-testid="manage-media-job-card" data-media-job-state={state} data-task-id={task?.id || undefined}>
      <div className="mb-1 flex items-center gap-2 px-1 text-[11px] text-[var(--app-text-muted)]" role="status">
        <span className={state === 'failed' ? 'text-[var(--app-danger)]' : state === 'ready' ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]'}>{STATE_LABEL[state]}</span>
        {task?.title ? <span className="min-w-0 truncate">{task.title}</span> : null}
      </div>
      {task ? <MediaTaskCard task={task} onPreview={onPreview} /> : null}
    </div>
  )
}

export function ManageMediaJobCard({ job }: { job: ManageMediaJobRef }) {
  const project = useDesktopProject(job.projectId)
  const task = project?.tasks.find(candidate => candidate.id === job.taskId) ?? null
  const items = useMemo(() => (task?.deliverables ?? [])
    .filter(output => (output.status === 'ready' || output.status === 'accepted') && Boolean(output.mediaUrl || output.previewUrl))
    .map(output => deliverableToMediaItem(output, task ?? undefined)), [task])
  const [open, setOpen] = useState<string | null>(null)
  const item = open ? items.find(candidate => candidate.id === open) ?? null : null
  return (
    <>
      <ManageMediaJobCardView task={task} onPreview={output => setOpen(output.id)} />
      {item ? <MediaViewerModal item={item} items={items} onClose={() => setOpen(null)} onSelect={next => setOpen(next.id)} /> : null}
    </>
  )
}
