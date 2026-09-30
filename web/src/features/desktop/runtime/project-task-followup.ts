import { requestJson } from '../../../app/api'

// A deterministic payload identity survives component remount/reload without
// persisting user content in browser storage. Revision separates later requests.
export async function projectTaskFollowupPayload(projectId: string, taskId: string, revision: number, feedback: string, repair = false) {
  if (!Number.isInteger(revision) || revision < 1) throw new Error('Missing task revision; refresh and retry')
  const payload = JSON.stringify([projectId, taskId, revision, feedback, repair])
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(payload))
  const key = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
  return { client_request_id: `task-followup-${key}`, revision, feedback, repair }
}

export async function reopenProjectTask(projectId: string, taskId: string, revision: number, feedback: string, repair = false) {
  const body = await projectTaskFollowupPayload(projectId, taskId, revision, feedback, repair)
  return requestJson<{ status: string; task: any }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/reopen`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
}
