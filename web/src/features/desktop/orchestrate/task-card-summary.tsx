import type { ReactNode } from 'react'
import { Code2, Film, GitBranch, Image as ImageIcon, Layers, Music } from 'lucide-react'
import type { RunningTask, MediaDeliverable } from './orchestrate-types'

/** Formats an elapsed minute or duration string into mins, hours, or days (e.g. "5m", "2h 15m", "1d 4h") */
export function formatElapsedString(elapsed?: string): string {
  if (!elapsed) return ''
  const trimmed = elapsed.trim()
  const match = trimmed.match(/^(\d+)\s*(?:m|min|mins|minute|minutes)?$/i)
  if (match) {
    const totalMins = parseInt(match[1], 10)
    if (isNaN(totalMins)) return trimmed
    if (totalMins >= 1440) {
      const days = Math.floor(totalMins / 1440)
      const remHours = Math.floor((totalMins % 1440) / 60)
      return remHours > 0 ? `${days}d ${remHours}h` : `${days}d`
    }
    if (totalMins >= 60) {
      const hours = Math.floor(totalMins / 60)
      const remMins = totalMins % 60
      return remMins > 0 ? `${hours}h ${remMins}m` : `${hours}h`
    }
    return `${totalMins}m`
  }
  return trimmed
}

/** Formats dynamic elapsed seconds into running clock (mm:ss) or hours/days when >= 60 mins */
export function formatElapsedSeconds(totalSec: number): string {
  if (totalSec < 0) totalSec = 0
  const totalMins = Math.floor(totalSec / 60)
  if (totalMins >= 1440) {
    const days = Math.floor(totalMins / 1440)
    const remHours = Math.floor((totalMins % 1440) / 60)
    return remHours > 0 ? `${days}d ${remHours}h` : `${days}d`
  }
  if (totalMins >= 60) {
    const hours = Math.floor(totalMins / 60)
    const remMins = totalMins % 60
    return remMins > 0 ? `${hours}h ${remMins}m` : `${hours}h`
  }
  const secs = totalSec % 60
  return `${totalMins}:${secs.toString().padStart(2, '0')}`
}

/** Summary is deliberately evidence-only: absence of Git or validation evidence is not success. */
export function taskCardFacts(task: RunningTask) {
  const completed = task.subtasksCount?.completed ?? task.subtasks?.filter((step) => step.completed).length ?? 0
  const total = task.subtasksCount?.total ?? task.subtasks?.length ?? 0
  const progress = total > 0 ? `${completed}/${total} steps` : task.taskProgramStatus?.jobs?.length
    ? `${task.taskProgramStatus.jobs.filter((job) => ['integrated', 'completed', 'handoff_ready'].includes(job.state)).length}/${task.taskProgramStatus.jobs.length} jobs`
    : task.planProgressPercent !== undefined ? `${task.planProgressPercent}% progress` : null
  const git = task.gitStatus === 'unknown' || task.gitStatus === 'stale' || !task.gitStatus
    ? task.gitStatus === 'stale' ? 'Git: last known state' : 'Git: not inspected'
    : task.syncWarning || (task.behindCommits ?? 0) > 0 ? 'Out of sync'
    : task.isDirty ? 'Changes pending commit'
    : (task.unintegratedCommits ?? 0) > 0 ? `${task.unintegratedCommits} unintegrated commit(s)`
    : task.isIntegrated === true ? 'Integrated' : 'Integration not verified'
  const deliverable = task.deliverables?.find((item) =>
    (item.status === 'ready' || item.status === 'accepted') &&
    (item.type === 'image' || item.type === 'video') && Boolean(item.previewUrl || item.mediaUrl))
  const running = task.status === 'running' || task.status === 'in_progress' || task.status === 'planning'
  return {
    worktree: task.worktreeName || null,
    branch: task.worktreeBranch || null,
    progress,
    validation: task.whatDidDo?.find((line) => /\b(test|validat|check|build)\w*/i.test(line)) || null,
    git,
    deliverable,
    // A completed task's last tool/focus is history, not current activity.
    activity: running ? task.activeTodo?.trim() || task.toolActivitySummary?.trim() || null : null,
    identity: task.activeAgent?.trim() || null,
    model: task.activeModel?.trim() || null,
    provider: task.activeProvider?.trim() || null,
  }
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap min-w-0">
      <span className="text-slate-400/80 font-normal">{label}:</span>
      {children}
    </span>
  )
}

