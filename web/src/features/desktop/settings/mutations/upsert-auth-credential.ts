import { requestJson } from '../../../../app/api'
import { mapAuthCredential } from '../types/auth'
import type { AuthCredential, AuthCredentialWire, UpsertAuthCredentialInput } from '../types/auth'

export async function upsertAuthCredential(input: UpsertAuthCredentialInput): Promise<AuthCredential> {
  const response = await requestJson<AuthCredentialWire>('/v1/auth/credentials', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return mapAuthCredential(response)
}
