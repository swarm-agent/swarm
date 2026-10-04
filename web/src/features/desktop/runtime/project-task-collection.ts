import { apiFetch, readErrorMessage } from '../../../app/api'
import { STARTUP_REQUEST_TIMEOUT_MS, withRequestDeadline } from '../../../app/request-lifecycle'

// Migration is an explicit not-ready state. Never retry in a timer or turn a
// partial backfill into an empty board; the existing board Retry action owns it.
export async function fetchProjectTaskCollection(projectId: string, signal?: AbortSignal): Promise<{ tasks?: any[] }> {
  return withRequestDeadline(async requestSignal => {
    const response = await apiFetch(`/v3/projects/${encodeURIComponent(projectId)}/tasks`, { signal: requestSignal })
    if (response.status === 503 && response.headers.has('Retry-After')) {
      await response.body?.cancel()
      throw new Error('Task board is not ready while its index is being prepared. Existing tasks are retained. Use Retry to continue loading.')
    }
    if (!response.ok) throw new Error(await readErrorMessage(response))
    return response.json()
  }, STARTUP_REQUEST_TIMEOUT_MS, signal)
}
