import { requestJson } from '../../../../app/api'
import { mapProviderStatus } from '../types/auth'
import type { ProviderStatus, ProvidersResponseWire } from '../types/auth'

export async function listProviders(): Promise<ProviderStatus[]> {
  const response = await requestJson<ProvidersResponseWire>('/v1/providers')
  const providers = Array.isArray(response.providers) ? response.providers.map(mapProviderStatus) : []
  return providers
}
