import type { ReactNode } from 'react'
import { Code2, Film, GitBranch, Image as ImageIcon, Layers, Music } from 'lucide-react'
import type { RunningTask, MediaDeliverable } from './orchestrate-types'

/** Summary is deliberately evidence-only: absence of Git or validation evidence is not success. */
export function taskCardFacts(task: RunningTask) {
  const workspaces = task.workspacesInvolved?.length ? task.workspacesInvolved : [task.sourceWorkspacePath || task.workspacePath || task.workspaceTarget].filter(Boolean)
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
  return {
    workspaces,
    worktree: task.worktreeName || null,
    branch: task.worktreeBranch || null,
    target: task.baseBranch || null,
    progress,
    changed: task.dirtyCount && task.isDirty ? `${task.dirtyCount} changed files` : task.diffSummary || null,
    validation: task.whatDidDo?.find((line) => /\b(test|validat|check|build)\w*/i.test(line)) || null,
    git,
    deliverable,
  }
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <div className="swarm-task-fact"><dt>{label}</dt><dd>{children}</dd></div>
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
      <svg viewBox="0 0 158 112" aria-hidden="true" className="swarm-task-dependencies"><path d="M18 82 L54 34 L100 69 L139 22"/><circle cx="18" cy="82" r="4"/><circle cx="54" cy="34" r="4"/><circle cx="100" cy="69" r="4"/><circle cx="139" cy="22" r="4"/></svg>
      <Icon size={32} strokeWidth={1.5} />
    </div>}
    <div className="swarm-task-summary-content">
      <h3>{task.title}</h3>
      <div className="swarm-task-byline"><span>{task.agentType}</span><span aria-hidden="true">·</span><span>{task.status.replace(/_/g, ' ')}</span>{task.elapsed && <><span aria-hidden="true">·</span><span>{task.elapsed}</span></>}</div>
      {(task.subtitle || task.description || task.planSummary) && <p className="swarm-task-description">{task.subtitle || task.description || task.planSummary}</p>}
      <dl className="swarm-task-facts">
        <Fact label="Workspace">{facts.workspaces.length ? facts.workspaces.map((workspace, index) => <span key={`${index}-${workspace}`} className="swarm-task-break">{index > 0 ? ', ' : ''}{workspace.split('/').filter(Boolean).pop() || workspace}</span>) : 'Not specified'}</Fact>
        <Fact label="Worktree">{facts.worktree || 'Not assigned'}</Fact>
        <Fact label="Branch → target"><GitBranch size={13} aria-hidden="true" /> {facts.branch || 'Not available'} → {facts.target || 'Not available'}</Fact>
      </dl>
      <div className="swarm-task-evidence" aria-label="Task evidence">
        {facts.progress && <span>{facts.progress}</span>}
        {facts.changed && <span>{facts.changed}</span>}
        <span>{facts.validation || 'Validation not reported'}</span>
        <span>{facts.git}</span>
      </div>
    </div>
  </div>
}
