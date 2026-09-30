// Shared projections of durable session progress; never infer validation or integration.
export interface TaskTodo {
  id: string
  title: string
  status: 'pending' | 'in_progress' | 'completed'
}

export function sessionTaskTodos(metadata?: Record<string, unknown>): TaskTodo[] | undefined {
  if (!Array.isArray(metadata?.task_todos)) return undefined
  return metadata.task_todos.flatMap((value, index) => {
    if (!value || typeof value !== 'object') return []
    const item = value as Record<string, unknown>
    const title = typeof item.title === 'string' ? item.title.trim() : ''
    if (!title) return []
    const status = item.status === 'completed' || item.status === 'in_progress' ? item.status : 'pending'
    return [{ id: typeof item.id === 'string' ? item.id : `task-${index + 1}`, title, status } as TaskTodo]
  })
}

type RunEvidence = { run_id?: string; event_seq?: number; created_at?: number; status?: string }
const terminal = new Set(['completed', 'failed', 'cancelled', 'interrupted', 'expired', 'dispatch_blocked'])

// Across attempts creation order wins: an old attempt may finish after its successor starts.
// Within an attempt durable event sequence wins; equal-sequence replay cannot resurrect it.
export function preferRunEvidence<T extends RunEvidence, U extends RunEvidence>(current: T | null | undefined, incoming: U | null | undefined): T | U | undefined {
  if (!incoming?.run_id) return current ?? undefined
  if (!current?.run_id) return incoming
  if (current.run_id !== incoming.run_id && current.created_at !== incoming.created_at) {
    return (incoming.created_at ?? 0) > (current.created_at ?? 0) ? incoming : current
  }
  if ((incoming.event_seq ?? 0) !== (current.event_seq ?? 0)) {
    return (incoming.event_seq ?? 0) > (current.event_seq ?? 0) ? incoming : current
  }
  return terminal.has(current.status || '') ? current : incoming
}
