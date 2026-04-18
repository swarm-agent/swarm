import { requestJson } from '../../../../app/api'
import { mapAuthConnectionStatus } from '../types/auth'
import type { AuthCredentialActionInput, AuthConnectionStatus, VerifyAuthCredentialResponseWire } from '../types/auth'

export async function verifyAuthCredential(input: AuthCredentialActionInput): Promise<AuthConnectionStatus> {
  const response = await requestJson<VerifyAuthCredentialResponseWire>('/v1/auth/credentials/verify', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return (
    mapAuthConnectionStatus(response.connection) ?? {
      connected: false,
      method: 'unavailable',
      message: 'Verification unavailable',
      verifiedAt: 0,
    }
  )
}
