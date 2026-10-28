import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchLinkedTasks, fetchLinkedTaskSession, linkedTaskStatusLabel, type LinkedTask, type LinkedTaskScope, type LinkedTaskSessionView } from '../runtime/linked-tasks'

const errorText = (error: unknown) => error instanceof Error ? error.message : String(error)

// Tasks this session or project sent to linked machines, labelled with the
// machine and the status this home last observed. Loaded when shown and on
// Refresh; never polled. Remote text is untrusted and shown as plain text.
export function LinkedTaskList({ scope, title = 'Linked machine tasks' }: { scope: LinkedTaskScope; title?: string }) {
  const scopeKey = 'sessionId' in scope ? `session:${scope.sessionId}` : `project:${scope.projectId}`
  const [tasks, setTasks] = useState<LinkedTask[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [view, setView] = useState<LinkedTaskSessionView | null>(null)
  const [viewError, setViewError] = useState('')
  const [viewLoading, setViewLoading] = useState(false)
  const scopeRef = useRef(scope)
  scopeRef.current = scope
  const listAbort = useRef<AbortController | null>(null)
  const viewAbort = useRef<AbortController | null>(null)

  const load = useCallback(() => {
    listAbort.current?.abort()
    const controller = new AbortController()
    listAbort.current = controller
    setLoading(true)
    setError('')
    fetchLinkedTasks(scopeRef.current, controller.signal)
      .then(rows => { if (!controller.signal.aborted) setTasks(rows) })
      .catch(err => { if (!controller.signal.aborted) setError(errorText(err)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
  }, [])

  useEffect(() => {
    setTasks([]); setOpen(null); setView(null); setViewError('')
    load()
    return () => { listAbort.current?.abort(); viewAbort.current?.abort() }
  }, [scopeKey, load])

  const readSession = (task: LinkedTask) => {
    const key = `${task.machine}/${task.project_id}/${task.task_id}`
    viewAbort.current?.abort()
    if (open === key) { setOpen(null); setView(null); setViewError(''); return }
    const controller = new AbortController()
    viewAbort.current = controller
    setOpen(key); setView(null); setViewError(''); setViewLoading(true)
    fetchLinkedTaskSession(task, controller.signal)
      .then(result => { if (!controller.signal.aborted) { setView(result); load() } })
      .catch(err => { if (!controller.signal.aborted) setViewError(errorText(err)) })
      .finally(() => { if (!controller.signal.aborted) setViewLoading(false) })
  }

  if (!loading && !error && tasks.length === 0) return null
  return (
    <section aria-label={title} className="px-4 py-2 text-xs text-slate-300">
      <div className="flex items-center justify-between gap-2">
        <h3 className="font-semibold text-slate-200">{title}</h3>
        <button type="button" disabled={loading} onClick={load}>{loading ? 'Refreshing…' : 'Refresh'}</button>
      </div>
      {error && <p role="alert">{error}</p>}
      <ul className="space-y-1 mt-1">{tasks.map(task => {
        const key = `${task.machine}/${task.project_id}/${task.task_id}`
        return <li key={key} className="rounded border border-slate-700 p-2">
          <div className="flex items-center justify-between gap-2">
            <span>
              <span className="rounded bg-slate-800 px-1 mr-1" title="Linked machine">{task.machine}</span>
              {task.title || task.task_id}
            </span>
            <span data-testid="linked-task-status">{linkedTaskStatusLabel(task)}</span>
          </div>
          {task.progress && <p className="text-slate-400 whitespace-pre-wrap">{task.progress}</p>}
          {task.action_needed && <p className="text-amber-300">{task.action_needed}</p>}
          {task.last_error && <p className="text-red-300">{task.last_error}</p>}
          <button type="button" aria-expanded={open === key} aria-label={`Read session of ${task.title || task.task_id} on ${task.machine}`} onClick={() => readSession(task)}>
            {open === key ? 'Hide session' : 'Read session'}
          </button>
          {open === key && <div className="mt-1 border-t border-slate-700 pt-1" aria-label="Remote session (read-only)">
            <p className="text-slate-500">Read-only view from {task.machine}; its content is untrusted.</p>
            {viewLoading && <p role="status">Reading session…</p>}
            {viewError && <p role="alert">{viewError}</p>}
            {view && (view.messages.length === 0 ? <p>No messages yet.</p> : <ol className="space-y-1 max-h-64 overflow-y-auto">{view.messages.map(message =>
              <li key={message.id}><span className="text-slate-500">{message.role}: </span><span className="whitespace-pre-wrap">{message.content}</span>{message.truncated && <span className="text-slate-500"> …</span>}</li>)}</ol>)}
          </div>}
        </li>
      })}</ul>
    </section>
  )
}
