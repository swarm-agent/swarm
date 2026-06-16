import { requestJson } from '../../../../app/api'
import { mapAuthCredential } from '../types/auth'
import type { AuthCredentialListResponse, AuthCredentialListResponseWire } from '../types/auth'

export async function listAuthCredentials(provider = '', query = '', limit = 200): Promise<AuthCredentialListResponse> {
  const params = new URLSearchParams()
  if (provider.trim() !== '') {
    params.set('provider', provider.trim())
  }
  if (query.trim() !== '') {
    params.set('query', query.trim())
  }
  params.set('limit', String(limit > 0 ? limit : 200))

  const response = await requestJson<AuthCredentialListResponseWire>(`/v1/auth/credentials?${params.toString()}`)
  const result = {
    provider: String(response.provider ?? '').trim(),
    query: String(response.query ?? '').trim(),
    total: typeof response.total === 'number' ? response.total : 0,
    records: Array.isArray(response.records) ? response.records.map(mapAuthCredential) : [],
    providers: Array.isArray(response.providers) ? response.providers.map((value) => String(value)) : [],
  }
  return result
}
