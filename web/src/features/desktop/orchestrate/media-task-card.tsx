import type { ReactNode } from 'react'
import type { MediaDeliverable, RunningTask } from './orchestrate-types'
import type { QuickRouteMode } from '../tools/media-library/media-viewer-modal'

import { DesignThumbnail } from '../tools/media-library/design-thumbnail'
import type { ProjectDesign } from '../session-v3/design-api'
import type { MediaLibraryItem } from '../tools/media-library/types'
import { designMediaItem, designRequestId, designStatus, readyDesignRevision } from './design-media-task'

type TaskCardProps = Parameters<typeof ArtifactMediaTaskCard>[0]
export function MediaTaskCard(props: TaskCardProps | { source: 'independent-design'; design: ProjectDesign; onDesignPreview: (item: MediaLibraryItem) => void }) {
  if ('source' in props && props.source === 'independent-design') {
    const { design: row, onDesignPreview } = props
    const ready = row.request.candidates.filter(candidate => readyDesignRevision(candidate)).length
    return <article className="rounded-xl border border-indigo-500/30 bg-slate-950/70 p-3 space-y-3" data-testid="media-task-card" data-task-id={designRequestId(row)}>
      <header className="flex flex-wrap justify-between gap-2"><h3 className="font-semibold text-slate-100 break-words">{row.title}</h3><span role="status">{designStatus(row.request.state)} · {ready}/{row.request.candidates.length} ready</span></header>
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-3">
        {row.request.candidates.map((candidate, index) => {
          const revision = readyDesignRevision(candidate)
          return <section key={index} className="min-w-0 rounded-lg border border-slate-700 p-2">
            <button type="button" className="w-full min-h-24 bg-black text-slate-100" disabled={!revision} onClick={() => revision && onDesignPreview(designMediaItem(row, index, revision))} aria-label={`Preview ${row.title} candidate ${index + 1}`}>{revision && <DesignThumbnail session={row.request.parent_session_id} revision={revision} />}Candidate {index + 1} · {designStatus(candidate.state)}{revision ? ' · Open preview' : ''}</button>
            {candidate.failure_reason && <p role="alert">{candidate.failure_reason}</p>}
            {candidate.router_alert && <p role="alert">{candidate.router_alert}</p>}
            {candidate.attempts?.map(attempt => <div key={attempt.number} className="text-xs"><p>Attempt {attempt.number}: {designStatus(attempt.state)} {attempt.reason_code}</p>{attempt.router_alert && <p role="alert">{attempt.router_alert}</p>}</div>)}
          </section>
        })}
      </div>
    </article>
  }
  if ('task' in props) return <ArtifactMediaTaskCard {...props} />
  return null
}

function ArtifactMediaTaskCard({ task, onPreview, onApprove, onArchive, onDelete, isApproving, error, attention }: {
  task: RunningTask
  attention?: ReactNode
  onPreview?: (output: MediaDeliverable, mode?: QuickRouteMode) => void
  onApprove?: () => void
  onArchive?: () => void
  onDelete?: () => void
  isApproving?: boolean
  error?: string
}) {
  const outputs = task.deliverables || []
  const ready = outputs.filter(d => d.status === 'ready' || d.status === 'accepted').length
  const failed = outputs.filter(d => d.status === 'failed').length
  const generating = outputs.filter(d => d.status === 'generating').length
  const queued = outputs.length - ready - failed - generating
  return (
    <article className="rounded-xl border border-indigo-500/30 bg-slate-950/70 p-3 space-y-3" data-testid="media-task-card" data-task-id={task.id}>
      <header className="flex flex-wrap justify-between gap-2">
        <div className="min-w-0"><span className="text-xs uppercase text-indigo-300">{task.agentType} studio</span><h3 className="font-semibold text-slate-100 break-words">{task.title}</h3></div>
        <span className="text-xs text-slate-300" role="status">{ready}/{outputs.length} ready · {generating} generating · {queued} queued{failed > 0 ? ` · ${failed} failed` : ''}</span>
      </header>
      {attention}
      {(error || task.lastError) && <p role="alert" className="text-xs text-rose-300">{error || task.lastError}</p>}
      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-3" data-testid="task-media-thumbnails-strip">
        {outputs.map((output, index) => {
          const isReady = output.status === 'ready' || output.status === 'accepted'
          const url = output.mediaUrl || output.previewUrl
          const available = isReady && Boolean(url)
          return (
            <section key={output.id} className="min-w-0 rounded-lg border border-slate-700 overflow-hidden">
              <button type="button" disabled={!available} onClick={() => onPreview?.(output)} className="w-full aspect-square bg-black flex items-center justify-center" aria-label={`Preview ${output.title}`}>
                {available ? output.type === 'video' ? <video src={url} muted playsInline preload="metadata" className="w-full h-full object-contain" /> : output.type === 'audio' ? <span className="text-slate-300">Audio ready</span> : <img src={url} alt={output.title} className="w-full h-full object-contain" /> : <span className="text-xs text-slate-400">{isReady ? 'Preview loading' : output.status === 'pending' ? 'Queued' : output.status}</span>}
              </button>
              <div className="p-2 space-y-2">
                <p className="text-xs text-slate-300">{index + 1}. {output.status}</p>
                {output.status === 'failed' && output.description && <p className="text-xs text-rose-300">{output.description}</p>}
                <div className="flex flex-wrap gap-2 text-xs text-indigo-300">
                  <button type="button" disabled={!available} onClick={() => onPreview?.(output)}>Preview / save</button>
                  {output.type === 'image' && <><button type="button" disabled={!available} onClick={() => onPreview?.(output, 'fine_tune')}>Edit</button><button type="button" disabled={!available} onClick={() => onPreview?.(output, 'to_video')}>Turn into video</button></>}
                  {available && <a href={url} download={`${output.id}.${output.type === 'image' ? 'png' : output.type === 'video' ? 'mp4' : 'wav'}`}>Download</a>}
                </div>
              </div>
            </section>
          )
        })}
      </div>
      <footer className="flex gap-3 text-xs text-slate-400">
        {task.status === 'pending_approval' && onApprove && <button type="button" onClick={onApprove} disabled={isApproving}>{isApproving ? 'Starting…' : 'Generate media'}</button>}
        {onArchive && <button type="button" onClick={onArchive}>Archive</button>}
        {onDelete && <button type="button" onClick={onDelete}>Delete</button>}
      </footer>
    </article>
  )
}
