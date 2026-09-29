import { memo } from 'react'
import type { RunningTask } from './orchestrate-types'

const toolLabels: Record<string, string> = {
  read: 'Read', edit: 'Editing', write: 'Writing', search: 'Searching',
  find: 'Finding files', list: 'Listing files', bash: 'Running command',
  websearch: 'Searching the web', webfetch: 'Fetching page',
  git_status: 'Checking Git status', git_diff: 'Reviewing diff',
  git_add: 'Staging changes', git_commit: 'Committing',
  task_progress: 'Updating progress',
}

function activityLabel(tool?: string): string {
  const name = tool?.trim().replace(/^tool:\s*/i, '').replace(/^functions\./i, '')
  if (!name || name === '-') return 'Thinking'
  const key = name.toLowerCase()
  return Object.hasOwn(toolLabels, key) ? toolLabels[key] : name.replace(/[_-]+/g, ' ').replace(/^./, c => c.toUpperCase())
}

// Only semantic values cross the memo boundary: token deltas, elapsed time and
// historical tool summaries must not repaint this row or announce a text ticker.
const ActivityLine = memo(function ActivityLine({ label, progress }: { label: string; progress?: number }) {
  return (
    <div data-testid="task-card-activity"
      className="flex h-9 w-full min-w-0 items-center gap-2 overflow-hidden rounded-lg border border-blue-500/30 bg-blue-950/50 px-2 text-[11px]">
      <span role="status" aria-live="polite" aria-atomic="true" title={label}
        className="block min-w-0 flex-1 truncate font-medium leading-5 text-blue-200">{label}</span>
      <span data-testid="task-card-activity-progress" aria-label={progress === undefined ? undefined : `Plan progress: ${progress}%`}
        className="w-12 shrink-0 text-right font-mono tabular-nums leading-5 text-blue-300">
        {progress === undefined ? null : `${progress}%`}
      </span>
    </div>
  )
})

/** Presentation only: no timers, transcript state, model resolution or lifecycle cache. */
export function TaskCardActivity({ task }: { task: RunningTask }) {
  if (task.status !== 'running' && task.status !== 'in_progress') return null
  // Prefer the resolved session identity. An absent identity is not proof of Coder.
  const identity = task.activeAgent?.trim() || task.agentType?.trim() || 'Agent'
  const agent = identity.replace(/^@/, '').replace(/^system[-_ ]?/i, '').replace(/^./, c => c.toUpperCase())
  const label = `${agent}: ${activityLabel(task.currentTool)}`
  const progress = task.planProgressPercent !== undefined && Number.isFinite(task.planProgressPercent)
    ? Math.round(Math.min(100, Math.max(0, task.planProgressPercent))) : undefined
  return <ActivityLine label={label} progress={progress} />
}
