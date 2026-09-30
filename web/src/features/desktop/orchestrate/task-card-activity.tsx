import { Terminal } from 'lucide-react'
import type { RunningTask } from './orchestrate-types'

/** One event-backed slot; no transcript cache or polling. Event attributes update repeated tools without remount motion. */
export function TaskCardActivity({ task }: { task: RunningTask }) {
  if (task.status !== 'running' && task.status !== 'in_progress') return null
  const display = task.currentTool?.trim() || ''
  const tool = task.currentToolName?.replace(/^functions\./, '') || display.split(/\s+/)[0] || 'Thinking'
  const target = display.startsWith(tool) ? display.slice(tool.length).trim() : display
  return (
    <div data-testid="task-card-activity" className="swarm-task-focus">
      <span className="shrink-0">▸ Focus</span>
      <span data-event-key={task.currentToolEventKey}
        className="swarm-task-focus-event" role="status" aria-live="polite" aria-atomic="true">
        <Terminal size={12} className="shrink-0" aria-hidden="true" />
        <span className="swarm-task-focus-tool">{tool}</span>
        <span className="swarm-task-focus-target" title={target}>{target}</span>
      </span>
      <span className="swarm-task-focus-count">{task.toolCallCount ? `call ${task.toolCallCount}` : ''}</span>
    </div>
  )
}
