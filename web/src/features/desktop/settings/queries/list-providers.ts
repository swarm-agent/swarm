import { requestJson } from '../../../../app/api'
import { createDebugTimer } from '../../../../lib/debug-log'
import { mapProviderStatus } from '../types/auth'
import type { ProviderStatus, ProvidersResponseWire } from '../types/auth'

export async function listProviders(): Promise<ProviderStatus[]> {
  const finish = createDebugTimer('desktop-auth-queries', 'listProviders')
  const response = await requestJson<ProvidersResponseWire>('/v1/providers')
  const providers = Array.isArray(response.providers) ? response.providers.map(mapProviderStatus) : []
  finish({ providerCount: providers.length })
  return providers
}
