import { requestJson } from '../../../../app/api'
import type { ScopedTokenRecord } from '../queries/list-scoped-tokens'

export interface CreateScopedTokenInput {
  name: string
  scopes: string[]
  worker_id?: string
  worker_name?: string
  expires_in_seconds?: number
}

export interface CreateScopedTokenOutput {
  ok: boolean
  token: string
  record: ScopedTokenRecord
}

export async function createScopedToken(input: CreateScopedTokenInput): Promise<CreateScopedTokenOutput> {
  return requestJson<CreateScopedTokenOutput>('/v3/auth/tokens', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
}
