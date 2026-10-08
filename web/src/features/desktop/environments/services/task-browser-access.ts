import { requestJson } from '../../../../app/api'
import type { TaskBrowserEndpoint, TaskEnvironmentAttachment } from '../types/environments'

// Require literal authority before URL parsing: the URL parser normalizes numeric,
// octal and shortened IPs, which must not expand the backend loopback contract.
export function verifiedBrowserURL(endpoint: TaskBrowserEndpoint): string | null {
  const raw = endpoint.url
  if (!endpoint.ready || !raw || !Number.isInteger(endpoint.host_port) || endpoint.host_port! < 1 || endpoint.host_port! > 65535) return null
  if (!/^https?:\/\/(localhost|127\.0\.0\.1|\[::1\])(?::[0-9]+)?(?:\/[^\s?#\\]*)?$/.test(raw)) return null
  try {
    const url = new URL(raw)
    if (url.username || url.password || url.search || url.hash) return null
    if (Number(url.port || (url.protocol === 'https:' ? 443 : 80)) !== endpoint.host_port) return null
    return url.href
  } catch { return null }
}

export async function checkTaskBrowserAccess(attachment: TaskEnvironmentAttachment): Promise<TaskBrowserEndpoint[]> {
  const response = await requestJson<{ browser_endpoints: TaskBrowserEndpoint[] }>('/v1/task-environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'browser_endpoints', project_id: attachment.project_id,
      task_id: attachment.task_id, workspace_id: attachment.source.workspace_id,
      attachment_id: attachment.id, expected_attachment_revision: attachment.revision }),
  })
  if (!Array.isArray(response.browser_endpoints) || response.browser_endpoints.length > 16) throw new Error('Invalid browser endpoint response')
  const ids = new Set<string>()
  for (const endpoint of response.browser_endpoints) {
    if (!endpoint || typeof endpoint.id !== 'string' || !endpoint.id || ids.has(endpoint.id) || typeof endpoint.name !== 'string') throw new Error('Invalid browser endpoint identity')
    ids.add(endpoint.id)
  }
  return response.browser_endpoints
}
