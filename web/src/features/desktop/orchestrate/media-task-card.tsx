import type { MediaDeliverable, RunningTask } from './orchestrate-types'
import type { QuickRouteMode } from '../tools/media-library/media-viewer-modal'

export function MediaTaskCard({ task, onPreview, onApprove, onArchive, onDelete, isApproving, error }: {
  task: RunningTask
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
