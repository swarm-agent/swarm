import type { RunningTask, MediaDeliverable } from './orchestrate-types'
import { taskReviewMessage } from './task-review-message'
import { taskCardSessions } from './task-card-sessions'

export function TaskCardHandoff({ task }: { task: RunningTask }) {
  const review = taskReviewMessage(task)
  return review.raw ? <pre className="swarm-task-handoff">{review.raw}</pre> : null
}

export function TaskCardAgents({ task, onOpen }: { task: RunningTask; onOpen?: (id: string) => void }) {
  return <section aria-label="Task AI sessions">
    {taskCardSessions(task).map(session => <button type="button" key={session.sessionId}
      data-session-id={session.sessionId} data-session-state={session.status}
      className="swarm-task-agent-row" disabled={!onOpen}
      aria-label={`View ${session.role || 'AI'} session ${session.title || session.sessionId}`}
      onClick={event => { event.stopPropagation(); onOpen?.(session.sessionId) }}>
      <span>{session.role || 'AI'} · {session.title || session.sessionId.slice(0, 8)}</span>
      <span>{session.status === 'unknown' ? 'State unavailable' : session.status.replace(/_/g, ' ')}</span>
    </button>)}
  </section>
}

/** A ready status alone is not an openable output, and a preview is not a code target. */
export function taskOutputTarget(item: MediaDeliverable): string | undefined {
  if (!['ready', 'accepted'].includes(item.status)) return undefined
  const value = item.mediaUrl || (['image', 'video', 'artifact'].includes(item.type) ? item.previewUrl : undefined)
  return value && /^(https?:\/\/|\/[^/]|data:(image|audio|video)\/)/i.test(value) ? value : undefined
}

export function TaskCardOutputs({ task, onPreview }: { task: RunningTask; onPreview?: (item: MediaDeliverable) => void }) {
  const produced = task.deliverables?.filter(item => taskOutputTarget(item)) || []
  return <section aria-label="Outputs">
    <h4>Outputs</h4>
    {produced.length === 0 && <p>No openable outputs recorded.</p>}
    {produced.map(item => <div key={item.id} className="swarm-task-output-row">
      {['code', 'pr', 'report'].includes(item.type)
        ? <a href={taskOutputTarget(item)} target="_blank" rel="noreferrer">{item.title}</a>
        : <button type="button" disabled={!onPreview} onClick={() => onPreview?.(item)}>{item.title}</button>}
      <span>{item.type}</span>
    </div>)}
    {!!task.attachedMedia?.length && <section aria-label="Inputs"><h4>Inputs</h4>
      {task.attachedMedia.map(item => <p key={item.id}>{item.url && /^(https?:\/\/|\/[^/])/i.test(item.url)
        ? <a href={item.url} target="_blank" rel="noreferrer">{item.title || item.filename || 'Input'}</a>
        : item.title || item.filename || 'Input'}</p>)}
    </section>}
  </section>
}

export function TaskExpectedOutputs({ task }: { task: RunningTask }) {
  const expected = task.deliverables?.filter(item => !taskOutputTarget(item)) || []
  return expected.length ? <section aria-label="Expected outputs"><h5>Expected outputs</h5><ul>
    {expected.map(item => <li key={item.id}>{item.title} · {item.status}{['ready', 'accepted'].includes(item.status) ? ' · target unavailable' : ''}</li>)}
  </ul></section> : null
}
