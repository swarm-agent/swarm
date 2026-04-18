import { requestJson } from '../../../../app/api'
import type { ContainerProfile, ContainerProfileDraft, ContainerProfileWire } from '../types/container-profiles'
import { mapContainerProfile, mapDraftToUpsertPayload } from '../types/container-profiles'

export async function upsertContainerProfile(draft: ContainerProfileDraft): Promise<ContainerProfile> {
  const response = await requestJson<{ ok: boolean; profile: ContainerProfileWire }>('/v1/swarm/containers/profiles/upsert', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(mapDraftToUpsertPayload(draft)),
  })
  return mapContainerProfile(response.profile)
}