export function TaskCardSummary({
  task,
  onPreview,
  statusBadge,
  timer,
  actions,
  extraBadges,
}: {
  task: RunningTask
  onPreview?: (deliverable: MediaDeliverable) => void
  statusBadge?: ReactNode
  timer?: ReactNode
  actions?: ReactNode
  extraBadges?: ReactNode
}) {
  const facts = taskCardFacts(task)
  const Icon = task.agentType === 'image' ? ImageIcon : task.agentType === 'video' ? Film
    : task.agentType === 'audio' || task.agentType === 'sound' ? Music
    : task.agentType === 'swarm' ? Layers : Code2
  const preview = facts.deliverable
  const isAllStepsDone = facts.progress && /^\d+\/\d+ steps$/.test(facts.progress) && facts.progress.split('/')[0] === facts.progress.split('/')[1].split(' ')[0]
  const displayAgent = facts.identity
    ? facts.identity.replace(/^system[-_ ]?/i, '')
    : `${task.agentType} requested`

  return (
    <div className="swarm-task-summary" data-testid="task-card-summary">
      {preview ? (
        <button
          type="button"
          className="swarm-task-visual"
          aria-label={`Preview ${preview.title}`}
          onClick={(event) => {
            event.stopPropagation()
            onPreview?.(preview)
          }}
          disabled={!onPreview}
        >
          {preview.type === 'video' && preview.mediaUrl ? (
            <video src={preview.mediaUrl} poster={preview.previewUrl} muted playsInline preload="metadata" aria-label={preview.title} />
          ) : (
            <img src={preview.previewUrl || preview.mediaUrl} alt={preview.title} />
          )}
        </button>
      ) : (
        <div className="swarm-task-visual swarm-task-visual-icon" aria-label={`${task.agentType} task`}>
          <Icon size={18} strokeWidth={1.75} />
        </div>
      )}

      <div className="swarm-task-summary-content min-w-0 flex-1">
        {/* Row 1: Agent Tag + Title on Left, Status + Timer + Actions on Right */}
        <div className="swarm-task-header-row flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 min-w-0 flex-1">
            <span
              className={`text-[11px] font-medium px-2 py-0.5 rounded-md border shrink-0 ${
                task.agentType === 'swarm'
                  ? 'bg-amber-500/10 text-amber-300 border-amber-500/25'
                  : task.agentType === 'designer' || task.agentType === 'video'
                  ? 'bg-blue-500/10 text-blue-400 border-blue-500/25'
                  : 'bg-indigo-500/10 text-indigo-300 border-indigo-500/25'
              }`}
            >
              {displayAgent}
            </span>
            <h3 className="truncate font-semibold text-slate-100 text-[13px] tracking-tight leading-snug" title={task.title}>
              {task.title}
            </h3>
          </div>

          <div className="swarm-task-action-buttons flex items-center gap-2 flex-shrink-0">
            {statusBadge ? (
              statusBadge
            ) : (
              <span className="text-[11px] font-medium capitalize px-2 py-0.5 rounded-md border bg-slate-800/80 text-slate-300 border-slate-700/60 flex items-center gap-1.5">
                <span className="h-1.5 w-1.5 rounded-full bg-slate-400 shrink-0" />
                <span>{task.status.replace(/_/g, ' ')}</span>
              </span>
            )}
            {timer ? (
              timer
            ) : task.elapsed ? (
              <span className="text-[11px] text-slate-400 font-normal">{formatElapsedString(task.elapsed)}</span>
            ) : null}
            {actions}
          </div>
        </div>

        {/* Row 2: Compact, consolidated metadata strip: Model, Branch, Progress, Verified Git, Extra Badges */}
        <div className="swarm-task-metadata-strip flex items-center gap-1.5 flex-wrap mt-1 text-[11px]">
          {(facts.provider || facts.model) && (
            <span
              className="swarm-task-model px-2 py-0.5 rounded-md bg-slate-800/70 text-slate-300 border border-slate-700/50 flex items-center gap-1 font-normal text-[11px]"
              aria-label="Active session model"
            >
              <span>{facts.provider ? `${facts.provider} / ` : ''}{facts.model}</span>
            </span>
          )}

          {(facts.branch || facts.worktree) && (
            <span
              className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-indigo-500/10 text-indigo-300 border border-indigo-500/25 text-[11px] font-normal whitespace-nowrap shrink min-w-0 max-w-[650px]"
              title={`Branch: ${facts.branch || facts.worktree}`}
            >
              <Fact label="Branch">
                <GitBranch size={11} className="text-indigo-400 shrink-0 inline" aria-hidden="true" />
                <span className="truncate font-medium text-indigo-200">{facts.branch || facts.worktree}</span>
              </Fact>
            </span>
          )}

          {facts.progress && (
            <span
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-[11px] font-medium whitespace-nowrap ${
                isAllStepsDone
                  ? 'bg-emerald-500/10 text-emerald-300 border-emerald-500/25'
                  : 'bg-blue-500/10 text-blue-300 border-blue-500/25'
              }`}
            >
              {isAllStepsDone && <span className="text-emerald-400 font-bold">✓</span>}
              <span>{facts.progress}</span>
            </span>
          )}

          {/* Omit noisy "Git: last known state" / "Git: not inspected"; only render real verified statuses */}
          {facts.git && !facts.git.startsWith('Git:') && (
            <span
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-[11px] font-medium whitespace-nowrap ${
                facts.git === 'Integrated'
                  ? 'bg-emerald-500/10 text-emerald-300 border-emerald-500/25'
                  : facts.git.includes('Out of sync')
                  ? 'bg-rose-500/10 text-rose-300 border-rose-500/25'
                  : 'bg-amber-500/10 text-amber-300 border-amber-500/25'
              }`}
            >
              <span>{facts.git}</span>
            </span>
          )}

          {facts.validation && (
            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-emerald-500/10 text-emerald-300 border border-emerald-500/25 text-[11px] font-medium whitespace-nowrap">
              {facts.validation}
            </span>
          )}

          {extraBadges}
        </div>

        {task.taskTodos && task.taskTodos.length > 0 && (
          <details className="mt-1 text-[11px] text-slate-400">
            <summary>Agent checklist</summary>
            <ul aria-label="Agent checklist">
              {task.taskTodos.map((todo) => (
                <li key={todo.id} className="break-words" data-status={todo.status}>
                  {todo.status === 'completed' ? '✓' : todo.status === 'in_progress' ? '→' : '○'} {todo.title}
                  <span className="sr-only"> — {todo.status.replace(/_/g, ' ')}</span>
                </li>
              ))}
            </ul>
          </details>
        )}
        {task.handoffSummary && task.status === 'needs_review' && (
          <p className="mt-1 text-[11px] text-slate-300 break-words" aria-label="Implementation handoff">
            Ready for review: {task.handoffSummary}
          </p>
        )}

        {/* Row 3: Live activity (only when actively running) */}
        {facts.activity && (
          <div className="swarm-task-activity text-[11px] text-blue-300/90 mt-1 flex items-center gap-1.5 truncate font-normal" aria-label="Live session activity">
            <span className="h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse shrink-0" />
            <span className="truncate">{facts.activity}</span>
          </div>
        )}
      </div>
    </div>
  )
}
