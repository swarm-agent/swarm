import type { ReactNode } from 'react'
import { taskPreviewURL } from './task-preview-url'
import { DesignArchiveButton } from '../tools/media-library/design-archive-button'
import { DesignThumbnail } from '../tools/media-library/design-thumbnail'
import { creativeThreads } from '../tools/media-library/creative-thread'
import type { ProjectDesign } from '../session-v3/design-api'
import type { MediaLibraryItem } from '../tools/media-library/types'
import type { MediaDeliverable, RunningTask } from './orchestrate-types'
import type { QuickRouteMode } from '../tools/media-library/media-viewer-modal'
import { designMediaItem, designRequestId, designStatus, readyDesignRevision } from './design-media-task'
import { CreativeThreadCard, type CreativeCardTurn } from './creative-thread-card'
import { TaskAttention, useTaskAttention } from './task-attention'

export const isCreativeMediaTask = (task: RunningTask) => ['image', 'video', 'audio', 'sound'].includes(task.agentType)
export function mediaTaskThreads(tasks: readonly RunningTask[]) {
  return creativeThreads(tasks.filter(isCreativeMediaTask).map(task => ({
    id: task.id, value: task, outputs: (task.deliverables ?? []).map(output => output.id),
    parents: [...(task.deliverables ?? []).flatMap(output => [output.parentDeliverableId, output.sourceMediaRef].filter((id): id is string => Boolean(id))), ...(task.attachedMedia ?? []).map(media => media.id)],
  })))
}
function MediaTaskAttention({ task }: { task: RunningTask }) {
  const attention = useTaskAttention(task)
  return <TaskAttention attention={attention} />
}
export interface MediaTaskActions {
  onPreview?: (output: MediaDeliverable, mode?: QuickRouteMode) => void
  onApprove?: () => void
  onArchive?: () => void
  onDelete?: () => void
  isApproving?: boolean
  error?: string
}
type TaskCardProps = MediaTaskActions & { task: RunningTask; attention?: ReactNode }
type DesignCardProps = { source: 'independent-design'; archived?: boolean; design: ProjectDesign; designs?: readonly ProjectDesign[]; threadId?: string; onDesignPreview: (item: MediaLibraryItem) => void; selectionControl?: ReactNode; archiveDisabled?: boolean }

