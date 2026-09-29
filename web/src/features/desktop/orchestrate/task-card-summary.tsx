import type { ReactNode } from 'react'
import { Code2, Film, GitBranch, Image as ImageIcon, Layers, Music } from 'lucide-react'
import type { RunningTask, MediaDeliverable } from './orchestrate-types'

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
    activity: running ? task.toolActivitySummary?.trim() || null : null,
    identity: task.activeAgent?.trim() || null,
    model: task.activeModel?.trim() || null,
    provider: task.activeProvider?.trim() || null,
  }
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <span className="swarm-task-fact"><span>{label}: </span>{children}</span>
}

export function TaskCardSummary({ task, onPreview }: {
  task: RunningTask
  onPreview?: (deliverable: MediaDeliverable) => void
}) {
  const facts = taskCardFacts(task)
  const Icon = task.agentType === 'image' ? ImageIcon : task.agentType === 'video' ? Film
    : task.agentType === 'audio' || task.agentType === 'sound' ? Music
    : task.agentType === 'swarm' ? Layers : Code2
  const preview = facts.deliverable
  return <div className="swarm-task-summary" data-testid="task-card-summary">
    {preview ? <button type="button" className="swarm-task-visual" aria-label={`Preview ${preview.title}`}
      onClick={(event) => { event.stopPropagation(); onPreview?.(preview) }} disabled={!onPreview}>
      {preview.type === 'video' && preview.mediaUrl
        ? <video src={preview.mediaUrl} poster={preview.previewUrl} muted playsInline preload="metadata" aria-label={preview.title} />
        : <img src={preview.previewUrl || preview.mediaUrl} alt={preview.title} />}
    </button> : <div className="swarm-task-visual swarm-task-visual-icon" aria-label={`${task.agentType} task`}>
      <Icon size={20} strokeWidth={1.5} />
    </div>}
    <div className="swarm-task-summary-content">
      <h3>{task.title}</h3>
      <div className="swarm-task-byline"><span>{facts.identity || `${task.agentType} requested`}</span><span aria-hidden="true">·</span><span>{task.status.replace(/_/g, ' ')}</span>{task.elapsed && <><span aria-hidden="true">·</span><span>{task.elapsed}</span></>}</div>
      {(facts.provider || facts.model) && <div className="swarm-task-model" aria-label="Active session model">{facts.provider || 'Provider unavailable'} / {facts.model || 'Model unavailable'}</div>}
      {facts.activity && <div className="swarm-task-activity" aria-label="Live session activity">{facts.activity}</div>}
      <div className="swarm-task-evidence" aria-label="Task evidence">
        {facts.progress && <span>{facts.progress}</span>}
        {facts.branch && <Fact label="Branch"><GitBranch size={12} aria-hidden="true" /> {facts.branch}</Fact>}
        {facts.worktree && !facts.branch && <Fact label="Worktree">{facts.worktree}</Fact>}
        {facts.validation && <span>{facts.validation}</span>}
        <span>{facts.git}</span>
      </div>
    </div>
  </div>
}
