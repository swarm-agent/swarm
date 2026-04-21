import { requestJson } from '../../../../app/api'
import type { AuthCredentialActionInput } from '../types/auth'

export interface DeleteAuthCredentialCleanup {
  providerUnavailable: boolean
  clearedGlobalPreference: boolean
  resetAgents: string[]
}

export interface DeleteAuthCredentialResult {
  ok: boolean
  deleted: boolean
  provider: string
  id: string
  cleanup: DeleteAuthCredentialCleanup
}

export async function deleteAuthCredential(input: AuthCredentialActionInput): Promise<DeleteAuthCredentialResult> {
  const response = await requestJson<{
    ok?: boolean
    deleted?: boolean
    provider?: string
    id?: string
    cleanup?: {
      provider_unavailable?: boolean
      cleared_global_preference?: boolean
      reset_agents?: string[]
    }
  }>('/v1/auth/credentials/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return {
    ok: Boolean(response.ok),
    deleted: Boolean(response.deleted),
    provider: String(response.provider ?? '').trim(),
    id: String(response.id ?? '').trim(),
    cleanup: {
      providerUnavailable: Boolean(response.cleanup?.provider_unavailable),
      clearedGlobalPreference: Boolean(response.cleanup?.cleared_global_preference),
      resetAgents: Array.isArray(response.cleanup?.reset_agents)
        ? response.cleanup.reset_agents.map((value) => String(value).trim()).filter(Boolean)
        : [],
    },
  }
}
