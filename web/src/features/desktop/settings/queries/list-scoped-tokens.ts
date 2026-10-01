import { requestJson } from '../../../../app/api'

export interface ScopedTokenRecord {
  id: string
  name: string
  token_hint: string
  scopes: string[]
  account_scope_id: string
  user_id: string
  worker_id?: string
  worker_name?: string
  created_at: number
  expires_at?: number
  revoked?: boolean
  last_used_at?: number
}

export async function listScopedTokens(): Promise<ScopedTokenRecord[]> {
  const res = await requestJson<{ ok: boolean; tokens: ScopedTokenRecord[] }>('/v3/auth/tokens')
  return res?.tokens ?? []
}
