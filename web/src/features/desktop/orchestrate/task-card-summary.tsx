import type { ReactNode } from 'react'
import { Code2, Film, Image as ImageIcon, Layers, Music } from 'lucide-react'
import type { RunningTask, MediaDeliverable } from './orchestrate-types'
import { taskReviewMessage } from './task-review-message'

export function formatElapsedString(elapsed?: string): string {
  if (!elapsed) return ''
  const trimmed = elapsed.trim()
  const match = trimmed.match(/^(\d+)\s*(?:m|min|mins|minute|minutes)?$/i)
  if (!match) return trimmed
  const mins = Number(match[1])
  if (mins >= 1440) return `${Math.floor(mins / 1440)}d${Math.floor(mins % 1440 / 60) ? ` ${Math.floor(mins % 1440 / 60)}h` : ''}`
  if (mins >= 60) return `${Math.floor(mins / 60)}h${mins % 60 ? ` ${mins % 60}m` : ''}`
  return `${mins}m`
}

export function formatElapsedSeconds(totalSec: number): string {
  totalSec = Math.max(0, totalSec)
  if (totalSec >= 3600) return formatElapsedString(String(Math.floor(totalSec / 60)))
  return `${Math.floor(totalSec / 60)}:${(totalSec % 60).toString().padStart(2, '0')}`
}

/** Absence of Git or validation evidence is not success. */
export function taskCardFacts(task: RunningTask) {
  const completed = task.subtasksCount?.completed ?? task.subtasks?.filter(step => step.completed).length ?? 0
  const total = task.subtasksCount?.total ?? task.subtasks?.length ?? 0
  const progress = total > 0 ? `${completed}/${total} steps` : task.taskProgramStatus?.jobs?.length
    ? `${task.taskProgramStatus.jobs.filter(job => ['integrated', 'completed', 'handoff_ready'].includes(job.state)).length}/${task.taskProgramStatus.jobs.length} jobs`
    : task.planProgressPercent !== undefined ? `${task.planProgressPercent}% progress` : null
  const git = task.gitStatus === 'unknown' || task.gitStatus === 'stale' || !task.gitStatus
    ? task.gitStatus === 'stale' ? 'Git: last known state' : 'Git: not inspected'
    : task.syncWarning || (task.behindCommits ?? 0) > 0 ? 'Out of sync'
    : task.isDirty ? 'Changes pending commit'
    : (task.unintegratedCommits ?? 0) > 0 ? `${task.unintegratedCommits} unintegrated commit(s)`
    : task.isIntegrated === true ? 'Integrated' : 'Integration not verified'
  const deliverable = task.deliverables?.find(item =>
    (item.status === 'ready' || item.status === 'accepted') &&
    (item.type === 'image' || item.type === 'video') && Boolean(item.previewUrl || item.mediaUrl))
  const running = ['running', 'in_progress', 'planning'].includes(task.status)
  return {
    worktree: task.worktreeName || null, branch: task.worktreeBranch || null, progress, git, deliverable,
    validation: task.whatDidDo?.find(line => /\b(test|validat|check|build)\w*/i.test(line)) || null,
    activity: running ? task.activeTodo?.trim() || task.toolActivitySummary?.trim() || null : null,
    identity: task.activeAgent?.trim() || null,
    model: task.activeModel?.trim() || null, provider: task.activeProvider?.trim() || null,
  }
}

export function TaskCardSummary({ task, onPreview, statusBadge, timer, actions, extraBadges }: {
  task: RunningTask
  onPreview?: (deliverable: MediaDeliverable) => void
  statusBadge?: ReactNode
  timer?: ReactNode
  actions?: ReactNode
  extraBadges?: ReactNode
}) {
  const facts = taskCardFacts(task)
  const review = task.status === 'needs_review' ? taskReviewMessage(task) : null
  const Icon = task.agentType === 'image' ? ImageIcon : task.agentType === 'video' ? Film
    : task.agentType === 'audio' || task.agentType === 'sound' ? Music : task.agentType === 'swarm' ? Layers : Code2
  const preview = facts.deliverable
  const role = (facts.identity || task.agentType).replace(/^@/, '').replace(/^system[-_ ]?/i, '')
  return (
    <div className="swarm-task-summary" data-testid="task-card-summary">
      <div className="swarm-task-summary-content">
        <div className="swarm-task-header-row">
          {preview ? <button type="button" className="swarm-task-visual" aria-label={`Preview ${preview.title}`}
            disabled={!onPreview} onClick={event => { event.stopPropagation(); onPreview?.(preview) }}>
            {preview.type === 'video' && preview.mediaUrl
              ? <video src={preview.mediaUrl} poster={preview.previewUrl} muted playsInline preload="metadata" aria-label={preview.title} />
              : <img src={preview.previewUrl || preview.mediaUrl} alt={preview.title} />}
          </button> : <Icon size={18} className="swarm-task-icon" aria-label={`${task.agentType} task`} />}
          <h3 className="min-w-0 flex-1">{task.title}</h3>
          <div className="shrink-0">{actions}</div>
        </div>
        <div className="swarm-task-meta" title={[facts.provider, facts.model].filter(Boolean).join(' / ')}>
          {role}{(facts.branch || facts.worktree) && <> · {facts.branch || facts.worktree}</>}
        </div>
        <div className="swarm-task-status-row">
          {statusBadge || <span className="swarm-task-state"><i className="swarm-task-dot" />{task.status.replace(/_/g, ' ')}</span>}
          {timer || (task.elapsed && <span>{formatElapsedString(task.elapsed)}</span>)}
          {!facts.git.startsWith('Git:') && <span className="swarm-task-state"><i className={`swarm-task-dot ${facts.git === 'Integrated' ? 'is-success' : 'is-warning'}`} />{facts.git}</span>}
          {facts.progress && <span className="swarm-task-progress">{facts.progress}</span>}
        </div>
        {extraBadges && <div className="swarm-task-extra">{extraBadges}</div>}
        {review && <section aria-label="Ready for review">
          <p>{review.outcome}{review.hasOutcome && ' Review the changes.'}</p>
          {review.warnings.length > 0 && <p aria-label="Review warnings">{review.warnings.join(' ')}</p>}
          {review.raw && <details key={`${task.id}:${task.activeAttemptId || ''}:${review.raw}`}
            onClick={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
            <summary>Details — full implementation handoff</summary>
            <pre className="whitespace-pre-wrap break-words" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{review.raw}</pre>
          </details>}
        </section>}
      </div>
    </div>
  )
}
