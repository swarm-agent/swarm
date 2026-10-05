import type { ReactNode } from 'react'
import { TaskEnvironments } from './task-environments'
import { taskPreviewURL } from './task-preview-url'
import { TaskThumbnail } from './task-thumbnail'
import { Code2, Film, Image as ImageIcon, Layers, Music } from 'lucide-react'
import type { RunningTask, MediaDeliverable } from './orchestrate-types'
import { taskCardSessions } from './task-card-sessions'
import { taskDelivery, taskOutcome } from './task-outcome'

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
  const git = taskDelivery(task)?.summary ?? (task.gitStatus === 'unknown' || task.gitStatus === 'stale' || !task.gitStatus
    ? task.gitStatus === 'stale' ? 'Git: last known state' : 'Git: not inspected'
    : task.syncWarning || (task.behindCommits ?? 0) > 0 ? 'Out of sync'
    : task.isDirty ? 'Changes pending commit'
    : (task.unintegratedCommits ?? 0) > 0 ? `${task.unintegratedCommits} unintegrated ${task.unintegratedCommits === 1 ? 'commit' : 'commits'}`
    : task.isIntegrated === true ? 'Integrated' : 'Integration not verified')
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

export function TaskCardSummary({ task, projectId, onPreview, statusBadge, timer, actions, extraBadges }: {
  projectId?: string
  expanded?: boolean
  task: RunningTask
  onPreview?: (deliverable: MediaDeliverable) => void
  onOpenSession?: (sessionId: string) => void
  statusBadge?: ReactNode
  timer?: ReactNode
  actions?: ReactNode
  extraBadges?: ReactNode
}) {
  const facts = taskCardFacts(task)
  const outcome = taskOutcome(task)
  const sessions = taskCardSessions(task)
  const working = sessions.filter(session => session.status === 'running').length
  const unknown = sessions.filter(session => session.status === 'unknown').length
  const Icon = task.agentType === 'image' ? ImageIcon : task.agentType === 'video' ? Film
    : task.agentType === 'audio' || task.agentType === 'sound' ? Music : task.agentType === 'swarm' ? Layers : Code2
  const preview = facts.deliverable
  const thumbnail = taskPreviewURL(preview?.previewUrl || preview?.mediaUrl)
  const workerLinked = Boolean(task.workerId?.trim() || task.worker_id?.trim())
  const pending = task.status === 'pending_approval'
  const role = (workerLinked ? 'Worker' : pending ? task.agentType : facts.identity || task.agentType).replace(/^@/, '').replace(/^system[-_ ]?/i, '')
  const workspace = task.sourceWorkspacePath?.trim()
  const workspaceLabel = workspace?.split('/').filter(Boolean).pop() || 'Workspace unavailable'
  const worktree = facts.branch || facts.worktree
  const model = pending ? '' : [facts.provider, facts.model].filter(Boolean).join(' / ')
  const countKnown = pending || Boolean(task.sessionSummary && (sessions.length || task.sessionSummary.totalSessions === 0))
  const sessionErrors = sessions.filter(session => session.lastError && ['failed', 'blocked', 'paused'].includes(session.status)).length
  const title = workerLinked ? task.worker_name || task.workerName || task.title : task.title
  return (
    <div className="swarm-task-summary" data-testid="task-card-summary">
      <div className="swarm-task-summary-content">
        <div className="swarm-task-header-row flex-wrap">
          {preview ? <button type="button" className="swarm-task-visual" aria-label={`Preview ${preview.title}`}
            disabled={!onPreview} onClick={event => { event.stopPropagation(); onPreview?.(preview) }}>
            <TaskThumbnail src={thumbnail} title={preview.title} />
          </button> : <Icon size={18} className="swarm-task-icon" aria-label={`${task.agentType} task`} />}
          <h3 className="min-w-0 flex-1" title={title}>{title}</h3>
          <div className="shrink-0">{actions}</div>
        </div>
        <div className="swarm-task-meta" aria-label="Task deployment metadata">
          <span className="swarm-task-meta-agent" title={`${pending ? 'Proposed' : 'Agent'}: ${role}`}>{pending ? 'Proposed ' : ''}{role}</span>
          {model && <span title={model}>{model}</span>}
          <span title={workspace || 'Source workspace unavailable'}>{workspaceLabel}</span>
          {worktree && <span className="font-mono" title={`Worktree: ${worktree}`}>{worktree}</span>}
        </div>
        <div className="swarm-task-status-row">
          {statusBadge || <span className="swarm-task-state"><i className="swarm-task-dot" />{task.status.replace(/_/g, ' ')}</span>}
          {(timer || task.elapsed) && <span className="swarm-task-elapsed">{timer || formatElapsedString(task.elapsed)}</span>}
          <span className="swarm-task-agent-count" title={unknown ? `${unknown} session state unavailable; running count is confirmed only` : undefined}>
            {countKnown ? `${pending ? 0 : working} ${working === 1 && !pending ? 'AI' : 'AIs'} working${unknown && !pending ? ' · ?' : ''}` : 'AIs working: unknown'}
          </span>
          {!pending && <span className="swarm-task-state" title={facts.git}>{facts.git}</span>}
          {sessionErrors > 0 && <span className="swarm-task-state" role="alert">{sessionErrors} session {sessionErrors === 1 ? 'error' : 'errors'} · open details</span>}
          {task.status === 'blocked' && <span className="swarm-task-state">Action required · open details</span>}
          {facts.progress && <span className="swarm-task-progress">{facts.progress}</span>}
        </div>
        {!pending && <div className="swarm-task-status-row" aria-label="Task outcome">
          {outcome.blocker && <span role="alert" title={outcome.blocker.message}>{outcome.blocker.title} · open details</span>}
          {outcome.delivery && <span>{outcome.delivery}</span>}
          {(task.taskProgramStatus?.state === 'completed' || task.status === 'completed' || outcome.blocker) && <span>{outcome.verification}</span>}
        </div>}
        <TaskEnvironments task={task} projectId={projectId} />
        {extraBadges && <div className="swarm-task-extra">{extraBadges}</div>}
      </div>
    </div>
  )
}
