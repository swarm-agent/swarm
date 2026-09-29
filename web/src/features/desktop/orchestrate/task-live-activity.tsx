import type { RunningTask } from './orchestrate-types'

/** A glanceable progress hint; the transcript and tool details belong in chat. */
export function TaskLiveActivity({ task }: { task: RunningTask }) {
  if (task.status !== 'running' && task.status !== 'in_progress') return null
  const action = task.toolActivitySummary?.trim()
  const text = task.liveAssistantText?.trim().replace(/\s+/g, ' ')
  const detail = action || text?.slice(-160) || 'Starting session…'

  return (
    <div data-testid="task-live-activity" role="status" aria-live="polite" aria-label={`Agent working: ${detail}`}
      className="inline-flex max-w-full min-w-0 items-center gap-1.5 rounded-md bg-blue-500/5 px-2 py-1 text-[11px] text-slate-400">
      <span aria-hidden="true" className="h-1.5 w-1.5 shrink-0 rounded-full bg-blue-400 motion-safe:animate-pulse" />
      <span className="shrink-0 text-blue-300">Working</span>
      <span className="min-w-0 truncate" title={detail}>{detail}</span>
    </div>
  )
}
