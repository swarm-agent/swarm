import { requestJson } from '../../../../app/api'
import { mapAuthCredential } from '../types/auth'
import type { AuthCredential, AuthCredentialActionInput, AuthCredentialWire } from '../types/auth'

export async function setActiveAuthCredential(input: AuthCredentialActionInput): Promise<AuthCredential> {
  const response = await requestJson<AuthCredentialWire>('/v1/auth/credentials/active', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return mapAuthCredential(response)
}
