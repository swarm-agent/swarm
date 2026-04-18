import { requestJson } from '../../../../app/api'
import type { AuthCredentialActionInput } from '../types/auth'

export async function deleteAuthCredential(input: AuthCredentialActionInput): Promise<void> {
  await requestJson('/v1/auth/credentials/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
}
