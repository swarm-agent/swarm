import { requestJson } from '../../../app/api'

// The home's records of tasks its sessions sent to linked machines
// (GET /v3/fleet/linked-tasks). Everything observed from a machine is
// untrusted: render it as plain text only.
export type LinkedTask = {
  machine: string
  project_id: string
  task_id: string
  title?: string
  home_session_id: string
  home_project_id?: string
  created_at: number
  status?: string
  action_needed?: string
  last_error?: string
  progress?: string
  remote_session_id?: string
  archived?: boolean
  missing?: boolean
  observed_at?: number
}

export type LinkedTaskScope = { sessionId: string } | { projectId: string }

export type LinkedTaskMessage = { id: string; role: string; content: string; global_seq: number; created_at: number; truncated?: boolean }

export type LinkedTaskSessionView = {
  machine: string
  project_id: string
  task_id: string
  session_id: string
  title?: string
  status?: string
  progress?: string
  messages: LinkedTaskMessage[]
  has_more_older?: boolean
  read_only: true
  untrusted_content: true
}

export async function fetchLinkedTasks(scope: LinkedTaskScope, signal?: AbortSignal): Promise<LinkedTask[]> {
  const query = 'sessionId' in scope ? `session_id=${encodeURIComponent(scope.sessionId)}` : `project_id=${encodeURIComponent(scope.projectId)}`
  const response = await requestJson<{ linked_tasks?: LinkedTask[] }>(`/v3/fleet/linked-tasks?${query}`, { signal })
  return Array.isArray(response.linked_tasks) ? response.linked_tasks : []
}

// Read-only: the remote task session's recent messages, through the link.
export async function fetchLinkedTaskSession(task: Pick<LinkedTask, 'machine' | 'project_id' | 'task_id'>, signal?: AbortSignal): Promise<LinkedTaskSessionView> {
  const query = new URLSearchParams({ machine: task.machine, project_id: task.project_id, task_id: task.task_id })
  return requestJson<LinkedTaskSessionView>(`/v3/fleet/linked-tasks/session?${query.toString()}`, { signal })
}

export function linkedTaskStatusLabel(task: LinkedTask): string {
  if (task.missing) return 'missing on machine'
  if (task.archived) return `${task.status || 'unknown'} (archived)`
  return task.status || 'sent'
}
