import { requestJson } from '../../../../app/api'
import type { ScopedTokenRecord } from '../queries/list-scoped-tokens'

export async function revokeScopedToken(tokenId: string): Promise<ScopedTokenRecord> {
  const res = await requestJson<{ ok: boolean; record: ScopedTokenRecord }>(`/v3/auth/tokens/${tokenId}/revoke`, {
    method: 'POST',
  })
  return res.record
}