export function MediaTaskCard(props: TaskCardProps | DesignCardProps) {
  if ('source' in props) {
    const rows = props.designs ?? [props.design]
    const turns: CreativeCardTurn[] = rows.flatMap(row => {
      const visible = row.request.candidates.map((candidate, index) => ({ candidate, index })).filter(({ candidate }) => Boolean(candidate.archived) === Boolean(props.archived))
      if (!visible.length) return []
      return [{
        id: designRequestId(row), title: row.title, status: designStatus(row.request.state),
        outputs: visible.map(({ candidate, index }) => {
          const revision = readyDesignRevision(candidate)
          return {
            id: JSON.stringify([designRequestId(row), index]), candidateNumber: index + 1, title: `${row.title} candidate ${index + 1}`, status: designStatus(candidate.state), ready: Boolean(revision),
            preview: revision ? <DesignThumbnail session={row.request.parent_session_id} revision={revision} /> : null,
            open: () => { if (revision) props.onDesignPreview(designMediaItem(row, index, revision)) },
            actions: revision && !props.archiveDisabled ? <DesignArchiveButton key={`${row.project_id}:${row.request.parent_session_id}:${revision.ref.artifact_id}`} session={row.request.parent_session_id} reference={revision.ref} version={candidate.archive_version ?? 0} archived={candidate.archived} /> : undefined,
          }
        }),
        alerts: visible.map(({ candidate, index }) => <div key={index}>
          {candidate.failure_reason && <p role="alert">{candidate.failure_reason}</p>}
          {candidate.router_alert && <p role="alert">{candidate.router_alert}</p>}
          {candidate.attempts?.map(attempt => <div key={attempt.number} className="text-xs">{attempt.reason_code && <p>Attempt {attempt.number}: {designStatus(attempt.state)} {attempt.reason_code}</p>}{attempt.router_alert && <p role="alert">{attempt.router_alert}</p>}</div>)}
        </div>),
      }]
    })
    if (!turns.length) return null
    return <CreativeThreadCard id={props.threadId ?? designRequestId(props.design)} title={props.design.title} studio="Design" turns={turns} selectionControl={props.selectionControl} />
  }
  return <CreativeThreadCard id={props.task.id} title={props.task.title} studio={props.task.agentType} turns={[artifactTurn(props.task, props)]} attention={props.attention} />
}
function artifactTurn(task: RunningTask, actions: MediaTaskActions): CreativeCardTurn {
  return {
    id: task.id, title: task.description || task.title, status: task.status,
    alerts: (actions.error || task.lastError) && <p role="alert">{actions.error || task.lastError}</p>,
    controls: <footer className="flex flex-wrap gap-3 text-xs">
      {task.status === 'pending_approval' && actions.onApprove && <button type="button" onClick={actions.onApprove} disabled={actions.isApproving}>{actions.isApproving ? 'Starting…' : Math.max(task.variantCount || 0, task.deliverables?.length || 0) >= 25 ? `Confirm ${Math.max(task.variantCount || 0, task.deliverables?.length || 0)} iterations` : 'Generate media'}</button>}
      {actions.onArchive && <button type="button" onClick={actions.onArchive}>Archive</button>}
      {actions.onDelete && <button type="button" onClick={actions.onDelete}>Delete</button>}
    </footer>,
    outputs: (task.deliverables ?? []).map(output => {
      const url = output.mediaUrl || output.previewUrl
      const available = ['ready', 'accepted'].includes(output.status) && Boolean(url)
      const thumbnail = taskPreviewURL(output.previewUrl || (output.type !== 'audio' ? output.mediaUrl : undefined))
      return {
        id: output.id, title: output.title, status: output.status, ready: available,
        preview: available ? thumbnail ? <img src={thumbnail} alt={output.title} loading="lazy" decoding="async" /> : <span>{output.type} ready · open preview</span> : null,
        open: () => actions.onPreview?.(output),
        actions: <>
          <button type="button" disabled={!available} onClick={() => actions.onPreview?.(output)}>Preview / save</button>
          {output.type === 'image' && <><button type="button" disabled={!available} onClick={() => actions.onPreview?.(output, 'fine_tune')}>Edit</button><button type="button" disabled={!available} onClick={() => actions.onPreview?.(output, 'to_video')}>Turn into video</button></>}
          {output.type === 'video' && <button type="button" disabled={!available} onClick={() => actions.onPreview?.(output, 'next_scene')}>Continue video</button>}
          {available && <a href={url} download={`${output.id}.${output.type === 'image' ? 'png' : output.type === 'video' ? 'mp4' : 'wav'}`}>Download</a>}
          {output.status === 'failed' && output.description && <p role="alert">{output.description}</p>}
        </>,
      }
    }),
  }
}
export function MediaTaskThreads({ tasks, visibleTaskIds, column, actions, selectionControl }: {
  tasks: readonly RunningTask[]; visibleTaskIds: ReadonlySet<string>; column?: string; actions: (task: RunningTask) => MediaTaskActions
  selectionControl?: (tasks: readonly RunningTask[]) => ReactNode
}) {
  return <>{mediaTaskThreads(tasks).map(thread => {
    if (!thread.turns.some(task => visibleTaskIds.has(task.id))) return null
    const active = thread.turns.find(task => ['running', 'in_progress', 'queued', 'pending', 'pending_approval'].includes(task.status)) ?? thread.turns[thread.turns.length - 1]!
    const status = ['running', 'in_progress'].includes(active.status) ? 'running' : ['queued', 'pending', 'pending_approval', 'planning'].includes(active.status) ? 'queued' : active.status
    if (column && status !== column) return null
    return <CreativeThreadCard key={thread.id} id={thread.id} title={thread.turns[0].title} studio={thread.turns[0].agentType} turns={thread.turns.map(task => artifactTurn(task, actions(task)))} selectionControl={selectionControl?.(thread.turns)} attention={thread.turns.map(task => <MediaTaskAttention key={task.id} task={task} />)} />
  })}</>
}
