import { requestJson } from '../../../../app/api'

export async function deleteContainerProfile(id: string): Promise<string> {
  const response = await requestJson<{ ok: boolean; deleted?: string }>('/v1/swarm/containers/profiles/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ id }),
  })
  return String(response.deleted ?? '').trim()
}
