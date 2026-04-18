import { requestJson } from '../../../../app/api'
import type { ContainerProfile, ContainerProfileWire, ListContainerProfilesResponse } from '../types/container-profiles'
import { mapContainerProfile, sortContainerProfiles } from '../types/container-profiles'

export async function listContainerProfiles(): Promise<ContainerProfile[]> {
  const response = await requestJson<ListContainerProfilesResponse & { profiles: ContainerProfileWire[] }>('/v1/swarm/containers/profiles')
  return sortContainerProfiles((response.profiles ?? []).map(mapContainerProfile))
}
